# CLAUDE.md

콜센터(IPCC) 엔진 개발을 학습하는 프로젝트다. 사용자는 한국어를 쓴다. 문서·코드 주석·커밋 메시지는 한국어로 쓴다.
설계와 진행 상황은 `docs/`에 있다. 작업 전에 `docs/02-roadmap.md`(어느 Stage인지)와 관련 설계 문서를 먼저 읽는다.

## 구조

- `asterisk/` — Asterisk 22.11 이미지(Dockerfile)와 설정(`conf/`, 컨테이너 `/etc/asterisk`에 읽기 전용 마운트)
- `engine/` — Go 모듈 `ipcc/engine`. `ami/`(AMI 클라이언트), `cmd/amicli/`(학습용 CLI), `testdata/`(AMI 녹화)
- `tests/sipp/` — SIPp 시나리오 (컨테이너의 `/sipp`)
- `docs/` — 설계 문서. 결정을 바꾸면 `docs/09-decisions.md`에 항목을 추가한다

## 명령 (저장소 루트에서)

| 목적 | 명령 |
|---|---|
| Asterisk 기동 / 중지 / 재시작 | `make up` / `make down` / `make restart` |
| Asterisk CLI 한 번 실행 | `make rx c="queue show support"` |
| 설정 다시 읽기 | `make reload` (transport 템플릿·환경변수 변경은 `make restart`) |
| Go 테스트 (vet + race) | `make test` — 코드를 바꾸면 반드시 실행 |
| CLI 빌드 | `make build` → `bin/amicli` |
| 가짜 상담원 / 가짜 콜 | `make sim-agents`, `make sim-call n=3`, `make sim-abandon` |
| SIP 자동 테스트 | `make sip-register`, `make sip-echo`, `make sip-queue` |
| 이벤트 녹화·재생 | `bin/amicli watch -record DIR`, `bin/amicli replay [-only E1,E2] [-v] FILE` |

동작을 확인할 때는 사람에게 묻기 전에 직접 돌려 본다: `make up` → `bin/amicli watch -record <임시 디렉터리>`를 백그라운드로 →
`make sim-agents` + `make sim-call` → 녹화를 `bin/amicli replay`로 확인.

## 작업 규칙

- **공개 저장소다.** 특정 회사명이나 면접·내부에서 들은 정보는 파일과 커밋 메시지에 넣지 않는다.
- **이벤트의 의미·순서는 추측하지 말고 녹화로 확인한다.** 문서에 넣는 예시도 실제 녹화에서 가져온다.
- 녹화를 `testdata`로 옮길 때는 전화번호와 호스트명을 가린다 (`recordings/`, `data/`는 git 제외).
- Go: 표준 라이브러리 우선, `gofmt`, 에러는 `%w`로 감싼다, 라이브러리 코드에서 panic하지 않는다.
- AMI 읽기 루프를 막는 코드를 넣지 않는다. DB·외부 API 호출은 별도 워커에서 한다.
- 상담원 업무 상태(이석·후처리)의 기준은 엔진이다. Asterisk의 `wrapuptime`/`autopause`로 해결하지 않는다 (D-008).
- Asterisk 설정을 바꾸면 시작 로그에 WARNING/ERROR가 없는지 확인한다 (D-010):
  `make restart && sleep 5 && docker compose logs --since 15s asterisk | grep -E "WARNING|ERROR"`
- 테스트가 실패하면 테스트를 고쳐서 통과시키지 말고 원인을 보고한다.
- Stage 2 이후 과제(사용자가 직접 할 학습 과제)를 대신 구현하지 않는다. 요청받았을 때만 한다.

## 도메인 핵심 (실측으로 확인한 것)

- 콜 ID는 `Linkedid`, 채널 ID는 `Uniqueid`다. 콜 하나에 채널이 여러 개다 (Asterisk는 B2BUA).
- Local 채널은 `;1`/`;2` 쌍이다. 시뮬레이션 콜은 모두 Local 채널이다.
- `Newchannel` 시점의 CallerIDNum은 `<unknown>`일 수 있다. `NewCallerid`에서 확정된다.
- `AgentComplete`는 상담원·고객 채널의 `Hangup`보다 늦게 올 수 있다.
- `OriginateResponse`는 `Response:` 필드를 가진 **이벤트**다. 다른 AMI 세션의 ActionID를 단 채로도 온다.
- queue_log의 callid는 큐에 들어간 채널의 Uniqueid다 (Linkedid가 아니다).
- Asterisk를 재시작하면 큐 멤버가 비워진다 (`persistentmembers = no`). 엔진이 다시 넣어야 한다.
