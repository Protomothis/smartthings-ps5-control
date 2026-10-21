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
- [ ] 3. HTTP 서버 + 설정 페이지 (basic auth, `/data/config.json`, WebHID 주소 추출)
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

컨테이너로 실행할 때는 `--net=host` 와 `NET_ADMIN`, `NET_RAW` 가 필요하다 (`compose.yml` 참고).

## 주소 추출 (임시, 설정 페이지 전까지)

듀얼센스를 PC 에 USB 로 꽂고 HID feature report `0x09` (20 바이트) 를 읽으면
1~6 바이트가 컨트롤러 주소, 10~15 바이트가 페어링된 PS5 주소다 (둘 다 리틀엔디언, 역순으로 읽음).
리눅스 `hidraw` 또는 `dualsensectl` 로 읽을 수 있다. 설정 페이지가 생기면 크롬에서 바로 읽는다.

## 참고 구현

- [FreeTHX/pywakepsx-on-bt](https://github.com/FreeTHX/pywakepsx-on-bt) — 칩셋별 스푸핑 전략과 HCI 흐름의 원본
- [gamahacka/ps5-wake-bluetooth-linux](https://github.com/gamahacka/ps5-wake-bluetooth-linux) — Realtek 동글용 펌웨어 config 우회
- 자세한 설계 메모: [docs/design.md](docs/design.md)
