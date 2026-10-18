# 11. 실시간 현황판 — Stage 3 첫 화면 (Next.js)

상태머신 엔진(Stage 2)이 낸 줄을 브라우저에서 실시간으로 본다. 상담원 카드의 버튼으로 엔진에 명령(로그인·대기·이석·후처리 완료·로그아웃)도 보낸다.
화면은 한 장이고, 구조는 운영 포탈에서 흔한 배치를 따랐다: 떠 있는 아이콘 레일 | 페이지 헤더 + 섹션(요약 카드 → 항목 카드 → 표 → 로그).

## 1. 실행

```bash
make up        # Asterisk
make engine    # 터미널 1: 상태머신 엔진 (AMI 연결 + 화면용 API http://127.0.0.1:8090)
make web       # 터미널 2: 현황판 http://localhost:3000 (처음에는 npm ci로 의존성 설치)

make sim-call n=5   # 터미널 3: 가짜 고객 콜 → 화면에서 대기 → 벨 → 통화 → 후처리 흐름이 보인다
```

화면의 로그인 칸에서 `Local/1001@sim-agent`를 로그인하고 카드의 **대기**를 누르면 콜을 받기 시작한다
(로그인 직후는 이석(로그인) 상태다, D-019). `make sim-agents`로 Asterisk에 직접 넣은 상담원도 보인다:
엔진이 떠 있으면 `QueueMemberAdded` 이벤트로, 엔진이 나중에 뜨면 재동기화 때(`resync_adopt`) 받아들인다.

해 볼 것:

- 통화 중인 카드에서 **이석 예약 ▾ → 식사** → 통화가 끝나고 후처리 10초가 지나면 대기가 아니라 이석(식사)로 바뀐다
- **후처리 완료**를 누르면 타이머를 기다리지 않고 대기로 돌아간다
- `make engine`을 Ctrl+C → 헤더가 "엔진 꺼짐" → 다시 실행하면 3초 안에 화면이 다시 붙고 전체 상태를 새로 받는다
- `make restart`(Asterisk 재시작) → "Asterisk 재접속 중" → 엔진이 상담원을 큐에 다시 넣는다(이벤트 로그의 `resync_readd`)

## 2. 데이터 흐름

```text
Asterisk ──AMI──▶ bin/engine ──────────── HTTP 127.0.0.1:8090 ───────────▶ Next.js (web/) ──────▶ 브라우저
                  live.Runner (actor)      GET  /api/events  (SSE)          라우트 핸들러 = 게이트웨이   EventSource
                   ├ state.Engine (코어)    GET  /api/state                  /api/engine/events          fetch POST
                   └ board (화면용 상태)    POST /api/commands               /api/engine/commands
```

- **브라우저는 엔진 주소를 모른다.** Next.js 라우트 핸들러가 엔진의 SSE를 그대로 이어 주고(`ENGINE_URL`, 기본 `http://127.0.0.1:8090`),
  명령을 넘긴다. Stage 3 계획의 "Node.js 게이트웨이" 자리를 Next.js 서버가 맡은 것이다.
- **접속하면 전체 상태, 이후에는 바뀐 줄만.** SSE의 첫 메시지(`event: state`)가 전체 상태이고, 이어서 상태머신 출력 줄이 한 줄씩 온다.
  전체 상태를 만드는 일과 구독 등록이 엔진 actor 안에서 한 번에 일어나서, 그 사이에 나온 줄을 빠뜨리거나 두 번 받지 않는다.
- **끊기면 처음부터.** 브라우저의 EventSource는 끊기면 `retry` 간격(3초)마다 다시 붙고, 다시 붙으면 전체 상태부터 받는다.
  그래서 화면은 "놓친 줄"을 걱정하지 않는다. 받지 못할 만큼 밀린 화면은 엔진이 끊는다(actor가 화면을 기다리지 않는다).
- **화면은 상태를 추측하지 않는다.** 화면의 상담원·콜 상태는 엔진이 보낸 줄을 접은 것뿐이다(`web/lib/engine.ts`의 `reduce`).
  버튼을 눌러도 화면을 먼저 바꾸지 않고, 엔진이 낸 상태 변화 줄이 돌아와야 바뀐다.

화면에만 필요한 값(상태가 바뀐 시각, 큐에 처음 들어온 시각, 누적 결과, 최근 200줄)은 **상태머신 코어를 고치지 않고**
엔진 껍데기(`engine/live/view.go`)가 출력 줄을 보며 모은다. 코어의 출력은 Rust 레거시와 바이트 단위로 맞춘 계약이라서다(D-016).

## 3. 엔진 HTTP API (`engine/live/http.go`)

인증이 없으므로 `127.0.0.1`에만 연다(`bin/engine -http`, 빈 값이면 끈다). `next dev`/`next start`도 `127.0.0.1`에만 연다.

### GET /api/events — SSE

```text
retry: 3000
event: state
data: {"kind":"state","now":"1792298396.552902","queue":"support","connected":true,"synced":true,"policy":{"acw_ms":10000,"max_missed":2},"snapshot":{"kind":"snapshot","agents":[{"id":"Local/1001@sim-agent","name":"SIM-1001","state":"Ready",…}],"calls":[…]},"since":{"agents":{…},"calls":{…},"queued":{…}},"totals":{"handled":…},"recent":[…]}

data: {"t":"1792298184.054001","kind":"agent","id":"Local/1001@sim-agent","name":"SIM-1001","from":"LoggedOut","to":"NotReady","reason":"로그인","call":""}

data: {"t":"1792298185.147622","kind":"action","action":"QueuePause","iface":"Local/1001@sim-agent","paused":false,"reason":""}

data: {"t":"1792298220.723550","kind":"call_end","id":"1792298202.0","outcome":"completed:agent","queue":"support","customer":"01092177353","agent":"Local/1001@sim-agent","wait_ms":2012,"talk_ms":14518}

: ping
```

| 필드 (state) | 뜻 |
|---|---|
| `now` | 엔진 시각. 화면은 이것으로 브라우저 시계와의 차이를 잡아 경과 시간을 센다 |
| `connected` / `synced` | 엔진 ↔ Asterisk(AMI) 연결, 이 연결에서 재동기화를 마쳤는지 |
| `snapshot` | 상태머신 스냅샷 (`bin/state replay`의 마지막 줄과 같은 형식) |
| `since.agents` / `since.calls` | 상담원·콜이 지금 상태가 된 시각 |
| `since.queued` | 콜이 큐에 처음 들어온 시각 (무응답으로 대기·벨을 오가도 대기 시간은 여기서부터) |
| `totals` | 엔진 시작 이후 결과 수: `handled`(completed·completed:*), `abandoned`, `timeout`, `other` |
| `recent` | 최근 출력 줄 200개 (이벤트 로그와 최근 종료 표의 재료) |

이후 `data:` 줄은 상태머신 출력 그대로(06 문서 10절)이고, 엔진 껍데기가 내는 줄이 하나 더 있다:
`{"kind":"status","t":"…","connected":true,"synced":false}` — AMI 연결 상태가 바뀔 때. `: ping`은 15초마다 오는 주석이다.

### POST /api/commands

```json
{"kind":"notready","iface":"Local/1001@sim-agent","arg":"식사"}
```

`kind`는 `login`(arg = 이름), `logout`, `ready`, `notready`(arg = 사유), `wrapup`. 검사는 명령 파일(`*.cmds`)과 같은 파서가 한다.
성공하면 `202 {"ok":true}`, 모르는 명령은 `400 {"error":"1번째 줄: 모르는 명령: dance"}`. 결과(상태 변화)는 SSE로 돌아온다.

### GET /api/state

SSE 첫 메시지와 같은 JSON을 한 번 준다. `curl -s 127.0.0.1:8090/api/state | jq .snapshot`로 엔진 상태를 볼 때 쓴다.

## 4. 화면 구성 (`web/`)

| 영역 | 파일 | 내용 |
|---|---|---|
| 셸 | `components/rail.tsx`, `world.tsx` | 떠 있는 유리 레일(섹션 이동, 현재 위치 물방울, 툴팁, 테마 전환), 빛 덩어리·원근 격자 배경 |
| 헤더 | `components/header.tsx` | 연결 상태 한 단어(실시간 / 재동기화 중 / Asterisk 재접속 중 / 엔진 꺼짐), 엔진 시각 |
| 큐 현황 | `components/overview.tsx` | 대기 고객(최장 대기), 통화 중(보류), 받을 수 있는 상담원(상태별 수), 처리한 콜(응대율) |
| 상담원 | `components/agents.tsx` | 상태별 카드: 경과 시간(후처리는 남은 시간), 고객 번호, 상태에 맞는 버튼, 로그인 폼 |
| 콜 | `components/calls.tsx` | 진행 중인 콜(대기 중이면 대기 시간), 최근 종료(결과·대기·통화 시간) |
| 이벤트 로그 | `components/event-log.tsx` | 엔진이 낸 줄을 문장으로. 종류별 필터. 액션 줄 = 엔진이 Asterisk에 보낸 명령 |
| 데이터 | `lib/engine.ts`, `lib/use-engine.ts` | 줄 타입, 리듀서, 상태 이름·색, SSE 구독, 1초 시계 |
| 게이트웨이 | `app/api/engine/*/route.ts` | 엔진 SSE 중계, 명령 전달, 엔진이 꺼졌을 때 `engine-down` 이벤트 |
| 디자인 | `app/globals.css` | 토큰(라이트·다크), 유리 재질, 컴포넌트 클래스. Tailwind 4는 배치용 |

상태 색: 대기 초록 · 벨울림 노랑(깜빡임) · 통화중 파랑 · 보류 주황 · 후처리 보라 · 이석 분홍 · 로그아웃 회색.

## 5. 검사

- `make web-check` — `npm ci` + ESLint(next core-web-vitals, typescript) + `tsc --noEmit` + `next build`. CI의 `web` 작업
- `engine/live` 단위 테스트 — 가짜 Asterisk로 SSE 첫 메시지(전체 상태), 이후 줄, 명령 → QueuePause, 잘못된 명령 400 (`TestHTTPEventsAndCommands`)
- 화면 동작은 실제 Asterisk로 확인했다: 콜 흐름, 이석 예약 → 후처리 뒤 이석, 버튼 클릭 → 엔진 → Asterisk 큐 멤버,
  엔진 재시작 → 화면 재접속·상담원 채택, 다크 모드, 휴대폰 폭(390px)

## 6. 다음 과제 (Stage 3의 나머지)

- [ ] 상담원 툴바 화면: 상담원 한 명 입장의 화면 (걸기, 전환, 보류, 당겨받기 → 엔진 명령 API 확장)
- [ ] 게이트웨이를 여러 대 띄우거나 엔진과 따로 재시작하려면 Redis pub/sub을 사이에 둔다 (02 문서 Stage 3)
- [ ] WebSocket이 필요한 곳 찾기: 지금은 서버 → 화면 한 방향이라 SSE로 충분하다. 화면 → 서버가 잦아지면 바꾼다
- [ ] 감청·코칭·강제개입 (`ChanSpy`를 `Originate`로)
- [ ] 큐 대기열 순번과 최장 대기를 엔진 모델에 넣기 (06 문서 11절) → 현황판의 대기 고객 카드가 정확해진다
