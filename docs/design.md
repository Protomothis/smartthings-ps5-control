# 설계 메모

2026-10-21 착수. 원 계획은 아래 "초기 요구사항" 절에 그대로 두고, 결정 사항은 위에 쌓는다.

## 결정 사항

### 주소 추출은 설정 페이지의 WebHID 로 (2026-10-21)

서버에 듀얼센스를 꽂거나 파이썬을 쓰는 대신, 설정 페이지에서 크롬 WebHID 로 데스크탑에 USB 연결된
듀얼센스를 직접 연다.

- `navigator.hid.requestDevice({filters:[{vendorId:0x054C, productId:0x0CE6}, {vendorId:0x054C, productId:0x0DF2}]})`
  (DualSense, DualSense Edge). DualShock 4 (0x05C4/0x09CC) 는 report `0x12` 16 바이트, 오프셋은 같음
- `device.receiveFeatureReport(0x09)` → 20 바이트 DataView. 바이트 0 은 report ID
  - 컨트롤러 BD_ADDR: 바이트 1..6 역순
  - PS5 BD_ADDR: 바이트 10..15 역순
- 브라우저가 파싱해서 `PUT /api/config` 로 저장. 서버는 USB 를 전혀 만지지 않는다
- 제약: 크로미움 계열만. **보안 컨텍스트 필요** (HTTPS 또는 localhost). `http://192.168.1.50:포트` 그대로는 막힘
  - 선택지 A: 컨테이너가 자체서명 인증서로 HTTPS 도 서비스 (가장 단순, 한 번 예외 허용)
  - 선택지 B: `chrome://flags/#unsafely-treat-insecure-origin-as-secure` 에 주소 등록
  - 선택지 C: 팡골린 뒤에 내부 전용 도메인으로 두고 LAN 에서만 접근 허용
  - 어느 쪽이든 설정 페이지는 LAN 전용 + basic auth

### wake 흐름 (pywakepsx-on-bt 이식)

```
hci0 up (ioctl HCIDEVUP, EALREADY 무시)
Read_Local_Version(0x1001) → manufacturer → 전략 선택 (없으면 UnsupportedError)
Read_BD_ADDR(0x1009) → 원래 주소 기억
반복 (기본 3회, 2초 간격):
  벤더 쓰기(주소 LE 6바이트) → Command Complete status 0 확인 → 필요 시 HCI_Reset(0x0C03)
  Create_Connection(0x0405): addr LE, 0xCC18, R2(0x02), 0x00, clock 0x0000, role switch 0x01
  이벤트 대기 (기본 6초):
    Command Status ≠0 → 즉시 거부 (error)
    Connection Complete status → 0x00/0x13/0x16 성공, 0x04/0x10 timeout, 기타 error
    status 0x00 이면 Disconnect(0x0406, 0x13) 보내고 종료
    이벤트 없음 → local timeout
defer: 원래 주소로 벤더 쓰기 (실패 시 로그 + Result.Restored=false)
```

- raw HCI 소켓 필터: type_mask = 1<<EVENT, event_mask 전부. 벤더 이벤트(0xFF) 는 커널 필터 구조상 못 받음
- 소켓은 단계마다 새로 연다. HCI_Reset 뒤 소켓 상태가 무효가 될 수 있어서
- 모든 패킷 빌더/파서는 OS 독립이라 Windows 에서 테스트됨. 소켓은 `//go:build linux`

### 칩셋별 BD_ADDR 쓰기

| 제조사 | SIG ID | opcode | 파라미터 | 리셋 | 비고 |
|---|---|---|---|---|---|
| Broadcom | 0x000F | 0xFC01 | addr LE | 필수 | 평점 4/4 |
| Cypress | 0x0131 | 0xFC01 | addr LE | 시도 | RPi 내장 |
| Intel | 0x0002 | 0xFC31 | addr LE | 필수 | 리셋 없으면 주소 반영 안 됨 |
| MediaTek | 0x0046 | 0xFC1A | addr LE | 불필요 | btmtk_set_bdaddr |
| TI | 0x000D | 0xFC06 | addr LE | 시도 | 미검증 |
| Atheros / Qualcomm ROME | 0x0045 / 0x00E0 | 0xFC0B | `01 01 00 06` + addr LE | 시도 | 미검증. WCN6855+ 는 0xFC14, 미지원 |
| CSR | 0x000A | 0xFC00 BCCMD | 25바이트 PS-key 프레임 | WARM_RESET | 미구현. 필요해지면 pywakepsx-on-bt `strategies/csr.py` 이식 |
| Realtek | 0x005D | 없음 | - | - | `/lib/firmware/rtl_bt/<chip>_config.bin` 에 15바이트 blob (sig `55 AB 23 87`, 오프셋 0x0030 Gen4 / 0x0044 Gen3 / 0x003C Gen1-2) 쓰고 btusb 재적재. 컨테이너에서는 호스트 작업이 필요해 범위 밖 |

### 상태 조회

- UDP 9302, 페이로드 `SRCH * HTTP/1.1\ndevice-discovery-protocol-version:00030010\n`
- 응답 첫 줄 `HTTP/1.1 200 Ok` / `HTTP/1.1 620 Server Standby`, 이후 `key:value` 줄 (host-id, host-type, host-name, host-request-port, system-version)
- 타임아웃(기본 2초) 안에 0.5초마다 재전송. 무응답은 에러가 아니라 `off`
- 출발 IP 가 대상과 다른 데이터그램은 무시

### 3단계 HTTP 서버 (2026-10-21 구현)

- 단일 바이너리 유지: `ps5ctl serve` 서브커맨드. 이미지 `CMD ["serve"]`
- `GET /status` → discovery 즉시 실행, 캐시 없음 (Edge 드라이버가 30~60초 폴링). 미설정 409, 로컬 오류 502
- `POST /wake` → `wake.Wake`. `sync.Mutex.TryLock` 으로 동시 실행 금지(423). 클라이언트가 끊어도 원복까지
  끝나도록 `context.WithoutCancel` + 예산(시도수×(대기+간격)+15초). 응답 코드: 200 성공 / 504 무응답 /
  501 동글 미지원 / 500 권한 / 502 기타. 마지막 결과는 `GET /api/wake/last`
- 설정 페이지·`/api/config`·`/api/info` 만 basic auth. 자격은 환경변수 `PS5_AUTH_USER`/`PS5_AUTH_PASS`.
  없으면 그 경로들은 503 (열어두지 않음). sha256 후 constant-time 비교
- `/data/config.json` 은 임시파일 + rename 으로 원자적 저장. 부분 파일은 기본값으로 채움. 알 수 없는 필드 거부
- HTTPS: WebHID 가 보안 컨텍스트를 요구해서 선택지 A(자체서명)를 채택. `/data/tls/{cert,key}.pem` 없으면
  ECDSA P-256 10년짜리 생성, SAN = localhost + 127.0.0.1 + ::1 + 호스트의 모든 IP + `PS5_TLS_HOSTS`.
  HTTP(:8580) 는 Edge 드라이버용으로 같이 열어둠 (Lua 쪽에서 자체서명 TLS 검증을 피하려고)
- 설정 페이지는 임베드된 단일 HTML. `http.FileServer` 는 `/index.html` 을 `./` 로 301 시키므로 바이트를
  직접 서빙. 시스템 테마 따름
- WebHID: `requestDevice` 필터 Sony 0x054C × {0x0CE6, 0x0DF2, 0x05C4, 0x09CC}. `receiveFeatureReport(0x09|0x12)`
  결과는 크롬이 report ID 를 앞에 붙이므로 `b[0]==reportId` 면 오프셋 1, 아니면 0 으로 양쪽 다 허용
- 검증: 유닛 테스트(httptest + 가짜 deps) 와 Windows 루프백에서 1회 수동 호출(401/200, 설정 저장, status off,
  wake 는 리눅스 아님 502). 실제 WebHID 읽기와 실기 wake 는 미검증

### 5단계 Edge 드라이버 골격 (2026-10-21)

smartthings-pc-control 의 `edge/` 에서 가져온 것: 테스트 러너(`tools/lua.js` fengari, `tests/run.lua`,
`tests/syntax.lua`), 허브 모듈 mock(`tests/mocks/`), 주석 제거 패키저(`tools/build.js`), `.luacheckrc`,
CI 흐름(node + C Lua 5.3 이중 실행, luacheck, `edge-v*` 태그 패키징). 가져오지 않은 것: 커스텀 capability,
SSDP 검색, push 구독, 프로필 생성기, i18n — 이 드라이버는 표준 `switch`·`refresh` 만 쓰므로 필요 없다.

- **검색은 SSDP (2026-10-28 에 교체)**. 처음엔 "검색 때마다 `ps5-control-N` 장치를 만들고 주소는 환경설정"이었다.
  지금은 서버의 `internal/ssdp` 응답기(pc-control 의 `service/stapi/ssdp.go` 이식)가 UDP 1900 멀티캐스트 그룹을 모든
  IPv4 인터페이스에서 받아 `urn:smartthings-ps5-control:device:ps5:1` 검색에 unicast 200 OK 로 답한다. 응답의
  USN 은 `uuid:<instance_id>::<st>`, LOCATION 은 `http://<받은 인터페이스 IP>:8580/ssdp.json`, 그리고 드라이버가 두 번째
  요청 없이 쓰도록 `PS5CTL-ID` · `PS5CTL-NAME`(Remote Play 닉네임 또는 "PS5") · `PS5CTL-PORT` 헤더를 더했다.
  MX 는 3초로 클램프하고 그 안에서 무작위 지연, 출발 IP 당 1초에 한 번만 답한다. `instance_id` 는 `config.Open` 이 한 번
  만들어 저장하고 `Set` 이 못 바꾼다(드라이버 DNI `ps5-control-<id>` 의 근거).
- **드라이버 쪽**: `discovery.lua` 가 cosock UDP 로 M-SEARCH(MX 2) 를 보내고 4초 동안 응답을 모아 id 로 합친다. id 가
  이미 있는 장치면 만들지 않는다. 응답이 없고 장치도 없을 때만 `ps5-control-manual-<time>-<n>` 장치 하나를 만들어
  환경설정 경로를 남긴다. 주소는 생성 때 pending 테이블 → `added` 의 `adopt` 로 필드(`discovered_ip/port/name`)에
  저장. **`serverAddress` 환경설정이 비어 있지 않으면 언제나 그 값이 우선**(`discovery.address`)이고, 비어 있으면
  필드. 폴링이 `unreachable` 로 끝나면 pinned 가 아닌 장치는 5분에 한 번 자기 id 로 재검색해 주소가 바뀌었으면
  옮기고 바로 폴링한다(`discovery.research`, poll.lua 가 `once` 훅을 꽂아 줌).
- 실측 대기: 허브의 M-SEARCH 가 호스트 네트워크 컨테이너에 닿는지(`answered search from …` 로그), 이 PC 에서 본
  "새 UDP 리스너의 첫 데이터그램 유실"이 허브-서버 사이에는 없는지(허브는 검색을 2~3번 반복하므로 괜찮을 것).
- **스위치 = 전원.** `on` 만 켜짐, `standby`·`off` 는 꺼짐. 휴식 모드를 구분해 보여 주려면 커스텀 capability 가
  필요하고(네임스페이스 발급·허브 캐시 함정), v1 에서는 하지 않는다.
- **켜기 = `POST /wake` 동기 호출.** 서버가 최대 ~30초 걸리므로 드라이버 쪽 luasocket TIMEOUT 을 60초로 둔다
  (cosock 코루틴이라 다른 장치를 막지 않는다). 스위치는 즉시 `on` 으로 그리고, 200 이면 90초 동안 5초마다
  `/status` 로 확인, 그래도 `on` 이 아니면 되돌린다. 504/5xx 면 바로 되돌린다. 423(진행 중)이면 그냥 따라간다.
- **끄기는 무시**하고 현재 값을 `state_change = true` 로 다시 보내 토글을 되돌린다(앱은 이벤트가 와야 회전을
  멈춘다 — 플랫폼 노트 "상세 화면").
- **이벤트 예산**: 같은 값은 보내지 않고 10분에 한 번만 순환 재전송(`model.should_emit`). 장치당 줄이 하나라
  pc-control 의 묶음 분할은 필요 없다.
- **서버 불통과 PS5 꺼짐은 다르다.** HTTP 가 안 오면 `device:offline()`, 오면 `online()`. 스위치 값은 마지막
  값을 유지한다.
- 실측: `GameConsole` 은 API 가 거절(NotValidValue, 2026-10-28) → `Others`. 드라이버는 Dev 채널(Protomothis Dev)에 올라감(driverId d0946d3a…). 대기: `device:online/offline` 표시, 60초 TIMEOUT 이 cosock
  에서 실제로 먹는지, 깨우기 뒤 PS5 가 9302 에 답하기까지의 시간(90초 창의 근거).

### Remote Play: 휴식 모드 전환과 네트워크 깨우기 (2026-10-21, v0.1.0 에 포함)

끄기(휴식 모드)를 지원하려면 PS5 의 Remote Play 프로토콜이 필요하고, 같은 등록 키로 휴식 모드에서 네트워크 깨우기까지
된다. 그래서 블루투스는 완전 종료 상태 전용이 되고 동글 칩셋 리스크의 비중이 내려간다.

- **출처**: dhleong/playactor (ISC) 의 PS5 부분만 `internal/remoteplay` 로 이식. 키 테이블은 `keys_ps5.go` 로 생성.
  검증은 playactor 의 테스트 벡터(chiaki 가 만든 등록 본문 포함)와 playactor 를 직접 실행해 뽑은 세션 벡터
  (`scratchpad` 의 `vectors.ts`), 그리고 가짜 콘솔로. 실기 PS5 검증은 사용자 몫.
- **흐름**: 등록(설정 페이지: 계정 ID + PS5 의 PIN → `POST /sie/ps5/rp/sess/rgst`)
  → 등록 키·RP-Key 를 `config.json` 의 `remote_play` 에 저장. 깨우기는 UDP 9302 `WAKEUP`, 휴식 모드는
  `/sess/init` → `/sess/ctrl` 세션에서 Login 후 Standby 패킷.
- **서버 통합** (`internal/server/remoteplay.go`): 서버는 프로토콜 패키지를 직접 모르고 `RemotePlayClient` 인터페이스
  (config 타입)로만 쓴다. `cmd/ps5ctl/remoteplay.go` 가 어댑터. 그래서 서버 테스트는 가짜로 돈다.
- **`POST /wake` 순서**: 상태 조회 → `on` 이면 아무것도 안 함(`method: none`) → `standby` 이고 등록돼 있으면 네트워크
  깨우기 후 최대 30초 상태 폴링 → 실패하거나 `off` 면 블루투스. `?method=` 로 고정 가능. 응답에 `method` 와
  블루투스 세부(`bluetooth`)를 같이 준다. 블루투스 성공 뒤 상태 대기는 하지 않는다(드라이버가 폴링).
- **`POST /standby`**: 켜져 있지 않으면 즉시 성공(세션 안 엶). 세션 뒤 최대 30초 동안 `standby`/`off` 를 기다리고
  못 보면 504. 깨우기와 같은 mutex.
- **API 시크릿**: `api_secret` 을 정하면 `/wake` `/standby` 에 `X-PS5-Secret` 필수(sha256 constant-time 비교).
  `/status` 는 열어 둔다. 드라이버 환경설정 `secret`.
- **등록 키 보호**: `GET /api/config` 는 `regist_key`/`rp_key` 를 `***` 로, `PUT /api/config` 는 `remote_play` 를
  무시하고 저장된 것을 유지(`Store.Set`). 등록·해제는 `/api/remoteplay/*` (basic auth) 로만.
- **계정 ID 는 직접 입력 (2026-10-27)**: 처음엔 PSN OAuth(리다이렉트 URL 의 코드 → 토큰 → `user_id`)를 뒀지만
  뺐다. 소니 인증 서버와 Remote Play 앱의 공개 client secret 에 기대는 경로가 서버에 남고, 한 번 하는 등록에 비해
  화면이 복잡했다. 이제 base64 계정 ID(12자) 또는 숫자 `user_id` 를 받아 `remoteplay.NormalizeAccountID` 가 base64 로
  맞춘다. 서버는 PSN 과 전혀 통신하지 않는다.
- **등록 잠금**: 등록돼 있으면 `POST /api/remoteplay/register` 는 409. 바꾸려면 `DELETE /api/remoteplay` 로 먼저
  해제한다. 설정 페이지는 등록 정보를 읽기 전용으로 보이고 해제는 한 번 더 확인한다.
- **드라이버**: 끄기 = `POST /standby`(즉시 off, 실패 시 되돌림), `offAction` 환경설정(`standby`/`ignore`),
  `secret` 환경설정. `/wake` 의 네트워크 경로는 드라이버가 몰라도 된다.
- 실측 대기: 등록 검색(UDP 9295 바인드)이 호스트 네트워크 컨테이너에서 되는지, 세션 `ctrl` 응답 뒤 패킷 스트림,
  PIN 등록 뒤 PS5 가 보이는 닉네임, 휴식 모드 전환까지 걸리는 시간, 로그인 비밀번호 사용자의 `ErrPasscode`.

### 로깅 (2026-10-21)

- `internal/logx`: `log/slog` 핸들러 하나가 stderr(컨테이너 로그) · 회전 파일 · 메모리 링(500줄)에 같은 줄을 쓴다.
  줄 형식 `2026-10-21 15:04:05.123 INFO  wake: message key=value`. `component` 속성이 접두어.
- 회전: `/data/logs/ps5ctl.log` 1 MiB 를 넘기 전에 `.1`·`.2` 로 밀고 셋만 남긴다(최대 3 MiB). 한 줄이 상한보다
  커도 통째로 쓴다. 재시작 시 기존 파일 크기를 이어 센다.
- 레벨: 기본 INFO. HTTP 접근 로그는 DEBUG(5xx 는 ERROR, 401 뺀 4xx 는 WARN)라 드라이버의 30초 폴링이 파일을
  채우지 않는다. `PS5_LOG_LEVEL=debug` 로 켠다. `/api/logs*` 요청 자체는 접근 로그에서 뺀다(패널 폴링).
- 페이지: `GET /api/logs?after=<seq>&limit=` 로 링에서 증분 수신(3초), 레벨 필터는 클라이언트에서, 다운로드는
  `GET /api/logs/download`(회전분 오래된 순 + 현재). 둘 다 basic auth.
- 구성요소: server, http, config, wake, standby, remoteplay. wake 패키지의 `Log func` 콜백은 그대로 두고 서버가
  component=wake 로 감싼다(패키지는 slog 를 모른다).

## 초기 요구사항 (2026-10-21 원문)

### 목표
홈서버(리눅스)에서 도는 경량 Docker 컨테이너. PS5 전원 상태 조회 + 완전 종료/휴식 모드에서 wake.
이후 스마트싱스 Edge 드라이버(Lua)가 LAN에서 이 API를 호출해 연동할 예정.
메모리 최소화가 최우선 → 전부 Go, 단일 바이너리, distroless/scratch 이미지.
Remote Play 프로토콜(standby)은 이번 범위에서 제외 (필요하면 HDMI-CEC로 대체).

### 기능 1: 상태 조회
- PS5 IP의 UDP 9302로 전송: `SRCH * HTTP/1.1\ndevice-discovery-protocol-version:00030010\n`
- 응답 "200 Ok" = 켜짐, "620 Server Standby" = 휴식 모드, 무응답(타임아웃) = 꺼짐
- 인증 불필요. 휴식 모드 응답은 PS5에서 "인터넷 연결 유지" 옵션이 켜져 있어야 함

### 기능 2: 블루투스 wake
- 원리: PS5에 페어링된 듀얼센스의 BD_ADDR로 동글 주소를 스푸핑 → PS5 BD_ADDR로 HCI Create Connection → 동글 주소 원복
- 주소 변경은 표준 HCI 명령이 없어 벤더 전용 명령 필요 (Broadcom/Cypress/Intel만 지원 예정)
- 참고 구현: PyPI의 pywakepsXonbt, pywakepsx-on-bt 소스를 읽고 Go로 이식
- Go에서 HCI raw 소켓: golang.org/x/sys/unix 사용
- 실패 대비 재시도 로직 포함

### 주소 추출 (1회성)
- 듀얼센스를 USB 연결 시 HID feature report로 컨트롤러 주소 + 페어링된 PS5 주소를 얻을 수 있음
- /dev/hidraw ioctl로 Go 구현하거나, 1회만 파이썬 라이브러리로 추출해도 됨 → WebHID 로 대체 (위 결정 참고)

### API / UI
- GET /status, POST /wake
- 설정 페이지: PS5 IP, 컨트롤러 BT 주소, PS5 BT 주소, hci 장치(hci0 등), 테스트 버튼
- 설정은 /data 볼륨에 JSON 저장
- 설정 페이지만 basic auth, LAN 전용 (외부 노출 금지)

### 컨테이너
- --net=host, NET_ADMIN 권한 (HCI 접근 및 UDP)

### 미검증 리스크 (가장 먼저 확인)
- 블루투스 동글 칩셋이 아직 미확인 (Realtek/저가 CSR이면 스푸핑 불가 가능성 높음)
- 완전 종료 상태에서 스푸핑 wake가 실제로 되는지 미확인
- PS5와 서버 간 블루투스 거리

### 진행 순서
1. 상태 조회 CLI로 검증
2. wake CLI로 동글 칩셋 확인 + 실제 wake 테스트
3. HTTP 서버 + 설정 페이지
4. Dockerfile (최소 이미지)
5. 이후 Edge 드라이버: Lua, cosock으로 LAN HTTP, 30~60초 폴링, switch capability (off는 무시)
