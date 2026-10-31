# Changelog

서버 컨테이너(`ps5ctl`)의 변경 이력입니다. SmartThings Edge 드라이버는 버전이 따로 돌므로 [edge/CHANGELOG.md](edge/CHANGELOG.md)에 기록합니다.

## [Unreleased]

## [v0.1.1] - 2026-10-31

### 변경

- **휴식 모드 재시도** — `POST /standby` 에서 PS5 가 세션 연결을 끊거나 "Remote Play 가 이미 사용 중"(80108b10) 이라고 답하면 20초 뒤 한 번 더 시도합니다. 2026-10-28 밤 다른 기기 없이 첫 시도가 끊기고 14초 뒤 재시도가 "사용 중" 으로 거절된 사례에서, PS5 가 반쯤 열린 세션을 잠시 붙들고 있는 것으로 보입니다
- **상태 조회 잡음 무시** — PS5 IP 에서 온 UDP datagram 이 탐색 응답 형식이 아니면(3바이트 바이너리가 한 번 관측됨) 502 로 실패하지 않고 마감까지 진짜 응답을 기다립니다
- **SSDP 응답 주소** — 검색이 온 서브넷의 인터페이스만 답합니다. 리눅스는 멀티캐스트를 모든 소켓에 전달하므로, 도커 브리지(`docker0` · `br-*`)가 많은 서버에서 허브가 닿지 못하는 브리지 주소를 받을 수 있었습니다
- **시간대** — 바이너리에 zoneinfo 를 넣고 compose 에 `TZ`(기본 `Asia/Seoul`)를 둡니다. scratch 이미지는 UTC 로 돌아 드라이버 화면의 시각이 9시간 어긋났습니다
- **동글 점검과 깨우기 분리** — 10분마다의 백그라운드 점검과 `/api/adapter` 가 깨우기·휴식 모드 진행 중에는 건너뜁니다(423). 주소를 바꿔 둔 동안 점검이 끼어들면 둘 다 망가집니다
- **종료 대기** — 종료 신호를 받으면 진행 중인 깨우기·휴식 모드가 끝날 때까지(최대 90초) 기다린 뒤 나갑니다. 중간에 죽으면 동글이 듀얼센스 주소를 단 채 남고, 다음 깨우기는 그 주소를 "원래 주소" 로 저장했습니다. compose 에 `stop_grace_period: 100s`
- **`POST /wake` 응답 정리** — 스푸핑 전에 실패하면(미지원 칩셋 · 권한) `outcome` 이 비어 있고 `restored: false` 라 설정 페이지가 거짓 "복구 실패" 경고를 띄웠습니다. 이제 `outcome: error`, `restored: true`. `?method=network` 는 미등록이면 409, 이 빌드에 Remote Play 가 없으면 501, 깨우기를 보냈는데 안 켜졌을 때만 504 입니다
- **`POST /standby` 기록** — 미등록(409) · 상태 조회 실패(502)로 세션을 열기 전에 거절된 요청도 `last_standby` 에 남아 장치 화면에 보입니다
- **`GET /api/wake/last` 삭제** — `/status` 의 `server.last_wake` 와 중복이었고 아무도 쓰지 않았습니다
- **로그 잡음 줄임** — SSDP 응답은 검색한 주소마다 첫 번째만 INFO 로, 이후는 DEBUG 로 남깁니다. LAN 의 다른 기기가 20~30초마다 `ssdp:all` 을 검색해 나흘 만에 로그 1만 줄 가운데 대부분을 채웠습니다. 10분마다의 동글 점검도 결과가 바뀔 때만 INFO · WARN 으로 남습니다

## [v0.1.0] - 2026-10-28

### 추가

- **상태 조회** — `ps5ctl status` 와 `GET /status` 가 PS5 의 UDP 9302 탐색 프로토콜로 켜짐 · 휴식 모드 · 꺼짐을 구분합니다. 인증이 필요 없고, 휴식 모드 응답은 PS5 의 "인터넷에 연결된 상태 유지" 가 켜져 있어야 합니다. 콘솔이 부팅 중이거나 자리에 없어 소켓이 unreachable 오류를 돌려주면 502 대신 꺼짐으로 보고하고, 그 밖의 탐색 실패는 이유를 로그에 남깁니다
- **블루투스 깨우기** — `ps5ctl wake` 와 `POST /wake` 가 동글 주소를 페어링된 듀얼센스 주소로 바꿔 PS5 를 페이지하고 원래 주소로 되돌립니다. Broadcom · Cypress · Intel · MediaTek 동글을 지원하고(TI · Atheros · Qualcomm 은 미검증), Realtek · CSR 은 지원하지 않는다고 알려 줍니다. `ps5ctl adapter` 로 동글 칩셋을 미리 확인할 수 있습니다
- **설정 페이지** — `https://<서버>:8543/` (basic auth) 에서 PS5 상태를 보고 켜기 · 휴식 모드를 누르며, 기본 설정 · 블루투스 · Remote Play · 고급 · 로그를 접고 펼치는 카드로 다룹니다. 넓은 화면은 두 칸, 좁은 화면은 한 칸으로 배치하고, 결과는 응답 JSON 대신 상태 카드로 보여 줍니다. 크롬에서 USB 로 꽂은 듀얼센스를 읽어(WebHID) 컨트롤러와 PS5 주소를 채웁니다. 자체서명 인증서는 첫 실행 때 `/data/tls/` 에 만들어집니다
- **Remote Play 등록 · 휴식 모드 · 네트워크 깨우기** — 설정 페이지에서 PSN 계정 ID(base64 또는 숫자 ID)와 PS5 의 PIN 으로 Remote Play 클라이언트로 등록하면(한 번 등록하면 해제 전까지 잠김), `POST /standby` 가 켜진 PS5 를 휴식 모드로 보내고 `POST /wake` 는 휴식 모드의 PS5 를 블루투스 없이 네트워크로 깨웁니다(완전 종료 상태는 그대로 블루투스). 등록 키는 `/data/config.json` 에만 있고 API 는 보여 주지 않습니다. `ps5ctl rp account|register|wake|standby` 로 설정 페이지 없이도 시험할 수 있습니다. playactor(ISC) 의 PS5 구현을 Go 로 옮긴 것입니다
- **API 시크릿** — 설정 페이지의 API 시크릿을 정하면 `POST /wake` · `POST /standby` 는 `X-PS5-Secret` 헤더가 맞아야 합니다. `GET /status` 는 그대로 열려 있습니다
- **`GET /status` 의 `server` 블록** — 드라이버 화면용으로 서버 버전 · 설치 ID · 블루투스 동글 확인 결과(칩셋, 지원 여부, 주소 설정 여부; 10분마다 백그라운드로 다시 확인) · Remote Play 등록 여부 · 마지막 깨우기/휴식 모드 결과 · 깨우는 중 여부를 함께 돌려줍니다
- **SSDP 검색 응답** — 컨테이너가 UDP 1900 의 SSDP 검색(`urn:smartthings-ps5-control:device:ps5:1`)에 답해, SmartThings 앱의 "주변 기기 검색"이 서버 주소를 손으로 넣지 않고 장치를 만듭니다. 응답에는 첫 실행 때 만든 설치 ID(`instance_id`)와 HTTP 포트만 들어가고, `GET /ssdp.json` 이 같은 내용을 보여 줍니다. `PS5_SSDP=off` 로 끕니다
- **로그** — 모든 줄이 `시각 레벨 구성요소: 메시지 key=value` 꼴로 컨테이너 로그와 `/data/logs/ps5ctl.log` 에 같이 남고, 파일은 1 MB 마다 회전해 3개(최대 3 MB)까지만 둡니다. 설정 페이지의 로그 패널이 3초마다 새 줄을 받아 레벨별로 보여 주고(시각은 브라우저 언어와 상관없이 `HH:MM:SS.mmm`) 파일을 내려받습니다. `PS5_LOG_LEVEL=debug` 면 모든 HTTP 요청도 남습니다
- **컨테이너** — scratch 이미지 하나(`ps5ctl serve`), 호스트 네트워크와 `NET_ADMIN` · `NET_RAW` 만 필요합니다
