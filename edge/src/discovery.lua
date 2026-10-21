-- "Scan nearby" creates one PS5 device per scan; the server address is a
-- device preference. There is no network search: the service does not
-- announce itself (no SSDP) and a scan that finds nothing would otherwise
-- never let the user add the device.

local discovery = {}

discovery.PROFILE = "ps5.v1"
discovery.DNI_PREFIX = "ps5-control-"
discovery.MANUFACTURER = "Protomothis"
discovery.MODEL = "PS5 Control"

--- The next free `ps5-control-N` and the label to go with it.
function discovery.next_identity(devices)
  -- The prefix has a `-`, a pattern magic character: escape it.
  local pattern = "^" .. discovery.DNI_PREFIX:gsub("%p", "%%%0") .. "(%d+)$"
  local used = {}
  for _, d in pairs(devices or {}) do
    local n = tonumber(tostring(d.device_network_id or ""):match(pattern))
    if n then
      used[n] = true
    end
  end
  local n = 1
  while used[n] do
    n = n + 1
  end
  return discovery.DNI_PREFIX .. n, n == 1 and "PS5" or ("PS5 " .. n)
end

--- The hub's discovery handler.
function discovery.handle(driver, _opts, _should_continue)
  local dni, label = discovery.next_identity(driver:get_devices())
  driver:try_create_device({
    type = "LAN",
    device_network_id = dni,
    label = label,
    profile = discovery.PROFILE,
    manufacturer = discovery.MANUFACTURER,
    model = discovery.MODEL,
    vendor_provided_label = label,
  })
end

return discovery
