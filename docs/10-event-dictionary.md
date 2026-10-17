# 10. AMI 이벤트 사전 (자동 생성)

> `make event-dict`로 다시 만든다. 손으로 고치지 않는다. 원본: `engine/testdata/`의 녹화 2개 (sim-normal-and-abandon.log, sip-agent-call.log), 이벤트 189개.
> 이 환경(Asterisk 22.11)에서 실제로 본 이벤트와 필드만 있다. 녹화에 없는 이벤트는 `make rx c="manager show event 이름"`으로 본다.
>
> **{채널}** = 채널 스냅샷 필드 14개 (`Channel` `ChannelState` `ChannelStateDesc` `CallerIDNum` `CallerIDName`
> `ConnectedLineNum` `ConnectedLineName` `Language` `AccountCode` `Context` `Exten` `Priority` `Uniqueid` `Linkedid`).
> **{Dest 채널}** = 같은 필드 앞에 `Dest`가 붙은 것 (큐·Dial 이벤트에서 상담원·상대 쪽 채널).

| 이벤트 | 횟수 | 엔진 입장의 의미 | 필드 |
|---|---|---|---|
| AgentCalled | 2 | 큐가 상담원을 부른다 → 상담원 **벨울림** | Privilege, Timestamp, {채널}, {Dest 채널}, Queue, Interface, MemberName |
| AgentComplete | 2 | 상담원 통화가 끝났다 → 상담원 **후처리**. 채널 정보 없이 올 수 있어 `Interface`로 상담원을 찾는다 | Privilege, Timestamp, {채널}, {Dest 채널}, Queue, Interface, MemberName, HoldTime, TalkTime, Reason |
| AgentConnect | 2 | 상담원이 받았다 → 상담원 **통화중**. 고객·상담원 채널 정보가 다 있어 '상담원 ↔ 콜' 짝을 여기서 잡는다 | Privilege, Timestamp, {채널}, {Dest 채널}, Queue, Interface, MemberName, HoldTime, RingTime |
| BridgeCreate | 2 | 채널을 이을 브리지가 생겼다 | Privilege, Timestamp, BridgeUniqueid, BridgeType, BridgeTechnology, BridgeCreator, BridgeName, BridgeNumChannels, BridgeVideoSourceMode |
| BridgeDestroy | 2 | 브리지가 사라졌다 | Privilege, Timestamp, BridgeUniqueid, BridgeType, BridgeTechnology, BridgeCreator, BridgeName, BridgeNumChannels, BridgeVideoSourceMode |
| BridgeEnter | 4 | 채널이 브리지에 들어갔다. 3개 이상이면 회의 | Privilege, Timestamp, BridgeUniqueid, BridgeType, BridgeTechnology, BridgeCreator, BridgeName, BridgeNumChannels, BridgeVideoSourceMode, {채널} |
| BridgeLeave | 4 | 채널이 브리지에서 나갔다 | Privilege, Timestamp, BridgeUniqueid, BridgeType, BridgeTechnology, BridgeCreator, BridgeName, BridgeNumChannels, BridgeVideoSourceMode, {채널} |
| Cdr | 6 | 통화 구간 기록 (cdr_manager). 통계 대조용 | Privilege, Timestamp, AccountCode, Source, Destination, DestinationContext, CallerID, Channel, DestinationChannel, LastApplication, LastData, StartTime, AnswerTime, EndTime, Duration, BillableSeconds, Disposition, AMAFlags, UniqueID, UserField |
| ChallengeSent | 1 | 보안 이벤트: SIP 인증을 요구했다 (401) | Privilege, Timestamp, EventTV, Severity, Service, EventVersion, AccountID, SessionID, LocalAddress, RemoteAddress, Challenge |
| ContactStatus | 1 | PJSIP 등록 주소 상태 (Reachable, Unreachable 등) | Privilege, Timestamp, URI, ContactStatus, AOR, EndpointName, RoundtripUsec |
| DeviceStateChange | 35 | 장치 상태가 바뀌었다. 큐의 가상 장치까지 나와서 양이 많다. 상담원 상태 판단에 쓰지 않는다 | Privilege, Timestamp, Device, State |
| DialBegin | 4 | Dial(큐 포함)이 상대를 부르기 시작했다 | Privilege, Timestamp, {Dest 채널}, DialString, {채널} |
| DialEnd | 4 | Dial이 끝났다 (`DialStatus`: ANSWER, NOANSWER, BUSY …) | Privilege, Timestamp, {Dest 채널}, DialStatus, {채널} |
| DialState | 2 | Dial 진행 중 상태 (RINGING 등) | Privilege, Timestamp, {채널}, {Dest 채널}, DialStatus |
| FullyBooted | 2 | Asterisk가 모듈을 다 올렸다. 로그인 직후 한 번 온다 | Privilege, Uptime, LastReload, Status |
| Hangup | 8 | 채널이 끊겼다 (`Cause`). 콜의 마지막 채널이면 콜 종료 | Privilege, Timestamp, {채널}, Cause, Cause-txt |
| HangupRequest | 4 | 끊으라는 요청이 왔다 (상대가 끊음 등) | Privilege, Timestamp, {채널}, Cause |
| LocalBridge | 3 | Local 채널 쌍(;1/;2)이 서로 이어졌다. 가짜 상담원·가짜 고객 콜에서만 나온다 | Privilege, Timestamp, LocalOneChannel, LocalOneChannelState, LocalOneChannelStateDesc, LocalOneCallerIDNum, LocalOneCallerIDName, LocalOneConnectedLineNum, LocalOneConnectedLineName, LocalOneLanguage, LocalOneAccountCode, LocalOneContext, LocalOneExten, LocalOnePriority, LocalOneUniqueid, LocalOneLinkedid, LocalTwoChannel, LocalTwoChannelState, LocalTwoChannelStateDesc, LocalTwoCallerIDNum, LocalTwoCallerIDName, LocalTwoConnectedLineNum, LocalTwoConnectedLineName, LocalTwoLanguage, LocalTwoAccountCode, LocalTwoContext, LocalTwoExten, LocalTwoPriority, LocalTwoUniqueid, LocalTwoLinkedid, Context, Exten, LocalOptimization |
| MusicOnHoldStart | 3 | 대기음 시작 (큐 대기, 보류) | Privilege, Timestamp, {채널}, Class |
| MusicOnHoldStop | 3 | 대기음 끝 | Privilege, Timestamp, {채널} |
| NewCallerid | 6 | 채널의 발신번호가 정해졌다. Local 채널은 Newchannel 때 `<unknown>`이라 여기서 확정 | Privilege, Timestamp, {채널}, CID-CallingPres |
| NewConnectedLine | 8 | 상대편 번호 정보가 바뀌었다 | Privilege, Timestamp, {채널} |
| Newchannel | 8 | 채널(통화 구간)이 생겼다. `Linkedid`로 콜에 붙인다 | Privilege, Timestamp, {채널} |
| Newexten | 31 | 다이얼플랜 한 줄을 실행했다. 흐름 추적용, 양이 많다 | Privilege, Timestamp, {채널}, Extension, Application, AppData |
| Newstate | 10 | 채널 상태가 바뀌었다 (`Up` = 응답) | Privilege, Timestamp, {채널} |
| OriginateResponse | 2 | 비동기 Originate의 결과. `Response` 필드가 있지만 **이벤트**다 | Privilege, Timestamp, ActionID, Response, Channel, Application, Data, Reason, Uniqueid, CallerIDNum, CallerIDName |
| QueueCallerAbandon | 1 | 고객이 기다리다 끊었다 → **포기호** | Privilege, Timestamp, {채널}, Queue, Position, OriginalPosition, HoldTime |
| QueueCallerJoin | 3 | 고객이 큐에 들어왔다 → 대기 시작 | Privilege, Timestamp, {채널}, Queue, Position, Count |
| QueueCallerLeave | 3 | 고객이 큐에서 나갔다 (연결·포기·타임아웃 모두) | Privilege, Timestamp, {채널}, Queue, Position, Count |
| QueueMemberAdded | 2 | 상담원이 큐에 들어왔다 (로그인) | Privilege, Timestamp, Queue, MemberName, Interface, StateInterface, Membership, Penalty, CallsTaken, LastCall, LastPause, LoginTime, InCall, Status, Paused, PausedReason, Ringinuse, Wrapuptime |
| QueueMemberPause | 1 | 상담원 이석(`Paused=1`, `PausedReason`) / 대기(`Paused=0`) | Privilege, Timestamp, Queue, MemberName, Interface, StateInterface, Membership, Penalty, CallsTaken, LastCall, LastPause, LoginTime, InCall, Status, Paused, PausedReason, Ringinuse, Wrapuptime |
| QueueMemberRemoved | 2 | 상담원이 큐에서 빠졌다 (로그아웃) | Privilege, Timestamp, Queue, MemberName, Interface, StateInterface, Membership, Penalty, CallsTaken, LastCall, LastPause, LoginTime, InCall, Status, Paused, PausedReason, Ringinuse, Wrapuptime |
| QueueMemberStatus | 4 | 상담원 장치 상태 (`Status`). 벨이 울리는 중에도 BUSY(3)라 벨울림 판단에 쓰지 않는다 | Privilege, Timestamp, Queue, MemberName, Interface, StateInterface, Membership, Penalty, CallsTaken, LastCall, LastPause, LoginTime, InCall, Status, Paused, PausedReason, Ringinuse, Wrapuptime |
| SoftHangupRequest | 4 | Asterisk 내부에서 끊으라는 요청이 났다 | Privilege, Timestamp, {채널}, Cause |
| SuccessfulAuth | 10 | 보안 이벤트: 로그인 성공 (다른 AMI 클라이언트 것도 보인다) | Privilege, Timestamp, EventTV, Severity, Service, EventVersion, AccountID, SessionID, LocalAddress, RemoteAddress, UsingPassword, SessionTV |
