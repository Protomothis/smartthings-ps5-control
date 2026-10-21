# 기여 안내

smartthings-pc-control 의 규칙을 이 저장소 크기에 맞게 줄인 것이다.

## 브랜치

- `main`은 릴리스된 코드다. 릴리스 태그(`vX.Y.Z`, `edge-vX.Y.Z`)는 여기서만 단다.
- 한 릴리스의 작업은 `milestone/vX.Y.Z` 브랜치에 모은다. CI는 `main`과 `milestone/**` 푸시, `main`으로 가는 PR에서 돈다.
- 작업마다 마일스톤 브랜치에서 짧은 브랜치를 딴다(`feat/…`, `fix/…`, `edge/…`, `docs/…`, `ci/…`). 끝나면 `Merge <브랜치> into milestone/vX.Y.Z (#n)`로 합치고 지운다. 혼자 하는 작은 변경은 마일스톤 브랜치에 바로 커밋해도 된다.
- 마일스톤은 **사용자 실기 테스트가 끝난 뒤** `main`에 합치고 태그를 단다. rc 태그(`v0.1.0-rc1`)는 시험판으로 나간다.

## 커밋

- 제목은 `<스코프>: <한국어 제목>`, 50자 이하, 마침표 없이 무엇을 하는지 쓴다. 예: `wake: 원복 실패를 결과에 남긴다`
- 스코프: `discovery` `hci` `spoof` `wake` `server` `config` `cli` `edge` `docker` `docs` `ci` `test`. 둘에 걸치면 `server/config:`처럼 쓴다. 첫 세 커밋(영문 제목)은 이 규칙 전의 것이다.
- 본문에는 **왜** 바꾸는지를 쓴다. 실측 결과, 버린 대안, 남은 위험도 여기에 둔다. 관련 이슈는 `(#n)`으로 적는다.
- AI 도구의 도움을 받았으면 본문 끝에 `Co-Authored-By:` 트레일러를 붙인다.
- 사용자에게 보이는 변경은 `CHANGELOG.md`(서버) 또는 `edge/CHANGELOG.md`(드라이버)의 `[Unreleased]`에 한두 문장으로 적는다. 구현 세부는 커밋 본문에 둔다.

## 테스트

Go 쪽은 cgo 가 없어 어느 OS 에서나 돈다. 소켓 코드는 리눅스 빌드 태그 뒤에 있으므로 리눅스 타깃 vet 도 함께 돈다.

```bash
gofmt -l .                                   # 비어 있어야 한다
go vet ./... && GOOS=linux go vet ./...
go test ./...
GOOS=linux GOARCH=amd64 go build ./... && GOOS=linux GOARCH=arm64 go build ./...
golangci-lint run ./...                      # 설정은 .golangci.yml, CI 와 같은 버전
```

```bash
cd edge
bun tools/lua.js tests/run.lua               # Lua 5.3 테스트 (npm install && npm test 도 같다)
bun tools/lua.js tests/syntax.lua            # 전 모듈 컴파일
node tools/build.js                          # 패키지 트리 build/edge/
```

무엇을 테스트하나:

- **버그를 고치면 회귀 테스트 1개**를 같은 커밋에 넣는다.
- 반드시 지키는 것: HTTP API 의 응답 코드와 JSON 모양(Edge 드라이버가 읽는다), HCI 패킷 바이트, 칩셋별 스푸핑 순서와 원복, 설정 파일 호환, 인증 경계.
- 하지 않는 것: 상수 목록을 그대로 다시 적는 단언, 실제 소켓·실제 어댑터(실기 검증은 사람이 한다).
- 같은 모양의 경우가 여럿이면 표 하나로. 서버 핸들러는 `server.Deps` 가짜로, 드라이버는 `deps.http` 가짜로 돈다.

**로컬에서 서버·Docker 를 띄우지 않는다.** 검증은 lint·테스트·빌드까지이고, 실제 실행은 홈서버에서 사용자가 한다.

## 릴리스 순서

허브는 채널에 배정된 드라이버를 자동으로 받지만 서버 이미지는 사용자가 `compose pull` 로 올린다. 드라이버가 기대는 API 가 먼저 나가 있어야 한다.

1. `CHANGELOG.md` 의 `[Unreleased]` 를 `[vX.Y.Z] - 날짜` 로 바꾼다.
2. **서버 릴리스 먼저** — `vX.Y.Z` 태그. `release.yml` 이 ghcr 이미지(`vX.Y.Z`, `latest`)와 리눅스 바이너리를 GitHub Release 에 올린다.
3. 홈서버에서 `docker compose pull && docker compose up -d`.
4. 드라이버에 변경이 있으면 `edge/src/driver_version.lua` 를 올리고 `edge-vX.Y.Z` 태그. `edge.yml` 이 패키징하고, 시크릿이 있으면 채널에 배정한다.

태그·Release·채널 배정은 사용자가 결정한다. 자동으로 만들지 않는다.

## 문서

- README 는 150줄, `edge/README.md` 는 100줄 안으로 둔다.
- `docs/design.md` 는 결정과 근거만 담는다. 진행 메모와 실측 대기는 이슈로 옮긴다.
- SmartThings 플랫폼의 함정은 smartthings-pc-control 의 `docs/design/edge-platform-notes.md` 를 정본으로 본다. 여기서 새로 실측한 것만 `docs/design.md` 에 적는다.
