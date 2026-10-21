local h = require "helpers"
local Driver = require "st.driver"
local discovery = require "discovery"

local T = {}

function T.test_next_identity()
  local dni, label = discovery.next_identity({})
  h.assert_equal(dni, "ps5-control-1")
  h.assert_equal(label, "PS5")
  dni, label = discovery.next_identity({ { device_network_id = "ps5-control-1" }, { device_network_id = "other" } })
  h.assert_equal(dni, "ps5-control-2")
  h.assert_equal(label, "PS5 2")
  dni = discovery.next_identity({ { device_network_id = "ps5-control-2" } })
  h.assert_equal(dni, "ps5-control-1", "fills the gap")
end

function T.test_handle_creates_a_lan_device()
  local driver = Driver("t")
  discovery.handle(driver, {}, function() return true end)
  local spec = driver.created[1]
  h.assert_equal(spec.type, "LAN")
  h.assert_equal(spec.device_network_id, "ps5-control-1")
  h.assert_equal(spec.profile, "ps5.v1")
  h.assert_equal(spec.label, "PS5")
  h.assert_equal(spec.manufacturer, "Protomothis")
end

return T
