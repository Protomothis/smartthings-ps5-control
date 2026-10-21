-- Pure decisions: what the switch shows for a server state, and when a switch
-- value goes out again. No hub objects, so every rule is unit-testable.

local model = {}

-- The hub's event budget is per device and silently drops everything once it
-- is spent (smartthings-pc-control, platform notes "이벤트 예산"), so a value
-- that did not change is not re-sent on every poll. It is re-sent once in a
-- while so a dropped event heals on its own.
model.REEMIT_SECONDS = 600

-- After a wake the console takes a few seconds to answer discovery; keep
-- polling fast for this long before giving up on the optimistic "on".
model.WAKE_WINDOW_SECONDS = 90
model.WAKE_POLL_SECONDS = 5

--- The `switch` value for a `/status` state. Rest mode is "off": from the
--- user's point of view the console is not on, and the only thing the switch
--- can do about either is wake it.
function model.switch_value(state)
  if state == "on" then
    return "on"
  end
  return "off"
end

--- Whether `value` should be emitted now, given the last value sent and when.
--- Forced emits (answers to a command) always go.
function model.should_emit(value, last_value, last_at, now, force)
  if force then
    return true
  end
  if last_value ~= value then
    return true
  end
  if type(last_at) ~= "number" or type(now) ~= "number" then
    return true
  end
  return now - last_at >= model.REEMIT_SECONDS
end

--- Whether a wake window started at `waking_until` is still open.
function model.waking(waking_until, now)
  return type(waking_until) == "number" and type(now) == "number" and now < waking_until
end

--- The poll interval in seconds for the `pollInterval` preference.
function model.poll_interval(prefs, default)
  local n = tonumber((prefs or {}).pollInterval)
  if n == nil or n < 5 then
    return default
  end
  return math.floor(n)
end

return model
