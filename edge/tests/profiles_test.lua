local h = require "helpers"
local profiles = require "profiles"

local T = {}

local function device_on(name)
  local d = h.device()
  d.id = "dev-" .. tostring(name)
  d.profile = name and { name = name } or nil
  d.updates = {}
  function d:try_update_metadata(meta)
    self.updates[#self.updates + 1] = meta
    return true
  end
  return d
end

function T.test_ensure_migrates_old_and_unknown_once()
  profiles.reset()
  local old = device_on("ps5.v1")
  h.assert_equal(profiles.ensure(old), profiles.CURRENT)
  h.assert_equal(old.updates[1].profile, profiles.CURRENT)
  h.assert_equal(old.fields[profiles.F_NAME], profiles.CURRENT)
  h.assert_nil(profiles.ensure(old), "once per run")
  h.assert_equal(#old.updates, 1)

  local unknown = device_on(nil)
  h.assert_equal(profiles.ensure(unknown), profiles.CURRENT, "no readable name counts as oldest")
end

function T.test_ensure_leaves_current_alone()
  profiles.reset()
  local cur = device_on(profiles.CURRENT)
  h.assert_nil(profiles.ensure(cur))
  h.assert_equal(#cur.updates, 0)
  h.assert_equal(cur.fields[profiles.F_NAME], profiles.CURRENT, "remembered")
  -- what the hub reports wins over the stored name: a migration is only
  -- requested, so a device the hub still shows on ps5.v1 is asked again
  -- (once per run) even though the request was remembered
  local stored = device_on("ps5.v1")
  stored.fields[profiles.F_NAME] = profiles.CURRENT
  h.assert_equal(profiles.ensure(stored), profiles.CURRENT)
  -- without the hub's view the stored name is all there is
  local blind = device_on(nil)
  blind.fields[profiles.F_NAME] = profiles.CURRENT
  h.assert_nil(profiles.ensure(blind))
end

function T.test_ensure_survives_a_refusing_hub()
  profiles.reset()
  local d = device_on("ps5.v1")
  function d:try_update_metadata() error("hub says no") end
  h.assert_nil(profiles.ensure(d))
  h.assert_nil(d.fields[profiles.F_NAME])
end

return T
