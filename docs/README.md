# 문서 지도

IPCC Playground의 설계 문서다. "무엇을, 왜 이렇게 만들었는지"와 "다음에 무엇을 만들지"를 적는다.
처음에는 01 → 02 순서로 읽고, 나머지는 해당 Stage를 시작할 때나 필요할 때 찾아본다.

| 문서 | 내용 | 언제 읽나 |
|---|---|---|
| [01-architecture.md](01-architecture.md) | 전체 구조, 구성요소별 책임, 포트, 콜 한 건의 흐름, 설계 원칙 | 처음 |
| [02-roadmap.md](02-roadmap.md) | Stage 0~6 체크리스트 (제공된 것 / 직접 해볼 것 / 완료 기준) | 처음, 그리고 매주 |
| [03-glossary.md](03-glossary.md) | 콜센터·통계·Asterisk·SIP 용어집 | 모르는 단어가 나올 때 |
| [04-sip-sdp-rtp.md](04-sip-sdp-rtp.md) | SIP 흐름, SDP 읽는 법, RTP, NAT와 "소리 안 들림" 진단 | Stage 0 |
| [05-ami.md](05-ami.md) | AMI 프로토콜, 실제 녹화로 본 이벤트 순서, Go 클라이언트 설계 | Stage 1 |
| [06-state-model.md](06-state-model.md) | 콜·상담원·큐 상태 모델: 녹화로 확인한 전이표, 재동기화, 불변조건, 출력 형식 | Stage 2 |
| [07-rust-to-go.md](07-rust-to-go.md) | Rust→Go 포팅 가이드, 차분 테스트, 코딩 에이전트 활용, 실제 포팅 메모(10절) | Stage 2 |
| [08-runbook.md](08-runbook.md) | 실행, 소프트폰 연결, 디버깅 명령, 문제 해결 | 막힐 때 |
| [09-decisions.md](09-decisions.md) | 설계 결정 기록 (왜 이렇게 했나) | 무언가를 바꾸고 싶을 때 |
| [11-board.md](11-board.md) | 실시간 현황판(Next.js): 실행, 데이터 흐름, 엔진 HTTP API(SSE·명령), 화면 구성 | Stage 3 |
| [10-event-dictionary.md](10-event-dictionary.md) | AMI 이벤트 사전 — 녹화에서 실제로 본 이벤트·필드와 엔진 입장의 의미 (자동 생성) | 이벤트 이름이 나올 때 |

## 문서 규칙

- **사실은 녹화에서 가져온다.** 이벤트 순서나 필드 예시는 실제 녹화(`engine/testdata/`, `recordings/`)와
  로그(`data/asterisk/log/`)에서 옮긴다. 기억이나 블로그 글에 기대지 않는다. Asterisk 버전마다 다르기 때문이다.
- **결정을 바꾸면 기록한다.** [09-decisions.md](09-decisions.md)에 새 항목을 추가하고, 이전 항목은 지우지 않고 "대체됨"으로 표시한다.
- **설계 초안은 초안이라고 쓴다.** 아직 검증하지 않은 설계는 문서 머리에 상태를 적고, 녹화로 확인한 뒤 고친다.
- 다이어그램은 mermaid로 그렸다. GitHub에서는 바로 보이고, VS Code 미리보기에서는
  "Markdown Preview Mermaid Support" 확장이 필요하다.
