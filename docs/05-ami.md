# 05. AMI — 프로토콜, 실제 이벤트, 클라이언트 설계

AMI(Asterisk Manager Interface)는 CTI 엔진이 Asterisk와 대화하는 통로다. 회사의 Rust 엔진이 하는 일의 바닥이 이것이고,
Go로 옮길 때도 가장 먼저 옮기게 될 부분이다. 이 문서의 이벤트 예시는 모두 이 환경(Asterisk 22.11, AMI 11.0.0)에서 녹화한 것이다.

## 1. 개요

- **TCP 5038**의 텍스트 프로토콜이다. 접속을 유지한 채로 양방향으로 메시지를 주고받는다.
- 메시지는 세 종류다.

| 종류 | 방향 | 첫 줄 | 예 |
|---|---|---|---|
| Action | 엔진 → Asterisk | `Action: …` | 전화 걸기(Originate), 큐 로그인(QueueAdd), 상태 조회(QueueStatus) |
| Response | Asterisk → 엔진 | `Response: …` | 액션에 대한 답. `ActionID`로 짝을 짓는다 |
| Event | Asterisk → 엔진 | `Event: …` | 상태 변화 알림. 언제든 날아온다 |

- AMI는 **암호화되지 않은 평문**이다. 이 프로젝트는 5038을 Mac의 127.0.0.1에만 연다.
  운영에서는 내부망 + 접속 허용 목록, 필요하면 TLS(`manager.conf`의 `tlsenable`)나 SSH 터널을 쓴다.

## 2. 메시지 형식

`Key: Value` 줄을 CRLF로 끝내고, **빈 줄 하나**로 메시지를 끝낸다. 접속 직후 한 줄짜리 배너가 먼저 온다.

```text
Asterisk Call Manager/11.0.0                ← 배너 (빈 줄로 끝나지 않는 유일한 줄)

Action: Login                               ← 엔진이 보냄
Username: ipcc
Secret: ipcc-secret
Events: on
ActionID: myhost-1234-1

Response: Success                           ← Asterisk의 응답
ActionID: myhost-1234-1
Message: Authentication accepted

Event: FullyBooted                          ← 로그인 직후 바로 오는 이벤트
Privilege: system,all
Timestamp: 1792122876.676421
Status: Fully Booted
```

규칙과 함정:

- 키는 대소문자를 구분하지 않는다. 같은 키가 **여러 번** 나올 수 있다(`Output`, `Variable`). 그래서 `map`이 아니라 순서 있는 목록으로 담는다.
- 값의 앞 공백은 한 칸만 뗀다. `Command` 액션의 출력처럼 들여쓰기가 의미 있는 값이 있다.
- 값에 CR/LF가 들어가면 안 된다. 외부 입력(예: 고객 이름)을 그대로 넣으면 `\r\nAction: Hangup` 같은
  필드가 끼어든다(헤더 인젝션). `Message.Encode`가 이런 값을 거부한다.
- `Privilege`는 이벤트 클래스(call, agent, system …)다. `Login`의 `Events` 값이나 `manager.conf`의 `read`로 거른다.
- `Timestamp`는 `manager.conf`의 `timestampevents = yes` 때문에 붙는다. 유닉스 초.마이크로초이고 시간대와 무관하다.

### 목록형 응답

`CoreShowChannels`, `QueueStatus`처럼 여러 항목을 돌려주는 액션은 응답 하나 + 이벤트 여러 개 + 완료 이벤트로 온다.
모두 같은 `ActionID`를 단다.

```text
Response: Success                 ActionID: X   EventList: start
Event: QueueParams                ActionID: X   (큐 설정과 누적 통계)
Event: QueueMember                ActionID: X   (멤버마다)
Event: QueueEntry                 ActionID: X   (대기 고객마다)
Event: QueueStatusComplete        ActionID: X   EventList: Complete   ListItems: N
```

목록 사이에 다른 실시간 이벤트가 끼어들 수 있다. 그래서 **ActionID로 골라 담아야 한다.** (`Client.SendList`)

## 3. 설정 (`asterisk/conf/manager.conf`)

| 항목 | 이 프로젝트 | 의미 |
|---|---|---|
| `[ipcc]` | `secret = ipcc-secret` | AMI 사용자. 엔진·amicli가 쓴다 |
| `deny` / `permit` | 사설 대역 허용 | Mac에서 온 접속은 컨테이너 안에서 172.23.0.1(Docker 게이트웨이)로 보인다 |
| `read` / `write` | `all` | 학습용. 운영에서는 최소 권한 (예: read=call,agent,reporting / write=originate,call,agent) |
| `eventfilter` | `!Event: VarSet`, `!Event: RTCP(Sent\|Received)` | 서버에서 시끄러운 이벤트를 거른다. 녹화에서 VarSet·RTCP 0건 확인 |
| `timestampevents` | `yes` | 모든 이벤트에 Timestamp |
| `writetimeout` | 기본 100ms | Asterisk가 이 세션에 쓰는 데 이 시간을 넘기면 느린 클라이언트로 보고 연결을 끊는다 |

## 4. 자주 쓰는 액션

| 액션 | 엔진에서 쓰는 장면 | 비고 |
|---|---|---|
| `Login` / `Logoff` | 접속 | `Events: on/off/클래스` |
| `Ping` | 하트비트 | 응답이 없으면 연결이 죽은 것으로 본다 |
| `CoreShowChannels` | 재접속 직후 스냅샷 (지금 있는 채널) | 목록형 |
| `QueueStatus` / `QueueSummary` | 재접속 직후 스냅샷, 현황판 | 목록형 |
| `QueueAdd` / `QueueRemove` | 상담원 로그인 / 로그아웃 | `Interface`, `MemberName`, `Paused` |
| `QueuePause` | 이석, 후처리, 대기 | `Paused=true/false`, `Reason`(사유코드) |
| `Originate` | 상담원 발신(click-to-call), 감청(ChanSpy), 시뮬레이션 | **`Async: true`** 를 쓰지 않으면 상대가 받을 때까지 응답이 안 온다 |
| `Redirect` | 콜을 다른 다이얼플랜 위치로 보내기 | ARS 재진입 등 |
| `BlindTransfer` / `Atxfer` | 상담원 호전환 | |
| `Hangup` | 강제 종료 | |
| `Getvar` / `Setvar` | 채널 변수 읽기/쓰기 | 콜에 업무 데이터 붙이기 |
| `Command` | CLI 명령 실행 | 출력 형식이 버전마다 다르므로 **파싱하지 말 것.** 전용 액션을 쓴다 |

Asterisk CLI에서 문서를 바로 볼 수 있다: `make rx c="manager show command QueuePause"`,
`make rx c="manager show event AgentConnect"`.

## 5. 실제 녹화로 본 "콜 1건"

샘플 녹화 `engine/testdata/sim-normal-and-abandon.log`의 첫 번째 콜이다(`make sim-agents` + `make sim-call`로 만든 것).
고객 1명이 대표번호로 들어와 상담원 SIM-1001과 5초 통화하고, 상담원이 먼저 끊었다. 주요 이벤트만 추렸고,
모든 이벤트의 `Linkedid`는 `1792123138.0`이다. 직접 보려면 `bin/amicli replay engine/testdata/sim-normal-and-abandon.log`.
실제 SIP 고객과 SIP 상담원 사이의 콜은 `engine/testdata/sip-agent-call.log`에 있다 (`make sip-agent-call`로 만든 것, 6절 13번).

| 시각 | 이벤트 | 채널 | 핵심 필드 | 엔진 입장의 의미 |
|---|---|---|---|---|
| 12:58:58.292 | Newchannel ×2 | customer;1, ;2 | CallerIDNum=`<unknown>` | 고객 콜 시작 (Local 채널 쌍) |
| 12:58:58.293 | NewCallerid ×2 | customer;1, ;2 | CallerIDNum=01066426585 | 발신번호가 **뒤늦게** 채워진다 |
| 12:58:58.301 | Newstate ×2 | customer | ChannelStateDesc=Up | `[inbound]`의 Answer() |
| 12:59:00.216 | QueueCallerJoin | customer;2 | Queue=support Position=1 Count=1 | 대기 시작 (안내멘트 약 2초 뒤) |
| 12:59:00.216 | MusicOnHoldStart | customer;2 | | 대기음 |
| 12:59:00.217 | Newchannel ×2 | 1001@sim-agent;1, ;2 | | 큐가 상담원 쪽 구간을 만든다 |
| 12:59:00.217 | AgentCalled | customer;2 → agent;1 | MemberName=SIM-1001 | 상담원 **벨울림** |
| 12:59:00.217 | DialBegin / DialState | | DialStatus=RINGING | |
| 12:59:02.226 | Newstate ×2, DialEnd | agent | DialStatus=ANSWER | 상담원 응답 |
| 12:59:02.227 | QueueCallerLeave | customer;2 | Count=0 | 대기열에서 빠짐 |
| 12:59:02.227 | AgentConnect | customer;2 + Dest=agent;1 | HoldTime=2 RingTime=2 | 상담원 **통화중** |
| 12:59:02.228 | BridgeEnter ×2 | agent;1, customer;2 | BridgeUniqueid=67e07799… | 두 구간이 한 브리지에 |
| 12:59:07.741 | SoftHangupRequest, HangupRequest, Hangup | agent;2 | Cause=16 Normal Clearing | 상담원이 끊음 |
| 12:59:07.742 | BridgeLeave ×2 | agent;1, customer;2 | | |
| 12:59:07.742 | Hangup ×2 | customer;2, customer;1 | | 고객 쪽 구간 정리 |
| 12:59:07.743 | AgentComplete | customer;2 | HoldTime=2 TalkTime=5 Reason=agent | 통화 종료 → 엔진은 **후처리** 시작 |
| 12:59:07.743 | Hangup | agent;1 | | 마지막 채널 정리 |

같은 녹화의 `AgentConnect` 실제 필드 (`amicli replay -only AgentConnect -v`):

```text
Event: AgentConnect
Privilege: agent,all
Channel: Local/customer@sim-00000000;2      ← 고객 쪽 채널 스냅샷
CallerIDNum: 01066426585
CallerIDName: 고객
Uniqueid: 1792123138.1
Linkedid: 1792123138.0
DestChannel: Local/1001@sim-agent-00000001;1 ← 상담원 쪽 채널 스냅샷 (Dest 접두사)
DestUniqueid: 1792123140.2
DestLinkedid: 1792123138.0
Queue: support
Interface: Local/1001@sim-agent
MemberName: SIM-1001
HoldTime: 2
RingTime: 2
(…일부 생략)
```

큐 관련 이벤트는 **채널 스냅샷 두 개**(접두사 없음 = 고객, `Dest` = 상담원)를 한 이벤트에 담는다.

샘플 녹화의 두 콜(정상 1건, 포기 1건)은 `data/asterisk/log/queue_log`에 이렇게 남는다
(`시각|callid|큐|상담원|이벤트|데이터…`):

```text
1792123140|1792123138.1|support|NONE|ENTERQUEUE||01066426585|1
1792123142|1792123138.1|support|SIM-1001|CONNECT|2|1792123140.2|2        대기 2초, 상담원 채널, 벨 2초
1792123147|1792123138.1|support|SIM-1001|COMPLETEAGENT|2|5|1            대기 2초, 통화 5초, 처음 위치 1
1792123150|NONE|support|SIM-1001|PAUSE|휴식
1792123156|1792123150.5|support|NONE|ABANDON|1|1|4                      위치 1, 처음 위치 1, 4초 기다리다 포기
```

## 6. 실측에서 배운 것

녹화를 보면서 확인한 사실과, 그 과정에서 찾은 버그다. Stage 2 상태머신을 설계할 때 그대로 반영해야 한다.

1. **Newchannel 시점에는 발신번호가 없을 수 있다.** Originate로 만든 Local 채널은 `<unknown>`으로 왔다가
   `NewCallerid`로 채워진다. 실제 SIP 채널(`PJSIP/9001-…`)은 Newchannel부터 번호가 있었다 (`sip-agent-call.log`).
   어느 쪽이든 번호는 NewCallerid까지 보고 확정한다.
   Newchannel의 CallerIDNum으로 고객을 판단하면 안 된다.
2. **Local 채널은 `;1`/`;2` 쌍이다.** 큐에 들어간 것은 `customer;2`, '고객 전화기' 역할은 `customer;1`이다.
   실제 SIP 고객이면 `PJSIP/…` 채널 하나지만, 전환·ARS에서 Local 채널이 끼면 이 구조가 다시 나온다.
3. **AgentComplete는 Hangup보다 늦게 올 수 있다.** 샘플에서는 상담원 `;2`와 고객 `;1`·`;2`가 모두 Hangup된
   **뒤에** AgentComplete가 왔다. "Hangup이 오면 통화 종료"로 상태를 바꾸면 AgentComplete 처리 때 이미 끝난 통화를
   다시 만지게 된다. 어떤 이벤트를 상태 전이의 기준으로 삼을지 정하고, 순서가 바뀌어도 결과가 같게 만든다.
4. **queue_log의 callid는 큐에 들어간 채널의 Uniqueid(`…138.1`)다. Linkedid(`…138.0`)가 아니다.**
   엔진 이력과 queue_log를 조인할 때 주의한다.
5. **이벤트 대부분은 콜 흐름과 직접 관계가 없다.** 샘플 녹화(콜 2건)의 이벤트 133개 중 Newexten이 25개,
   DeviceStateChange가 24개였다. DeviceStateChange에는 `Queue:support_pause_…` 같은 큐의 가상 디바이스 상태까지 나온다.
   처리하지 않을 이벤트는 엔진 입구에서 빨리 버린다.
6. **OriginateResponse는 `Response:` 필드를 가진 '이벤트'다.** (버그로 발견)
   처음 구현은 `Response` 키가 있으면 응답으로 분류했고, 그 결과 비동기 Originate의 결과 이벤트가 조용히 사라졌다.
   지금은 `Event` 키를 먼저 본다(`Message.IsResponse`). 재발 방지 테스트: `TestEventWithResponseFieldIsAnEvent`,
   `TestOriginateResponseReachesEventStream`.
7. **다른 AMI 세션의 ActionID가 달린 이벤트도 온다.** 같은 OriginateResponse가 그 Originate를 보내지 않은
   `watch` 세션에도 왔다. ActionID를 클라이언트마다 겹치지 않게 만들어야 하는 이유다 (기본값 `<호스트>-<pid>-<순번>`).
8. **Asterisk 재시작 직후에는 Docker 포트포워더가 접속만 받고 바로 끊는다.** 클라이언트에는 `banner: EOF`로 보인다.
   백오프 재접속으로 자연스럽게 해결되지만, 로그만 보고 "AMI 버그"로 착각하기 쉽다.
9. **다른 클라이언트의 로그인도 보인다.** `SuccessfulAuth` 이벤트(보안 클래스)로 온다.
10. **재시작 뒤에는 큐 멤버가 비어 있다** (`persistentmembers = no`). 엔진이 재접속 후 스냅샷을 보고
    로그인 중인 상담원을 다시 `QueueAdd` 해야 한다 → Stage 2 재동기화.
11. **AgentComplete가 채널 정보 없이 올 수 있다.** (`make e2e`로 재현)
    콜 2건이 같은 순간에 끝나면, 나중에 나온 AgentComplete에는 아래 필드만 있고
    `Channel`·`CallerIDNum`·`Uniqueid`·`Linkedid`(고객 쪽)와 `Dest…`(상담원 쪽)가 통째로 빠진다.
    e2e 실행 몇 번에 한 번꼴로 나왔다. 원인은 소스에 있다(`apps/app_queue.c`): AgentComplete는 Hangup을 받은 **뒤**
    `handle_hangup()`에서 보내는데, 이때 채널 정보를 `ast_channel_snapshot_get_latest()`로 캐시에서 다시 찾는다.
    그사이 두 채널이 다 정리되어 캐시에서 지워졌으면 스냅샷이 없고, `queue_multi_channel_to_ami()`는 그 부분을 빈 채로 둔다.

    ```text
    Event: AgentComplete
    Privilege: agent,all
    Timestamp: 1792138283.747839
    Queue: support
    Interface: Local/1002@sim-agent
    MemberName: SIM-1002
    HoldTime: 1
    TalkTime: 2
    Reason: agent
    ```

    → 상태머신은 AgentComplete를 **상담원(`Interface`/`MemberName`) 기준**으로 처리하고, 어느 콜이었는지는
    AgentConnect 때 기록해 둔 '상담원 ↔ 콜' 짝으로 찾는다. 콜 ID로만 짝짓는 코드는 가끔 통화 종료를 놓친다.
12. **같은 정보도 이벤트와 스냅샷에서 필드 이름이 다르다.** 실시간 이벤트(`QueueMemberAdded`, `QueueMemberPause` …)는
    `MemberName`·`Interface`이고, `QueueStatus` 응답의 `QueueMember`는 `Name`·`Location`이다.
    재동기화 코드에서 이벤트 처리 함수를 그대로 재사용하면 빈 값을 읽는다.
13. **실제 SIP 상담원과 가짜 상담원(Local)은 이벤트 모양이 다르다.** (`engine/testdata/sip-agent-call.log`)
    SIP 고객 → 큐 → SIP 상담원(PJSIP/9002) 콜 1건은 채널 2개, 이벤트 56개였다. 가짜 상담원 콜은 Local 채널 쌍 때문에
    채널 4개에 `LocalBridge`까지 붙어 이벤트가 두 배 넘게 나온다. 상태머신 테스트에는 두 종류의 녹화가 다 필요하다.
14. **벨이 울리는 동안에도 상담원 장치 상태는 `BUSY(3)`다.** SIP 상담원의 `QueueMemberStatus`가 AgentCalled 직후
    `Status=3(BUSY)`, 통화가 끝나자 `1(NOT_INUSE)`였다 (`device_state_busy_at = 1`이라 채널 하나만 있어도 BUSY).
    상담원의 '벨울림'은 장치 상태가 아니라 AgentCalled로 판단한다.

아래는 Stage 2에서 시나리오 녹화(`engine/state/testdata/*.ami`, `make scenarios`)와 e2e로 확인한 것이다.

15. **Asterisk는 모듈을 다 올리기 전에도 AMI 로그인을 받는다.** 재시작 직후 바로 붙은 엔진이 보낸 `QueueStatus`가
    `Invalid/unknown command: QueueStatus`로 실패했다 (app_queue가 아직 없다). e2e 재시작 단계가 찾았다.
    `FullyBooted` 이벤트가 신호다. 소스(`main/manager.c` `action_login`)를 보면 이미 부팅된 상태면 로그인 응답 바로 뒤에
    보내고(이때는 `Timestamp`가 없다), 부팅 중이면 끝나는 순간(`main/asterisk.c`) 모든 세션에 보낸다.
    → 재동기화는 `Connected`가 아니라 FullyBooted를 받은 뒤에 한다.
16. **큐 타임아웃도 `QueueCallerAbandon`을 낸다.** (`queue-timeout.ami`) app_queue가 타임아웃 때도 `record_abandoned()`를 부르고,
    queue_log에는 `EXITWITHTIMEOUT`이 적힌다. 진짜 포기는 고객 채널의 `HangupRequest`가 Abandon보다 **먼저** 오고,
    타임아웃은 끊기 요청 없이 Abandon이 온 뒤 다이얼플랜이 계속된다. 이 차이로 구분한다.
17. **`RingTime`의 단위가 이벤트마다 다르다.** (`noanswer.ami`) `AgentConnect`의 `RingTime: 1`은 초,
    `AgentRingNoAnswer`의 `RingTime: 15000`은 밀리초다.
18. **Hold/Unhold는 보류를 건 쪽 채널에 온다.** (`hold.ami`) SIP 상담원이 re-INVITE(sendonly)로 보류하면 `PJSIP/9002` 채널에
    Hold가 온다. 블라인드 전환 때는 Hold 없이 **Unhold만** 오고(`blind-transfer.ami`), 3자통화로 합쳐질 때는 Unhold가 오지 않는다.
19. **전환과 3자통화에서도 Linkedid는 그대로다.** 블라인드·어텐디드 전환 대상과 3자통화 참여자 채널이 모두 원래 콜의 Linkedid를 가졌다.
    원래 상담원의 AgentComplete는 전환이면 `Reason: transfer`, 3자통화에서 먼저 빠지면 `Reason: agent`였다.
    전환 대상은 큐가 아니라 다이얼플랜이 부르므로 대상 쪽 Agent* 이벤트는 없다.
20. **AMI `Atxfer`는 DTMF로 동작한다.** 상담원 채널에 `DTMFEnd`가 6개(`*2` + `1002`) 찍히고 그 사이에 Hold가 온다.
    즉 features.conf의 `atxfer` 키를 누른 것처럼 흉내 낸다. 같은 방법으로 `PlayDTMF`에 `Receive: true`를 주면
    채널이 키를 '받은' 것처럼 만들 수 있어서, `*3`(atxferthreeway)으로 3자통화를 만들었다 (`three-way.ami`).
    3자통화가 되면 `BridgeMerge`(전환용 브리지 → 원래 브리지)와 `AttendedTransfer`(`DestType: Threeway`)가 온다.
    일반 어텐디드 전환은 `DestType: Bridge`다.
21. **벨이 울리는 중에 고객이 끊으면 상담원 쪽 이벤트가 없다.** (`ring-abandon.ami`) AgentRingNoAnswer도 AgentComplete도 없이,
    상담원 채널의 `Hangup`과 `QueueCallerAbandon`만 온다. 상담원을 '벨울림'에서 돌려놓는 건 엔진이 Abandon을 보고 해야 한다.

아래는 룸 큐(app_queue 없이 직접 만든 큐, [12 문서](12-room-queue.md))를 만들며 녹화로 확인한 것이다.

22. **비동기 Originate가 실패하면 `OriginateResponse`의 Channel은 요청한 문자열 그대로다.** 성공이면
    `Local/1001@sim-agent-0000000b;1`처럼 만들어진 채널 이름이, 상담원이 받지 않으면 `Response: Failure`, `Reason: 3`과 함께
    `Channel: Local/1004@sim-noanswer`, `Uniqueid: <unknown>`이 온다. 접미사(`-0000000b;1`)를 떼면 둘 다 같은 인터페이스가 된다.
23. **dialplan에서 Linkedid는 `${CHANNEL(linkedid)}`다.** `${LINKEDID}`라는 변수는 없어서 빈 문자열이 된다 — 방 이름이 모두
    `rq-`가 되어 여러 콜이 한 회의방에 모인 뒤에야 알았다. (`${UNIQUEID}`는 변수로 있다.)
24. **ConfBridge의 `end_marked`는 '마지막' marked 사용자가 나갈 때 동작한다.** 상담원(marked) 둘이 있는 방에서 한 명이 나가도
    고객은 남고, 마지막 상담원이 나가면 고객이 곧바로 방에서 나온다(ConfbridgeLeave → 끊김 처리기의 UserEvent).
    그래서 회의방 기반 전환은 **새 상담원이 들어온 뒤에** 원래 상담원을 빼야 한다.

## 7. 클라이언트 설계 (`engine/ami`)

### 구성

```text
                 ┌────────────── Run (재접속 루프) ───────────────┐
 Asterisk ──TCP──▶ readLoop ──┬─ ActionID가 기다리는 액션이면 ──▶ pending[id] ──▶ Send/SendList 호출자
                 │            └─ 아니면 이벤트 ──▶ in ──▶ pump(무제한 큐) ──▶ Events() ──▶ 소비자
                 │  pingLoop: 15초마다 Ping, 5초 안에 답이 없으면 연결을 끊는다
                 └─ Send/SendList: writeMu로 직렬화해서 쓰기, ActionID 붙이기
```

| 결정 | 이유 |
|---|---|
| 로그인은 readLoop 전에 **동기로** | 로그인 전엔 이벤트가 안 온다. 덕분에 `Connected`가 그 연결의 어떤 이벤트보다 먼저 나간다 |
| 연결 상태(`Connected`/`Disconnected`)를 **이벤트와 같은 채널**로 | 소비자가 "어디까지가 끊기기 전 이벤트인지" 순서로 알 수 있다 |
| 읽기 루프와 소비자 사이에 **무제한 큐** | 소비자가 느려도 읽기는 멈추지 않는다 (`writetimeout`으로 끊기는 것 방지). 대신 `QueueDepth()`로 적체를 감시 |
| 목록 항목은 `SendList`에만 | 스냅샷 결과가 실시간 이벤트로 새면 상태머신이 두 번 반영한다 |
| 상태머신용으로 `SendListInline`을 따로 | 응답·목록 항목·Complete를 **받은 순서 그대로** `Events()`로 흘린다. 스냅샷과 실시간 이벤트의 앞뒤를 지킨다 (06 문서 7절) |
| ActionID = `<호스트>-<pid>-<순번>` | 다른 세션 이벤트에 달린 ActionID와 겹치지 않게 |
| 끊기면 기다리던 액션을 **즉시** 실패 (`ErrDisconnected`) | 타임아웃(10초)까지 붙잡혀 있지 않게 |
| 녹화는 **받은 바이트만, 로그인 성공한 연결만** | 보낸 Secret은 파일에 남지 않고, 재접속 시도마다 빈 파일이 생기지 않는다 |
| 적체 경고는 **경고선에서 한 번, 절반 아래에서 해소 한 번** | 경고선 근처에서 깊이가 오르내려도 로그가 쏟아지지 않게 (`QueueWarnDepth`) |
| MD5 챌린지 로그인 선택 (`AuthMD5`) | 평문 AMI에서도 비밀번호 자체는 흘리지 않는다. Key = MD5(challenge + secret) |

### 고루틴과 채널

Stage 1 과제 "고루틴 구성을 그려 보라"의 답이다. `Run` 하나가 살아 있는 동안:

| 고루틴 | 수명 | 하는 일 |
|---|---|---|
| `Run` (부른 쪽 고루틴) | 클라이언트 전체 | 접속 → `handshake`(동기 로그인) → 세션 대기 → 백오프 → 재접속을 반복. `session`도 여기서 돈다 |
| `pump` | 클라이언트 전체 | `in` → 내부 슬라이스 → `out`. 소비자 속도와 관계없이 `in`을 계속 비운다 |
| `readLoop` | 연결 하나 | 소켓에서 메시지를 읽어 ① `pending[ActionID]`에 넣거나 ② `in`으로 보낸다 |
| `pingLoop` | 연결 하나 | 15초마다 `Send(Ping)`. 실패하면 `pingFailed`로 알린다 |
| `Send`/`SendList`를 부른 고루틴 | 요청 하나 | 쓰기(`writeMu`) 후 `pending.done`이 닫힐 때까지 기다린다 |

즉 연결 하나에 고정 고루틴은 4개(`Run`, `pump`, `readLoop`, `pingLoop`)이고, 요청마다 부른 쪽 고루틴이 잠깐 기다린다.

| 채널 | 보내는 쪽 → 받는 쪽 | 내용 |
|---|---|---|
| `in` | `readLoop`, `session` → `pump` | AMI 이벤트, `Connected`/`Disconnected` (같은 채널이라 순서가 섞이지 않는다) |
| `out` (`Events()`) | `pump` → 소비자 | 위와 같은 순서 그대로 |
| `pendingAction.done` | `readLoop`(응답 도착) 또는 `dropConn`(끊김) → `Send` 호출자 | 닫히면 결과가 준비된 것 |
| `readerDone` | `readLoop` → `session` | 읽기가 끝났다 (끊김) |
| `pingFailed` | `pingLoop` → `session` | Ping에 답이 없다 → 연결을 끊고 재접속 |

### 타임아웃과 재접속

| 값 | 기본 | 의미 |
|---|---|---|
| DialTimeout | 5s | TCP 접속 |
| ActionTimeout | 10s | 응답 대기 (ctx에 데드라인이 없을 때) |
| PingInterval / PingTimeout | 15s / 5s | 하트비트. 응답 없는 Asterisk(멈춤, `docker pause`)를 약 20초 안에 감지 |
| BackoffMin / BackoffMax | 0.5s / 30s | 재접속 대기. 실패할 때마다 2배, ±20% 지터. 정상 동작하다 끊긴 경우는 0.5s부터 다시 |

TCP 연결이 살아 있는데 상대가 답하지 않는 경우(프로세스 정지, 중간 장비 장애)는 읽기 오류가 나지 않는다.
Ping 없이는 영원히 모른다. 실측: `docker compose pause` 18초 뒤 감지, `unpause` 즉시 재접속.

**왜 바로가 아니라 5~20초 뒤인가** (Stage 1 과제의 답): 멈춘 순간부터 다음 Ping이 나갈 때까지 0~15초(PingInterval),
그 Ping의 응답을 기다리는 5초(PingTimeout)가 더해진다. 그래서 감지까지 최소 5초, 최대 20초다.
실측 18초는 멈춘 지 약 13초 뒤에 Ping이 나갔다는 뜻이다. 더 빨리 알아야 하면 간격을 줄이되, Ping도 AMI 부하라는 점을 같이 본다.

### 재동기화

이벤트만으로 상태를 쌓는 엔진은 끊긴 동안의 변화를 모른다. 그래서 다시 붙을 때마다 기준점을 다시 잡는다.
`amicli watch`는 `Connected`를 받으면 바로 하고, 상태머신 엔진(`engine/live`)은 **`FullyBooted`를 받은 뒤에** 한다 (6절 15번).

1. `CoreShowChannels` → 지금 살아 있는 채널
2. `QueueStatus` → 큐 설정·누적 통계, 멤버(로그인·이석 상태), 대기 고객
3. 메모리 상태와 비교해서 보정한다. 보정 내역은 경고 줄로 남긴다 ([06 문서 7절](06-state-model.md#7-재동기화))

주의: 스냅샷을 받는 동안에도 실시간 이벤트는 계속 쌓인다. `SendList`로 받으면 목록과 그 사이·뒤 이벤트의 순서를 잃는다.
엔진은 `SendListInline`으로 받아서 녹화와 같은 순서(응답 → 항목 → Complete가 이벤트 사이에 끼어 있음)로 처리한다.

## 8. 테스트 전략

- **가짜 Asterisk 서버**(`client_test.go`의 `fakeAMI`): 실제 TCP로 접속을 받고, 테스트가 응답을 조종한다.
  응답 순서 뒤섞기, 응답 없이 끊기, Ping 무시 같은 장애를 코드로 재현한다.
- **한 바이트씩 읽기**(`iotest.OneByteReader`): TCP는 메시지 경계를 지켜 주지 않는다. 쪼개져 와도 같은 결과여야 한다.
- **녹화 재생**: 녹화 파일 = 받은 바이트 그대로라서 같은 `Reader`로 다시 읽힌다. Stage 2의 golden test와
  Rust↔Go 차분 테스트가 이 성질 위에 선다.
- 모든 테스트는 `-race`로 돌린다 (`make test`). 10회 반복 실행으로 타이밍 의존 테스트가 흔들리지 않는지 확인했다.
- **e2e** (`make e2e`, `engine/e2e`): 실제 Asterisk에 붙어서 로그인 → 정상 콜 2건 → 포기 → SIPp 등록·통화 → 로그아웃 → 실제 SIP 상담원 콜 → 음성(RTP) 왕복을
  이벤트로 검증한다. `E2E_RESTART=1`이면 Asterisk 재시작 후 재접속과 녹화 재생까지 본다.
  고정된 sleep 대신 "기대한 이벤트가 올 때까지 기다리기"로 짜서 느린 환경에서도 흔들리지 않는다.
  실패하면 그 사이에 받은 이벤트 종류와 개수를 보여 준다. 위 11번 사실도 이 테스트가 찾았다.
- **CI** (`.github/workflows/ci.yml`): PR과 `main` 푸시마다 `make test`, golangci-lint, `make e2e`(재시작 포함).
  e2e가 실패하면 Asterisk 로그와 AMI 녹화를 아티팩트로 올린다.
