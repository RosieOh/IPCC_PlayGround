# script/ — 바로 실행하는 스크립트

저장소 어디에서 실행해도 된다 (스크립트가 저장소 루트를 스스로 찾는다). 옵션은 모두 `--help`로 볼 수 있다.

| 스크립트 | 하는 일 |
|---|---|
| 🚀 `script/start.sh` | Docker → Asterisk(부팅 완료까지 기다림) → 상태머신 엔진 → 룸 큐 엔진 → 화면(Next.js) → 브라우저를 한 번에 켠다. 이미 켜진 것은 건너뛴다 |
| 🚀 `script/start.sh --demo` | 켜고 나서 가짜 상담원 3명을 대기로 만들고 가짜 콜 4통을 건다 |
| 🛑 `script/stop.sh` | 화면 → 가짜 상담원 정리 → 엔진 → 룸 큐 엔진 순서로 끈다. Asterisk는 그대로 둔다 |
| 🛑 `script/stop.sh --all` | Asterisk 컨테이너까지 내린다 (`data/`는 남는다) |
| 📊 `script/status.sh` | Docker · Asterisk · 엔진 · 룸 큐 엔진 · 화면 · 큐 · 통화 현황을 한눈에. `--watch`면 2초마다 새로 그린다 |
| 📞 `script/demo.sh` | 가짜 상담원 대기 + 가짜 콜. `-n 10`(콜 수), `-a 2`(상담원 수), `--loop`(Ctrl+C까지 계속) |
| 🏠 `script/demo.sh --rooms` | 룸 큐 현황판(`/rooms`)용: 스킬이 다른 가짜 상담원(1001 support · 1002 sales · 1003 모든 큐) + support·sales 콜 |
| 📜 `script/logs.sh` | 로그 따라가기: `engine`(기본, 줄 종류별 색) · `rq`(룸 큐 엔진) · `web` · `asterisk` |
| ✅ `script/check.sh` | CI와 같은 검사(Go 테스트·lint, Rust, 차분 테스트, 화면, e2e)를 돌리고 결과를 표로. `--quick`이면 e2e를 뺀다 |

```bash
script/start.sh --demo     # 켜고 바로 움직이는 화면 보기
script/demo.sh --loop      # 다른 터미널에서 콜을 계속 걸기
script/status.sh --watch   # 또 다른 터미널에서 상태 지켜보기
script/stop.sh             # 다 봤으면 끄기
```

## 동작 방식

- 엔진과 화면은 **터미널과 떨어진 새 세션**에서 돈다. 스크립트가 끝나거나 터미널 창을 닫아도 꺼지지 않는다.
  (`nohup`만으로는 터미널을 닫을 때 Next.js 개발 서버가 같이 꺼졌다. macOS에는 `setsid` 명령이 없어서 perl의 `POSIX::setsid`를 쓴다)
- PID와 로그는 저장소의 `.run/`에 남는다 (git 제외): `engine.pid`, `engine.log`, `rqengine.pid`, `rqengine.log`, `web.pid`, `web.log`, `check-*.log` …
  룸 큐 엔진의 상담원 업무 상태는 `data/rqengine/agents.json`에 남아 다시 켜면 되살아난다 (12 문서 5절).
- 3000번 포트를 다른 프로그램이 쓰면 `WEB_PORT=3001 script/start.sh` (status·stop에도 같은 값을 준다).
- `stop.sh`는 이 저장소에서 켠 것만 끈다. 3000번 포트를 쓰는 다른 프로그램은 건드리지 않는다
  (`web/`에서 실행된 프로세스인지 작업 디렉터리로 확인한다). `make engine`으로 켠 엔진도 찾아서 끈다.
- `check.sh`는 엔진(룸 큐 엔진 포함)이 떠 있으면 e2e를 건너뛰고(e2e의 엔진과 같은 상담원·콜을 움직인다), 화면 서버가 떠 있으면
  화면 검사를 건너뛴다(`npm ci`가 `node_modules`를 지운다). 먼저 `script/stop.sh`.
- macOS 기본 bash(3.2)에서 돈다. 색은 터미널일 때만 쓰고 `NO_COLOR=1`이면 끈다. 공용 함수는 `script/lib.sh`.
