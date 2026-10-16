# 02. 로드맵

미니 IPCC를 7단계로 만든다. 단계마다 **제공된 것**(이미 있는 것), **직접 해볼 것**(학습 과제), **완료 기준**이 있다.
체크박스는 진행하면서 직접 채운다.

Stage 0~1은 바로 실험할 수 있도록 레퍼런스 구현을 넣어 두었다. 다만 회사 업무가 "에이전트가 만든 코드를
읽고, 검증하고, 고치는 일"에 가깝기 때문에, 이 두 단계는 **읽고 설명하고 바꿔 보는 것**이 과제다.
Stage 2부터는 설계 문서만 있고 구현은 직접 한다.

## 진행 현황

| Stage | 주제 | 예상 기간 | 상태 |
|---|---|---|---|
| 0 | 전화부터 걸어보기 (Asterisk, SIP) | 3~5일 | 환경 제공 ✅ / 과제 진행 전 |
| 1 | Go AMI 클라이언트 | 1~2주 | 레퍼런스 제공 ✅ / 과제 진행 전 |
| 2 | 상태머신: 콜·상담원·큐 (Rust→Go 리허설) | 2~3주 | 설계 초안 ([06](06-state-model.md), [07](07-rust-to-go.md)) |
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
- 내선 1001~1003(소프트폰용), 9001(SIPp 자동 테스트용)
- 다이얼플랜: 600 에코, 601 안내멘트, 1001~1009 내선 통화, 5000 대표번호 → `support` 큐
- Mac Docker Desktop용 NAT 설정 (`EXTERNAL_IP`), RTP 포트 10000-10099
- SIPp 시나리오: `make sip-register`, `make sip-echo`, `make sip-queue` — 모두 통과 확인
- 컨테이너 안의 sngrep, tcpdump, Asterisk 소스(`/usr/src/asterisk`)

**직접 해볼 것**
- [ ] Linphone(Mac)으로 1001 등록 → 600에 걸어 내 목소리가 돌아오는지 듣기 ([08-runbook.md](08-runbook.md#3-소프트폰-연결))
- [ ] 두 번째 소프트폰으로 1002 등록 → 1001과 1002 사이 통화 (휴대폰을 쓰려면 `make up-lan`)
- [ ] `make sngrep`을 켜 둔 채 등록·통화 → 래더에서 REGISTER/401/200, INVITE~BYE를 하나씩 설명하기
- [ ] 200 OK의 SDP에서 c=, m= 줄을 읽고 어떤 주소·포트·코덱으로 정해졌는지 말하기
- [ ] **일부러 고장 내기**: `asterisk/conf/pjsip_transport.conf.tmpl`의 `external_media_address`를 지우고
  `make restart` → 통화해서 "연결은 되는데 소리가 안 들림"을 재현 → sngrep으로 원인을 찾고 되돌리기
- [ ] `make pcap`으로 캡처 → Wireshark의 Telephony › VoIP Calls에서 통화를 찾아 RTP 재생
- [ ] `make sh` → `/usr/src/asterisk/apps/app_queue.c`에서 `AgentConnect` 이벤트를 만드는 곳 찾아보기

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
- `make e2e` (`engine/e2e`): 실제 Asterisk로 로그인 → 정상 콜 2건 → 포기 → SIPp 등록·통화 → 로그아웃
  (`E2E_RESTART=1`이면 재시작 후 재접속까지)을 이벤트로 검증한다. 약 30초
- `make lint`: golangci-lint (errcheck, exhaustive, errorlint)
- CI (`.github/workflows/ci.yml`): PR과 `main` 푸시마다 `make test`, golangci-lint, `make e2e`
- e2e가 찾은 사실: **AgentComplete가 채널 정보 없이 올 수 있다** (05 문서 6절 11번). Stage 2 설계에 바로 영향을 준다

**직접 해볼 것** — 읽고, 설명하고, 바꿔 보기
- [ ] `client.go`의 고루틴 구성을 그림으로 그리기: 연결 하나에 고루틴이 몇 개고, 채널은 어디서 어디로 흐르나
- [ ] `make watch`를 켜 두고 `docker compose restart asterisk` → 출력 해석.
  `docker compose pause asterisk` 30초 → 왜 바로 끊기지 않고 약 20초 뒤에 끊기는지 설명 (PingInterval + PingTimeout)
- [ ] `amicli replay -all -v`로 샘플 녹화를 읽으며 **이벤트 사전** 만들기: 이벤트마다 어떤 필드가 오는지
- [ ] 과제: `amicli stats <녹화>` 추가 — 이벤트 종류별 개수, 콜(Linkedid)별 이벤트 수
- [ ] 과제: 이벤트 큐 적체(`QueueDepth`)가 1,000을 넘으면 경고 로그 남기기 (+ 테스트)
- [ ] 과제: `Command` 액션의 `Output` 여러 줄을 사람이 보기 좋게 출력
- [ ] 과제: MD5 챌린지 로그인(`Action: Challenge` → `AuthType: md5`) 옵션 추가

**완료 기준**
- [x] 통화 중 Asterisk를 재시작해도 자동으로 다시 붙고, 스냅샷이 실제와 맞는다 (검증됨)
- [ ] 위 과제 중 2개 이상을 테스트와 함께 구현했다
- [ ] "왜 읽기 루프를 막으면 안 되나", "왜 재접속 뒤 스냅샷이 필요한가"를 설명할 수 있다

---

## Stage 2. 상태머신: 콜 · 상담원 · 큐

**목표**: 이벤트를 받아 상태를 정확하게 유지한다. **Rust로 먼저 쓰고 Go로 포팅**해서 입사 후 업무를 리허설한다.
→ 설계 초안 [06-state-model.md](06-state-model.md), 포팅 방법 [07-rust-to-go.md](07-rust-to-go.md)

**직접 해볼 것**
- [ ] 시나리오별 녹화 만들기 (소프트폰 또는 sim): 정상, 포기, 무응답 재분배, 보류, 블라인드 전환,
  어텐디드 전환, 3자통화, 상담원이 먼저 끊음 / 고객이 먼저 끊음
- [ ] 녹화를 보고 06 문서의 이벤트 → 상태 전이표를 검증하고 고치기 (확인한 사실만 반영)
- [ ] Rust로 상태머신 코어(순수 함수) 작성 → `legacy-rust/`
- [ ] Go로 포팅 → `engine/state/`, 두 구현을 같은 녹화로 비교하는 차분 테스트 하네스
- [ ] 재동기화: 스냅샷과 메모리 상태를 비교해 보정하고, 보정 내역을 로그로 남기기
- [ ] 엔진이 후처리(ACW) 타이머를 관리하고 끝나면 `QueuePause(false)`
- [ ] 정책: 무응답 N회 → 자동 이석, 이석 예약(통화 중에 누르면 통화 끝난 뒤 이석)

**완료 기준**
- [ ] 모든 시나리오 녹화의 replay 결과가 기대 스냅샷과 같다 (golden test)
- [ ] Rust 구현과 Go 구현의 출력이 모든 녹화에서 같다 (차분 테스트)
- [ ] `make test`, `make lint`(exhaustive 포함), `make e2e` 통과. 상태머신 시나리오를 e2e에도 추가

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
