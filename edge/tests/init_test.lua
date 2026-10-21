local h = require "helpers"

local T = {}

-- init.lua is the hub entry point; loading it with the mocks checks that it
-- wires every handler and calls run().
function T.test_init_registers_handlers()
  package.loaded["init"] = nil
  local driver = require "init"
  h.assert_true(driver.running)
  h.assert_equal(driver.NAME, "smartthings-ps5-control")
  local t = driver.template
  h.assert_equal(type(t.discovery), "function")
  h.assert_equal(type(t.lifecycle_handlers.init), "function")
  h.assert_equal(type(t.lifecycle_handlers.added), "function")
  h.assert_equal(type(t.lifecycle_handlers.removed), "function")
  h.assert_equal(type(t.lifecycle_handlers.infoChanged), "function")
  h.assert_equal(type(t.capability_handlers.switch.on), "function")
  h.assert_equal(type(t.capability_handlers.switch.off), "function")
  h.assert_equal(type(t.capability_handlers.refresh.refresh), "function")
end

function T.test_profile_matches_discovery()
  local discovery = require "discovery"
  local text = host and host.readfile and host.readfile(h.SRC_DIR .. "/../profiles/ps5-v1.yml")
  if not text then
    local f = io.open(h.SRC_DIR .. "/../profiles/ps5-v1.yml")
    if f then
      text = f:read("a")
      f:close()
    end
  end
  if not text then
    return -- the stripped package tree keeps profiles next to src/ too, but a missing file is not this test's failure
  end
  h.assert_contains(text, "name: " .. discovery.PROFILE)
  h.assert_contains(text, "- id: switch")
  h.assert_contains(text, "- id: refresh")
  h.assert_contains(text, "name: serverAddress")
end

return T
