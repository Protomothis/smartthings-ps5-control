-- SmartThings Edge driver for PS5 Control: entry point only. The logic lives
-- in client/model/poll/handlers/discovery so it runs without a hub in tests.

local Driver = require "st.driver"
local capabilities = require "st.capabilities"
local log = require "log"

local discovery = require "discovery"
local handlers = require "handlers"
local poll = require "poll"
local version = require "driver_version"

-- Custom capabilities only resolve once the account owner has created them.
-- Missing ones are logged and their rows skipped; switch and refresh work
-- either way.
local missing = poll.use_capabilities(capabilities)
if #missing > 0 then
  log.warn("custom capabilities not available yet: " .. table.concat(missing, ", "))
end

local ps5_driver = Driver("smartthings-ps5-control", {
  discovery = discovery.handle,
  lifecycle_handlers = {
    init = handlers.init,
    added = handlers.added,
    removed = handlers.removed,
    infoChanged = handlers.info_changed,
  },
  capability_handlers = {
    [capabilities.switch.ID] = {
      [capabilities.switch.commands.on.NAME] = handlers.switch_on,
      [capabilities.switch.commands.off.NAME] = handlers.switch_off,
    },
    [capabilities.refresh.ID] = {
      [capabilities.refresh.commands.refresh.NAME] = handlers.refresh,
    },
  },
})

log.info("starting smartthings-ps5-control driver " .. version)
ps5_driver:run()

-- The hub ignores what this chunk returns; tests inspect the handlers on it.
return ps5_driver
