-- HTTP client for the ps5-control service (README "서버"):
--
--   GET  /status  -> { state = "on" | "standby" | "off" | "unknown", ... }
--   POST /wake    -> 200 woke, 504 no answer, 501 adapter unsupported,
--                    423 another wake running, 409 not configured
--
-- Every call returns `(ok, body, kind)`:
--   ok = true   -> body is the decoded JSON table, kind is nil
--   ok = false  -> kind is one of "unreachable" (no HTTP answer at all),
--                  "unauthorized" (wrong `secret` preference), "unconfigured",
--                  "busy", "unsupported", "timeout", "error", "incompatible";
--                  body may still carry the decoded response
--                  (`{ error = "..." }`).
--
-- No module-level mutable state: the address comes from `device.preferences`,
-- and `deps` lets tests inject a fake http/json/ltn12 instead of cosock.

local VERSION = require "driver_version"

local client = {}

client.DEFAULT_PORT = 8580
-- A status probe answers within the server's own 2 s timeout.
client.STATUS_TIMEOUT = 8
-- A wake is synchronous on the server: up to attempts x (page wait + retry
-- delay), ~30 s with the defaults. luasocket's TIMEOUT is per socket
-- operation, and the server sends nothing until it is done.
client.WAKE_TIMEOUT = 60
-- A standby is a session plus the 30 s confirmation wait, and the server
-- pauses 20 s before its one retry when the console refused the session.
client.STANDBY_TIMEOUT = 100
client.USER_AGENT = "smartthings-ps5-control-edge/" .. VERSION

--- `http://<ip>:<port>` from an explicit address.
function client.url_for(ip, port)
  if type(ip) ~= "string" or ip == "" then
    return nil
  end
  return string.format("http://%s:%d", ip, math.floor(tonumber(port) or client.DEFAULT_PORT))
end

--- `http://<serverAddress>:<serverPort>` from preferences alone, or nil
--- while the address is empty.
function client.base_url(prefs)
  prefs = prefs or {}
  return client.url_for(prefs.serverAddress, prefs.serverPort)
end

--- The URL a device talks to: the `serverAddress` preference first, else the
--- address discovery stored on the device (discovery.address).
function client.device_base_url(device)
  local ip, port = require("discovery").address(device)
  return client.url_for(ip, port)
end

--- Request headers. The `secret` preference travels in `X-PS5-Secret`
--- (the server's api_secret); it is omitted when empty.
function client.headers(prefs)
  prefs = prefs or {}
  local headers = {
    ["User-Agent"] = client.USER_AGENT,
    ["Accept"] = "application/json",
  }
  if type(prefs.secret) == "string" and prefs.secret ~= "" then
    headers["X-PS5-Secret"] = prefs.secret
  end
  return headers
end

-- The request function: `deps.http` (a function or a module with `.request`),
-- else cosock's asyncified socket.http so the blocking call does not stall the
-- driver thread. `timeout` is luasocket's per-operation TIMEOUT.
local function request_fn(deps, timeout)
  local http = deps.http
  if type(http) == "function" then
    return http
  end
  if type(http) == "table" and type(http.request) == "function" then
    http.TIMEOUT = timeout
    return http.request
  end
  local cosock = require "cosock"
  local mod = cosock.asyncify "socket.http"
  mod.TIMEOUT = timeout
  return mod.request
end

--- Map an HTTP status code to a kind, or nil for a success. A non-number is
--- socket.http's error message for a request that got no answer at all.
function client.classify(code)
  if type(code) ~= "number" then
    return "unreachable"
  end
  if code >= 200 and code < 300 then
    return nil
  end
  if code == 401 then
    return "unauthorized"
  end
  if code == 409 then
    return "unconfigured"
  end
  if code == 423 then
    return "busy"
  end
  if code == 501 then
    return "unsupported"
  end
  if code == 504 then
    return "timeout"
  end
  if code == 404 then
    -- Something else answers on that port.
    return "incompatible"
  end
  return "error"
end

--- Perform one request. `opts`: `method`, `path`, `timeout`.
function client.request(device, opts, deps)
  deps = deps or {}
  opts = opts or {}
  local base = client.device_base_url(device)
  if not base then
    return false, nil, "unconfigured"
  end
  local json = deps.json or require "st.json"
  local ltn12 = deps.ltn12 or require "ltn12"

  local chunks = {}
  local req = {
    url = base .. (opts.path or ""),
    method = opts.method or "GET",
    headers = client.headers((device or {}).preferences),
    sink = ltn12.sink.table(chunks),
  }
  if req.method == "POST" then
    -- An empty POST still needs a length, or luasocket sends a chunked
    -- body some servers reject.
    req.headers["Content-Length"] = "0"
  end

  local called, result, code = pcall(request_fn(deps, opts.timeout or client.STATUS_TIMEOUT), req)
  if not called then
    return false, nil, "unreachable"
  end
  if not result then
    return false, nil, "unreachable"
  end

  local raw = table.concat(chunks)
  local body
  if raw ~= "" then
    local decoded_ok, decoded = pcall(json.decode, raw)
    if decoded_ok and type(decoded) == "table" then
      body = decoded
    end
  else
    body = {}
  end

  local kind = client.classify(code)
  if kind then
    return false, body, kind
  end
  if body == nil then
    return false, nil, "incompatible"
  end
  return true, body, nil
end

--- `GET /status`.
function client.get_status(device, deps)
  return client.request(device, { method = "GET", path = "/status", timeout = client.STATUS_TIMEOUT }, deps)
end

--- `POST /wake`. Blocks until the server has finished its attempts.
function client.wake(device, deps)
  return client.request(device, { method = "POST", path = "/wake", timeout = client.WAKE_TIMEOUT }, deps)
end

--- `POST /standby`: rest mode over Remote Play. The server opens a session
--- and then waits for discovery to confirm, up to ~30 s.
function client.standby(device, deps)
  return client.request(device, { method = "POST", path = "/standby", timeout = client.STANDBY_TIMEOUT }, deps)
end

return client
