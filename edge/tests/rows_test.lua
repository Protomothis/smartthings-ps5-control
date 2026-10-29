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

function T.test_clock_and_ago()
  h.assert_equal(rows.clock_of("2026-10-28T12:23:18.4126078+09:00"), "12:23", "the server zone, not the hub zone")
  h.assert_equal(rows.clock_of("yesterday"), "")
  h.assert_equal(rows.clock_of(nil), "")
  h.assert_equal(rows.ago(1000, 1030), "30초 전")
  h.assert_equal(rows.ago(1000, 1000 + 5 * 60), "5분 전")
  h.assert_equal(rows.ago(1000, 1000 + 3 * 3600), "3시간 전")
  h.assert_equal(rows.ago(nil, 1000), "")
end

function T.test_last_wake()
  local text, outcome = rows.last_wake(nil)
  h.assert_equal(text, "없음")
  h.assert_equal(outcome, "none")
  text, outcome = rows.last_wake({ at = "2026-10-28T12:23:18+09:00", method = "network", outcome = "success" })
  h.assert_equal(text, "12:23 · 네트워크 · 성공")
  h.assert_equal(outcome, "success")
  text, outcome = rows.last_wake({ method = "bluetooth", outcome = "timeout" })
  h.assert_equal(text, "블루투스 · 응답 없음")
  h.assert_equal(outcome, "timeout")
  outcome = select(2, rows.last_wake({ method = "bluetooth", outcome = "weird" }))
  h.assert_equal(outcome, "error")
end

function T.test_last_standby()
  local text, outcome = rows.last_standby(nil)
  h.assert_equal(text, "없음")
  h.assert_equal(outcome, "none")
  text, outcome = rows.last_standby({ at = "2026-10-28T11:26:51+09:00", outcome = "success" })
  h.assert_equal(text, "11:26 · 성공")
  h.assert_equal(outcome, "success")
  text, outcome = rows.last_standby({ at = "2026-10-28T23:52:10+09:00", outcome = "error",
    error = "remoteplay: console answered HTTP 403 (80108b10: Remote Play is already in use on the console)" })
  h.assert_equal(text, "23:52 · 실패 · Remote Play 사용 중")
  h.assert_equal(outcome, "error")
  text = rows.last_standby({ outcome = "error", error = "remoteplay: read response: read tcp 1.2.3.4:5->6.7.8.9:9295: read: connection reset by peer" })
  h.assert_equal(text, "실패 · PS5 가 연결을 끊음")
  text, outcome = rows.last_standby({ outcome = "timeout", error = "standby was sent but the console still reports on" })
  h.assert_equal(text, "응답 없음 · 아직 켜짐")
  h.assert_equal(outcome, "timeout")
  h.assert_equal(rows.standby_reason("something quite unexpected happened here and it goes on"), "something quite unexpected happened here…", "40 chars then an ellipsis")
  h.assert_equal(rows.standby_reason(""), "")
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
  h.assert_equal(text, "Broadcom · 컨트롤러·PS5 주소 미설정")
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
      last_standby = { at = "2026-10-28T11:26:51+09:00", outcome = "success" },
      waking = false,
    },
  }
  local list = rows.from_status(body, 1000, false, "0.1.0")
  h.assert_equal(find(list, "power", "powerState"), "standby")
  h.assert_equal(find(list, "info", "summary"), "휴식 모드 · 확인 12:23")
  h.assert_equal(find(list, "info", "connection"), "ok")
  h.assert_equal(find(list, "info", "lastSeen"), "12:23")
  h.assert_equal(find(list, "wake", "lastWake"), "11:25 · 블루투스 · 성공")
  h.assert_equal(find(list, "wake", "lastOutcome"), "success")
  h.assert_equal(find(list, "standby", "lastStandby"), "11:26 · 성공")
  h.assert_equal(find(list, "standby", "lastOutcome"), "success")
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
  h.assert_equal(find(bare, "standby", "lastOutcome"), "none")
end

function T.test_from_failure()
  local list = rows.from_failure("unreachable", nil, false)
  h.assert_equal(find(list, "info", "connection"), "unreachable")
  h.assert_equal(find(list, "info", "summary"), "서버 응답 없음")
  h.assert_nil(find(list, "power", "powerState"), "power rows untouched")
  list = rows.from_failure("unauthorized", 1000, true, 1000 + 120)
  h.assert_equal(find(list, "info", "connection"), "unauthorized")
  h.assert_equal(find(list, "info", "summary"), "시크릿 불일치 · 마지막 응답 2분 전")
  h.assert_equal(find(list, "power", "powerState"), "waking")
  list = rows.from_failure("busy", nil, false)
  h.assert_equal(find(list, "info", "connection"), "unreachable", "other kinds fold into unreachable")
end

return T
