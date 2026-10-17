# 상태머신 테스트용 AMI 녹화

`make scenarios`로 다시 만든다 (engine/e2e/scenarios_test.go). 호스트명은 `amicli-host`로 가렸다.
각 `<이름>.ami`의 기대 출력은 `<이름>.golden.jsonl`이고, Rust 레거시로 만든다 (`make golden`).
`make diff-test`는 이 녹화들과 Stage 1 녹화(`engine/testdata/*.log`)를 Rust와 Go로 재생해 출력을 비교한다.

| 녹화 | 시나리오 |
|---|---|
| `normal.ami` | 가짜 상담원 2명, 고객 2명 — 큐 → 연결 → 2초 통화 → 상담원이 끊음 |
| `abandon.ami` | 상담원이 이석인 동안 고객이 4초 기다리다 끊음 — 포기호 |
| `noanswer.ami` | 받지 않는 상담원에게 15초 벨 → 무응답 → 다음 상담원이 받음 |
| `ring-abandon.ami` | 상담원 벨이 울리는 동안(6초) 고객이 3초 만에 끊음 |
| `caller-hangup.ami` | 통화 중에 고객이 먼저 끊음 (AgentComplete Reason=caller) |
| `queue-timeout.ami` | 로그인한 상담원이 없고 고객은 큐에서 8초 뒤 타임아웃 |
| `hold.ami` | 실제 SIP 상담원(9002)이 통화 중 보류했다가 해제 (re-INVITE sendonly → sendrecv) |
| `blind-transfer.ami` | 상담원 1001이 받고 1초 뒤 고객을 1002에게 블라인드 전환 (AMI BlindTransfer) |
| `attended-transfer.ami` | 상담원 1001이 받고 1002와 먼저 통화한 뒤 넘김 (AMI Atxfer) |
| `three-way.ami` | 상담원 1001이 1002를 어텐디드 전환으로 부른 뒤 *3(atxferthreeway)으로 3자통화 → 1001이 먼저 빠짐 |
| `sip-agent.ami` | 실제 SIP 고객(9001) → 대표번호 → 실제 SIP 상담원(9002)이 받고 먼저 끊음 |

그 밖의 파일 (손으로 만든 것, `make scenarios`가 건드리지 않는다)

| 파일 | 내용 |
|---|---|
| `normal.cmds` | `normal.ami`에 끼워 넣는 상담원 명령 (통화 중 이석 예약, 후처리 끝 버튼) → `normal.cmds.golden.jsonl` |
| `truncated.ami` | `normal.ami`를 메시지 중간(줄바꿈 바로 뒤)에서 자른 것. Rust 레거시의 파서 버그를 차분 테스트가 잡은 사례 (docs/07 10절) |
