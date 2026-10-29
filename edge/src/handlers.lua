-- Capability and lifecycle handlers. The hub calls them as
-- (driver, device, command); tests pass a fourth `deps` for the fake http
-- and clock.

local client = require "client"
local discovery = require "discovery"
local log = require "log"
local model = require "model"
local poll = require "poll"
local profiles = require "profiles"
local rows = require "rows"
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
  poll.emit_row(device, "power", "powerState", "waking", true, deps)

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
  poll.emit_row(device, "power", "powerState", rows.power(device:get_field(poll.F_POWER), false), true, deps)
end

--- switch.off: rest mode over Remote Play (`POST /standby`) unless the
--- `offAction` preference says to ignore it. The switch goes "off" at once;
--- a refused or unconfirmed standby springs it back. A console that is not
--- on is just answered with its real value.
function handlers.switch_off(driver, device, _cmd, deps)
  deps = deps or {}
  local prefs = device.preferences or {}
  if prefs.offAction == "ignore" then
    log.info(device.id .. ": switch off ignored (offAction = ignore)")
    poll.emit_switch(device, poll.current_switch(device), true, deps)
    return
  end
  if device:get_field(poll.F_POWER) ~= "on" then
    poll.emit_switch(device, poll.current_switch(device), true, deps)
    return
  end
  poll.emit_switch(device, "off", true, deps)
  local ok, body, kind = client.standby(device, deps)
  if ok then
    log.info(string.format("%s: standby %s (%s)", device.id, tostring(body.outcome), tostring(body.state)))
    poll.once(driver, device, deps)
    return
  end
  local detail = type(body) == "table" and body.error or nil
  log.warn(string.format("%s: standby failed: %s%s", device.id, tostring(kind),
    detail and (" (" .. tostring(detail) .. ")") or ""))
  -- Still on as far as we know: the toggle springs back, and a poll paints
  -- the "last rest mode" row with the server's reason right away.
  poll.emit_switch(device, poll.current_switch(device), true, deps)
  poll.once(driver, device, deps)
end

function handlers.refresh(driver, device, _cmd, deps)
  poll.once(driver, device, deps)
end

--- Device lifecycle.
function handlers.init(driver, device, _event, deps)
  log.info(string.format("init %s (driver %s)", device.id, version))
  -- A device keeps the screen it was created with; one on an older profile
  -- moves to the current one, once, and every row is painted again.
  local moved = profiles.ensure(device)
  if moved then
    log.info(string.format("%s: migrated to %s", device.id, moved))
    poll.repaint(device)
  end
  poll.start(driver, device, deps)
  poll.once(driver, device, deps)
end

function handlers.added(_driver, device, _event, deps)
  log.info("added " .. device.id)
  profiles.remember(device, profiles.CURRENT)
  -- A device the search created arrives with the server's address.
  if discovery.adopt(device) then
    log.info(string.format("%s: using discovered address %s", device.id, tostring(device:get_field(discovery.F_IP))))
  end
  -- Paint the tile now; the first poll fills in the real value.
  poll.emit_switch(device, "off", true, deps)
  if not client.device_base_url(device) then
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
