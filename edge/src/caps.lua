-- Custom capability ids, in one place.
--
-- The namespace is the one SmartThings assigned to the owner's account.
-- SmartThings lower-cases the name part of an id ("ps5Power" becomes
-- ".ps5power"); the definition files keep the camelCase `name`. The hub
-- caches a definition by id for the whole hub, so any change to attributes
-- or enums needs a new id, and a presentation change that must reach the
-- phone does too (smartthings-pc-control, platform notes).

local caps = {}

caps.NAMESPACE = "numbersystem53811"

caps.POWER = caps.NAMESPACE .. ".ps5power"            -- powerState enum, dashboard + condition
caps.INFO = caps.NAMESPACE .. ".ps5info"              -- summary row, connection condition, lastSeen
caps.WAKE = caps.NAMESPACE .. ".ps5wake"              -- lastWake row, lastOutcome condition
caps.STANDBY = caps.NAMESPACE .. ".ps5standby"        -- lastStandby row, lastOutcome condition
caps.BLUETOOTH = caps.NAMESPACE .. ".ps5bluetooth"    -- summary row, state condition
caps.REMOTE_PLAY = caps.NAMESPACE .. ".ps5remoteplay" -- summary row, state condition
caps.VERSION = caps.NAMESPACE .. ".ps5version"        -- versions row

-- Stable short keys -> capability id. `caps.load` returns the same keys.
caps.ids = {
  power = caps.POWER,
  info = caps.INFO,
  wake = caps.WAKE,
  standby = caps.STANDBY,
  bluetooth = caps.BLUETOOTH,
  remote_play = caps.REMOTE_PLAY,
  version = caps.VERSION,
}

--- Resolve the custom capability objects from `st.capabilities`. Indexing
--- with an unknown id raises, so each lookup is guarded: a missing one yields
--- nil for that key instead of taking the driver down. Returns the table and
--- the sorted list of ids that failed.
function caps.load(capabilities)
  local loaded, missing = {}, {}
  for key, id in pairs(caps.ids) do
    local ok, cap = pcall(function() return capabilities[id] end)
    if ok and cap then
      loaded[key] = cap
    else
      missing[#missing + 1] = id
    end
  end
  table.sort(missing)
  return loaded, missing
end

return caps
