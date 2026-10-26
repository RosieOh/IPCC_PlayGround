# 12. 룸 큐 — app_queue 없이 직접 만든 큐

Stage 2의 엔진은 Asterisk `app_queue`가 상담원을 고르고, 엔진은 그 결과(AgentCalled, AgentConnect …)를 따라간다.
실무에서는 반대로 **큐를 직접 만드는** 구조도 흔하다. 분배 규칙을 마음대로 바꾸고, 전환·보류·협의를 자유롭게 하고,
상태를 한 곳에서 소유하기 위해서다. 이 문서는 그런 구조를 작게 만들어 본 것이다 (`engine/rq`, `bin/rqengine`, 현황판 `/rooms`).

| | Stage 2 (app_queue) | 룸 큐 |
|---|---|---|
| 상담원을 고르는 것 | Asterisk `app_queue` | **엔진** (스킬 숙련도 → 가장 오래 쉰 상담원) |
| 고객이 기다리는 곳 | `Queue()` 앱 | **통화마다 생기는 회의방**(ConfBridge)에서 대기음 |
| 상담원 연결 | app_queue가 Dial | 엔진이 **AMI Originate**로 상담원을 같은 방에 부름 |
| 엔진이 받는 사실 | app_queue 이벤트 (Agent*, QueueCaller*) | dialplan이 보내는 **UserEvent** 7종 + OriginateResponse |
| 상담원 상태 | 한 축 (Ready, Ringing, Talking, ACW …) | **두 축**: 업무 모드 × 통화 단계 |
| 전환 · 3자통화 | Asterisk 전환 기능 (BlindTransfer, Atxfer) | 방에 상담원을 더 부르고 / 빼면 끝 |
| 보류 · 협의 전환 | 채널 보류, Atxfer | 고객만 **보류 방**으로 옮기고, 그 사이 원래 방에서 상담원끼리 통화 |
| 엔진 재시작 | 큐 멤버(QueueStatus)로 복원 | 업무 상태는 **엔진 저장 파일**, 통화 상태는 **채널 목록(CoreShowChannels)** 으로 복원 |

## 1. 구조

```text
고객 ──▶ [rq-in] Answer → UserEvent(RQEnter) → ConfBridge(rq-<큐>-<Linkedid>, 고객용)   … 대기음
                                       │
              AMI :5038 ───────────────┼──▶ bin/rqengine (actor: 대기열·상담원·분배를 혼자 소유)
                                       │        ├─ Originate Channel=<상담원> ChannelId=<방>-a<시각> Context=rq-agent
                                       │        ├─ Redirect  고객 → [rq-hold] / [rq-back]   (보류 / 해제)
                                       │        ├─ Hangup    채널 이름 또는 Uniqueid       (전환·벨 취소)
                                       │        └─ ConfbridgeStartRecord                   (녹음)
상담원 ◀── 벨 ──[rq-agent] (받은 뒤) UserEvent(RQAgentIn) → ConfBridge(같은 방, 상담원용)
보류     [rq-hold] UserEvent(RQHold) → ConfBridge(<방>-hold, 혼자 대기음)   [rq-back] UserEvent(RQUnhold) → 원래 방
끊김 처리기 [rq-hangup]: 채널이 어떻게 끊기든 마지막에 RQCustomerOut / RQAgentOut
```

- **dialplan은 상태를 저장하지 않는다.** DB에 쓰지도 않고, 일어난 일을 UserEvent로 알리기만 한다.
  누가 대기 중이고 누구를 부를지는 엔진 하나가 정한다 (단일 소유자 — Stage 2의 D-008과 같은 원칙을 큐에까지 넓힌 것).
- 방 설정은 `asterisk/conf/confbridge.conf`:
  - 고객 `rq_customer`: `wait_marked`(상담원이 올 때까지 대기음) + `end_marked`(마지막 상담원이 나가면 같이 나감)
  - 상담원 `rq_agent`: `marked`
  - 보류 방의 고객 `rq_hold`: 혼자 있으면 대기음(`music_on_hold_when_empty`)
  - 그래서 **상담원이 한 명이라도 방에 있으면 고객은 남는다** — 이 한 가지 성질로 전환과 3자통화가 된다.
- 방 이름은 `rq-<큐>-<Linkedid>`다. 엔진이 다시 시작해 채널 목록에서 방을 읽을 때 큐도 함께 알기 위해서다.
- 대표번호 `5100`(support)·`5101`(sales). 내선 이름이 곧 큐 이름이다 (`[rq-in]`의 `_[a-z].`).
  가짜 고객은 `Local/rq-customer@sim`(support), `Local/rq-sales@sim`(sales).

## 2. 녹화로 확인한 이벤트

`bin/amicli watch -record`로 녹화하며 손으로 액션을 보내 확인했다 (2026-10-26, Asterisk 22.11).

| 경우 | 이벤트 순서 (실제 필드) |
|---|---|
| 정상 | `UserEvent: RQEnter Room: rq-support-1792991351.8 Queue: support Customer: 01071292759` → (엔진 Originate `ChannelId: rq-support-1792991351.8-o1`) → `OriginateResponse Response: Success Reason: 4 Channel: Local/1001@sim-agent-00000006;1 Uniqueid: rq-support-1792991351.8-o1` → `RQAgentIn` → (통화) → `RQAgentOut … Cause: 16` → 고객이 곧바로 방에서 나감(`end_marked`) → `RQCustomerOut` |
| 상담원 무응답 | `OriginateResponse Response: Failure Reason: 3 Channel: Local/1004@sim-noanswer Uniqueid: rqt-room1-o2` — **실패면 Channel은 요청한 문자열 그대로, Uniqueid는 붙인 ChannelId** |
| 벨 취소 | 벨이 울리는 동안 `Hangup Channel: rqt-room1-o1`(**Uniqueid로 끊기**, 응답 `Channel Hungup`) → `Hangup …;2` → `OriginateResponse Response: Failure Reason: 0 Uniqueid: rqt-room1-o1`. Local 채널의 반대쪽 Uniqueid는 `<ChannelId>;2` |
| 보류 | `Redirect` → `ConfbridgeLeave`(원래 방) → `UserEvent: RQHold` → `ConfbridgeStart`·`ConfbridgeJoin Conference: rq-support-1792991351.8-hold`. **끊김 처리기는 불리지 않는다** (끊긴 게 아니라 옮겨졌으니까). 원래 방은 상담원이 있으니 남는다 |
| 보류 해제 | `Redirect` → `ConfbridgeLeave`·`ConfbridgeEnd`(보류 방) → `UserEvent: RQUnhold` → `ConfbridgeJoin`(원래 방) |
| 녹음 | `ConfbridgeStartRecord` → `ConfbridgeRecord`. 방이 닫히면 `ConfbridgeStopRecord`. 파일은 `rq-support-1792991351.8-1792991354.wav`처럼 **이름 뒤에 시각이 붙는다**. 녹음 채널은 CoreShowChannels·ConfbridgeList에 나오지 않고 `Parties`에도 세지 않는다 |
| 채널 목록 (재동기화) | 방 안: `Application: ConfBridge`, `ApplicationData: rq-support-1792991351.8,rq_bridge,rq_customer` (상담원은 `rq_agent`, 보류 중 고객은 `…-hold,rq_bridge,rq_hold`). 벨 울리는 상담원: `Application: AppDial2`, `ApplicationData: (Outgoing Line)`, `Uniqueid: <ChannelId>` |
| 3자 → 전환 | 두 상담원이 `RQAgentIn` → 먼저 들어온 상담원이 `RQAgentOut` → **고객은 남은 상담원과 계속 통화** → 마지막 상담원이 나가면 `RQCustomerOut` |

녹화하며 고친 것: Asterisk에는 `${LINKEDID}` 변수가 없다. 처음에는 방 이름이 모두 `rq-`가 되어 세 콜이 한 방에 모였다.
`${CHANNEL(linkedid)}`로 고쳤다. **추측한 dialplan은 녹화로 확인해야 한다**는 좋은 예다.

## 3. 두 축 상태 모델

| 축 | 값 | 누가 바꾸나 |
|---|---|---|
| 업무 모드 `Mode` | `Logout` · `Inbound`(대기) · `NotReady`(이석, 사유) · `ACW`(후처리) | 상담원 명령, 후처리 타이머 |
| 통화 단계 `Phase` | `Idle` · `Ring`(엔진이 부르는 중) · `Talk`(방에 있음) | 교환기에서 일어난 일 (UserEvent·OriginateResponse·채널 목록) |

**콜은 `Mode=Inbound`이고 `Phase=Idle`이며 그 큐를 받을 수 있는 상담원에게만 간다.** 보류는 상담원 상태가 아니라 콜의 표시(`hold`)다:
고객만 보류 방에 있고 상담원은 원래 방에서 통화 중(Talk)이다.

두 축이 좋은 점: 통화 중에 이석을 누르면 모드만 `NotReady`로 바꾸면 된다. 통화가 끝나도 Inbound가 아니니 콜이 오지 않고,
후처리 대신 이석에 머문다. Stage 2 엔진(한 축)에서 따로 만들어야 했던 **이석 예약이 필요 없다** (`TestNotReadyDuringCall`).
현황판(`/rooms`)은 상담원 카드에 배지·타이머를 축마다 하나씩 두고, 모드 × 단계 표로 "콜을 받는 칸"(대기 × 통화 없음)을 보여 준다.

## 4. 엔진 규칙 (`engine/rq/rq.go`)

| 입력 | 처리 |
|---|---|
| `RQEnter` | 콜 생성(Waiting, 고객 채널 기억) → 분배 |
| 분배 (입력마다) | 기다리는 콜을 **먼저 온 순서**로. 그 큐의 스킬이 있는 `Inbound/Idle` 상담원 중 **숙련도(숫자가 작을수록 먼저) → 가장 오래 쉰 사람**. 스킬을 정하지 않은 상담원은 모든 큐를 받되 맨 뒤. 앞 콜을 받을 사람이 없어도 뒤의 다른 큐 콜은 분배한다 |
| 상담원 부르기 | 상담원 Ring, 콜 Offering, 액션 `Originate` — 만들 채널의 Uniqueid(`ringID` = `<방>-a<시각>`)를 엔진이 정한다 |
| `RQAgentIn` | 상담원 Talk, 콜 Talking. 녹음 정책이면 방의 첫 상담원일 때 `Record`. 전환으로 부른 상담원이면 **이때** 원래 상담원 채널에 `Hangup` |
| `OriginateResponse` Failure | **Uniqueid = ringID**로 어느 벨인지 찾는다(이미 끊은 벨이면 무시). 무응답 +1 → Idle, 콜은 다시 Waiting → 다음 상담원. 연속 2번이면 자동 이석(무응답). 협의로 부른 상담원이면 고객을 돌려보낸다 |
| 벨 시간 + 5초 동안 응답 없음 | 위와 같음 (`offer_timeout` 경고). OriginateResponse를 놓쳐도 콜이 멈추지 않게 |
| `RQCustomerOut` | 콜 끝(`call_end`: 연결된 적 있으면 completed, 없으면 abandoned). **방에 남은 상담원 채널은 끊고, 이 방으로 벨이 울리는 상담원은 ringID로 벨을 끊는다** (무응답으로 세지 않는다) |
| `RQAgentOut` | 상담원 Idle. 대기 모드였으면 후처리(10초 뒤 자동 대기), 이석이었으면 이석 유지. **보류 중인 콜이면 고객을 원래 방으로 돌려보낸다**: 남은 상담원이 있으면 이어지고, 없으면 다시 대기열 |
| `RQHold` / `RQUnhold` | 콜의 `hold` 표시를 켜고 끈다. 돌아왔는데 방에 상담원이 없으면 Waiting → 다시 분배 |
| 명령 `transfer A B` | B를 방으로 부른다. **B가 들어온 뒤에** A를 뺀다 — 먼저 빼면 방에 상담원이 없어져 고객도 나간다(`end_marked`) |
| 명령 `conference A B` | B를 방으로 부른다. 아무도 빼지 않는다 (3자통화) |
| 명령 `hold A` / `unhold A` | 고객 채널을 보류 방 / 원래 방으로 (`Redirect`). 상태는 RQHold/RQUnhold가 오면 바뀐다 |
| 명령 `consult A B` | 고객을 보류하고 B를 원래 방에 부른다 → A와 B가 고객 모르게 먼저 통화 |
| 명령 `complete A` | 협의 완료: A를 빼고 고객을 돌려보낸다 → 고객은 B와 통화 (B가 아직 방에 없으면 `consult_not_ready`) |
| 명령 `cancel A` | 협의 취소: B를 빼고(벨이면 벨을 끊고) 고객을 돌려보낸다 → 고객은 다시 A와 통화 |
| 협의 중 `unhold A` | 아무도 빼지 않고 고객을 돌려보낸다 = 3자통화 |
| 명령 `skills A support:1,sales:2` | 스킬과 숙련도. 비우면 모든 큐 |

출력은 JSONL: `agent`(id, mode, phase, reason, skills, room), `call`(state, hold, consult, agents, entered), `call_end`(outcome, wait_ms, talk_ms),
`action`(Originate/Hangup/Hold/Unhold/Record), `warn`.

## 5. 엔진이 다시 시작할 때 — 재동기화

엔진은 상태를 메모리에만 들고 있다. 엔진이 죽었다 살아나면 무엇을 어디서 되찾을지가 이 구조의 핵심이다.

| 무엇 | 기준 | 어떻게 되찾나 |
|---|---|---|
| 업무 상태 (로그인·대기/이석/후처리·사유·스킬) | **엔진** (D-008) — 교환기는 모른다 | 바뀔 때마다 별도 고루틴이 파일(`-state`, 기본 `data/rqengine/agents.json`)에 저장 → 시작할 때 `Restore`. 실무라면 DB다 |
| 통화 상태 (누가 어느 방에 있나, 누구에게 벨이 울리나) | **교환기** — 물리적으로 그렇다 | `FullyBooted`를 받으면 `CoreShowChannels` → 목록 전체를 `Resync` 입력 하나로 |

`Resync`가 채널 목록을 읽는 법 (모두 2절의 실제 필드):

- `ConfBridge` + `rq_customer` = 고객이 방에 있다 / `rq_hold` = 보류 방에 있다 → 콜. 큐는 방 이름에서, 대기 시작은 `Duration`에서
- `ConfBridge` + `rq_agent` = 상담원이 방에서 통화 중 (인터페이스는 채널 이름에서 접미사를 뗀 것)
- `Uniqueid`가 `<방>-a<숫자>` = 엔진이 부른 벨이 아직 울리는 중 → 그 ringID로 응답·취소를 이어서 처리한다
- 엔진이 알던 콜이 목록에 없으면 엔진이 못 본 사이 끝난 것 → `call_gone` 경고 + `call_end`
- 저장된 기록에 없는 상담원이 방에 있으면 이석(재동기화)으로 만들어 둔다 → 관리자가 정한다

재동기화를 마칠 때까지는 **분배하지 않는다** (`Unsync` → `Resync`): 누가 통화 중인지 모르는 채로 콜을 주면 통화 중인 상담원에게 벨이 간다.
Asterisk가 재시작한 경우도 같은 길이다 — 채널이 하나도 없으니 모든 콜이 끝나고 상담원은 Idle이 된다.

되찾지 못하는 것: 보류·협의·전환 같은 **진행 중인 의도**(예: "B가 들어오면 A를 뺀다"), 정확한 연결 시각(채널 Duration으로 근사),
녹음 중인지(목록에 나오지 않아 켜져 있다고 본다). 이것까지 지키려면 의도도 저장소에 써야 한다 (9절).

## 6. 장단점 — 왜 app_queue를 안 쓰나, 쓰면 무엇을 잃나

| 얻는 것 | 잃는 것 / 새로 생기는 책임 |
|---|---|
| 분배 규칙을 코드로 자유롭게 (스킬·숙련도, 우선순위, VIP, 상담원별 한도 …) | 분배·재시도·타임아웃·벨 취소를 전부 직접 만들고 테스트해야 한다 |
| 전환·3자통화·협의가 "방에 넣고 빼기"로 단순해진다 | 회의방 믹싱(softmix) 비용. 보류도 방을 옮겨서 직접 만든다 |
| 상태의 주인이 하나 — Asterisk 큐 상태와 엔진 상태가 어긋날 일이 없다 | **엔진이 죽으면 아무도 분배하지 않는다.** 고객은 방에서 대기음만 듣는다 (app_queue는 엔진 없이도 분배를 계속한다) |
| dialplan을 거의 고치지 않고 규칙을 바꾼다 (reload 불필요) | 엔진이 재시작하면 진행 중인 방을 다시 맞춰야 한다 (5절). 진행 중인 의도는 잃는다 |
| 녹음이 방 하나에 파일 하나 (3자·협의도 한 파일) | 보류 중에는 고객이 방에 없어 상담원 쪽만 녹음된다 |

## 7. 실행

```bash
make up
make rq-engine              # 터미널 1: 엔진 + 화면용 API 127.0.0.1:8091 (녹음 켬, 가짜 상담원 통화 20초). 명령은 stdin
  login Local/1001@sim-agent 상담원1
  skills Local/1001@sim-agent support:1,sales:2
  ready Local/1001@sim-agent
make sim-rq-call n=3        # 터미널 2: 가짜 고객 셋(support) → 1001이 차례로 받는다
make sim-rq-call q=sales    # sales 큐(5101)로
```

통화 중에 해 볼 것 (stdin 또는 현황판 버튼): `hold`/`unhold`, `consult A B` → `complete A`(넘기기) / `cancel A`(취소) / `unhold A`(3자로),
`transfer A B`, `conference A B`, 통화 중 `notready A 식사`(통화가 끝나면 후처리 없이 이석).
엔진을 Ctrl+C로 끄고 다시 켜면 업무 상태는 파일에서, 진행 중인 통화는 채널 목록에서 되살아난다 (이벤트 로그에서 확인).

**현황판**: `script/start.sh`가 두 엔진과 화면을 모두 켠다 → `http://localhost:3000/rooms` (레일 아래쪽 ⇄ 버튼으로 두 현황판을 오간다).
`script/demo.sh --rooms`는 스킬이 다른 가짜 상담원 셋(1001 support · 1002 sales · 1003 모든 큐)과 support·sales 콜을 넣는다.
따로 켜려면 `make rq-engine` + `make web`. 두 엔진을 같이 켜 두면 Stage 2 현황판에는 룸 큐 콜이 "큐 진입 전 종료(no_queue)"로 보인다
(Stage 2 엔진은 모든 채널을 콜로 보고, 룸 큐 콜은 app_queue에 들어가지 않으니까).

실제 SIP 상담원도 된다(확인함): 엔진에 `login PJSIP/9002 SIPP-9002` → `ready PJSIP/9002`, 상담원 전화기 SIPp(`tests/sipp/uas-agent.xml`,
`make sip-agent-call`이 띄우는 것과 같다)를 띄운 채 `make sim-rq-call`. 엔진이 PJSIP/9002를 방으로 부르고 SIPp가 받는다.
(`make sip-agent-call` 자체는 5000, 즉 app_queue 큐로 건다.)

## 8. 검사

- `engine/rq` 단위 테스트 33개: 정상, 포기, 무응답 → 다음 상담원, 연속 무응답 자동 이석, 벨 취소와 늦게 온 실패 응답 무시,
  전환, 3자통화, 보류·해제, 보류 중 상담원 이탈 → 다시 대기열, 협의 완료·취소(통화 중·벨 중)·무응답·3자로 합치기, 보류 중 전환,
  스킬(큐별·숙련도·스킬 없음), 녹음은 방마다 한 번, 업무 상태 저장·복원, 채널 목록으로 재동기화(통화·벨·보류·못 받은 RQEnter·끝난 콜),
  Asterisk 재시작, 모르는 상담원, 통화 중 이석(두 축), 명령 해석
- `engine/rq` 껍데기 테스트 3개 (가짜 AMI 서버): 되살린 업무 상태 + 재동기화 전에는 분배하지 않음 → CoreShowChannels 뒤 Originate
  (ChannelId·변수), 녹음·보류의 AMI 액션 모양, 업무 상태 저장, 화면 API(SSE 첫 상태·명령·잘못된 명령 400)
- e2e `TestRoomQueue` (실제 Asterisk, 약 100초, `make e2e`에 포함). 엔진 출력과 함께 Asterisk 쪽 사실도 본다:
  - 고객 셋·상담원 둘(셋째는 기다렸다가 받음) + 방마다 녹음 파일
  - 무응답 → 다음 상담원
  - 벨 취소: 엔진의 Hangup으로 끝났다(`OriginateResponse Reason: 0`, 벨 시간 초과면 3)
  - 전환, 보류(보류 방 `ConfbridgeJoin`), 보류 중 상담원 이탈 → 다시 분배
  - 협의 전환: 방 입장 순서가 고객 → 고객(보류 방) → 1002 → 고객
  - 스킬(sales 콜은 sales 상담원에게), 포기
  - 재동기화: 통화 중에 엔진을 내리고, 꺼진 사이 고객 하나 더 → 새 엔진이 통화를 이어서 관리하고 늦게 온 콜도 분배
- 현황판: `make web-check`, 실제 엔진으로 화면 확인(협의 완료 버튼 → 고객이 넘겨받은 상담원과 남음)

## 9. 다음 과제 (직접 해 보기)

- [ ] 진행 중인 의도까지 복원: 보류·협의·전환 요청을 저장소에 쓰고 Resync에서 되살리기 (지금은 "B가 들어오면 A를 뺀다"를 잊는다)
- [ ] 엔진 이중화: 엔진 두 대(active-standby). 상태 저장소를 공유하고, standby가 넘겨받을 때 Resync. 둘이 동시에 분배하지 않게 하는 잠금
- [ ] 큐 우선순위·VIP: 대기 순서를 enteredAt만이 아니라 큐 가중치·고객 등급으로 (dispatch의 정렬만 바꾸면 된다)
- [ ] 대기 안내: 대기 중인 고객에게 순번·예상 대기 시간 멘트 (엔진이 계산해서 방에 재생: `ConfBridge`의 announcement 또는 Originate로 재생 채널)
- [ ] 콜백 예약: 오래 기다린 고객이 끊고 나중에 엔진이 다시 걸어 준다
- [ ] 상담원 단말 상태 반영: PJSIP 등록이 끊긴 상담원(`DeviceStateChange`, `ContactStatus`)에게는 벨을 보내지 않기
- [ ] 녹음 메타데이터: 파일 이름·방·상담원·고객을 DB(MySQL)에 남기는 워커 (AMI 읽기 루프를 막지 않게 별도 고루틴)
- [ ] 감청·코칭: 관리자를 방에 듣기 전용 사용자(`ConfbridgeMute`)로 부르기
