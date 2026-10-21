-- Assertion helpers and the fakes the suite shares: a device, a clock and an
-- http function. Every failure raises with a message that says what was
-- expected, so run.lua only has to print it.

local h = {}

local function render(value)
  if type(value) == "string" then
    return string.format("%q", value)
  end
  if type(value) ~= "table" then
    return tostring(value)
  end
  local keys = {}
  for k in pairs(value) do
    keys[#keys + 1] = k
  end
  table.sort(keys, function(a, b) return tostring(a) < tostring(b) end)
  local parts = {}
  for _, k in ipairs(keys) do
    parts[#parts + 1] = tostring(k) .. " = " .. render(value[k])
  end
  return "{ " .. table.concat(parts, ", ") .. " }"
end

h.render = render

function h.assert_equal(actual, expected, context)
  if actual ~= expected then
    error(string.format("%sexpected %s, got %s",
      context and (context .. ": ") or "", render(expected), render(actual)), 2)
  end
end

function h.assert_true(value, context)
  if value ~= true then
    error(string.format("%sexpected true, got %s", context and (context .. ": ") or "", render(value)), 2)
  end
end

function h.assert_false(value, context)
  if value ~= false then
    error(string.format("%sexpected false, got %s", context and (context .. ": ") or "", render(value)), 2)
  end
end

function h.assert_nil(value, context)
  if value ~= nil then
    error(string.format("%sexpected nil, got %s", context and (context .. ": ") or "", render(value)), 2)
  end
end

function h.assert_contains(haystack, needle, context)
  if type(haystack) ~= "string" or not haystack:find(needle, 1, true) then
    error(string.format("%sexpected %s to contain %s",
      context and (context .. ": ") or "", render(haystack), render(needle)), 2)
  end
end

--------------------------------------------------------------------------------
-- fakes
--------------------------------------------------------------------------------

--- A fake device: fields, emitted events and online/offline calls recorded.
function h.device(prefs)
  local device = {
    id = "dev-1",
    label = "PS5",
    device_network_id = "ps5-control-1",
    preferences = prefs or { serverAddress = "192.168.1.50", serverPort = 8580, pollInterval = "30" },
    fields = {},
    emitted = {},
    online_calls = 0,
    offline_calls = 0,
  }
  function device:get_field(key)
    return self.fields[key]
  end
  function device:set_field(key, value, _opts)
    self.fields[key] = value
  end
  function device:emit_event(event)
    self.emitted[#self.emitted + 1] = event
  end
  function device:online()
    self.online_calls = self.online_calls + 1
  end
  function device:offline()
    self.offline_calls = self.offline_calls + 1
  end
  return device
end

--- The values of every emitted `switch` event, in order.
function h.switch_values(device)
  local out = {}
  for _, e in ipairs(device.emitted) do
    if e.capability == "switch" and e.attribute == "switch" then
      out[#out + 1] = e.value
    end
  end
  return out
end

--- Whether the last emitted switch event was forced (`state_change = true`).
function h.last_switch_forced(device)
  local forced
  for _, e in ipairs(device.emitted) do
    if e.capability == "switch" then
      forced = (e.options or {}).state_change == true
    end
  end
  return forced
end

--- A clock that the test moves by hand: `clock.now()` and `clock.advance(s)`.
function h.clock(start)
  local t = start or 1000000
  return {
    now = function() return t end,
    advance = function(seconds) t = t + seconds end,
    set = function(value) t = value end,
  }
end

--- A fake socket.http `request` built from a script of answers, one per call,
--- each `{ code = 200, body = "..." }` or `{ err = "timeout" }`. The last
--- answer repeats once the script runs out. Every request is recorded in
--- `.requests`.
function h.http(script)
  local calls = 0
  local fake = { requests = {} }
  fake.request = function(req)
    calls = calls + 1
    fake.requests[#fake.requests + 1] = req
    local answer = script[math.min(calls, #script)] or { err = "no script" }
    if answer.err then
      return nil, answer.err
    end
    if answer.body and req.sink then
      req.sink(answer.body)
      req.sink(nil)
    end
    return 1, answer.code or 200, {}, "HTTP/1.1 " .. tostring(answer.code or 200)
  end
  return fake
end

--- The `deps` a test hands to the driver modules.
function h.deps(http, clock)
  return {
    http = http and http.request or nil,
    json = require "st.json",
    ltn12 = require "ltn12",
    now = clock and clock.now or nil,
  }
end

return h
