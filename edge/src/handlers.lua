-- Capability and lifecycle handlers. The hub calls them as
-- (driver, device, command); tests pass a fourth `deps` for the fake http
-- and clock.

local client = require "client"
local log = require "log"
local model = require "model"
local poll = require "poll"
local version = require "driver_version"

local handlers = {}

local function now_of(deps)
  if deps and type(deps.now) == "function" then
    return deps.now()
  end
  return os.time()
end

--- switch.on: wake the console. The switch goes "on" at once so the tile
--- reacts, the server pages the console (blocking, up to ~30 s), and a fast
--- poll follows. A refused or unanswered wake springs the switch back.
function handlers.switch_on(driver, device, _cmd, deps)
  deps = deps or {}
  if device:get_field(poll.F_POWER) == "on" then
    poll.emit_switch(device, "on", true, deps)
    return
  end
  device:set_field(poll.F_WAKING_UNTIL, now_of(deps) + model.WAKE_WINDOW_SECONDS, { persist = true })
  poll.emit_switch(device, "on", true, deps)

  local ok, body, kind = client.wake(device, deps)
  if ok then
    log.info(string.format("%s: wake %s (%s, %s attempt(s))", device.id,
      tostring(body.outcome), tostring(body.status_text), tostring(body.attempts)))
    poll.follow_wake(driver, device, deps)
    return
  end
  if kind == "busy" then
    -- Another wake is already paging; its result shows up on the next polls.
    log.info(device.id .. ": wake already in progress")
    poll.follow_wake(driver, device, deps)
    return
  end
  local detail = type(body) == "table" and (body.error or body.status_text) or nil
  log.warn(string.format("%s: wake failed: %s%s", device.id, tostring(kind),
    detail and (" (" .. tostring(detail) .. ")") or ""))
  device:set_field(poll.F_WAKING_UNTIL, nil, { persist = true })
  poll.emit_switch(device, poll.current_switch(device), true, deps)
end

--- switch.off: there is no way to put a PS5 to rest from here (that would
--- need the Remote Play protocol). Answer with the real value so the toggle
--- springs back instead of spinning.
function handlers.switch_off(_driver, device, _cmd, deps)
  log.info(device.id .. ": switch off is not supported; use the console or HDMI-CEC")
  poll.emit_switch(device, poll.current_switch(device), true, deps)
end

function handlers.refresh(driver, device, _cmd, deps)
  poll.once(driver, device, deps)
end

--- Device lifecycle.
function handlers.init(driver, device, _event, deps)
  log.info(string.format("init %s (driver %s)", device.id, version))
  poll.start(driver, device, deps)
  poll.once(driver, device, deps)
end

function handlers.added(driver, device, _event, deps)
  log.info("added " .. device.id)
  -- Paint the tile now; the first poll fills in the real value.
  poll.emit_switch(device, "off", true, deps)
  if not client.base_url(device.preferences) then
    log.warn(device.id .. ": set the server address in the device settings")
  end
end

function handlers.removed(driver, device)
  log.info("removed " .. device.id)
  poll.stop(driver, device)
end

--- Preferences changed: restart the schedule with the new interval and poll
--- the (possibly new) address right away.
function handlers.info_changed(driver, device, _event, deps)
  poll.start(driver, device, deps)
  poll.once(driver, device, deps)
end

return handlers
