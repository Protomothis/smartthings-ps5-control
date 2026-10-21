# Changelog

서버 컨테이너(`ps5ctl`)의 변경 이력입니다. SmartThings Edge 드라이버는 버전이 따로 돌므로 [edge/CHANGELOG.md](edge/CHANGELOG.md)에 기록합니다.

## [Unreleased]

### 추가

- **상태 조회** — `ps5ctl status` 와 `GET /status` 가 PS5 의 UDP 9302 탐색 프로토콜로 켜짐 · 휴식 모드 · 꺼짐을 구분합니다. 인증이 필요 없고, 휴식 모드 응답은 PS5 의 "인터넷에 연결된 상태 유지" 가 켜져 있어야 합니다
- **블루투스 깨우기** — `ps5ctl wake` 와 `POST /wake` 가 동글 주소를 페어링된 듀얼센스 주소로 바꿔 PS5 를 페이지하고 원래 주소로 되돌립니다. Broadcom · Cypress · Intel · MediaTek 동글을 지원하고(TI · Atheros · Qualcomm 은 미검증), Realtek · CSR 은 지원하지 않는다고 알려 줍니다. `ps5ctl adapter` 로 동글 칩셋을 미리 확인할 수 있습니다
- **설정 페이지** — `https://<서버>:8543/` (basic auth) 에서 PS5 IP · 블루투스 주소 · 어댑터를 저장하고 상태 조회 · 깨우기 · 어댑터 확인을 바로 눌러 봅니다. 크롬에서 USB 로 꽂은 듀얼센스를 읽어(WebHID) 컨트롤러와 PS5 주소를 채웁니다. 자체서명 인증서는 첫 실행 때 `/data/tls/` 에 만들어집니다
- **Remote Play 등록 · 휴식 모드 · 네트워크 깨우기** — 설정 페이지에서 PSN 계정 ID 와 PS5 의 PIN 으로 Remote Play 클라이언트로 등록하면, `POST /standby` 가 켜진 PS5 를 휴식 모드로 보내고 `POST /wake` 는 휴식 모드의 PS5 를 블루투스 없이 네트워크로 깨웁니다(완전 종료 상태는 그대로 블루투스). 등록 키는 `/data/config.json` 에만 있고 API 는 보여 주지 않습니다. `ps5ctl rp account|register|wake|standby` 로 설정 페이지 없이도 시험할 수 있습니다. playactor(ISC) 의 PS5 구현을 Go 로 옮긴 것입니다
- **API 시크릿** — 설정 페이지의 API 시크릿을 정하면 `POST /wake` · `POST /standby` 는 `X-PS5-Secret` 헤더가 맞아야 합니다. `GET /status` 는 그대로 열려 있습니다
- **컨테이너** — scratch 이미지 하나(`ps5ctl serve`), 호스트 네트워크와 `NET_ADMIN` · `NET_RAW` 만 필요합니다
