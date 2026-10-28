local h = require "helpers"
local model = require "model"

local T = {}

function T.test_switch_value()
  h.assert_equal(model.switch_value("on"), "on")
  h.assert_equal(model.switch_value("standby"), "off")
  h.assert_equal(model.switch_value("off"), "off")
  h.assert_equal(model.switch_value("unknown"), "off")
  h.assert_equal(model.switch_value(nil), "off")
end

function T.test_should_emit()
  -- first time, changed value, forced
  h.assert_true(model.should_emit("on", nil, nil, 100, false))
  h.assert_true(model.should_emit("on", "off", 100, 100, false))
  h.assert_true(model.should_emit("on", "on", 100, 100, true))
  -- same value, recently sent: no
  h.assert_false(model.should_emit("on", "on", 100, 100 + model.REEMIT_SECONDS - 1, false))
  -- same value, but old enough: rotation re-send
  h.assert_true(model.should_emit("on", "on", 100, 100 + model.REEMIT_SECONDS, false))
end

function T.test_waking()
  h.assert_true(model.waking(200, 150))
  h.assert_false(model.waking(200, 200))
  h.assert_false(model.waking(nil, 150))
end

function T.test_poll_interval()
  h.assert_equal(model.poll_interval({ pollInterval = "60" }, 30), 60)
  h.assert_equal(model.poll_interval({ pollInterval = 10 }, 30), 10)
  h.assert_equal(model.poll_interval({}, 30), 30)
  h.assert_equal(model.poll_interval({ pollInterval = "1" }, 30), 30)
  h.assert_equal(model.poll_interval(nil, 30), 30)
end

return T
