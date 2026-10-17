# 02. 로드맵

미니 IPCC를 7단계로 만든다. 단계마다 **제공된 것**(이미 있는 것), **직접 해볼 것**(학습 과제), **완료 기준**이 있다.
체크박스는 진행하면서 직접 채운다.

처음에는 Stage 0~1만 레퍼런스로 넣고 Stage 2부터는 직접 구현하도록 남겨 두었다. 이후 요청에 따라 Stage 0~2의 과제도
에이전트가 구현하고 해설을 붙였다(✅ 표시). 회사 업무가 "에이전트가 만든 코드를 읽고, 검증하고, 고치는 일"에 가까우니,
✅ 항목은 **해설을 보기 전에 스스로 답해 보고, 코드를 읽고 설명하고 바꿔 보는 것**으로 공부하면 된다.
귀로 듣고 눈으로 보는 항목(소프트폰 통화, sngrep, Wireshark)은 사람 몫으로 남아 있다.

## 진행 현황

| Stage | 주제 | 예상 기간 | 상태 |
|---|---|---|---|
| 0 | 전화부터 걸어보기 (Asterisk, SIP) | 3~5일 | 환경 ✅ / 자동화·해설 ✅ / 소프트폰·Wireshark는 사람 몫 |
| 1 | Go AMI 클라이언트 | 1~2주 | 레퍼런스 ✅ / 과제 4개 구현 ✅ / 이벤트 사전 ✅ |
| 2 | 상태머신: 콜·상담원·큐 (Rust→Go 리허설) | 2~3주 | Rust 레거시 ✅ / Go 포팅 ✅ / 차분 테스트 ✅ / 실시간 엔진 ✅ ([06](06-state-model.md), [07](07-rust-to-go.md)) |
| 3 | 실시간 모니터링 (Node.js 게이트웨이, 화면) | 1~2주 | 계획 |
| 4 | MySQL 이력·통계 | 1~2주 | 계획 |
| 5 | ARS (Go FastAGI) | 1주 | 계획 |
| 6 | 고객사 연동, 장애 대응 | 1~2주 | 계획 |

**시간이 부족하면 Stage 0 → 1 → 2까지만** 해도 입사 직후 업무(엔진 Rust→Go 전환)에 바로 쓰인다.
3~6은 입사 뒤 실무에서 채워도 된다.

---

## Stage 0. 전화부터 걸어보기

**목표**: SIP 콜이 어떻게 맺어지고 음성이 어떻게 흐르는지 눈으로 확인한다. → [04-sip-sdp-rtp.md](04-sip-sdp-rtp.md)

**제공된 것** ✅
- Asterisk 22.11 LTS 소스 빌드 이미지 (`make up`), 시작 로그에 WARNING/ERROR 없음
- 내선 1001~1003(소프트폰용), 9001(SIPp 고객), 9002(SIPp 상담원 전화기, 고정 주소)
- 다이얼플랜: 600 에코, 601 안내멘트, 602 순수 에코(자동 검사용), 1001~1009 내선 통화, 5000 대표번호 → `support` 큐
- Mac Docker Desktop용 NAT 설정 (`EXTERNAL_IP`, 컨테이너 IP를 넣은 `local_net`), RTP 포트 10000-10099
- SIPp 시나리오: `make sip-register`, `make sip-echo`, `make sip-queue`, `make sip-agent-call`(실제 SIP 상담원 흐름) — 모두 통과 확인
- `make rtp-check`: Mac에서 Docker 포트포워딩을 거쳐 음성(RTP)이 왕복하는지 자동 검사 (보냄 100 / 받음 96 확인)
  - 이 검사가 첫 NAT 설정의 결함(SDP에 컨테이너 IP가 적힘)을 잡았고 고쳤다 (04 문서 6절, D-014)
- 컨테이너 안의 sngrep, tcpdump, Asterisk 소스(`/usr/src/asterisk`)

**직접 해볼 것** (✅ = 자동화했거나 해설을 넣어 둠. 귀로 듣고 눈으로 보는 항목은 사람 몫으로 남겨 뒀다)
- [x] `make rtp-check`로 서버 쪽 음성 경로가 정상인지 먼저 본다 → Mac에서 보냄 100 / 받음 96 확인, e2e와 CI가 매번 확인
- [ ] Linphone(Mac)으로 1001 등록 → 600에 걸어 내 목소리가 돌아오는지 듣기 ([08-runbook.md](08-runbook.md#3-소프트폰-연결)) — 사람 몫
- [ ] 두 번째 소프트폰으로 1002 등록 → 1001과 1002 사이 통화 (휴대폰을 쓰려면 `make up-lan`) — 사람 몫
- [x] 래더에서 REGISTER/401/200, INVITE~BYE를 하나씩 설명하기 → 해설: [04 문서 2·3절](04-sip-sdp-rtp.md#2-등록-register)
  (실제 캡처한 메시지 포함). sngrep으로 직접 보는 것은 사람 몫
- [x] 200 OK의 SDP에서 c=, m= 줄을 읽고 주소·포트·코덱 말하기 → 해설: [04 문서 4절](04-sip-sdp-rtp.md#4-sdp-읽는-법)
- [x] **일부러 고장 내기** → 실제로 이 결함이 있었다. 첫 설정에서 `rtp-check`가 SDP의 `c=172.23.0.2`로 잡았고,
  원인(local_net 조건)과 수정은 [04 문서 6절](04-sip-sdp-rtp.md#6-nat와-연결은-되는데-소리가-안-들려요)·D-014.
  다시 해 보려면 `local_net = @CONTAINER_IP@/32` 줄을 지우고 `make restart` → `make rtp-check`
- [ ] `engine/rtpcheck`를 sngrep 트레이스와 나란히 읽기 — SIP를 직접 말하는 최소 구현 (Digest 인증, SDP, RTP 헤더)
- [ ] `make pcap`으로 캡처 → Wireshark의 Telephony › VoIP Calls에서 통화를 찾아 RTP 재생 — 사람 몫
- [x] `make sh` → `/usr/src/asterisk/apps/app_queue.c`에서 `AgentConnect` 이벤트를 만드는 곳 찾아보기 → 해설:
  `try_calling()`에서 상담원이 받으면 Queue·Interface·MemberName·HoldTime·RingTime을 JSON으로 묶어
  `queue_publish_multi_channel_blob(고객, 상담원, queue_agent_connect_type(), …)`로 내보낸다.
  AMI 텍스트로 바꾸는 곳은 `queue_agent_connect_to_ami()` → `queue_multi_channel_to_ami()`이고, 여기서
  고객 스냅샷은 접두사 없이, 상담원 스냅샷은 `Dest` 접두사를 붙여 이어 붙인다 (05 문서 5절의 필드 모양이 이렇게 나온다)

**완료 기준**
- [ ] REGISTER와 INVITE~BYE 흐름을 보지 않고 그려서 설명할 수 있다
- [ ] "소리가 안 들려요" 신고를 받으면 무엇부터 볼지 순서대로 말할 수 있다

---

## Stage 1. Go AMI 클라이언트

**목표**: 회사 핵심 엔진의 바닥인 AMI 연동을 이해한다. → [05-ami.md](05-ami.md)

**제공된 것** ✅
- `engine/ami`: 프로토콜 파서(`Reader`), 메시지(`Message`), 클라이언트(`Client`)
  - 재접속(지수 백오프 + 지터), Ping 하트비트, ActionID 매칭, 목록형 응답, 읽기 루프를 막지 않는 무제한 큐, 원본 녹화
- `amicli`: `watch`(실시간 + 재접속 시 스냅샷), `action`, `replay`, `agent`, `sim-call`
- 테스트 25개 (가짜 Asterisk 서버로 재접속, 핑 타임아웃, 응답 순서 뒤섞임, 목록 응답, 녹화 재생 등). `make test`
- 샘플 녹화 `engine/testdata/sim-normal-and-abandon.log` (정상 콜 1건 + 포기 콜 1건)
- 실제 Asterisk로 확인한 것: 재시작 후 자동 재접속, `docker compose pause` 때 약 20초 안에 끊김 감지 후 복구,
  실측 중 발견한 버그 1건 수정 (OriginateResponse가 응답으로 잘못 분류되던 문제 — 05 문서 6절)
- `make e2e` (`engine/e2e`): 실제 Asterisk로 로그인 → 정상 콜 2건 → 포기 → SIPp 등록·통화 → 로그아웃 → 실제 SIP 상담원 콜 → 음성(RTP) 왕복
  (`E2E_RESTART=1`이면 재시작 후 재접속까지)을 이벤트로 검증한다. 약 30초
- `make lint`: golangci-lint (errcheck, exhaustive, errorlint)
- CI (`.github/workflows/ci.yml`): PR과 `main` 푸시마다 `make test`, golangci-lint, `make e2e`
- e2e가 찾은 사실: **AgentComplete가 채널 정보 없이 올 수 있다** (05 문서 6절 11번). Stage 2 설계에 바로 영향을 준다

**직접 해볼 것** — 읽고, 설명하고, 바꿔 보기 (✅ = 구현·해설을 넣어 둠. 해설을 보기 전에 먼저 스스로 답해 보면 좋다)
- [x] `client.go`의 고루틴 구성을 그림으로 그리기 → 해설: [05 문서 7절 "고루틴과 채널"](05-ami.md#고루틴과-채널)
- [x] `docker compose pause asterisk` 30초 → 왜 바로 끊기지 않고 최대 20초 뒤에 끊기는지 → 해설: [05 문서 7절](05-ami.md#타임아웃과-재접속)
- [x] **이벤트 사전** 만들기 → [10-event-dictionary.md](10-event-dictionary.md) (`make event-dict`로 녹화에서 자동 생성, 의미 칸 포함)
- [x] 과제: `amicli stats <녹화>` — 이벤트 종류별 개수, 콜(Linkedid)별 이벤트 수, `-fields`, `-md`
- [x] 과제: 이벤트 큐 적체 경고 — `Config.QueueWarnDepth`(기본 1,000), 절반 아래로 내려오면 해소 로그 (`TestQueueDepthWarning`)
- [x] 과제: `Command` 액션 출력 정리 — `amicli action Command Command="queue show support"`가 CLI 출력처럼 나온다
- [x] 과제: MD5 챌린지 로그인 — `Config.AuthMD5`, `amicli … -md5` (`TestMD5ChallengeLogin`, 실제 Asterisk로 확인)
- [ ] 새 과제: `amicli stats`에 콜별 대기시간·통화시간 집계 추가 (Stage 4 통계의 예습)

**완료 기준**
- [x] 통화 중 Asterisk를 재시작해도 자동으로 다시 붙고, 스냅샷이 실제와 맞는다 (검증됨)
- [x] 위 과제 중 2개 이상을 테스트와 함께 구현했다 (4개 모두)
- [ ] "왜 읽기 루프를 막으면 안 되나", "왜 재접속 뒤 스냅샷이 필요한가"를 설명할 수 있다 — 사람 몫

---

## Stage 2. 상태머신: 콜 · 상담원 · 큐

**목표**: 이벤트를 받아 상태를 정확하게 유지한다. **Rust로 먼저 쓰고 Go로 포팅**해서 입사 후 업무를 리허설한다.
→ 상태 모델 [06-state-model.md](06-state-model.md), 포팅 방법과 실제 포팅 메모 [07-rust-to-go.md](07-rust-to-go.md) 10절

**제공된 것** ✅
- `legacy-rust/` — Rust '레거시' 상태머신 (표준 라이브러리만, 단위 테스트 14개). `make rust-test` (Docker)
- `engine/state/` — Go 포팅. 골든 테스트, 불변조건 검사(입력마다), 단위 테스트. `bin/state replay [--cmds F] 녹화…`
- `make diff-test` — 녹화 13개 + 명령 파일 1개(출력 15개)를 Rust와 Go로 재생해 바이트 단위로 비교. CI의 `rust` 작업
- `engine/live/`, `bin/engine` — 상태머신을 실제 Asterisk에 붙인 실시간 엔진. 후처리 타이머, QueueAdd/QueuePause/QueueRemove 실행,
  FullyBooted 뒤 재동기화. 명령은 stdin (`login|ready|notready|wrapup|logout <인터페이스> [인자]`, `snapshot`)
- `make scenarios [only=이름]` — 실제 Asterisk로 시나리오 녹화 11개를 다시 만든다 (`engine/state/testdata/README.md`)
- e2e 두 단계: 엔진이 후처리·이석 예약을 큐 멤버에 반영 / Asterisk 재시작 뒤 엔진이 상담원을 다시 넣음
- 이 단계에서 찾은 것
  - 녹화로 확인한 AMI 동작 7개 (05 문서 6절 15~21번): FullyBooted 전 로그인, 타임아웃도 Abandon, RingTime 단위,
    Hold의 채널, 전환·3자통화의 Linkedid, Atxfer의 DTMF, 벨 중 포기
  - 레거시 파서 버그 1건 — Go와의 비교로 찾고 `truncated.ami`로 고정 (07 문서 10절)
  - 새 녹화가 드러낸 모델의 빈틈 2개 (3자통화의 보류 해제, 벨 중 포기) → Rust → Go 순서로 반영

**직접 해볼 것** (✅ = 구현·해설을 넣어 둠. 코드를 읽고 설명하고 바꿔 보는 것으로 공부한다)
- [x] 제공된 녹화 두 개로 시작 → 두 녹화도 차분 테스트에 들어 있다 (`engine/testdata/*.log`)
- [x] 시나리오별 녹화: 정상, 포기, 무응답 재분배, 벨 중 포기, 고객이 먼저 끊음, 큐 타임아웃, 보류(실제 SIP),
  블라인드 전환, 어텐디드 전환, 3자통화, 실제 SIP 상담원 → `make scenarios`
- [x] 녹화를 보고 06 문서의 전이표를 검증하고 고치기 → 06 문서 4~6절 (행마다 근거 녹화를 적었다)
- [x] Rust로 상태머신 코어(순수 함수) 작성 → `legacy-rust/src/engine.rs`
- [x] Go로 포팅 + 차분 테스트 하네스 → `engine/state/`, `make diff-test`, `tests/state/replay-all.sh`
- [x] 재동기화: 스냅샷과 메모리 상태를 비교해 보정하고 경고 줄로 남기기 → 06 문서 7절, e2e 재시작 단계
- [x] 엔진이 후처리(ACW) 타이머를 관리하고 끝나면 `QueuePause(false)` → `engine/live`, e2e 엔진 단계 (정책 2초 → 2초 뒤 대기)
- [x] 정책: 무응답 N회 → 자동 이석, 이석 예약 → `state.Policy`, `normal.cmds`
- [ ] 새 과제: 06 문서 11절 (후처리 직후 race 녹화로 확인, 큐 대기열 모델, 브리지 참여자 모델 …)
- [ ] 읽기 과제: `state.go`와 `engine.rs`를 나란히 놓고 07 문서 10절의 표를 보지 않고 다시 만들어 보기
- [ ] 바꿔 보기 과제: 정책 하나를 추가(예: 후처리 상한을 상담원마다 다르게)하고 Rust → 기대 출력 → Go 순서로 반영해 0 diff 만들기

**완료 기준**
- [x] 모든 시나리오 녹화의 replay 결과가 기대 출력과 같다 (golden test, `make test`)
- [x] Rust 구현과 Go 구현의 출력이 모든 녹화에서 같다 (`make diff-test`, CI)
- [x] `make test`, `make lint`(exhaustive 포함), `make e2e` 통과. 상태머신 시나리오를 e2e에도 추가

---

## Stage 3. 실시간 모니터링

**목표**: 상태를 화면으로 보여 준다. Node.js가 여기서 등장한다.

**직접 해볼 것**
- [ ] `docker-compose.yml`에 redis 추가. 엔진이 상태 변화를 Redis pub/sub로 발행
- [ ] `gateway/` (Node.js, Fastify 또는 NestJS): WebSocket — 접속하면 전체 스냅샷, 이후에는 바뀐 부분만
- [ ] 상담원 툴바: 대기/이석/후처리, 걸기, 전환, 보류, 당겨받기 → 엔진 명령 API
- [ ] 슈퍼바이저 현황판: 큐별 대기 수·최장 대기시간, 상담원별 상태·경과시간, 실시간 응대율
- [ ] 감청 / 코칭(상담원에게만 들림) / 강제개입: `ChanSpy`를 `Originate`로 호출
- [ ] (우대사항) 브라우저 WebRTC 소프트폰: JsSIP 또는 SIP.js + Asterisk WSS (`http.conf` TLS, PJSIP `webrtc=yes`)

**완료 기준**
- [ ] 상담원 탭 2개 + 현황판 1개에서 상태 변화가 1초 안에 반영된다
- [ ] 게이트웨이를 재시작해도 화면이 스냅샷으로 다시 맞춰진다

---

## Stage 4. MySQL 이력 · 통계

**직접 해볼 것**
- [ ] mysql 추가, 스키마: 콜, 콜 구간(leg), 상담원 상태 이력, 15분 단위 집계(큐별/상담원별)
- [ ] 엔진이 이력을 기록하되 이벤트 루프와 분리된 워커에서 (설계 원칙 3)
- [ ] Asterisk 자체 기록과 숫자 대조: `data/asterisk/log/queue_log`, `cdr-csv/Master.csv`
- [ ] 통계 정의를 설정으로: 서비스레벨 기준 시간, 짧은 포기호 제외 기준 ([03-glossary.md](03-glossary.md#통계-지표))
- [ ] 더미 1,000만 건을 넣고 `EXPLAIN`을 보며 인덱스·파티셔닝 튜닝
- [ ] (선택) Asterisk의 queue_log/CDR을 ODBC로 MySQL에 직접 쓰기 (`modules.conf`의 Stage 4 noload 해제)

**완료 기준**
- [ ] 1,000만 건에서 일자×큐 통계가 1초 안에 나온다
- [ ] 현황판의 오늘 누적 수치와 일 통계가 같다

---

## Stage 5. ARS (IVR)

**직접 해볼 것**
- [ ] Go FastAGI 서버(:4573): `agi_` 변수 읽기, `STREAM FILE` / `GET DATA` / `SET VARIABLE`, `200 result=` 파싱
- [ ] 흐름: 인입 → 안내멘트 → DTMF 메뉴 → 고객번호 입력 → Mock CRM 조회 → VIP는 우선순위를 올려 큐로
- [ ] `extensions.conf`의 `[inbound]`에서 `AGI(agi://host.docker.internal:4573/...)` 호출

**완료 기준**
- [ ] CRM 응답이 5초 늦어도 ARS는 타임아웃 뒤 기본 경로로 넘어간다 (고객이 무음을 듣지 않는다)

---

## Stage 6. 고객사 연동 · 장애 대응

**직접 해볼 것**
- [ ] REST: click-to-call, 상담원 상태 조회/변경, 콜 이력 조회, 콜 인입 웹훅(스크린팝)
- [ ] 한국 고객사 대비: **고정길이 전문(EUC-KR) TCP 연동** 시뮬레이터. 바이트 길이 ≠ 글자 수 주의
  (macOS `cut -c`로 녹화를 자르다가 '휴식'이 깨진 것도 같은 문제다)
- [ ] 장애 훈련: SIPp 동시 100콜 (RTP 포트 범위 확장 필요), Asterisk 재시작, MySQL 다운, AMI 끊김, CRM 지연
- [ ] 관측: 모든 로그에 Linkedid, 메트릭(이벤트 큐 깊이, 재접속 횟수, 재동기화 보정 건수)

**완료 기준**
- [ ] "어제 14:03에 이 번호의 콜이 왜 끊겼나요?"에 10분 안에 답할 수 있다

---

## 확장 과제 (여유가 있으면)

- 아웃바운드 캠페인 / 프리딕티브 다이얼러 (`app_amd` 자동응답기 감지)
- 콜백 예약 (대기 대신 나중에 전화 받기)
- ARI로 엔진이 직접 라우팅(ACD)하기 — `app_queue`와 비교
- 실시간 STT(AICC): AudioSocket이나 `chan_websocket`으로 음성을 엔진에 흘려 STT에 연결
