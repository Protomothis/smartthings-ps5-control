local h = require "helpers"
local Driver = require "st.driver"
local handlers = require "handlers"
local model = require "model"
local poll = require "poll"

local T = {}

local function status(state)
  return { code = 200, body = string.format('{"state":"%s"}', state) }
end

local WOKE = { code = 200, body = '{"outcome":"success","status_text":"Remote User Terminated Connection","attempts":1,"restored":true}' }
local NO_ANSWER = { code = 504, body = '{"outcome":"timeout","status_text":"Page Timeout","attempts":3,"restored":true}' }

function T.test_switch_on_wakes_and_follows()
  local driver, device, clock = Driver("t"), h.device(), h.clock()
  device.fields[poll.F_POWER] = "standby"
  local http = h.http({ WOKE, status("on") })
  handlers.switch_on(driver, device, {}, h.deps(http, clock))
  h.assert_equal(http.requests[1].method, "POST")
  h.assert_equal(http.requests[1].url, "http://192.168.1.50:8580/wake")
  local values = h.switch_values(device)
  h.assert_equal(values[1], "on", "optimistic on")
  h.assert_true(h.last_switch_forced(device))
  h.assert_true(type(device.fields[poll.F_WAKING_UNTIL]) == "number")
  h.assert_equal(driver.timers[1].name, "ps5-wake-follow")
  clock.advance(5)
  driver:fire("ps5-wake-follow")
  h.assert_nil(device.fields[poll.F_WAKING_UNTIL], "confirmed on closes the window")
end

function T.test_switch_on_refused_springs_back()
  local driver, device, clock = Driver("t"), h.device(), h.clock()
  device.fields[poll.F_POWER] = "off"
  local http = h.http({ NO_ANSWER })
  handlers.switch_on(driver, device, {}, h.deps(http, clock))
  local values = h.switch_values(device)
  h.assert_equal(values[1], "on")
  h.assert_equal(values[2], "off", "springs back")
  h.assert_true(h.last_switch_forced(device))
  h.assert_nil(device.fields[poll.F_WAKING_UNTIL])
  h.assert_equal(#driver.timers, 0, "no follow-up polls")
end

function T.test_switch_on_busy_still_follows()
  local driver, device, clock = Driver("t"), h.device(), h.clock()
  local http = h.http({ { code = 423, body = '{"error":"a wake is already in progress"}' } })
  handlers.switch_on(driver, device, {}, h.deps(http, clock))
  h.assert_equal(driver.timers[1].name, "ps5-wake-follow")
  h.assert_equal(h.switch_values(device)[1], "on")
end

function T.test_switch_on_when_already_on_only_answers()
  local driver, device, clock = Driver("t"), h.device(), h.clock()
  device.fields[poll.F_POWER] = "on"
  local http = h.http({ WOKE })
  handlers.switch_on(driver, device, {}, h.deps(http, clock))
  h.assert_equal(#http.requests, 0, "no wake request")
  h.assert_equal(h.switch_values(device)[1], "on")
  h.assert_true(h.last_switch_forced(device))
end

function T.test_switch_off_is_ignored_and_answered()
  local device, clock = h.device(), h.clock()
  device.fields[poll.F_POWER] = "on"
  local http = h.http({ WOKE })
  handlers.switch_off(nil, device, {}, h.deps(http, clock))
  h.assert_equal(#http.requests, 0)
  h.assert_equal(h.switch_values(device)[1], "on", "the real value, forced")
  h.assert_true(h.last_switch_forced(device))
end

function T.test_refresh_polls()
  local device, clock = h.device(), h.clock()
  local http = h.http({ status("on") })
  handlers.refresh(nil, device, {}, h.deps(http, clock))
  h.assert_equal(http.requests[1].url, "http://192.168.1.50:8580/status")
  h.assert_equal(h.switch_values(device)[1], "on")
end

function T.test_lifecycle()
  local driver, device, clock = Driver("t"), h.device(), h.clock()
  local http = h.http({ status("standby") })
  local deps = h.deps(http, clock)
  handlers.added(driver, device, {}, deps)
  h.assert_equal(h.switch_values(device)[1], "off", "painted off on add")
  handlers.init(driver, device, {}, deps)
  h.assert_equal(driver.timers[1].name, "ps5-poll")
  h.assert_equal(driver.timers[1].interval, 30)
  h.assert_equal(#http.requests, 1, "init polls once")
  device.preferences.pollInterval = "300"
  handlers.info_changed(driver, device, {}, deps)
  h.assert_true(driver.timers[1].cancelled)
  h.assert_equal(driver.timers[2].interval, 300)
  handlers.removed(driver, device)
  h.assert_true(driver.timers[2].cancelled)
end

function T.test_wake_window_length()
  h.assert_true(model.WAKE_WINDOW_SECONDS >= 60, "long enough for a console booting from off")
end

return T
