# smartthings-ps5-control

홈서버(리눅스)에서 PS5 전원 상태를 조회하고, 완전 종료·휴식 모드에서 블루투스로 깨우는
경량 컨테이너. 이후 스마트싱스 Edge 드라이버가 LAN 에서 이 API 를 호출한다.

- 전부 Go, 단일 정적 바이너리, `scratch` 이미지, 외부 의존성은 `golang.org/x/sys` 뿐
- Remote Play 프로토콜(standby 전환)은 범위 밖. 끄기가 필요하면 HDMI-CEC 로 대체

## 동작 원리

| 기능 | 방법 |
|---|---|
| 상태 조회 | PS5 IP 의 UDP 9302 로 `SRCH * HTTP/1.1` 탐색 패킷 전송. `200 Ok` = 켜짐, `620 Server Standby` = 휴식 모드, 무응답 = 꺼짐. 인증 불필요. 휴식 모드 응답은 PS5 의 "인터넷에 연결된 상태 유지" 옵션이 켜져 있어야 함 |
| 블루투스 wake | 동글의 BD_ADDR 를 PS5 에 페어링된 듀얼센스 주소로 바꾼 뒤(벤더 HCI 명령) PS5 BD_ADDR 로 `HCI_Create_Connection`. PS5 는 페이지의 출발 주소만 보고 켜지므로 링크키가 없어도 된다. 끝나면 동글 주소 원복 |
| 주소 추출 (1회) | 설정 페이지에서 크롬 WebHID 로 USB 연결된 듀얼센스의 feature report `0x09` 를 읽어 컨트롤러 주소·PS5 주소를 얻어 저장 (3단계에서 구현) |

### 동글 칩셋 지원 (BD_ADDR 변경 명령)

| 제조사 (SIG ID) | 명령 | 리셋 | 검증 |
|---|---|---|---|
| Broadcom (0x000F) | 0xFC01 | 필수 | 상위 구현에서 검증됨 |
| Cypress (0x0131) | 0xFC01 | 시도 | RPi 3/4/5 내장칩 검증됨 |
| Intel (0x0002) | 0xFC31 | 필수 | AX201 검증됨 |
| MediaTek (0x0046) | 0xFC1A | 불필요 | MT7921 검증됨 |
| TI / Atheros / Qualcomm ROME | 0xFC06 / 0xFC0B | 시도 | 커널 소스 기준, 미검증 |
| CSR (0x000A) | BCCMD | - | 미구현 |
| Realtek (0x005D) | 없음 | - | 런타임 변경 불가. 펌웨어 config blob 교체 + 드라이버 재적재 방식만 가능 (별도 검토) |

## 진행 상태

- [x] 1. 상태 조회 CLI (`ps5ctl status`)
- [x] 2. wake CLI (`ps5ctl adapter`, `ps5ctl wake`) — 실기 테스트 전
- [x] 3. HTTP 서버 + 설정 페이지 (`ps5ctl serve`: basic auth, `/data/config.json`, WebHID 주소 추출, 자체서명 HTTPS)
- [ ] 4. 최소 이미지 배포 (`ghcr.io`)
- [ ] 5. 스마트싱스 Edge 드라이버 (Lua, cosock, 30~60초 폴링, switch capability, off 무시)

### 가장 먼저 확인할 리스크

1. 홈서버 블루투스 동글 칩셋 — `ps5ctl adapter` 로 확인. Realtek/저가 CSR 이면 스푸핑 불가
2. 완전 종료 상태에서 스푸핑 wake 가 실제로 되는지
3. PS5 와 서버 사이 블루투스 거리

## 사용법

```bash
go build -o ps5ctl ./cmd/ps5ctl

# 1. 상태 (어느 OS 에서나 동작)
./ps5ctl status --ip 192.168.1.20            # on / standby / off, exit 0 / 2 / 3
./ps5ctl status --ip 192.168.1.20 --json

# 2. 동글 확인 (리눅스, root 또는 CAP_NET_RAW+CAP_NET_ADMIN)
sudo ./ps5ctl adapter --hci hci0

# 3. wake 테스트
#    --controller: 페어링된 듀얼센스 BD_ADDR
#    --ps5:        PS5 블루투스 BD_ADDR (Wi-Fi MAC 아님)
sudo ./ps5ctl wake --hci hci0 --controller AA:BB:CC:DD:EE:FF --ps5 11:22:33:44:55:66
```

wake 결과 해석: `0x00`/`0x13`/`0x16` 은 PS5 가 응답한 것(성공), `0x04` Page Timeout 은
무응답(꺼져 있거나 거리 밖이거나 주소가 페어링 목록에 없음). 기본 3회 재시도, 2초 간격.

## 서버 (`ps5ctl serve`)

```bash
cp .env.example .env          # PS5_AUTH_USER / PS5_AUTH_PASS 설정
docker compose up -d --build
```

호스트 네트워크를 쓰므로 포트 매핑이 없다. 기본 포트와 경로:

| 주소 | 용도 | 인증 |
|---|---|---|
| `http://<서버>:8580/status` | 전원 상태 JSON (`state`: on / standby / off). Edge 드라이버가 폴링 | 없음 (LAN 전용) |
| `http://<서버>:8580/wake` (POST) | wake 실행. 200 성공, 504 무응답, 501 동글 미지원, 423 이미 진행 중, 409 설정 없음 | 없음 (LAN 전용) |
| `http://<서버>:8580/api/adapter` | 동글 칩셋·지원 여부 | 없음 |
| `https://<서버>:8543/` | 설정 페이지 | basic auth |
| `/api/config` (GET/PUT), `/api/info` | 설정 API | basic auth |
| `/healthz` | 헬스체크 | 없음 |

환경변수: `PS5_DATA`(기본 `/data`), `PS5_HTTP_ADDR`(`:8580`, 빈 값이면 끔), `PS5_HTTPS_ADDR`(`:8543`, 빈 값이면 끔),
`PS5_TLS_HOSTS`(인증서 SAN 추가), `PS5_AUTH_USER`/`PS5_AUTH_PASS`(없으면 설정 페이지·설정 API 가 503 으로 닫힘).

HTTPS 는 첫 실행 때 `/data/tls/` 에 자체서명 인증서를 만든다(ECDSA, 10년, SAN 에 localhost 와 서버 IP 전부).
브라우저에서 한 번 예외를 허용하면 된다. 재발급하려면 두 파일을 지우고 재시작.
API 는 LAN 전용이고 외부에 노출하지 않는다.

### 주소 추출 (설정 페이지의 WebHID)

듀얼센스를 **설정 페이지를 여는 PC** 에 USB 로 꽂고 "USB 듀얼센스 읽기" 를 누르면 크롬 WebHID 가
feature report `0x09` (20 바이트) 를 읽어 1~6 바이트의 컨트롤러 주소와 10~15 바이트의 페어링된 PS5 주소를
칸에 채운다(둘 다 역순). 서버에는 아무것도 꽂지 않는다. 조건은 둘이다.

- 크롬·엣지 등 크로미움 계열 (파이어폭스·사파리는 WebHID 없음)
- HTTPS 로 열 것. `http://…:8580/` 에서는 버튼이 비활성화되고 이유를 보여준다

듀얼센스 엣지(0x0DF2)·듀얼쇼크4(report `0x12`) 도 같은 오프셋으로 읽는다.
PS5 주소가 `00:00:00:00:00:00` 이면 아직 PS5 와 블루투스로 페어링된 적이 없는 컨트롤러다.

## 참고 구현

- [FreeTHX/pywakepsx-on-bt](https://github.com/FreeTHX/pywakepsx-on-bt) — 칩셋별 스푸핑 전략과 HCI 흐름의 원본
- [gamahacka/ps5-wake-bluetooth-linux](https://github.com/gamahacka/ps5-wake-bluetooth-linux) — Realtek 동글용 펌웨어 config 우회
- 자세한 설계 메모: [docs/design.md](docs/design.md)
