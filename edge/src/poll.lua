-- Polling: ask the server for the PS5 state on a timer, turn the answer into
-- rows (switch, power state, connection, last wake, Bluetooth, Remote Play,
-- versions), and mark the device online/offline after the server itself
-- (not the console) answered or did not.
--
-- Every event goes through `emit_row`, which drops unchanged values (the
-- hub's event budget is per device and dropped events count too), forces
-- the first emit of each row per run (after a profile move the hub may hold
-- a value the cloud never stored) and re-sends each row once every
-- `model.REEMIT_SECONDS` so a lost event heals.

local capabilities = require "st.capabilities"
local caps = require "caps"
local client = require "client"
local discovery = require "discovery"
local model = require "model"
local rows = require "rows"
local version = require "driver_version"

local poll = {}

-- discovery.research polls after moving a device; it gets poll.once through
-- this hook so discovery never requires poll.
discovery.use({ once = function(driver, device, deps) return poll.once(driver, device, deps) end })

poll.DEFAULT_INTERVAL = 30

-- Device fields.
poll.F_POWER = "ps5_power"            -- last `/status` state: on / standby / off / unknown (persisted)
poll.F_LAST_SEEN = "last_seen"        -- epoch of the last good status (persisted)
poll.F_WAKING_UNTIL = "waking_until"  -- epoch while a wake window is open (persisted)
poll.F_ROWS_SENT = "rows_sent"        -- not persisted: row key -> { value, at }
poll.F_POLL_TIMER = "poll_timer"      -- not persisted: the schedule handle
poll.F_WAKE_TIMER = "wake_timer"      -- not persisted: the fast-poll handle
poll.F_LAST_ERROR = "last_error"      -- not persisted: the failure kind last logged

-- The custom capabilities, resolved once; a key is nil until the account
-- has that capability (caps.load).
local custom = {}

--- Resolve the custom capabilities (init.lua calls this with st.capabilities).
function poll.use_capabilities(capabilities_module)
  local loaded, missing = caps.load(capabilities_module)
  custom = loaded
  return missing
end

local function logger()
  local ok, log = pcall(require, "log")
  if ok then
    return log
  end
  local noop = function() end
  return { trace = noop, debug = noop, info = noop, warn = noop, error = noop }
end

local function now_of(deps)
  if deps and type(deps.now) == "function" then
    return deps.now()
  end
  return os.time()
end

local function field(device, key)
  return device:get_field(key)
end

local function set_field(device, key, value, persist)
  device:set_field(key, value, persist and { persist = true } or nil)
end

--- The event constructor for a row: `capabilities.switch.switch` for the
--- switch, else the custom capability's attribute. nil when the capability
--- is not available on this hub.
local function constructor(cap_key, attr)
  if cap_key == "switch" then
    return capabilities.switch.switch
  end
  local cap = custom[cap_key]
  if not cap then
    return nil
  end
  return cap[attr]
end

--- Emit one row unless the same value went out recently. `force` answers a
--- command (the app waits for an event even when nothing changed) and also
--- re-sends after a repaint. Returns true when an event was sent.
function poll.emit_row(device, cap_key, attr, value, force, deps)
  local make = constructor(cap_key, attr)
  if not make then
    return false
  end
  local now = now_of(deps)
  local sent = field(device, poll.F_ROWS_SENT)
  if type(sent) ~= "table" then
    sent = {}
    set_field(device, poll.F_ROWS_SENT, sent)
  end
  local key = cap_key .. "." .. attr
  local last = sent[key]
  -- First emit of a row in this run is forced: the hub may hold a stale
  -- value the cloud never got (platform notes "프로필 이전 뒤").
  local first = last == nil
  if not first and not model.should_emit(value, last.value, last.at, now, force) then
    return false
  end
  local forced = force or first
  device:emit_event(make(value, forced and { state_change = true } or nil))
  sent[key] = { value = value, at = now }
  return true
end

--- Forget what was sent, so every row goes out forced on its next emit (a
--- profile move leaves the new profile's cloud record empty).
function poll.repaint(device)
  set_field(device, poll.F_ROWS_SENT, nil)
end

--- Emit the switch value (`force` answers a command).
function poll.emit_switch(device, value, force, deps)
  return poll.emit_row(device, "switch", "switch", value, force, deps)
end

--- Emit a list of `{ cap, attr, value }` rows.
function poll.emit_rows(device, list, force, deps)
  local n = 0
  for _, r in ipairs(list or {}) do
    if poll.emit_row(device, r.cap, r.attr, r.value, force, deps) then
      n = n + 1
    end
  end
  return n
end

--- The switch value the device currently stands at.
function poll.current_switch(device)
  return model.switch_value(field(device, poll.F_POWER))
end

--- Whether the driver's own wake window is open.
function poll.waking(device, deps)
  return model.waking(field(device, poll.F_WAKING_UNTIL), now_of(deps))
end

local function set_online(device, online)
  local method = online and device.online or device.offline
  if type(method) == "function" then
    pcall(method, device)
  end
end

--- One poll. Returns the `/status` state, or nil with the error kind.
function poll.once(driver, device, deps)
  deps = deps or {}
  local log = logger()
  local ok, body, kind = client.get_status(device, deps)
  if not ok then
    -- A server that stays away logs once, not every poll.
    if field(device, poll.F_LAST_ERROR) ~= kind then
      set_field(device, poll.F_LAST_ERROR, kind)
      if kind == "unconfigured" then
        log.warn(string.format("%s: server address not set, or PS5 IP not configured on the server", device.id))
      else
        log.info(string.format("%s: status failed: %s", device.id, tostring(kind)))
      end
    end
    set_online(device, false)
    poll.emit_rows(device, rows.from_failure(kind, field(device, poll.F_LAST_SEEN), poll.waking(device, deps), now_of(deps)), false, deps)
    -- The server may have moved (DHCP): look for it again, within the
    -- cooldown, unless the address is pinned by preference.
    if kind == "unreachable" then
      pcall(discovery.research, driver, device, deps)
    end
    return nil, kind
  end
  if field(device, poll.F_LAST_ERROR) ~= nil then
    log.info(device.id .. ": server reachable again")
    set_field(device, poll.F_LAST_ERROR, nil)
  end
  set_online(device, true)
  local state = body.state
  local now = now_of(deps)
  set_field(device, poll.F_POWER, state, true)
  set_field(device, poll.F_LAST_SEEN, now, true)
  if state == "on" and poll.waking(device, deps) then
    -- The wake landed: the optimistic "on" is now the truth.
    set_field(device, poll.F_WAKING_UNTIL, nil, true)
  end
  poll.emit_switch(device, model.switch_value(state), false, deps)
  poll.emit_rows(device, rows.from_status(body, now, poll.waking(device, deps), version), false, deps)
  return state
end

local function cancel(driver, device, key)
  local timer = field(device, key)
  if timer and driver then
    pcall(function() driver:cancel_timer(timer) end)
  end
  set_field(device, key, nil)
end

--- Start (or restart) the periodic poll from the `pollInterval` preference.
function poll.start(driver, device, deps)
  cancel(driver, device, poll.F_POLL_TIMER)
  local interval = model.poll_interval(device.preferences, poll.DEFAULT_INTERVAL)
  local timer = driver:call_on_schedule(interval, function()
    poll.once(driver, device, deps)
  end, "ps5-poll")
  set_field(device, poll.F_POLL_TIMER, timer)
  return interval
end

function poll.stop(driver, device)
  cancel(driver, device, poll.F_POLL_TIMER)
  cancel(driver, device, poll.F_WAKE_TIMER)
end

--- Poll every `WAKE_POLL_SECONDS` while the wake window is open. When it
--- closes with the console still not on, the optimistic "on" springs back.
function poll.follow_wake(driver, device, deps)
  deps = deps or {}
  cancel(driver, device, poll.F_WAKE_TIMER)
  local function step()
    set_field(device, poll.F_WAKE_TIMER, nil)
    local state = poll.once(driver, device, deps)
    if state == "on" then
      return
    end
    if poll.waking(device, deps) then
      local timer = driver:call_with_delay(model.WAKE_POLL_SECONDS, step, "ps5-wake-follow")
      set_field(device, poll.F_WAKE_TIMER, timer)
      return
    end
    logger().warn(string.format("%s: PS5 did not come up within %ds", device.id, model.WAKE_WINDOW_SECONDS))
    set_field(device, poll.F_WAKING_UNTIL, nil, true)
    poll.emit_switch(device, poll.current_switch(device), true, deps)
    poll.emit_row(device, "power", "powerState", rows.power(field(device, poll.F_POWER), false), true, deps)
  end
  local timer = driver:call_with_delay(model.WAKE_POLL_SECONDS, step, "ps5-wake-follow")
  set_field(device, poll.F_WAKE_TIMER, timer)
end

return poll
