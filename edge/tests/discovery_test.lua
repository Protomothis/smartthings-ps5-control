local h = require "helpers"
local Driver = require "st.driver"
local cosock = require "cosock"
local discovery = require "discovery"

local T = {}

local ST = discovery.SSDP_ST

local function answer(id, ip, port, name)
  return table.concat({
    "HTTP/1.1 200 OK",
    "CACHE-CONTROL: max-age=1800",
    "EXT:",
    string.format("LOCATION: http://%s:%d/ssdp.json", ip, port),
    "SERVER: Linux UPnP/1.0 smartthings-ps5-control/v0.1.0",
    "ST: " .. ST,
    string.format("USN: uuid:%s::%s", id, ST),
    "PS5CTL-ID: " .. id,
    "PS5CTL-NAME: " .. (name or ""),
    "PS5CTL-PORT: " .. tostring(port),
    "", "",
  }, "\r\n")
end

function T.test_msearch_text()
  local m = discovery.msearch(2)
  h.assert_true(m:sub(1, 20) == "M-SEARCH * HTTP/1.1\r")
  h.assert_contains(m, 'MAN: "ssdp:discover"\r\n')
  h.assert_contains(m, "MX: 2\r\n")
  h.assert_contains(m, "ST: " .. ST .. "\r\n\r\n")
end

function T.test_parse_response()
  local hit = discovery.parse_response(answer("abc123", "192.168.1.50", 8580, "PS5-123"), "192.168.1.50")
  h.assert_equal(hit.id, "abc123")
  h.assert_equal(hit.ip, "192.168.1.50")
  h.assert_equal(hit.port, 8580)
  h.assert_equal(hit.name, "PS5-123")
  -- id from USN alone, address from the source, empty name
  hit = discovery.parse_response("HTTP/1.1 200 OK\r\nUSN: uuid:zzz::" .. ST .. "\r\nST: " .. ST .. "\r\n\r\n", "10.0.0.9")
  h.assert_equal(hit.id, "zzz")
  h.assert_equal(hit.ip, "10.0.0.9")
  h.assert_equal(hit.port, 8580)
  h.assert_nil(hit.name)
  -- foreign responders and garbage
  h.assert_nil(discovery.parse_response("HTTP/1.1 200 OK\r\nST: upnp:rootdevice\r\nUSN: uuid:tv::upnp:rootdevice\r\n\r\n", "1.2.3.4"))
  h.assert_nil(discovery.parse_response("NOTIFY * HTTP/1.1\r\n", "1.2.3.4"))
  h.assert_nil(discovery.parse_response("", "1.2.3.4"))
end

function T.test_identity_helpers()
  h.assert_equal(discovery.network_id("abc"), "ps5-control-abc")
  h.assert_equal(discovery.id_of({ device_network_id = "ps5-control-abc" }), "abc")
  h.assert_nil(discovery.id_of({ device_network_id = "ps5-control-manual-1-1" }))
  h.assert_nil(discovery.id_of({ device_network_id = "other" }))
  local m1, m2 = discovery.manual_network_id(1000), discovery.manual_network_id(1000)
  h.assert_true(m1 ~= m2, "manual ids differ within a second")
  local devices = { { device_network_id = "ps5-control-abc" }, { device_network_id = "x" } }
  h.assert_equal(discovery.find(devices, "abc"), devices[1])
  h.assert_nil(discovery.find(devices, "nope"))
  local merged = discovery.merge({ { id = "a", ip = "1" }, { id = "a", ip = "2" }, { id = "b", ip = "3" } })
  h.assert_equal(#merged, 2)
  h.assert_equal(merged[1].ip, "1", "first answer wins")
end

function T.test_address_precedence()
  local device = h.device({ serverAddress = "", serverPort = 8580 })
  h.assert_nil((discovery.address(device)))
  device.fields[discovery.F_IP] = "192.168.1.50"
  device.fields[discovery.F_PORT] = 8581
  local ip, port = discovery.address(device)
  h.assert_equal(ip, "192.168.1.50")
  h.assert_equal(port, 8581)
  device.preferences.serverAddress = "10.0.0.2"
  device.preferences.serverPort = 9000
  ip, port = discovery.address(device)
  h.assert_equal(ip, "10.0.0.2", "preference wins")
  h.assert_equal(port, 9000)
  h.assert_true(discovery.pinned(device))
end

function T.test_should_search_cooldown()
  h.assert_true(discovery.should_search(nil, 1000))
  h.assert_false(discovery.should_search(1000, 1000 + discovery.SEARCH_COOLDOWN - 1))
  h.assert_true(discovery.should_search(1000, 1000 + discovery.SEARCH_COOLDOWN))
end

function T.test_collect_sends_msearch_and_reads_answers()
  cosock.reset()
  cosock.inbox[1] = { data = answer("srv1", "192.168.1.50", 8580, "PS5-A"), ip = "192.168.1.50", port = 1900 }
  cosock.inbox[2] = { data = answer("srv1", "192.168.1.50", 8580, "PS5-A"), ip = "192.168.1.50", port = 1900 }
  cosock.inbox[3] = { data = "HTTP/1.1 200 OK\r\nST: upnp:rootdevice\r\nUSN: uuid:tv\r\n\r\n", ip = "192.168.1.7", port = 1900 }
  -- a clock that moves one second per look, like a socket timing out
  local t = 0
  local found = discovery.collect(cosock.socket, 4, { now = function() t = t + 1; return t end })
  h.assert_equal(#cosock.sent, 1)
  h.assert_equal(cosock.sent[1].ip, discovery.SSDP_GROUP)
  h.assert_equal(cosock.sent[1].port, discovery.SSDP_PORT)
  h.assert_contains(cosock.sent[1].data, "ST: " .. ST)
  h.assert_equal(#found, 1, "duplicates merged, foreign ignored")
  h.assert_equal(found[1].id, "srv1")
end

function T.test_handle_creates_device_from_search_with_address()
  local driver = Driver("t")
  local hits = { { id = "srv1", ip = "192.168.1.50", port = 8580, name = "PS5-A" } }
  local created = discovery.handle(driver, {}, function() return true end, { search = function() return hits end })
  h.assert_equal(created, 1)
  local spec = driver.created[1]
  h.assert_equal(spec.type, "LAN")
  h.assert_equal(spec.device_network_id, "ps5-control-srv1")
  h.assert_equal(spec.profile, discovery.PROFILE)
  h.assert_equal(spec.label, "PS5-A")
  h.assert_equal(spec.model, "PS5 Control · srv1")
  -- the address travels to `added` via adopt
  local device = h.device({ serverAddress = "" })
  device.device_network_id = "ps5-control-srv1"
  h.assert_true(discovery.adopt(device))
  h.assert_equal(device.fields[discovery.F_IP], "192.168.1.50")
  h.assert_equal(device.fields[discovery.F_PORT], 8580)
  h.assert_false(discovery.adopt(device), "taken once")
  -- a second scan with the device present creates nothing
  driver.devices = { { device_network_id = "ps5-control-srv1" } }
  created = discovery.handle(driver, {}, function() return true end, { search = function() return hits end })
  h.assert_equal(created, 0)
  h.assert_equal(#driver.created, 1)
end

function T.test_handle_without_answer_creates_one_manual_device()
  local driver = Driver("t")
  local none = { search = function() return {} end, now = function() return 1234 end }
  h.assert_equal(discovery.handle(driver, {}, nil, none), 1)
  h.assert_true(driver.created[1].device_network_id:match("^ps5%-control%-manual%-") ~= nil)
  h.assert_equal(driver.created[1].label, "PS5")
  -- with a device already there, silence creates nothing more
  driver.devices = { { device_network_id = driver.created[1].device_network_id } }
  h.assert_equal(discovery.handle(driver, {}, nil, none), 0)
end

function T.test_research_moves_an_unpinned_device()
  local device = h.device({ serverAddress = "" })
  device.device_network_id = "ps5-control-srv1"
  device.fields[discovery.F_IP] = "192.168.1.50"
  device.fields[discovery.F_PORT] = 8580
  local clock = h.clock()
  local polled = 0
  local deps = {
    now = clock.now,
    search = function() return { { id = "srv1", ip = "192.168.1.77", port = 8580 } } end,
    once = function() polled = polled + 1 end,
  }
  h.assert_true(discovery.research(nil, device, deps))
  h.assert_equal(device.fields[discovery.F_IP], "192.168.1.77")
  h.assert_equal(polled, 1)
  -- inside the cooldown nothing happens, even if the server moved again
  deps.search = function() return { { id = "srv1", ip = "192.168.1.78", port = 8580 } } end
  h.assert_false(discovery.research(nil, device, deps))
  h.assert_equal(device.fields[discovery.F_IP], "192.168.1.77")
  clock.advance(discovery.SEARCH_COOLDOWN)
  h.assert_true(discovery.research(nil, device, deps))
  h.assert_equal(device.fields[discovery.F_IP], "192.168.1.78")
  -- another server's answer is ignored
  clock.advance(discovery.SEARCH_COOLDOWN)
  deps.search = function() return { { id = "other", ip = "192.168.1.99", port = 8580 } } end
  h.assert_false(discovery.research(nil, device, deps))
  -- pinned by preference: never searched
  device.preferences.serverAddress = "10.0.0.1"
  clock.advance(discovery.SEARCH_COOLDOWN)
  h.assert_false(discovery.research(nil, device, deps))
end

return T
