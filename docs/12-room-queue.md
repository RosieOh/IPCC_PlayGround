# 12. 룸 큐 — app_queue 없이 직접 만든 큐

Stage 2의 엔진은 Asterisk `app_queue`가 상담원을 고르고, 엔진은 그 결과(AgentCalled, AgentConnect …)를 따라간다.
실무에서는 반대로 **큐를 직접 만드는** 구조도 흔하다. 분배 규칙을 마음대로 바꾸고, 전환·3자통화를 자유롭게 하고,
상태를 한 곳에서 소유하기 위해서다. 이 문서는 그런 구조를 작게 만들어 본 것이다 (`engine/rq`, `bin/rqengine`).

| | Stage 2 (app_queue) | 룸 큐 |
|---|---|---|
| 상담원을 고르는 것 | Asterisk `app_queue` | **엔진** (가장 오래 쉰 상담원) |
| 고객이 기다리는 곳 | `Queue()` 앱 | **통화마다 생기는 회의방**(ConfBridge)에서 대기음 |
| 상담원 연결 | app_queue가 Dial | 엔진이 **AMI Originate**로 상담원을 같은 방에 부름 |
| 엔진이 받는 사실 | app_queue 이벤트 (Agent*, QueueCaller*) | dialplan이 보내는 **UserEvent** 5종 + OriginateResponse |
| 상담원 상태 | 한 축 (Ready, Ringing, Talking, ACW …) | **두 축**: 업무 모드 × 통화 단계 |
| 전환 · 3자통화 | Asterisk 전환 기능 (BlindTransfer, Atxfer) | 방에 상담원을 더 부르고 / 빼면 끝 |

## 1. 구조

```text
고객 ──▶ [rq-in] Answer → UserEvent(RQEnter) → ConfBridge(rq-<Linkedid>, 고객용)   … 대기음
                                       │
              AMI :5038 ───────────────┼──▶ bin/rqengine (actor: 대기열·상담원·분배를 혼자 소유)
                                       │        │
                                       │        └─ Originate Channel=<상담원> Context=rq-agent RQ_ROOM=방
상담원 ◀── 벨 ──[rq-agent] (받은 뒤) UserEvent(RQAgentIn) → ConfBridge(같은 방, 상담원용)
끊김 처리기 [rq-hangup]: 채널이 어떻게 끊기든 마지막에 RQCustomerOut / RQAgentOut
```

- **dialplan은 상태를 저장하지 않는다.** DB에 쓰지도 않고, 일어난 일을 UserEvent로 알리기만 한다.
  누가 대기 중이고 누구를 부를지는 엔진 하나가 정한다 (단일 소유자 — Stage 2의 D-008과 같은 원칙을 큐에까지 넓힌 것).
- 방 설정은 `asterisk/conf/confbridge.conf`:
  - 고객 `rq_customer`: `wait_marked`(상담원이 올 때까지 대기음) + `end_marked`(마지막 상담원이 나가면 같이 나감)
  - 상담원 `rq_agent`: `marked`
  - 그래서 **상담원이 한 명이라도 방에 있으면 고객은 남는다** — 이 한 가지 성질로 전환과 3자통화가 된다.
- 대표번호 `5100`(내선에서 걸기) 또는 가짜 고객 `Local/rq-customer@sim`으로 들어온다.

## 2. 녹화로 확인한 이벤트

`bin/amicli watch -record`로 녹화하며 손으로 Originate를 보내 확인했다 (2026-10-26, Asterisk 22.11).

| 경우 | 이벤트 순서 (실제 필드) |
|---|---|
| 정상 | `UserEvent: RQEnter Room: rq-1792983005.18 Queue: support Customer: 01011110001` → (엔진 Originate) → `OriginateResponse Response: Success Reason: 4 Channel: Local/1001@sim-agent-0000000b;1` → `UserEvent: RQAgentIn Room: … Agent: Local/1001@sim-agent Channel: Local/1001@sim-agent-0000000b;1` → (통화) → `RQAgentOut … Cause: 16` → 고객이 곧바로 방에서 나감(`end_marked`) → `RQCustomerOut … Cause: 16` |
| 상담원 무응답 | `OriginateResponse Response: Failure Reason: 3 Channel: Local/1004@sim-noanswer Uniqueid: <unknown>` — **실패면 Channel이 요청한 문자열 그대로**(접미사 없음). 상담원 쪽 UserEvent는 없다 (방 컨텍스트에 들어간 적이 없으니까) |
| 벨 중 고객 이탈 | `RQCustomerOut` → (상담원이 뒤늦게 받음) → `RQAgentMissed … Why: customer-gone` → `RQAgentOut`. 받기 직전에 `CONFBRIDGE_INFO(parties,방)`가 0이라 빈 방에 들어가지 않는다 |
| 3자 → 전환 | 두 상담원이 `RQAgentIn` → 먼저 들어온 상담원이 `RQAgentOut` → **고객은 남은 상담원과 계속 통화** → 마지막 상담원이 나가면 `RQCustomerOut` |

녹화하며 고친 것: Asterisk에는 `${LINKEDID}` 변수가 없다. 처음에는 방 이름이 모두 `rq-`가 되어 세 콜이 한 방에 모였다.
`${CHANNEL(linkedid)}`로 고쳤다. **추측한 dialplan은 녹화로 확인해야 한다**는 좋은 예다.

## 3. 두 축 상태 모델

| 축 | 값 | 누가 바꾸나 |
|---|---|---|
| 업무 모드 `Mode` | `Logout` · `Inbound`(대기) · `NotReady`(이석, 사유) · `ACW`(후처리) | 상담원 명령, 후처리 타이머 |
| 통화 단계 `Phase` | `Idle` · `Ring`(엔진이 부르는 중) · `Talk`(방에 있음) | 교환기에서 일어난 일 (UserEvent·OriginateResponse) |

**콜은 `Mode=Inbound`이고 `Phase=Idle`인 상담원에게만 간다.** 화면 표시는 둘을 합친다 (예: "대기·통화중" = Inbound/Talk).

두 축이 좋은 점: 통화 중에 이석을 누르면 모드만 `NotReady`로 바꾸면 된다. 통화가 끝나도 Inbound가 아니니 콜이 오지 않고,
후처리 대신 이석에 머문다. Stage 2 엔진(한 축)에서 따로 만들어야 했던 **이석 예약이 필요 없다** (`TestNotReadyDuringCall`).

## 4. 엔진 규칙 (`engine/rq/rq.go`)

| 입력 | 처리 |
|---|---|
| `RQEnter` | 콜 생성(Waiting) → 분배 |
| 분배 (입력마다) | 기다리는 콜을 **먼저 온 순서**로, `Inbound/Idle` 상담원 중 **가장 오래 쉰 사람**에게: 상담원 Ring, 콜 Offering, 액션 `Originate` |
| `RQAgentIn` | 상담원 Talk, 콜 Talking. 전환으로 부른 상담원이면 **이때** 원래 상담원 채널에 `Hangup` |
| `OriginateResponse` Failure | 채널 이름에서 상담원을 찾아(접미사 제거) 무응답 +1 → Idle, 콜은 다시 Waiting → 다음 상담원. 연속 2번이면 자동 이석(무응답) |
| 벨 시간 + 5초 동안 응답 없음 | 위와 같음 (`offer_timeout` 경고). OriginateResponse를 놓쳐도 콜이 멈추지 않게 |
| `RQAgentMissed` | 상담원 Idle. 고객이 먼저 끊은 것이라 무응답으로 세지 않는다 |
| `RQAgentOut` | 상담원 Idle. 대기 모드였으면 후처리(10초 뒤 자동 대기), 이석이었으면 이석 유지 |
| `RQCustomerOut` | 콜 끝(`call_end`: 연결된 적 있으면 completed, 없으면 abandoned). **방에 남은 상담원 채널은 엔진이 끊는다** |
| 명령 `transfer A B` | B를 방으로 부른다. **B가 들어온 뒤에** A를 뺀다 — 먼저 빼면 방에 상담원이 없어져 고객도 나간다(`end_marked`) |
| 명령 `conference A B` | B를 방으로 부른다. 아무도 빼지 않는다 (3자통화) |

출력은 JSONL: `agent`(id, mode, phase, reason, room), `call`(state, agents), `call_end`(outcome, wait_ms, talk_ms), `action`(Originate/Hangup), `warn`.

## 5. 장단점 — 왜 app_queue를 안 쓰나, 쓰면 무엇을 잃나

| 얻는 것 | 잃는 것 / 새로 생기는 책임 |
|---|---|
| 분배 규칙을 코드로 자유롭게 (스킬, 우선순위, VIP, 상담원별 한도 …) | 분배·재시도·타임아웃을 전부 직접 만들고 테스트해야 한다 |
| 전환·3자통화가 "방에 넣고 빼기"로 단순해진다 | 회의방 믹싱(softmix) 비용. 보류는 회의방 안에서 따로 만들어야 한다 |
| 상태의 주인이 하나 — Asterisk 큐 상태와 엔진 상태가 어긋날 일이 없다 | **엔진이 죽으면 아무도 분배하지 않는다.** 고객은 방에서 대기음만 듣는다 (app_queue는 엔진 없이도 분배를 계속한다) |
| dialplan을 거의 고치지 않고 규칙을 바꾼다 (reload 불필요) | 엔진이 재시작하면 진행 중인 방을 다시 맞춰야 한다 (아직 없음, 아래 과제) |

## 6. 실행

```bash
make up
make rq-engine            # 터미널 1: 엔진 (명령은 stdin, 가짜 상담원 통화 6초)
  login Local/1001@sim-agent 상담원1
  ready Local/1001@sim-agent
make sim-rq-call n=3      # 터미널 2: 가짜 고객 셋 → 1001이 차례로 받는다 (그 사이 고객은 방에서 대기음)
```

실제 SIP 상담원도 된다(확인함): 엔진에 `login PJSIP/9002 SIPP-9002` → `ready PJSIP/9002`, 상담원 전화기 SIPp(`tests/sipp/uas-agent.xml`,
`make sip-agent-call`이 띄우는 것과 같다)를 띄운 채 `make sim-rq-call`. 엔진이 PJSIP/9002를 방으로 부르고 SIPp가 받는다.
(`make sip-agent-call` 자체는 5000, 즉 app_queue 큐로 건다.)

## 7. 검사

- `engine/rq` 단위 테스트 12개: 정상, 포기, 무응답 → 다음 상담원, 연속 무응답 자동 이석, 늦은 상담원, 전환, 3자통화,
  통화 중 이석(두 축), 고객이 먼저 끊음, 벨 시간 초과, 채널 이름 해석, 후처리 뒤 방 잊기
- e2e `TestRoomQueue` (실제 Asterisk, 약 35초): 고객 셋·상담원 둘(셋째는 기다렸다가 받음), 무응답 상담원 → 다음 상담원,
  전환(1002가 들어온 뒤 엔진이 1001을 끊음), 포기. `make e2e`에 포함

## 8. 다음 과제 (직접 해 보기)

- [ ] 엔진 재시작 시 진행 중인 방 다시 맞추기: `ConfbridgeListRooms` / `ConfbridgeList`로 방과 참가자를 받아 콜·상담원 상태 복원
- [ ] 벨이 울리는 사이 고객이 끊으면 상담원 벨도 끊기 (지금은 상담원이 받으면 `RQAgentMissed`로 끝난다 — 벨 채널을 추적해야 한다)
- [ ] 큐·스킬별 분배 (지금은 큐 하나, 모든 상담원이 모든 콜)
- [ ] 보류: 고객만 대기음 (`ConfbridgeMute`/`ConfbridgeUnmute` + 대기음 채널, 또는 고객을 다른 방으로)
- [ ] 협의 전환: 고객이 듣지 못하게 A와 B가 먼저 통화하기 (고객을 잠시 다른 방으로)
- [ ] 녹음: `ConfbridgeStartRecord` — 방 하나에 녹음 하나
- [ ] 실시간 현황판(`web/`)에 붙이기: 상담원 카드를 두 축으로 표시
