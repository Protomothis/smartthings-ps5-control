-- Polling: ask the server for the PS5 state on a timer, emit the switch, and
-- mark the device online/offline after the server itself (not the console)
-- answered or did not.

local capabilities = require "st.capabilities"
local client = require "client"
local discovery = require "discovery"
local model = require "model"

local poll = {}

-- discovery.research polls after moving a device; it gets poll.once through
-- this hook so discovery never requires poll.
discovery.use({ once = function(driver, device, deps) return poll.once(driver, device, deps) end })

poll.DEFAULT_INTERVAL = 30

-- Device fields. The switch bookkeeping persists so a driver restart does not
-- re-send the same value.
poll.F_POWER = "ps5_power"            -- last `/status` state: on / standby / off / unknown
poll.F_SWITCH_SENT = "switch_sent"    -- last switch value emitted
poll.F_SWITCH_SENT_AT = "switch_sent_at"
poll.F_WAKING_UNTIL = "waking_until"  -- epoch while a wake window is open
poll.F_POLL_TIMER = "poll_timer"      -- not persisted: the schedule handle
poll.F_WAKE_TIMER = "wake_timer"      -- not persisted: the fast-poll handle
poll.F_LAST_ERROR = "last_error"      -- not persisted: the failure kind last logged

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

--- Emit the switch value unless it was sent recently with the same value.
--- `force` answers a command: the app waits for an event even when nothing
--- changed (the toggle springs back on an ignored "off").
function poll.emit_switch(device, value, force, deps)
  local now = now_of(deps)
  if not model.should_emit(value, field(device, poll.F_SWITCH_SENT), field(device, poll.F_SWITCH_SENT_AT), now, force) then
    return false
  end
  device:emit_event(capabilities.switch.switch(value, force and { state_change = true } or nil))
  set_field(device, poll.F_SWITCH_SENT, value, true)
  set_field(device, poll.F_SWITCH_SENT_AT, now, true)
  return true
end

--- The switch value the device currently stands at.
function poll.current_switch(device)
  return model.switch_value(field(device, poll.F_POWER))
end

local function set_online(device, online)
  local method = online and device.online or device.offline
  if type(method) == "function" then
    pcall(method, device)
  end
end

--- One poll. Returns the `/status` state, or nil with the error kind.
function poll.once(_driver, device, deps)
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
    -- The server may have moved (DHCP): look for it again, within the
    -- cooldown, unless the address is pinned by preference.
    if kind == "unreachable" then
      pcall(discovery.research, _driver, device, deps)
    end
    return nil, kind
  end
  if field(device, poll.F_LAST_ERROR) ~= nil then
    log.info(device.id .. ": server reachable again")
    set_field(device, poll.F_LAST_ERROR, nil)
  end
  set_online(device, true)
  local state = body.state
  set_field(device, poll.F_POWER, state, true)
  poll.emit_switch(device, model.switch_value(state), false, deps)
  if state == "on" and model.waking(field(device, poll.F_WAKING_UNTIL), now_of(deps)) then
    -- The wake landed: the optimistic "on" is now the truth.
    set_field(device, poll.F_WAKING_UNTIL, nil, true)
  end
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
    if model.waking(field(device, poll.F_WAKING_UNTIL), now_of(deps)) then
      local timer = driver:call_with_delay(model.WAKE_POLL_SECONDS, step, "ps5-wake-follow")
      set_field(device, poll.F_WAKE_TIMER, timer)
      return
    end
    logger().warn(string.format("%s: PS5 did not come up within %ds", device.id, model.WAKE_WINDOW_SECONDS))
    set_field(device, poll.F_WAKING_UNTIL, nil, true)
    poll.emit_switch(device, poll.current_switch(device), true, deps)
  end
  local timer = driver:call_with_delay(model.WAKE_POLL_SECONDS, step, "ps5-wake-follow")
  set_field(device, poll.F_WAKE_TIMER, timer)
end

return poll
