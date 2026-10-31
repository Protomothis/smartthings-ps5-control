local h = require "helpers"
local Driver = require "st.driver"
local model = require "model"
local poll = require "poll"

local T = {}

local function status(state)
  return { code = 200, body = string.format('{"state":"%s"}', state) }
end

function T.test_once_emits_switch_and_marks_online()
  local device, clock = h.device(), h.clock()
  local http = h.http({ status("on") })
  local state = poll.once(nil, device, h.deps(http, clock))
  h.assert_equal(state, "on")
  h.assert_equal(device.online_calls, 1)
  h.assert_equal(device.fields[poll.F_POWER], "on")
  local values = h.switch_values(device)
  h.assert_equal(#values, 1)
  h.assert_equal(values[1], "on")
  h.assert_true(h.last_switch_forced(device), "first emit of a row per run is forced")
  -- a changed value later in the run goes out unforced
  local http2 = h.http({ status("standby") })
  clock.advance(30)
  poll.once(nil, device, h.deps(http2, clock))
  h.assert_equal(h.switch_values(device)[2], "off")
  h.assert_false(h.last_switch_forced(device))
end

function T.test_unchanged_value_is_not_resent_until_rotation()
  local device, clock = h.device(), h.clock()
  local http = h.http({ status("standby") })
  local deps = h.deps(http, clock)
  poll.once(nil, device, deps)
  clock.advance(30)
  poll.once(nil, device, deps)
  h.assert_equal(#h.switch_values(device), 1, "second poll with the same value")
  clock.advance(model.REEMIT_SECONDS)
  poll.once(nil, device, deps)
  h.assert_equal(#h.switch_values(device), 2, "rotation re-send")
end

function T.test_server_down_marks_offline_and_keeps_switch()
  local device, clock = h.device(), h.clock()
  local http = h.http({ status("on"), { err = "timeout" } })
  local deps = h.deps(http, clock)
  poll.once(nil, device, deps)
  local state, kind = poll.once(nil, device, deps)
  h.assert_nil(state)
  h.assert_equal(kind, "unreachable")
  h.assert_equal(device.offline_calls, 1)
  h.assert_equal(device.fields[poll.F_POWER], "on", "last known state stays")
  h.assert_equal(#h.switch_values(device), 1)
end

function T.test_start_uses_preference_interval_and_restarts()
  local driver = Driver("t")
  local device = h.device({ serverAddress = "1.2.3.4", pollInterval = "60" })
  h.assert_equal(poll.start(driver, device), 60)
  local first = device.fields[poll.F_POLL_TIMER]
  h.assert_equal(first.interval, 60)
  device.preferences.pollInterval = "10"
  h.assert_equal(poll.start(driver, device), 10)
  h.assert_true(first.cancelled, "old schedule cancelled")
  poll.stop(driver, device)
  h.assert_nil(device.fields[poll.F_POLL_TIMER])
end

function T.test_switch_stays_on_while_waking()
  -- The server answers the wake before the console finishes booting; a poll
  -- in that window still says standby and must not flip the tile back.
  local device, clock = h.device(), h.clock()
  local http = h.http({ status("standby"), status("on") })
  local deps = h.deps(http, clock)
  device.fields[poll.F_WAKING_UNTIL] = clock.now() + model.WAKE_WINDOW_SECONDS
  device.fields[poll.F_ROWS_SENT] = { ["switch.switch"] = { value = "on", at = clock.now() } } -- the optimistic "on"
  poll.once(nil, device, deps)
  h.assert_equal(#h.switch_values(device), 0, "standby during the window is not emitted as off")
  h.assert_equal(device.fields[poll.F_POWER], "standby", "the real state is still stored")
  poll.once(nil, device, deps)
  h.assert_nil(device.fields[poll.F_WAKING_UNTIL], "on closes the window")
end

function T.test_follow_wake_until_on()
  local driver, device, clock = Driver("t"), h.device(), h.clock()
  local http = h.http({ status("standby"), status("on") })
  local deps = h.deps(http, clock)
  device.fields[poll.F_WAKING_UNTIL] = clock.now() + model.WAKE_WINDOW_SECONDS
  poll.follow_wake(driver, device, deps)
  clock.advance(5)
  driver:fire("ps5-wake-follow")              -- standby: re-armed
  h.assert_equal(#driver.timers, 2)
  clock.advance(5)
  driver.timers[2].fn()                        -- on: done
  h.assert_equal(#driver.timers, 2, "no further timer")
  h.assert_nil(device.fields[poll.F_WAKING_UNTIL], "window cleared")
  h.assert_equal(h.switch_values(device)[#h.switch_values(device)], "on")
end

function T.test_follow_wake_gives_up_and_springs_back()
  local driver, device, clock = Driver("t"), h.device(), h.clock()
  local http = h.http({ status("standby") })
  local deps = h.deps(http, clock)
  device.fields[poll.F_WAKING_UNTIL] = clock.now() + model.WAKE_WINDOW_SECONDS
  device.fields[poll.F_ROWS_SENT] = { ["switch.switch"] = { value = "on", at = clock.now() } } -- the optimistic "on"
  poll.follow_wake(driver, device, deps)
  clock.advance(model.WAKE_WINDOW_SECONDS + 1)
  driver:fire("ps5-wake-follow")
  h.assert_nil(device.fields[poll.F_WAKING_UNTIL])
  local values = h.switch_values(device)
  h.assert_equal(values[#values], "off")
  h.assert_true(h.last_switch_forced(device), "spring back is forced")
end

return T
