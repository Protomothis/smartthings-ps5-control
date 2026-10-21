# SmartThings Edge 드라이버

ps5-control 서버를 위한 SmartThings Edge 드라이버다. 허브 안에서 로컬로 돌면서
서버의 `GET /status` · `POST /wake` 를 호출한다. 표준 capability(`switch`, `refresh`)만 쓴다.

- **스위치 = PS5 전원** — 켜짐이면 켜짐, 휴식 모드·완전 종료면 꺼짐
- **스위치 켜기 = 깨우기** — 서버가 휴식 모드면 네트워크(Remote Play), 아니면 블루투스로 깨운다. 끝날 때까지 기다렸다가 90초 동안 5초마다 확인. 안 켜지면 되돌아간다
- **스위치 끄기 = 휴식 모드** — 서버의 Remote Play 등록이 필요하다. 없으면 토글이 되돌아간다. 환경설정 `스위치 끄기 동작` 을 `무시` 로 두면 끄기를 받지 않는다
- **API 시크릿** — 서버 설정 페이지의 API 시크릿을 환경설정에 넣으면 `X-PS5-Secret` 헤더로 보낸다
- **서버에 닿지 않으면 오프라인** 표시. PS5 가 꺼진 것과는 다르다

## 요구 사항

| | |
|---|---|
| 허브 | Edge 드라이버를 실행할 수 있는 SmartThings 허브 |
| 서버 | ps5-control 컨테이너(HTTP `:8580`)가 허브와 같은 LAN 에 있을 것 |
| 설정 | 서버 설정 페이지에서 PS5 IP · 컨트롤러 주소 · PS5 블루투스 주소를 먼저 저장할 것 |

## 설치

1. 채널 초대 링크를 열고 [Enroll] → 허브를 고른다 (채널은 아직 만들지 않았다)
2. 채널의 **SmartThings PS5 Control** 을 [Install] 한다
3. SmartThings 앱 **[+] → 기기 추가 → 주변 기기 검색** → `PS5` 장치가 생긴다
4. 장치 설정에 **서버 주소**(예: `192.168.1.50`)와 서버 설정 페이지에서 정한 **API 시크릿**을 넣는다. 포트는 기본 8580

## 개발

```bash
cd edge
bun install                              # 또는 npm install (fengari, Lua 5.3 in JS)
bun tools/lua.js tests/run.lua           # 테스트
bun tools/lua.js tests/syntax.lua        # 전 모듈 컴파일
node tools/build.js                      # 주석을 뗀 패키지 트리 build/edge/
smartthings edge:drivers:package build/edge
```

CI(`.github/workflows/edge.yml`)는 같은 테스트를 node 와 C Lua 5.3 로 두 번 돌리고 luacheck 를 건다.
드라이버 버전은 `src/driver_version.lua` 하나이고 `edge-vX.Y.Z` 태그와 같아야 한다.

### 구조

| 파일 | 역할 |
|---|---|
| `src/init.lua` | 핸들러 등록만 |
| `src/client.lua` | HTTP 호출(`/status` `/wake` `/standby`, 시크릿 헤더)과 응답 코드 → 종류(`unreachable` `unauthorized` `unconfigured` `busy` `unsupported` `timeout` …) |
| `src/model.lua` | 순수 규칙: 상태 → 스위치 값, 재전송 시점, 깨우기 창 |
| `src/poll.lua` | 폴링 타이머, 스위치 emit(중복 제거 + 10분 순환 재전송), 온라인/오프라인, 깨우기 후 빠른 폴링 |
| `src/handlers.lua` | switch on/off · refresh · lifecycle |
| `src/discovery.lua` | 검색 때 `ps5-control-N` 장치 하나 생성 |

플랫폼 함정(이벤트 예산, 프로필 화면 고정 등)은 smartthings-pc-control 의
`docs/design/edge-platform-notes.md` 를 따른다. 화면을 바꾸면 `profiles/ps5-vN.yml` 로 이름을 올린다.
