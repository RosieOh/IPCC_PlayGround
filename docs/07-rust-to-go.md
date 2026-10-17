# 07. Rust → Go 포팅 가이드

## 1. 왜 이 연습을 하나

입사하면 맡을 일은 "Rust로 된 핵심 엔진(인터페이스)을 읽고, Go로 옮기고, **동작이 같다는 걸 증명**하는 것"이다.
JD가 '코딩 에이전트를 다루는 능력'을 가장 중요한 요건으로 꼽은 것을 보면, 옮기는 작업 자체는 에이전트와 함께 할 가능성이 크다.
그러면 사람에게 남는 일은 셋이다.

1. Rust 코드를 **읽고** 의도를 이해하기
2. 옮긴 결과가 **같다는 기준**(테스트 하네스)을 만들기
3. 에이전트가 낸 diff를 **검토**하기

이 문서는 Stage 2(상태머신)를 이 순서로 연습하는 방법이다.

## 2. 진행 방식

```text
① Rust로 '레거시' 작성      legacy-rust/      상태머신 코어 (순수 함수, I/O 없음)
② 공통 계약 정하기           입력 = AMI 녹화, 출력 = 상태 변화 JSONL (4절)
③ Go로 포팅                 engine/state/
④ 차분 테스트                같은 녹화 → 두 구현 → 출력 diff = 0
⑤ Go 버전을 엔진에 연결       ami.Client.Events() → state → 효과
```

①은 에이전트와 같이 써도 된다. 다만 목표는 **한 줄씩 설명할 수 있을 만큼 읽는 힘**이다.
The Rust Book의 소유권(4장), 구조체·열거형·match(5~6장), 에러 처리(9장), 트레이트(10장)와 tokio 튜토리얼이면 충분하다.

## 3. Rust 읽기 최소 문법

회사 코드를 읽을 때 가장 자주 마주칠 것들이다.

| Rust | 읽는 법 | Go에서는 |
|---|---|---|
| `let x = …;` / `let mut x` | 불변 / 가변 변수. 기본이 불변 | `x := …` (항상 가변) |
| `&T` / `&mut T` | 빌려 보기(읽기) / 빌려 쓰기. 동시에 쓰기 참조는 하나만 | 포인터 또는 값 복사 |
| `String` / `&str` | 소유한 문자열 / 빌린 문자열 조각. 둘 다 UTF-8 보장 | `string` (UTF-8 보장 없음, 그냥 바이트) |
| `Vec<T>` / `HashMap<K, V>` | 가변 배열 / 해시맵 | `[]T` / `map[K]V` |
| `Option<T>` | `Some(v)` 또는 `None` | 포인터(nil) 또는 `(T, bool)` |
| `Result<T, E>` + `?` | 성공/실패. `?`는 실패면 바로 반환 | `(T, error)` + `if err != nil { return … }` |
| `enum` + `match` | 데이터를 품는 열거형 + 빠짐없는 분기 | 타입 상수 + `switch`, 또는 인터페이스 + 타입 스위치 |
| `impl Foo { … }` | Foo의 메서드 | 리시버가 있는 함수 |
| `trait` / `impl Trait for Foo` | 인터페이스 / 명시적 구현 | `interface` (암묵적 구현) |
| `Box<dyn Trait>` | 힙에 둔 트레이트 객체 | 인터페이스 값 |
| `Arc<Mutex<T>>` | 여러 스레드가 공유하는 잠긴 값 | `sync.Mutex` 필드, 또는 고루틴 하나가 소유 |
| `async fn` / `.await` | 비동기 함수 / 결과 기다리기 | 그냥 함수 + 고루틴 |
| `tokio::spawn` | 비동기 태스크 실행 | `go f()` |
| `mpsc::channel` / `tokio::select!` | 채널 / 여러 채널 대기 | `chan` / `select` |
| `#[derive(Debug, Clone, Serialize)]` | 자동 구현 | 없음 / 구조체 태그 (`json:"…"`) |
| `'a` (수명) | 참조가 얼마나 살아야 하는지 | 없음 (GC) |
| `impl Drop` | 소멸 시 정리 | `defer`, `Close()` |
| `unwrap()` / `expect("…")` | 실패면 panic | 명시적 에러 처리로 바꾼다 |

## 4. 공통 계약 — 두 구현이 같은 걸 출력하게

차분 테스트는 "출력 바이트가 같은가"로 판단한다. 그래서 출력 형식을 엄격히 정한다.

- **입력**: AMI 녹화 파일 그대로 (받은 바이트). 상담원 명령은 명령 파일 — 한 줄에 `<시각> <명령> <인터페이스> [인자…]`.
- **실행**: `ipcc-state replay [--cmds F] 녹화…`(Rust) / `bin/state replay [--cmds F] 녹화…`(Go) — 인자와 출력이 같다.
- **출력**: 한 줄에 JSON 객체 하나 (06 문서 10절). 끝에 스냅샷 한 줄.
- **정규화 규칙**
  - 키 순서를 고정한다. 두 언어 모두 직렬화 라이브러리 없이 '넣은 순서대로' 직접 쓴다
    (`encoding/json`은 `<`·`>`·`&`를 `\u003c`로 바꾸고, 맵은 키 순서로 정렬한다. serde도 설정마다 다르다).
  - 맵을 순회하는 곳은 모두 키로 정렬한다 (Go map은 순회 순서가 일부러 무작위다).
  - 시각은 `Timestamp`를 정수 마이크로초로 읽어 계산하고, 낼 때 `초.마이크로초 6자리`로 쓴다. 실수(float)를 쓰지 않는다.
  - 숫자는 정수만. 비율·평균 같은 계산 값은 상태머신 출력에 넣지 않는다 (통계 계층의 일).
  - 문자열은 `"` `\` 제어문자만 이스케이프하고 한글은 그대로. 줄바꿈은 `\n`.

## 5. 차분 테스트 하네스

```bash
make diff-test
# 1. Rust(Docker rust:1.98.1)로 모든 녹화 재생 → 임시/rust/*.jsonl   (tests/state/replay-all.sh)
# 2. Go로 같은 녹화 재생                       → 임시/go/*.jsonl
# 3. diff -ru 임시/rust 임시/go
# 4. 저장소의 기대 출력(*.golden.jsonl)이 Rust 출력과 같은지 (낡은 기대 출력 방지)
```

녹화는 `engine/state/testdata/*.ami`(시나리오 11개 + 손으로 자른 `truncated.ami`)와 `engine/testdata/*.log`(Stage 1),
명령 파일 `normal.cmds`까지 출력 15개다. CI의 `rust` 작업이 PR마다 돌린다.

- 녹화가 곧 테스트 케이스다. 시나리오(정상, 포기, 무응답, 보류, 전환…)가 늘수록 하네스가 강해진다.
- diff가 나면 **어느 쪽이 맞는지는 녹화와 06 문서로 판단**한다. Rust가 항상 정답은 아니다.
  (레거시에서 버그를 찾는 것도 포팅에서 흔히 얻는 성과다. 찾으면 일부러 고치지 말고 먼저 기록한다.)

## 6. Rust 컴파일러가 막아 주던 것을 Go에서는 무엇으로 막나

포팅에서 가장 위험한 것은 "Rust에서는 컴파일이 안 되던 실수"가 Go에서는 조용히 통과한다는 점이다.

| 위험 | Rust | Go에서 막는 법 |
|---|---|---|
| 상태 하나를 빠뜨린 분기 | `match`가 빠짐없이 검사 | `exhaustive` 린터 (`make lint`). 상태머신에 치명적 |
| 데이터 레이스 | 소유권·`Send`/`Sync`가 컴파일 시 차단 | `go test -race`, 상태는 고루틴 하나가 소유 |
| null 역참조 | `Option`이 강제 | 포인터를 줄이고, 생성자에서 필수 필드 채우기, 테스트 |
| 슬라이스 공유로 인한 오염 | 소유권 이동 | `append`가 원본 배열을 공유할 수 있다. 복사해서 넘긴다 (`Message.With`와 그 테스트 참고) |
| 정수 오버플로 | 디버그 빌드에서 panic | 조용히 감긴다. 범위가 의심되면 직접 검사 |
| 처리 안 한 에러 | `Result`를 쓰지 않으면 경고 | `errcheck` 린터 (`make lint`) |
| UTF-8이 아닌 문자열 | `String`이 UTF-8 보장 | Go `string`은 아무 바이트나 담는다. EUC-KR 같은 입력은 경계에서 변환 |

**panic의 범위가 다르다.** tokio에서 `spawn`한 태스크가 panic하면 그 태스크만 죽고 `JoinError`로 돌아온다.
Go에서 고루틴 하나가 panic하면 **프로세스 전체가 죽는다.** CTI 엔진이 죽으면 모든 상담원 화면이 멈춘다.

- 라이브러리 코드에서는 panic하지 않고 에러를 반환한다 (Rust의 `unwrap()`을 그대로 옮기지 않는다).
- 최상위 작업 고루틴에만 `recover`를 두고, 로그·메트릭을 남긴 뒤 그 구성요소만 다시 시작한다.
  `recover`로 버그를 덮지 않도록, 복구 횟수를 경보로 연결한다.

## 7. 모듈 하나를 옮기는 절차

1. **읽고 요약한다.** 공개 함수, 상태, 불변조건, 에러 경로를 적는다. 에이전트에게 설명을 시키되,
   요약을 코드와 대조해서 틀린 부분을 찾는다(에이전트 설명은 그럴듯하게 틀리는 경우가 있다).
2. **테스트부터 옮긴다.** Rust의 `#[test]`를 Go 테스트로. 테스트가 없으면 녹화로 golden 출력을 먼저 만든다.
3. **코드를 옮긴다.** 관용구는 Go답게 바꾸되(3절 표), 동작은 바꾸지 않는다. 개선하고 싶은 점은 따로 적어 두고 나중에 한다.
4. **차분 테스트**가 모든 녹화에서 0 diff가 될 때까지 반복한다.
5. `make test`, `make lint`, `make e2e` (CI도 같은 명령을 돌린다).
6. **리뷰**한다 (9절 체크리스트).

## 8. 코딩 에이전트와 함께 포팅하기

에이전트를 잘 쓴다는 건 **에이전트가 스스로 돌려 보고 검증할 수 있는 환경**을 주는 것이다.
이 저장소의 `Makefile`과 `CLAUDE.md`가 그 역할을 한다.

요청 예시:

```text
legacy-rust/src/agent.rs의 상담원 상태머신을 engine/state/agent.go로 옮겨 줘.
규칙:
- 공개 동작은 바꾸지 말 것. 개선점은 코드 대신 PORTING_NOTES.md에 적을 것
- Rust enum은 Go 타입 상수 + switch로, switch에는 exhaustive 린터가 통과하게
- 테스트 먼저: agent.rs의 #[test]를 agent_test.go로 옮기고 실패하는 걸 확인한 뒤 구현
- make test와 차분 테스트(make diff-test)가 모두 통과할 때까지 반복
- 끝나면 Rust와 동작이 다를 수 있는 지점을 목록으로 보고할 것
```

- 한 번에 모듈 하나만 맡긴다. diff가 작아야 검토할 수 있다.
- "테스트를 고쳐서 통과시키는" 변경은 거부한다. 테스트를 바꿔야 한다면 이유를 먼저 설명하게 한다.
- 차분 테스트 결과를 에이전트가 직접 보게 한다. 사람이 결과를 옮겨 적어 주는 순간 속도가 떨어진다.

## 9. 리뷰 체크리스트

- [ ] 모든 녹화에서 차분 테스트 0 diff
- [ ] `make test`, `make lint`, `make e2e` 통과 (PR의 CI가 초록불)
- [ ] `switch`가 모든 상태·이벤트를 다루는가 (exhaustive 린터)
- [ ] 상태를 여러 고루틴이 만지지 않는가 (`-race`)
- [ ] 라이브러리 코드에 panic이 없는가. `unwrap()`이 그대로 옮겨진 곳은 없는가
- [ ] 슬라이스·맵을 넘기거나 저장할 때 원본과 공유하지 않는가
- [ ] 에러를 감싸서(`%w`) 원인을 남겼는가
- [ ] 타임아웃과 취소가 `context`로 전달되는가. 끝나지 않는 고루틴은 없는가
- [ ] 로그에 콜 ID(Linkedid)가 있는가
- [ ] 출력 형식(4절)을 지키는가

## 10. Stage 2 포팅에서 실제로 만난 것

`legacy-rust/src/engine.rs`(테스트 포함 약 1,100줄) → `engine/state/state.go`(약 970줄). 첫 차분 테스트에서 출력 10개가 모두 같았다.
운이 아니라 4절 계약 덕분이다: 출력에 맵 순서·실수·직렬화 라이브러리가 끼어들 자리를 처음부터 없앴다.

### 관용구를 어떻게 옮겼나

| Rust | Go | 메모 |
|---|---|---|
| `Option<i64>` (없을 수 있는 시각) | `optTime{at, set}` + `some(t)`, `o.or(d)` | 0을 '없음'으로 쓰면 0인 시각과 구분이 안 되고, `*int64`는 nil 역참조 위험. 타입으로 드러낸다 |
| `BTreeMap` (순서 있는 맵) | `map` + 순회할 때마다 `sortedKeys()` | 빠뜨리면 **가끔만** 깨진다. `TestTimersFireInDeadlineKindIDOrder`가 50번 돌려 확인한다 |
| `enum` + `match` | 정수 상수 + `switch` | 빠진 case는 `exhaustive` 린터가 잡는다. `CmdWrapUp` case를 지워 보니 `make lint`가 바로 실패했다 |
| 데이터를 품은 `enum Input` | `Input{Kind, Msg, Items, Cmd}` | Go에는 합 타입이 없다. Kind와 맞지 않는 필드를 읽는 실수는 컴파일러가 못 막는다 → Kind `switch`를 한 곳에만 둔다 |
| `self.agents[iface]` (없으면 panic) | `a := e.agents[iface]; if a == nil { return }` | 라이브러리 코드는 panic하지 않는다. 도달 가능한 경로에서는 동작이 같다 |
| `std::mem::take(&mut a.reserved)` | `r := a.reserved; a.reserved = ""` | |
| 빌림 검사기 때문에 목록을 먼저 `collect()`하고 바꾸기 | 맵을 정렬된 키로 돌면서 바로 바꾸기 | 루프 몸통이 **다른** 항목을 바꾸지 않을 때만 같다. 바꾼다면 Go도 먼저 모아야 한다 |
| `read_to_string`은 UTF-8이 아니면 실패 | `utf8.Valid` 검사 → `ErrNotUTF8` | Go `string`은 아무 바이트나 담는다. 경계에서 막지 않으면 두 구현이 다르게 동작한다 |
| `eq_ignore_ascii_case` | ~~`strings.EqualFold`~~ → `ami.EqualFoldASCII` | `EqualFold`는 유니코드 폴딩이라 켈빈 기호 `K`(U+212A)를 `k`로 본다. `TestKeysFoldASCIIOnly` |
| `trim()`, `split_whitespace()` | `strings.TrimSpace`, `strings.Fields` | 둘 다 유니코드 White_Space 기준이라 같다 (확인하고 넘어간 것도 적어 둔다) |
| `format!("{:06}", us % 1_000_000)` | `fmt.Sprintf("%06d", …)` | 음수가 아니면 같다. 시각은 음수가 될 수 없다 |
| `i64::MAX`, `u32` | `maxInt64`, `int` | |

### 차분 테스트가 찾은 레거시 버그

Go 파서(`ami.Reader`)와 Rust 파서(`ami::parse`)를 나란히 읽다가 의심 지점을 찾았고, 녹화로 재현해서 차분 테스트에 넣었다.

- **증상**: 녹화가 메시지 중간, 줄바꿈 **바로 뒤**에서 끊기면 (`…Interface: Local/1002@sim-agent\r\n`으로 끝남)
  Rust는 그 반쪽 메시지를 처리하고 Go는 버린다. 끝에 `QueueMemberRemoved`가 걸리면 Rust만 상담원을 로그아웃시켰다.
- **원인**: `text.split('\n')`은 마지막 줄바꿈 뒤에 빈 조각 `""`을 하나 더 내놓는다. 그걸 '빈 줄 = 메시지 끝'으로 봤다.
- **판단**: Rust 함수의 문서 주석부터 "빈 줄 없이 끝난 마지막 메시지는 버린다"였다. Go가 명세대로고 레거시가 틀렸다.
- **처리**: Rust를 `split_inclusive('\n')`로 고치고(줄바꿈으로 끝나야 줄이다), 재현 녹화 `truncated.ami`를 testdata에 남겼다.
  이제 차분 테스트가 이 회귀를 계속 지킨다.

실무에서 레거시가 운영 중이라 고칠 수 없다면, Go만 명세대로 만들고 차분 테스트에 **알려진 차이 목록**(녹화 이름 + 이유)을 둔다.
어느 쪽이든 "Rust와 다르다"를 발견하면 고치기 전에 먼저 기록한다 (5절).

### 녹화가 늘면서 바뀐 명세

포팅이 끝난 뒤에 새 시나리오를 녹화하니 모델의 빈틈이 두 번 드러났다. 둘 다 **Rust를 먼저 고치고** → `make golden` →
같은 수정을 Go에 → `make diff-test` 0 diff 순서로 반영했다.

| 녹화 | 드러난 것 | 바꾼 것 |
|---|---|---|
| `three-way.ami` | 어텐디드 전환을 시작할 때 Hold가 오는데, *3으로 3자통화가 되면 Unhold가 오지 않는다. 상담원이 끝까지 Hold로 남았다 | `AttendedTransfer DestType=Threeway`에서 보류를 푼다 |
| `ring-abandon.ami` | 벨이 울리는 중에 고객이 끊으면 상담원 쪽 이벤트가 하나도 없다. 정상 흐름인데 경고가 나왔다 | `QueueCallerAbandon`에서 벨이 울리던 상담원을 돌려보낸다 (무응답으로 세지 않는다) |

### 차분 테스트 밖에 있는 것

- **실시간 껍데기**(`engine/live`)는 Go에만 있다. 가짜 AMI 서버 단위 테스트와 e2e(`stepEngine`, `stepEngineResync`)로 검증한다.
  e2e가 "재시작 직후 AMI는 붙는데 `QueueStatus`가 없다"를 찾아서 재동기화 시점을 FullyBooted로 옮겼다 (06 문서 7절).
- `Engine.Advance`(입력 없이 시간만 흘리기)는 실시간에서만 쓰지만 API를 맞추려고 두 언어에 다 넣고 각자 단위 테스트를 뒀다.
