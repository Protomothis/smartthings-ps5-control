-- The capability JSON, presentations, translations, caps.lua and the current
-- profile agree with each other. Read with host.readfile (fengari) or io.open.
local h = require "helpers"
local caps = require "caps"
local json = require "st.json"
local profiles = require "profiles"

local T = {}

local DIR = h.SRC_DIR .. "/../capabilities"
local NAMES = { "ps5Power", "ps5Info", "ps5Wake", "ps5Standby", "ps5Bluetooth", "ps5RemotePlay", "ps5Version" }

local function read(path)
  if host and host.readfile then
    return (host.readfile(path))
  end
  local f = io.open(path)
  if not f then
    return nil
  end
  local text = f:read("a")
  f:close()
  return text
end

local function load_json(path)
  local text = read(path)
  if not text then
    return nil
  end
  return json.decode(text)
end

local function enum_of(def, attr)
  local a = def.attributes[attr]
  return a and a.schema.properties.value.enum
end

local function keys_of(alts)
  local set = {}
  for _, a in ipairs(alts or {}) do
    set[a.key] = true
  end
  return set
end

function T.test_definitions_match_caps_lua()
  local text = read(DIR .. "/ps5Power.json")
  if not text then
    return -- stripped package tree without the capabilities folder
  end
  local ids = {}
  for _, id in pairs(caps.ids) do
    ids[id] = true
  end
  for _, name in ipairs(NAMES) do
    local def = load_json(DIR .. "/" .. name .. ".json")
    h.assert_true(def ~= nil, name .. ".json")
    h.assert_equal(def.name, name)
    h.assert_equal(def.id, caps.NAMESPACE .. "." .. name:lower(), name .. " id")
    h.assert_true(ids[def.id] == true, name .. " is listed in caps.lua")
    ids[def.id] = nil
    -- attribute names: letters only, camelCase (the API rejects digits)
    for attr in pairs(def.attributes) do
      h.assert_true(attr:match("^[a-z]+[A-Za-z]*$") ~= nil, name .. "." .. attr)
    end
  end
  h.assert_nil(next(ids), "every caps.lua id has a definition")
end

function T.test_presentations_use_defined_enums_and_one_state_row()
  if not read(DIR .. "/ps5Power.json") then
    return
  end
  for _, name in ipairs(NAMES) do
    local def = load_json(DIR .. "/" .. name .. ".json")
    local pres = load_json(DIR .. "/" .. name .. ".presentation.json")
    h.assert_equal(pres.id, def.id, name .. " presentation id")
    local state_rows = 0
    for _, row in ipairs(pres.detailView or {}) do
      if row.displayType == "state" then
        state_rows = state_rows + 1
        local attr = row.state.label:match("^{{(%w+)%.value}}$")
        h.assert_true(def.attributes[attr] ~= nil, name .. " detail row attr " .. tostring(attr))
        if row.state.alternatives then
          local allowed = enum_of(def, attr)
          for key in pairs(keys_of(row.state.alternatives)) do
            local ok = false
            for _, v in ipairs(allowed or {}) do
              if v == key then ok = true end
            end
            h.assert_true(ok, name .. " detail alternative " .. key)
          end
        end
      end
    end
    h.assert_true(state_rows <= 1, name .. " shows at most one state row (two are drawn half-width)")
    for _, c in ipairs((pres.automation or {}).conditions or {}) do
      local attr = c.list.value:match("^(%w+)%.value$")
      local allowed = enum_of(def, attr)
      h.assert_true(allowed ~= nil, name .. " condition attr " .. tostring(attr) .. " is an enum")
      local set = keys_of(c.list.alternatives)
      for _, v in ipairs(allowed) do
        h.assert_true(set[v] == true, name .. " condition lists enum value " .. v)
      end
    end
  end
end

function T.test_translations_cover_attributes()
  if not read(DIR .. "/ps5Power.json") then
    return
  end
  for _, name in ipairs(NAMES) do
    local def = load_json(DIR .. "/" .. name .. ".json")
    for _, tag in ipairs({ "ko", "en" }) do
      local tr = load_json(DIR .. "/translations/" .. name .. "." .. tag .. ".json")
      h.assert_true(tr ~= nil, name .. " " .. tag)
      h.assert_equal(tr.tag, tag)
      h.assert_true(type(tr.label) == "string" and tr.label ~= "", name .. " " .. tag .. " label")
      for attr in pairs(def.attributes) do
        local a = tr.attributes[attr]
        h.assert_true(a ~= nil and type(a.label) == "string" and a.label ~= "", name .. " " .. tag .. " " .. attr)
      end
    end
  end
end

function T.test_current_profile_lists_every_capability()
  local file = h.SRC_DIR .. "/../profiles/" .. profiles.CURRENT:gsub("%.", "-") .. ".yml"
  local text = read(file)
  h.assert_true(text ~= nil, file)
  h.assert_contains(text, "name: " .. profiles.CURRENT)
  h.assert_contains(text, "- id: switch")
  h.assert_contains(text, "- id: refresh")
  for _, id in pairs(caps.ids) do
    h.assert_contains(text, "- id: " .. id)
  end
  h.assert_contains(text, "name: Switch")
  -- every known profile ships in the package
  for _, name in ipairs(profiles.KNOWN) do
    h.assert_true(read(h.SRC_DIR .. "/../profiles/" .. name:gsub("%.", "-") .. ".yml") ~= nil, name .. " file")
  end
end

return T
