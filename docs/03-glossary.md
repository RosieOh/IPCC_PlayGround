# 03. 용어집

콜센터 현장 용어는 회사·고객사마다 조금씩 다르다. 처음 듣는 말이 나오면 "정확히 어떤 상태·이벤트를 말하는지"
되물어서 아래 정의와 맞춰 보는 습관이 중요하다. 특히 통계 용어는 정의가 다르면 숫자가 달라진다.

## 시스템 구성

| 용어 | 영어 | 뜻 |
|---|---|---|
| IPCC | IP Contact Center | 교환기·CTI·IVR·녹취를 IP(SIP) 기반으로 통합한 콜센터 시스템 |
| 교환기 | PBX / IP-PBX | 전화를 연결해 주는 장비·소프트웨어. 여기서는 Asterisk |
| CTI | Computer Telephony Integration | 전화와 상담 화면을 연동하는 계층. 콜 제어, 상담원 상태 관리, 스크린팝. 이 프로젝트의 '엔진' |
| ACD | Automatic Call Distribution | 대기 중인 콜을 상담원에게 자동으로 나눠 주는 기능. 여기서는 Asterisk `app_queue` |
| ARS / IVR | Interactive Voice Response | 자동응답. 한국 현장에서는 ARS라고 부르는 경우가 많다 |
| 녹취 | Call Recording | 통화 녹음. Asterisk에서는 `MixMonitor` |
| 상담 AP | Agent Application | 상담원이 쓰는 화면(툴바 + 고객 정보). CTI 클라이언트 |
| 현황판 | Wallboard | 대기 콜 수, 상담원 상태 등을 실시간으로 보여 주는 화면 |
| 트렁크 | Trunk | 통신사 회선. 외부 고객 콜이 들어오는 길 (이 프로젝트에는 없고 `[inbound]`로 흉내 낸다) |
| 전문 | Fixed-length message | 금융권 등에서 쓰는 고정 길이 메시지 포맷. 보통 TCP 소켓 + EUC-KR |

## 업무 · 상담원 상태

| 용어 | 영어 | 뜻 |
|---|---|---|
| 인입 | Inbound / Offered | 고객 콜이 들어옴 |
| 응대 | Answered / Handled | 상담원이 받음 |
| 포기(호) | Abandoned | 고객이 대기 중에 끊음 |
| 로그인 / 로그아웃 | Login / Logout | 상담원이 업무를 시작 / 끝냄. Asterisk에는 큐 멤버 추가/삭제(`QueueAdd`/`QueueRemove`)로 반영 |
| 대기 | Ready / Available | 콜을 받을 수 있음 |
| 이석 | Not Ready / Aux | 자리 비움. 사유코드(휴식·식사·교육·회의 등)를 붙인다. Asterisk에는 `QueuePause` |
| 벨울림 | Ringing / Alerting | 상담원 전화기가 울리는 중 |
| 통화(중) | Talking / On Call | 고객과 통화 중 |
| 보류 | Hold | 통화를 잠시 멈춤. 고객은 대기음을 듣는다 |
| 후처리 | ACW (After Call Work), Wrap-up | 통화 직후 상담 내용 입력 시간. 이 동안은 콜을 받지 않는다 |
| 호전환 | Transfer | 블라인드(바로 넘김, Blind/Unattended), 어텐디드(상대와 먼저 통화하고 넘김, Attended/Consult) |
| 당겨받기 | Pickup | 다른 전화기에 울리는 콜을 내 전화기로 받음 |
| 3자통화 | Conference | 세 명 이상 통화 |
| 감청 | Listen / Monitor | 관리자가 통화를 듣기만 함 |
| 코칭 | Whisper | 관리자 목소리가 상담원에게만 들림 |
| 강제개입 | Barge-in | 관리자가 통화에 끼어듦 (모두에게 들림) |
| 스크린팝 | Screen Pop | 콜이 울릴 때 고객 정보 화면이 자동으로 뜸 |
| 콜백 | Callback | 대기하는 대신 나중에 다시 전화 받기를 예약 |
| 스킬 라우팅 | Skill-Based Routing | 상담원의 스킬(언어, 상품 등)에 맞춰 분배 |

## 통계 지표

**같은 이름이어도 센터마다 정의가 다르다.** 아래는 흔한 정의이고, 실제 구현에서는 기준값을 설정으로 뺀다.

| 지표 | 흔한 정의 | 주의할 점 |
|---|---|---|
| 인입호 | 큐에 들어온 콜 수 | ARS에서 끊은 콜을 넣을지 |
| 응대호 | 상담원과 연결된 콜 수 | 전환되어 다시 들어온 콜을 두 번 셀지 |
| 포기호 | 대기 중 고객이 끊은 콜 수 | **N초 이내 포기(짧은 포기)를 뺄지.** 센터마다 부르는 이름도 다르다 |
| 응대율 | 응대호 ÷ 인입호 | 분모에서 짧은 포기호를 뺄지 |
| 서비스레벨(SL) | 목표 시간(예: 20초) 안에 응대한 비율 | 분모가 센터마다 다르다 (아래) |
| ASA | 응대호의 대기시간 합 ÷ 응대호 | 평균 응답 대기시간 |
| ATT | 통화시간 합 ÷ 응대호 | 평균 통화시간 |
| AHT | (통화 + 보류 + 후처리) 시간 합 ÷ 응대호 | 평균 처리시간. 인력 산정의 기준 |
| 최장 대기시간 | 지금 대기 중인 콜 중 가장 오래 기다린 시간 | 현황판의 핵심 지표 |
| 가동률(점유율) | 처리시간 ÷ (처리시간 + 대기 상태 시간) | 이석 시간은 분모에서 뺀다 |
| 이석률 | 이석 시간 ÷ 로그인 시간 | 사유코드별로 본다 |

서비스레벨 분모의 흔한 변형 (T = 목표 시간):

- T 안에 응대 ÷ 인입호
- T 안에 응대 ÷ (응대호 + 포기호)
- T 안에 응대 ÷ (응대호 + T 이후 포기호) — T 안에 끊은 고객은 "기회가 없었다"고 보고 뺀다

Asterisk `queues.conf`의 `servicelevel = 20`은 `QueueParams` 이벤트의 `ServicelevelPerf`와 `ServicelevelPerf2`
계산에 쓰인다. 두 값의 계산식이 다르다. 엔진 통계와 비교하기 전에 Asterisk 소스(`app_queue.c`)에서 식을 확인한다.

## Asterisk

| 용어 | 뜻 |
|---|---|
| 채널 (Channel) | 통화의 한 구간(leg). 이름 예: `PJSIP/1001-00000003`, `Local/customer@sim-00000000;2` |
| Uniqueid | 채널마다 붙는 고유 ID. 예: `1792123138.1` |
| Linkedid | 같은 콜에 묶인 채널들이 공유하는 ID. 보통 콜을 처음 만든 채널의 Uniqueid. **이 프로젝트의 콜 ID** |
| 브리지 (Bridge) | 채널들을 연결해 음성을 섞는 객체. `BridgeUniqueid`로 식별 |
| B2BUA | Back-to-Back User Agent. 양쪽 통화를 각각 끝단으로 처리하는 중계기. Asterisk가 이 방식이라 콜 하나에 채널이 여러 개 생긴다 |
| Local 채널 | 다이얼플랜을 전화번호처럼 부르는 가상 채널. `;1`(부른 쪽)과 `;2`(다이얼플랜을 실행하는 쪽)가 쌍으로 생긴다 |
| 다이얼플랜 | `extensions.conf`. context(구역) / extension(번호) / priority(순서)로 "어떤 번호면 무엇을 할지"를 정한다 |
| 애플리케이션 | 다이얼플랜에서 부르는 기능. `Dial`, `Queue`, `Playback`, `Echo`, `MixMonitor`, `ChanSpy` 등 |
| 디바이스 상태 | Asterisk가 보는 단말 상태: NOT_INUSE, INUSE, BUSY, RINGING, UNAVAILABLE, ONHOLD … 상담원 업무 상태와 다르다 |
| 큐 멤버 | 큐에 들어간 상담원. `Interface`(예: `PJSIP/1001`), `MemberName`, `Paused`, `Penalty` |
| AMI | Asterisk Manager Interface. TCP 5038 텍스트 프로토콜. 이벤트 수신과 액션 → [05-ami.md](05-ami.md) |
| AGI / FastAGI | 다이얼플랜이 외부 프로그램에 로직을 물어보는 인터페이스. FastAGI는 TCP(4573)로 원격 서버에 묻는다 |
| ARI | Asterisk REST Interface. HTTP + WebSocket. 콜을 Stasis 앱으로 넘겨받아 직접 제어 |
| PJSIP 객체 | endpoint(단말 설정), aor(등록 주소), auth(인증), transport(포트·프로토콜), contact(실제 등록된 주소) |
| CDR | Call Detail Record. 구간 단위 통화 기록 (`cdr-csv/Master.csv`) |
| CEL | Channel Event Logging. 채널 이벤트 단위의 세밀한 기록 |
| queue_log | 큐 전용 로그 (`ENTERQUEUE`, `CONNECT`, `COMPLETEAGENT`, `ABANDON`, `PAUSE` …). 콜센터 통계의 원재료 |

## SIP · 음성

| 용어 | 뜻 |
|---|---|
| SIP | 통화를 맺고 끊는 시그널링 프로토콜 (RFC 3261). HTTP처럼 텍스트 요청/응답 |
| UA / UAC / UAS | User Agent. 요청을 보내는 쪽(UAC)과 받는 쪽(UAS) |
| REGISTER | 단말이 "나는 지금 이 주소에 있다"고 서버에 알림 |
| INVITE / ACK / BYE / CANCEL | 통화 요청 / 최종 응답 확인 / 통화 종료 / 응답 전 요청 취소 |
| 트랜잭션 | 요청 하나와 그 응답들. Via 헤더의 `branch`로 구분 |
| 다이얼로그 | 통화 하나. `Call-ID` + From tag + To tag로 구분 |
| SDP | 미디어 협상 내용(주소, 포트, 코덱). INVITE와 200 OK 본문에 실린다 (offer/answer) |
| RTP / RTCP | 실제 음성 패킷 / 그 품질 통계. RTCP는 보통 RTP 포트 + 1 |
| 코덱 | 음성 압축 방식. G.711 μ-law(PCMU, PT 0), A-law(PCMA, PT 8), Opus 등 |
| DTMF | 전화 키 신호. RFC 4733(RTP 이벤트, `telephone-event`), 음성 대역(in-band), SIP INFO 방식이 있다 |
| NAT | 사설 IP ↔ 공인 IP 변환. SIP/SDP에 적힌 주소와 실제 주소가 달라져서 "소리 안 들림"의 주원인 |
| rport | 응답을 요청이 실제로 온 포트로 돌려보내라는 표시 (RFC 3581) |
| Symmetric RTP | 받은 RTP의 출발지로 보낸다. NAT 뒤 단말 대응 (`rtp_symmetric`) |
| WebRTC | 브라우저 실시간 통신. ICE·DTLS-SRTP·WebSocket(WSS) 위의 SIP |
