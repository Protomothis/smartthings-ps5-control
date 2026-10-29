# SmartThings Edge 드라이버

ps5-control 서버를 위한 SmartThings Edge 드라이버다. 허브 안에서 로컬로 돌면서
서버의 `GET /status` · `POST /wake` · `POST /standby` 를 호출한다. 표준 `switch` · `refresh` 에
커스텀 capability 일곱(전원 상태 · 상태 · 마지막 깨우기 · 마지막 휴식 모드 · 블루투스 동글 · Remote Play · 버전)을 더한다.

- **스위치 = PS5 전원** — 켜짐이면 켜짐, 휴식 모드·완전 종료면 꺼짐
- **스위치 켜기 = 깨우기** — 서버가 휴식 모드면 네트워크(Remote Play), 아니면 블루투스로 깨운다. 끝날 때까지 기다렸다가 90초 동안 5초마다 확인. 안 켜지면 되돌아간다
- **스위치 끄기 = 휴식 모드** — 서버의 Remote Play 등록이 필요하다. 없으면 토글이 되돌아간다. 환경설정 `스위치 끄기 동작` 을 `무시` 로 두면 끄기를 받지 않는다
- **API 시크릿** — 서버 설정 페이지의 API 시크릿을 환경설정에 넣으면 `X-PS5-Secret` 헤더로 보낸다
- **IP 를 손으로 넣지 않는다** — 주변 기기 검색이 SSDP 로 서버를 찾아 주소가 채워진 장치를 만들고, DHCP 로 주소가 바뀌면 따라간다. `서버 주소` 환경설정을 채우면 그 값이 우선한다
- **서버에 닿지 않으면 오프라인** 표시. PS5 가 꺼진 것과는 다르다

## 요구 사항

| | |
|---|---|
| 허브 | Edge 드라이버를 실행할 수 있는 SmartThings 허브 |
| 서버 | ps5-control 컨테이너(HTTP `:8580`)가 허브와 같은 LAN 에 있을 것 |
| 설정 | 서버 설정 페이지에서 PS5 IP · 컨트롤러 주소 · PS5 블루투스 주소를 먼저 저장할 것 |

## 설치

1. 채널 **Protomothis** 초대 링크 <https://bestow-regional.api.smartthings.com/invite/Kr2zNWYgpp2A> 를 열고 [Enroll] → 허브를 고른다
2. 채널의 **SmartThings PS5 Control** 을 [Install] 한다
3. 서버 컨테이너가 켜진 상태에서 SmartThings 앱 **[+] → 기기 추가 → 주변 기기 검색** → 서버가 답하면 주소가 채워진 `PS5` 장치가 생긴다. 허브와 서버는 같은 서브넷이어야 한다(공유기는 멀티캐스트를 넘기지 않는다)
4. 장치 설정에 서버 설정 페이지에서 정한 **API 시크릿**을 넣는다. 검색이 안 되는 환경이면 **서버 주소**(예: `192.168.1.50`)를 직접 넣는다. 포트는 기본 8580

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
| `src/init.lua` | 핸들러 등록, 커스텀 capability 로드 |
| `src/caps.lua` | 커스텀 capability id(네임스페이스 `numbersystem53811`). 정의·프레젠테이션·번역은 `capabilities/`, 계정에 올리는 건 `tools/capabilities.sh` |
| `src/rows.lua` | `/status` 응답 → 화면 줄(순수 함수) |
| `src/profiles.lua` | 프로필 세대(`ps5.vN`)와 이전. 화면을 바꾸면 새 파일·이름 |
| `src/client.lua` | HTTP 호출(`/status` `/wake` `/standby`, 시크릿 헤더)과 응답 코드 → 종류(`unreachable` `unauthorized` `unconfigured` `busy` `unsupported` `timeout` …) |
| `src/model.lua` | 순수 규칙: 상태 → 스위치 값, 재전송 시점, 깨우기 창 |
| `src/poll.lua` | 폴링 타이머, 줄 emit(중복 제거 · 실행마다 첫 전송 강제 · 10분 재전송), 온라인/오프라인, 깨우기 후 빠른 폴링 |
| `src/handlers.lua` | switch on/off · refresh · lifecycle |
| `src/discovery.lua` | SSDP M-SEARCH, 응답 파서, 주소 우선순위(환경설정 > 검색), 실패 뒤 재검색(5분 쿨다운), 장치 생성·adopt |

플랫폼 함정(이벤트 예산, 프로필 화면 고정 등)은 smartthings-pc-control 의
`docs/design/edge-platform-notes.md` 를 따른다. 화면을 바꾸면 `profiles/ps5-vN.yml` 로 이름을 올린다.
