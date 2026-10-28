-- The rows the device shows, computed from a `/status` answer (or from a
-- failure). Pure: no hub objects. Each row is `{ cap = <caps key>, attr =
-- <attribute>, value = ... }`, and poll.lua turns them into events.
--
-- Values that people read are Korean with the English in brackets only
-- where the presentation's enum alternatives already carry both; free-text
-- rows are Korean (the app does not translate attribute values).

local rows = {}

--- Clock text "HH:MM:SS" for an epoch, or "" for nil.
local function clock(epoch)
  if type(epoch) ~= "number" then
    return ""
  end
  return os.date("%H:%M:%S", math.floor(epoch))
end

--- Epoch from an RFC 3339 timestamp like 2026-10-28T12:23:18.41+09:00, in
--- local time; nil when it cannot be read.
function rows.parse_time(s)
  if type(s) ~= "string" then
    return nil
  end
  local y, mo, d, h, mi, sec = s:match("^(%d+)%-(%d+)%-(%d+)T(%d+):(%d+):(%d+)")
  if not y then
    return nil
  end
  local frac = s:match("^%d+%-%d+%-%d+T%d+:%d+:%d+(%.%d+)") or ""
  local rest = s:sub(#("2026-10-28T12:23:18") + #frac + 1)
  local t = os.time({ year = tonumber(y), month = tonumber(mo), day = tonumber(d), hour = tonumber(h), min = tonumber(mi), sec = tonumber(sec) })
  if not t then
    return nil
  end
  -- os.time read the fields as local time; shift by the difference between
  -- the stamp's offset and ours.
  local local_offset = os.difftime(t, os.time(os.date("!*t", t)))
  local offset = 0
  local sign, oh, om = rest:match("^([%+%-])(%d%d):(%d%d)")
  if sign then
    offset = (tonumber(oh) * 3600 + tonumber(om) * 60) * (sign == "-" and -1 or 1)
  end
  return t + local_offset - offset
end

rows.POWER_LABEL = { on = "켜짐", standby = "휴식 모드", off = "꺼짐", waking = "깨우는 중", unknown = "알 수 없음" }

--- The powerState value for a status body (plus the driver's own wake window).
function rows.power(state, waking)
  if state == "on" then
    return "on"
  end
  if waking then
    return "waking"
  end
  if state == "standby" or state == "off" then
    return state
  end
  return "unknown"
end

local METHOD_LABEL = { none = "이미 켜져 있음", network = "네트워크", bluetooth = "블루투스" }
local OUTCOME_LABEL = { success = "성공", timeout = "응답 없음", error = "실패" }

--- The last-wake text and enum from the server record (may be nil).
function rows.last_wake(rec)
  if type(rec) ~= "table" then
    return "없음", "none"
  end
  local parts = {}
  local t = rows.parse_time(rec.at)
  if t then
    parts[#parts + 1] = clock(t)
  end
  parts[#parts + 1] = METHOD_LABEL[rec.method] or tostring(rec.method or "")
  parts[#parts + 1] = OUTCOME_LABEL[rec.outcome] or tostring(rec.outcome or "")
  local outcome = rec.outcome
  if outcome ~= "success" and outcome ~= "timeout" then
    outcome = "error"
  end
  return table.concat(parts, " · "), outcome
end

--- The Bluetooth text and enum from the server's bluetooth block.
function rows.bluetooth(bt)
  bt = bt or {}
  if bt.checked ~= true then
    return "확인 전", "unchecked"
  end
  if bt.error and bt.error ~= "" then
    return "오류 · " .. tostring(bt.error), "error"
  end
  local who = bt.manufacturer and bt.manufacturer ~= "" and bt.manufacturer or (bt.adapter or "동글")
  if bt.supported ~= true then
    return who .. " · 주소 변경 미지원", "unsupported"
  end
  if bt.configured ~= true then
    return who .. " · 컨트롤러·PS5 주소 미설정", "unconfigured"
  end
  return who .. " · 준비됨", "ready"
end

--- The Remote Play text and enum.
function rows.remote_play(rp)
  rp = rp or {}
  if rp.registered == true then
    local name = rp.nickname
    if type(name) == "string" and name ~= "" then
      return "등록됨 · " .. name, "registered"
    end
    return "등록됨", "registered"
  end
  return "미등록 · 휴식 모드 전환 불가", "unregistered"
end

--- The versions text.
function rows.versions(server_version, driver_version)
  local sv = server_version
  if type(sv) ~= "string" or sv == "" then
    sv = "?"
  end
  return "서버 " .. sv .. " · 드라이버 " .. tostring(driver_version)
end

--- All rows for a successful status. `body` is the decoded /status JSON,
--- `now` the epoch, `waking` the driver's wake window.
function rows.from_status(body, now, waking, driver_version)
  body = body or {}
  local srv = body.server or {}
  local seen = rows.parse_time(body.checked_at) or now
  local out = {}
  local function add(cap, attr, value) out[#out + 1] = { cap = cap, attr = attr, value = value } end
  local power = rows.power(body.state, waking or srv.waking == true)
  add("power", "powerState", power)
  add("info", "summary", (rows.POWER_LABEL[power] or power) .. " · 확인 " .. clock(seen))
  add("info", "connection", "ok")
  add("info", "lastSeen", clock(seen))
  local wake_text, wake_outcome = rows.last_wake(srv.last_wake)
  add("wake", "lastWake", wake_text)
  add("wake", "lastOutcome", wake_outcome)
  local bt_text, bt_state = rows.bluetooth(srv.bluetooth)
  add("bluetooth", "summary", bt_text)
  add("bluetooth", "state", bt_state)
  local rp_text, rp_state = rows.remote_play(srv.remote_play)
  add("remote_play", "summary", rp_text)
  add("remote_play", "state", rp_state)
  add("version", "versions", rows.versions(srv.version, driver_version))
  return out
end

local FAIL_LABEL = {
  unreachable = "서버 응답 없음",
  unauthorized = "시크릿 불일치",
  unconfigured = "서버 설정 없음",
  incompatible = "버전 불일치",
  busy = "서버 바쁨",
  error = "서버 오류",
  timeout = "서버 응답 없음",
}

--- The rows for a failed status: only what the failure says. The power rows
--- are left alone (the console may well be fine), except that a wake window
--- still shows "waking".
function rows.from_failure(kind, last_seen, waking)
  local conn = kind
  if conn ~= "unauthorized" and conn ~= "unconfigured" and conn ~= "incompatible" then
    conn = "unreachable"
  end
  local text = FAIL_LABEL[kind] or FAIL_LABEL.unreachable
  if last_seen then
    text = text .. " · 마지막 확인 " .. clock(last_seen)
  end
  local out = {
    { cap = "info", attr = "summary", value = text },
    { cap = "info", attr = "connection", value = conn },
  }
  if waking then
    out[#out + 1] = { cap = "power", attr = "powerState", value = "waking" }
  end
  return out
end

return rows
