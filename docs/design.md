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
