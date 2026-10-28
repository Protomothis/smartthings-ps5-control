# Changelog

서버 컨테이너(`ps5ctl`)의 변경 이력입니다. SmartThings Edge 드라이버는 버전이 따로 돌므로 [edge/CHANGELOG.md](edge/CHANGELOG.md)에 기록합니다.

## [Unreleased]

### 추가

- **상태 조회** — `ps5ctl status` 와 `GET /status` 가 PS5 의 UDP 9302 탐색 프로토콜로 켜짐 · 휴식 모드 · 꺼짐을 구분합니다. 인증이 필요 없고, 휴식 모드 응답은 PS5 의 "인터넷에 연결된 상태 유지" 가 켜져 있어야 합니다
- **블루투스 깨우기** — `ps5ctl wake` 와 `POST /wake` 가 동글 주소를 페어링된 듀얼센스 주소로 바꿔 PS5 를 페이지하고 원래 주소로 되돌립니다. Broadcom · Cypress · Intel · MediaTek 동글을 지원하고(TI · Atheros · Qualcomm 은 미검증), Realtek · CSR 은 지원하지 않는다고 알려 줍니다. `ps5ctl adapter` 로 동글 칩셋을 미리 확인할 수 있습니다
- **설정 페이지** — `https://<서버>:8543/` (basic auth) 에서 PS5 상태를 보고 켜기 · 휴식 모드를 누르며, 기본 설정 · 블루투스 · Remote Play · 고급 · 로그를 접고 펼치는 카드로 다룹니다. 넓은 화면은 두 칸, 좁은 화면은 한 칸으로 배치하고, 결과는 응답 JSON 대신 상태 카드로 보여 줍니다. 크롬에서 USB 로 꽂은 듀얼센스를 읽어(WebHID) 컨트롤러와 PS5 주소를 채웁니다. 자체서명 인증서는 첫 실행 때 `/data/tls/` 에 만들어집니다
- **Remote Play 등록 · 휴식 모드 · 네트워크 깨우기** — 설정 페이지에서 PSN 계정 ID(base64 또는 숫자 ID)와 PS5 의 PIN 으로 Remote Play 클라이언트로 등록하면(한 번 등록하면 해제 전까지 잠김), `POST /standby` 가 켜진 PS5 를 휴식 모드로 보내고 `POST /wake` 는 휴식 모드의 PS5 를 블루투스 없이 네트워크로 깨웁니다(완전 종료 상태는 그대로 블루투스). 등록 키는 `/data/config.json` 에만 있고 API 는 보여 주지 않습니다. `ps5ctl rp account|register|wake|standby` 로 설정 페이지 없이도 시험할 수 있습니다. playactor(ISC) 의 PS5 구현을 Go 로 옮긴 것입니다
- **API 시크릿** — 설정 페이지의 API 시크릿을 정하면 `POST /wake` · `POST /standby` 는 `X-PS5-Secret` 헤더가 맞아야 합니다. `GET /status` 는 그대로 열려 있습니다
- **SSDP 검색 응답** — 컨테이너가 UDP 1900 의 SSDP 검색(`urn:smartthings-ps5-control:device:ps5:1`)에 답해, SmartThings 앱의 "주변 기기 검색"이 서버 주소를 손으로 넣지 않고 장치를 만듭니다. 응답에는 첫 실행 때 만든 설치 ID(`instance_id`)와 HTTP 포트만 들어가고, `GET /ssdp.json` 이 같은 내용을 보여 줍니다. `PS5_SSDP=off` 로 끕니다
- **로그** — 모든 줄이 `시각 레벨 구성요소: 메시지 key=value` 꼴로 컨테이너 로그와 `/data/logs/ps5ctl.log` 에 같이 남고, 파일은 1 MB 마다 회전해 3개(최대 3 MB)까지만 둡니다. 설정 페이지의 로그 패널이 3초마다 새 줄을 받아 레벨별로 보여 주고 파일을 내려받습니다. `PS5_LOG_LEVEL=debug` 면 모든 HTTP 요청도 남습니다
- **컨테이너** — scratch 이미지 하나(`ps5ctl serve`), 호스트 네트워크와 `NET_ADMIN` · `NET_RAW` 만 필요합니다
