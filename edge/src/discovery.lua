-- Device discovery: an SSDP M-SEARCH for the ps5-control service, plus the
-- address rules. The server answers from the LAN with a stable instance id
-- (USN) and its HTTP port, so the device is created with its address filled
-- in and follows the server when DHCP moves it. The `serverAddress`
-- preference, when set, always wins over what discovery found.
--
-- Parsing (`msearch`, `parse_response`, `location_address`) and the decisions
-- (`plan`, `should_search`) are pure; only `collect`/`search` touch a socket,
-- and they take an injectable `deps.socket`.

local discovery = {}

discovery.PROFILE = "ps5.v1"
discovery.DNI_PREFIX = "ps5-control-"
discovery.MANUFACTURER = "Protomothis"
discovery.MODEL = "PS5 Control"

-- The search target the server's responder answers (internal/ssdp).
discovery.SSDP_ST = "urn:smartthings-ps5-control:device:ps5:1"
discovery.SSDP_GROUP = "239.255.255.250"
discovery.SSDP_PORT = 1900
-- MX is the longest the responder may wait before answering; the server
-- clamps it to 3 s anyway.
discovery.SSDP_MX = 2
discovery.SSDP_TIMEOUT = 4
-- At most one targeted re-search per device per five minutes, so an
-- unreachable server cannot turn into a multicast storm.
discovery.SEARCH_COOLDOWN = 300
discovery.DEFAULT_PORT = 8580

-- Device fields.
discovery.F_IP = "discovered_ip"
discovery.F_PORT = "discovered_port"
discovery.F_NAME = "discovered_name"
discovery.F_LAST_SEARCH = "last_search" -- not persisted

local function logger()
  local ok, log = pcall(require, "log")
  if ok then
    return log
  end
  local noop = function() end
  return { trace = noop, debug = noop, info = noop, warn = noop, error = noop }
end

-- What this module needs from the poll layer, handed in by poll.lua
-- (`discovery.use`) so that this module does not require it.
local wired = {}

--- `hooks.once(driver, device, deps)`: poll.once, for the poll after an
--- address change.
function discovery.use(hooks)
  wired = hooks or {}
end

--------------------------------------------------------------------------------
-- pure: SSDP text
--------------------------------------------------------------------------------

--- The M-SEARCH datagram. `MAN` is quoted, as the spec requires and as the
--- server's parser checks.
function discovery.msearch(mx)
  return table.concat({
    "M-SEARCH * HTTP/1.1",
    string.format("HOST: %s:%d", discovery.SSDP_GROUP, discovery.SSDP_PORT),
    'MAN: "ssdp:discover"',
    "MX: " .. tostring(math.floor(tonumber(mx) or discovery.SSDP_MX)),
    "ST: " .. discovery.SSDP_ST,
    "", "",
  }, "\r\n")
end

--- Parse one SSDP 200 OK into `{ id, ip, port, name, location }`, or nil.
--- The id comes from `USN: uuid:<id>::<st>` (or PS5CTL-ID), the address
--- from LOCATION, the name from PS5CTL-NAME.
function discovery.parse_response(text, source_ip)
  if type(text) ~= "string" or text == "" then
    return nil
  end
  local lines = {}
  for line in (text:gsub("\r\n", "\n") .. "\n"):gmatch("([^\n]*)\n") do
    lines[#lines + 1] = line
  end
  if not (lines[1] or ""):match("^HTTP/1%.%d%s+200") then
    return nil
  end
  local headers = {}
  for i = 2, #lines do
    local name, value = lines[i]:match("^([^:]+):%s*(.-)%s*$")
    if name then
      headers[name:lower()] = value
    end
  end
  if headers["st"] ~= nil and headers["st"] ~= discovery.SSDP_ST then
    return nil
  end
  local usn = headers["usn"] or ""
  local id = headers["ps5ctl-id"] or usn:match("^uuid:(.-)::") or usn:match("^uuid:(.+)$")
  if type(id) ~= "string" or id == "" then
    return nil
  end
  local ip, port = discovery.location_address(headers["location"])
  port = tonumber(headers["ps5ctl-port"]) or port or discovery.DEFAULT_PORT
  ip = ip or source_ip
  if type(ip) ~= "string" or ip == "" then
    return nil
  end
  local name = headers["ps5ctl-name"]
  if name == "" then
    name = nil
  end
  return { id = id, ip = ip, port = port, name = name, location = headers["location"] }
end

--- Split `http://192.168.1.50:8580/ssdp.json` into ip and port.
function discovery.location_address(location)
  if type(location) ~= "string" then
    return nil
  end
  local host, port = location:match("^https?://([^/:]+):(%d+)")
  if host then
    return host, tonumber(port)
  end
  host = location:match("^https?://([^/:]+)")
  return host, nil
end

--------------------------------------------------------------------------------
-- pure: identity
--------------------------------------------------------------------------------

--- Device network id for a server instance id.
function discovery.network_id(id)
  return discovery.DNI_PREFIX .. id
end

local manual_seq = 0

--- Device network id for a device added without a search hit.
function discovery.manual_network_id(now)
  manual_seq = manual_seq + 1
  return string.format("%smanual-%x-%d", discovery.DNI_PREFIX, math.floor(tonumber(now) or os.time()), manual_seq)
end

--- The instance id a device was created for, or nil for a manual device.
function discovery.id_of(device)
  local dni = (device or {}).device_network_id
  if type(dni) ~= "string" or dni:sub(1, #discovery.DNI_PREFIX) ~= discovery.DNI_PREFIX then
    return nil
  end
  local id = dni:sub(#discovery.DNI_PREFIX + 1)
  if id == "" or id:match("^manual%-") then
    return nil
  end
  return id
end

--- The device in `devices` created for `id`, or nil.
function discovery.find(devices, id)
  if type(id) ~= "string" or id == "" then
    return nil
  end
  for _, device in pairs(devices or {}) do
    if discovery.id_of(device) == id then
      return device
    end
  end
  return nil
end

--- Merge hits by id (a server with two interfaces answers twice).
function discovery.merge(found)
  local out, index = {}, {}
  for _, hit in ipairs(found or {}) do
    if hit.id and not index[hit.id] then
      index[hit.id] = true
      out[#out + 1] = hit
    end
  end
  return out
end

--- The address a device should use: the `serverAddress` preference first,
--- else what discovery stored. Returns `ip, port` or nil.
function discovery.address(device)
  local prefs = (device or {}).preferences or {}
  if type(prefs.serverAddress) == "string" and prefs.serverAddress ~= "" then
    return prefs.serverAddress, tonumber(prefs.serverPort) or discovery.DEFAULT_PORT
  end
  if type(device) ~= "table" or type(device.get_field) ~= "function" then
    return nil
  end
  local ip = device:get_field(discovery.F_IP)
  if type(ip) ~= "string" or ip == "" then
    return nil
  end
  return ip, tonumber(device:get_field(discovery.F_PORT)) or tonumber(prefs.serverPort) or discovery.DEFAULT_PORT
end

--- Whether the device is pinned to the preference (discovery must not move it).
function discovery.pinned(device)
  local prefs = (device or {}).preferences or {}
  return type(prefs.serverAddress) == "string" and prefs.serverAddress ~= ""
end

--- May this device do a targeted search now?
function discovery.should_search(last, now)
  now = tonumber(now) or 0
  last = tonumber(last)
  if not last then
    return true
  end
  return (now - last) >= discovery.SEARCH_COOLDOWN
end

--------------------------------------------------------------------------------
-- the search itself
--------------------------------------------------------------------------------

--- One M-SEARCH round: the parsed responses, merged by id.
function discovery.collect(socket, timeout, deps)
  deps = deps or {}
  local now = deps.now or os.time
  local udp = assert(socket.udp())
  pcall(function() udp:setsockname("0.0.0.0", 0) end)
  pcall(function() udp:settimeout(1) end)
  udp:sendto(discovery.msearch(discovery.SSDP_MX), discovery.SSDP_GROUP, discovery.SSDP_PORT)

  local deadline = now() + (tonumber(timeout) or discovery.SSDP_TIMEOUT)
  local found = {}
  while now() < deadline do
    local data, from = udp:receivefrom()
    if data then
      local hit = discovery.parse_response(data, from)
      if hit then
        found[#found + 1] = hit
      end
    elseif from ~= "timeout" and from ~= "wantread" then
      break
    end
  end
  pcall(function() udp:close() end)
  return discovery.merge(found)
end

--- Run a search. Any socket failure yields an empty list: discovery is best
--- effort, the preference is the fallback.
function discovery.search(deps)
  deps = deps or {}
  if deps.search then
    return deps.search() or {}
  end
  local socket = deps.socket
  if not socket then
    local loaded, cosock = pcall(require, "cosock")
    if not loaded then
      return {}
    end
    socket = cosock.socket
  end
  local ok, found = pcall(discovery.collect, socket, deps.timeout, deps)
  if not ok then
    logger().warn("SSDP search failed: " .. tostring(found))
    return {}
  end
  return found
end

--------------------------------------------------------------------------------
-- creating and adopting
--------------------------------------------------------------------------------

-- DNI -> address of a device created by a search but not yet initialised.
local pending = {}

function discovery.remember(dni, info)
  if type(dni) == "string" and dni ~= "" then
    pending[dni] = info
  end
end

function discovery.take(dni)
  local info = pending[dni or ""]
  pending[dni or ""] = nil
  return info
end

--- Create one device for a search hit.
function discovery.create(driver, hit)
  local dni = discovery.network_id(hit.id)
  local label = hit.name
  if type(label) ~= "string" or label == "" then
    label = "PS5"
  end
  discovery.remember(dni, { ip = hit.ip, port = hit.port, name = hit.name })
  return driver:try_create_device({
    type = "LAN",
    device_network_id = dni,
    label = label,
    profile = discovery.PROFILE,
    manufacturer = discovery.MANUFACTURER,
    model = discovery.MODEL .. " · " .. tostring(hit.id):sub(1, 8),
    vendor_provided_label = label,
  })
end

--- Create a device with no address (the user fills in `serverAddress`).
function discovery.create_manual(driver, deps)
  local now = (deps and deps.now) and deps.now() or os.time()
  return driver:try_create_device({
    type = "LAN",
    device_network_id = discovery.manual_network_id(now),
    label = "PS5",
    profile = discovery.PROFILE,
    manufacturer = discovery.MANUFACTURER,
    model = discovery.MODEL,
    vendor_provided_label = "PS5",
  })
end

--- Store the remembered address on a device the search just created.
--- Returns true when there was one.
function discovery.adopt(device)
  local info = discovery.take((device or {}).device_network_id)
  if not info then
    return false
  end
  device:set_field(discovery.F_IP, info.ip, { persist = true })
  device:set_field(discovery.F_PORT, info.port, { persist = true })
  device:set_field(discovery.F_NAME, info.name, { persist = true })
  return true
end

--- The hub's discovery handler: search, create a device for every server not
--- yet known. With no answer and no devices at all, create one manual device
--- so the preference path is still reachable.
function discovery.handle(driver, _opts, _should_continue, deps)
  deps = deps or {}
  local log = logger()
  local found = discovery.search(deps)
  local devices = driver:get_devices()
  local created = 0
  for _, hit in ipairs(found) do
    if discovery.find(devices, hit.id) then
      log.info(string.format("search: %s at %s already added", hit.id, hit.ip))
    else
      log.info(string.format("search: creating device for %s at %s:%s", hit.id, hit.ip, tostring(hit.port)))
      discovery.create(driver, hit)
      created = created + 1
    end
  end
  if #found == 0 and next(devices or {}) == nil then
    log.warn("search: no ps5-control answered; creating a device to configure by address")
    discovery.create_manual(driver, deps)
    created = created + 1
  end
  return created
end

--- After a failed poll: look for the server again, at most once per
--- cooldown, and move the device when it answers from a new address.
--- Returns true when the address changed.
function discovery.research(driver, device, deps)
  deps = deps or {}
  if discovery.pinned(device) then
    return false
  end
  local now = (deps.now or os.time)()
  if not discovery.should_search(device:get_field(discovery.F_LAST_SEARCH), now) then
    return false
  end
  device:set_field(discovery.F_LAST_SEARCH, now)
  local id = discovery.id_of(device)
  local hit
  for _, h in ipairs(discovery.search(deps)) do
    if id == nil or h.id == id then
      hit = h
      break
    end
  end
  if not hit then
    return false
  end
  if hit.ip == device:get_field(discovery.F_IP) and hit.port == device:get_field(discovery.F_PORT) then
    return false
  end
  logger().info(string.format("%s: server moved to %s:%s", device.id, hit.ip, tostring(hit.port)))
  device:set_field(discovery.F_IP, hit.ip, { persist = true })
  device:set_field(discovery.F_PORT, hit.port, { persist = true })
  if hit.name then
    device:set_field(discovery.F_NAME, hit.name, { persist = true })
  end
  local once = deps.once or wired.once
  if once then
    pcall(once, driver, device, deps)
  end
  return true
end

return discovery
