local h = require "helpers"
local rows = require "rows"

local T = {}

local function find(list, cap, attr)
  for _, r in ipairs(list) do
    if r.cap == cap and r.attr == attr then
      return r.value
    end
  end
  return nil
end

function T.test_power_mapping()
  h.assert_equal(rows.power("on", false), "on")
  h.assert_equal(rows.power("on", true), "on", "on beats waking")
  h.assert_equal(rows.power("standby", true), "waking")
  h.assert_equal(rows.power("off", true), "waking")
  h.assert_equal(rows.power("standby", false), "standby")
  h.assert_equal(rows.power("off", false), "off")
  h.assert_equal(rows.power("garbage", false), "unknown")
  h.assert_equal(rows.power(nil, false), "unknown")
end

function T.test_parse_time()
  local t = rows.parse_time("2026-10-28T12:23:18.4126078+09:00")
  h.assert_true(type(t) == "number")
  -- the same instant written in UTC parses to the same epoch
  h.assert_equal(rows.parse_time("2026-10-28T03:23:18Z"), t)
  h.assert_nil(rows.parse_time("yesterday"))
  h.assert_nil(rows.parse_time(nil))
end

function T.test_last_wake()
  local text, outcome = rows.last_wake(nil)
  h.assert_equal(text, "없음")
  h.assert_equal(outcome, "none")
  text, outcome = rows.last_wake({ at = "2026-10-28T12:23:18+09:00", method = "network", outcome = "success" })
  h.assert_contains(text, "네트워크 · 성공")
  h.assert_equal(outcome, "success")
  text, outcome = rows.last_wake({ method = "bluetooth", outcome = "timeout" })
  h.assert_equal(text, "블루투스 · 응답 없음")
  h.assert_equal(outcome, "timeout")
  _, outcome = rows.last_wake({ method = "bluetooth", outcome = "weird" })
  h.assert_equal(outcome, "error")
end

function T.test_bluetooth()
  local text, state = rows.bluetooth(nil)
  h.assert_equal(state, "unchecked")
  h.assert_equal(text, "확인 전")
  text, state = rows.bluetooth({ checked = true, error = "hci: permission denied" })
  h.assert_equal(state, "error")
  h.assert_contains(text, "permission denied")
  text, state = rows.bluetooth({ checked = true, supported = false, manufacturer = "Realtek" })
  h.assert_equal(state, "unsupported")
  h.assert_equal(text, "Realtek · 주소 변경 미지원")
  text, state = rows.bluetooth({ checked = true, supported = true, configured = false, manufacturer = "Broadcom" })
  h.assert_equal(state, "unconfigured")
  text, state = rows.bluetooth({ checked = true, supported = true, configured = true, manufacturer = "Broadcom" })
  h.assert_equal(state, "ready")
  h.assert_equal(text, "Broadcom · 준비됨")
end

function T.test_remote_play_and_versions()
  local text, state = rows.remote_play({ registered = true, nickname = "PS5-054" })
  h.assert_equal(text, "등록됨 · PS5-054")
  h.assert_equal(state, "registered")
  text, state = rows.remote_play({ registered = false })
  h.assert_equal(state, "unregistered")
  h.assert_contains(text, "미등록")
  h.assert_equal(rows.versions("00f33fe", "0.1.0"), "서버 00f33fe · 드라이버 0.1.0")
  h.assert_equal(rows.versions(nil, "0.1.0"), "서버 ? · 드라이버 0.1.0")
end

function T.test_from_status_builds_every_row()
  local body = {
    state = "standby",
    checked_at = "2026-10-28T12:23:18+09:00",
    server = {
      version = "00f33fe",
      bluetooth = { checked = true, supported = true, configured = true, manufacturer = "Broadcom" },
      remote_play = { registered = true, nickname = "PS5-054" },
      last_wake = { at = "2026-10-28T11:25:11+09:00", method = "bluetooth", outcome = "success" },
      waking = false,
    },
  }
  local list = rows.from_status(body, 1000, false, "0.1.0")
  h.assert_equal(find(list, "power", "powerState"), "standby")
  h.assert_contains(find(list, "info", "summary"), "휴식 모드 · 확인 ")
  h.assert_equal(find(list, "info", "connection"), "ok")
  h.assert_true(find(list, "info", "lastSeen") ~= nil)
  h.assert_contains(find(list, "wake", "lastWake"), "블루투스 · 성공")
  h.assert_equal(find(list, "wake", "lastOutcome"), "success")
  h.assert_equal(find(list, "bluetooth", "state"), "ready")
  h.assert_equal(find(list, "remote_play", "state"), "registered")
  h.assert_equal(find(list, "version", "versions"), "서버 00f33fe · 드라이버 0.1.0")
  -- the server's own waking flag counts too
  body.server.waking = true
  h.assert_equal(find(rows.from_status(body, 1000, false, "0.1.0"), "power", "powerState"), "waking")
  -- no server block at all (an older server) still yields every row
  local bare = rows.from_status({ state = "on" }, 1000, false, "0.1.0")
  h.assert_equal(find(bare, "power", "powerState"), "on")
  h.assert_equal(find(bare, "bluetooth", "state"), "unchecked")
  h.assert_equal(find(bare, "remote_play", "state"), "unregistered")
  h.assert_equal(find(bare, "wake", "lastOutcome"), "none")
end

function T.test_from_failure()
  local list = rows.from_failure("unreachable", nil, false)
  h.assert_equal(find(list, "info", "connection"), "unreachable")
  h.assert_equal(find(list, "info", "summary"), "서버 응답 없음")
  h.assert_nil(find(list, "power", "powerState"), "power rows untouched")
  list = rows.from_failure("unauthorized", 1000, true)
  h.assert_equal(find(list, "info", "connection"), "unauthorized")
  h.assert_contains(find(list, "info", "summary"), "시크릿 불일치 · 마지막 확인 ")
  h.assert_equal(find(list, "power", "powerState"), "waking")
  list = rows.from_failure("busy", nil, false)
  h.assert_equal(find(list, "info", "connection"), "unreachable", "other kinds fold into unreachable")
end

return T
