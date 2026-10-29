-- Profile names and the migration between them.
--
-- A device's screen is fixed by the capability presentations at creation
-- time; only a profile with a new name regenerates it (smartthings-pc-control,
-- platform notes "프로필과 화면 생성"). So every screen change is a new file
-- `profiles/ps5-vN.yml` with `name: ps5.vN`; the old files stay in the
-- package for devices not yet moved, and `ensure` moves each device on its
-- first `init` after the update.

local profiles = {}

profiles.VERSION = 3
profiles.CURRENT = "ps5.v" .. profiles.VERSION
-- Every name this driver ever shipped, oldest first.
profiles.KNOWN = { "ps5.v1", "ps5.v2", profiles.CURRENT }

-- Remembered at creation: `device.profile.name` is not always exposed.
profiles.F_NAME = "profile_name"

-- Migrations attempted this run, by device id: once per run is enough and
-- a hub that keeps refusing must not be hammered every init.
local attempted = {}

--- The name a device is on: the stored field, then the hub's profile, then
--- nil (treated as the oldest).
function profiles.name_of(device)
  if type(device) ~= "table" then
    return nil
  end
  if type(device.get_field) == "function" then
    local stored = device:get_field(profiles.F_NAME)
    if type(stored) == "string" and stored ~= "" then
      return stored
    end
  end
  local p = device.profile
  if type(p) == "table" and type(p.name) == "string" and p.name ~= "" then
    return p.name
  end
  return nil
end

function profiles.remember(device, name)
  if type(device) == "table" and type(device.set_field) == "function" then
    device:set_field(profiles.F_NAME, name, { persist = true })
  end
end

function profiles.is_current(name)
  return name == profiles.CURRENT
end

--- Move the device to the current profile when it is on an older one (or
--- on one we cannot read, which an older driver created). Returns the new
--- name when a migration was requested, else nil.
function profiles.ensure(device)
  if type(device) ~= "table" then
    return nil
  end
  local key = tostring(device.id or device.device_network_id or device)
  if attempted[key] then
    return nil
  end
  attempted[key] = true
  local name = profiles.name_of(device)
  if profiles.is_current(name) then
    profiles.remember(device, name)
    return nil
  end
  local ok, err = pcall(function()
    return device:try_update_metadata({ profile = profiles.CURRENT })
  end)
  if not ok then
    local log = pcall(require, "log") and require("log") or nil
    if log then
      log.warn(string.format("could not migrate %s to %s: %s", key, profiles.CURRENT, tostring(err)))
    end
    return nil
  end
  profiles.remember(device, profiles.CURRENT)
  return profiles.CURRENT
end

--- Forget the attempts (tests).
function profiles.reset()
  attempted = {}
end

return profiles
