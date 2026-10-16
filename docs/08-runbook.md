# 08. 런북 — 실행, 연결, 디버깅, 문제 해결

## 1. 준비물

| 필요 | 용도 | 비고 |
|---|---|---|
| Docker Desktop | Asterisk 컨테이너 | 필수 |
| Go 1.24 이상 | 엔진, amicli | 필수 |
| Linphone 또는 Zoiper | 소프트폰 (사람이 직접 통화) | Stage 0 과제용 |
| Wireshark | 패킷 분석 | 선택 |
| Rust (rustup) | Stage 2 레거시 구현 | Stage 2부터 |

## 2. 처음 실행

```bash
make up            # Asterisk 이미지 빌드(첫 실행 몇 분) + 기동
make logs          # 'Asterisk Ready.'가 보이면 Ctrl+C
make test          # Go 테스트

# 터미널 1
make watch         # AMI 이벤트 실시간 보기 (recordings/에 녹화)

# 터미널 2
make sim-agents    # 가짜 상담원 3명 로그인 (SIM-1001~1003)
make sim-call n=3  # 가짜 고객 콜 3건 → 터미널 1에서 흐름 확인
make sip-echo      # SIP 인증·통화 자동 테스트 (Successful call 1)
```

`make`만 치면 전체 명령 목록이 나온다.

## 3. 소프트폰 연결

계정 정보 (`asterisk/conf/pjsip.conf`):

| 내선 | 비밀번호 | 용도 |
|---|---|---|
| 1001 | ipcc1001 | 상담원 |
| 1002 | ipcc1002 | 상담원 / 고객 역할 |
| 1003 | ipcc1003 | 상담원 |
| 9001 | sipp9001 | SIPp 자동 테스트 전용 (소프트폰에 쓰지 않는다) |

**Mac의 Linphone** (`make up`으로 띄운 경우)

1. "SIP 계정 사용"(Use a SIP account)을 고른다 (Linphone 자체 계정이 아니라).
2. 사용자 이름 `1001`, SIP 도메인 `127.0.0.1`, 비밀번호 `ipcc1001`, 전송 `UDP`.
3. 등록되면 `make rx c="pjsip show contacts"`에 1001이 보인다.
4. **600**에 걸어서 내 목소리가 돌아오면 양방향 음성 성공. **601**은 안내멘트만 나온다.

한 Mac에서 소프트폰 두 개를 쓰면(예: Linphone 1001 + Zoiper 1002) 두 앱의 로컬 SIP 포트가 겹치지 않게 한다.
앱의 로컬 포트가 5060이면 Docker가 쓰는 5060과 부딪힐 수도 있으니 '임의 포트'나 5070 같은 값으로 바꾼다.

**휴대폰** (같은 Wi-Fi)

1. `make up-lan` → 출력에 나온 Mac의 LAN IP를 확인한다 (예: `EXTERNAL_IP=192.168.0.12`).
2. 휴대폰 Linphone에서 사용자 이름 `1002`, 도메인 `192.168.0.12`, 비밀번호 `ipcc1002`, UDP.
3. SIP/RTP 포트가 LAN에 열리므로 공용 Wi-Fi에서는 쓰지 않는다. 다 쓰면 `make up`으로 되돌린다.

**상담원으로 큐 콜 받기**

```bash
make agent-login ext=1001     # 1001을 support 큐에 로그인
# 1002(다른 소프트폰)에서 5000으로 걸기 → 안내멘트 → 1001에서 벨
make agent-logout ext=1001
```

## 4. 자주 쓰는 Asterisk CLI 명령

`make cli`로 들어가서 치거나, `make rx c="명령"`으로 한 번만 실행한다.

| 명령 | 용도 |
|---|---|
| `core show channels` / `core show channels verbose` | 지금 있는 채널 |
| `pjsip show endpoints` / `pjsip show contacts` | 내선 상태 / 실제 등록 주소 |
| `pjsip show endpoint 1001` | 내선 하나의 모든 설정 |
| `pjsip show transport transport-udp` | NAT 설정이 실제로 어떻게 들어갔는지 |
| `pjsip set logger on` / `off` | SIP 메시지 원문을 로그로 |
| `rtp set debug on` / `off` | RTP 송수신 (양이 많다. 짧게) |
| `queue show support` | 큐 멤버, 대기 고객, 누적 통계 |
| `dialplan show internal` | 다이얼플랜 |
| `dialplan reload` / `core reload` | 설정 다시 읽기 (`make reload`) |
| `manager show connected` | 붙어 있는 AMI 클라이언트 |
| `manager show events` / `manager show event AgentConnect` | AMI 이벤트 목록 / 필드 설명 |
| `manager show command QueuePause` | AMI 액션 설명 |
| `core show application Queue` | 다이얼플랜 앱 설명 (옵션 목록) |
| `channel request hangup all` | 모든 콜 강제 종료 |

## 5. 로그와 기록이 남는 곳

| 무엇 | 어디 | 비고 |
|---|---|---|
| Asterisk 콘솔 | `make logs` | 컨테이너 표준출력 |
| Asterisk 로그 파일 | `data/asterisk/log/messages.log` | Mac에서 바로 연다 |
| 보안 이벤트 (인증 실패 등) | `data/asterisk/log/security.log` | |
| 큐 로그 | `data/asterisk/log/queue_log` | 통계의 원재료 (05 문서 5절) |
| CDR | `data/asterisk/log/cdr-csv/Master.csv` | 구간 단위 통화 기록 |
| AMI 원본 녹화 | `recordings/ami-*.log` | `bin/amicli replay`로 읽는다 |
| 패킷 캡처 | `data/asterisk/log/capture.pcap` | `make pcap` |
| 통화 녹음 | `data/asterisk/monitor/` | MixMonitor (Stage 3~) |

`data/`와 `recordings/`는 git에 올라가지 않는다. 녹화를 테스트 데이터로 쓰려면 전화번호와 호스트명을 가리고
`engine/testdata/`로 옮긴다.

## 6. 문제 해결

| 증상 | 흔한 원인 | 확인 / 조치 |
|---|---|---|
| `make up` 빌드 실패 | 소스·pjproject·사운드 다운로드 실패 | 네트워크·프록시 확인 후 다시. 로그에서 처음 실패한 단계를 본다 |
| `port is already allocated` | Mac의 다른 앱이 5060·5038 사용 | 그 앱을 끄거나 `docker-compose.yml`의 포트를 바꾼다 |
| 등록 시 401이 반복되거나 403 | 사용자 이름·비밀번호 | `make rx c="pjsip set logger on"` 후 `make logs`, `security.log` |
| 걸면 404 | 다이얼플랜에 그 번호가 없다 | `make rx c="dialplan show internal"` |
| 걸면 바로 끊김 | 상대가 등록되지 않음 | `make rx c="pjsip show contacts"` |
| 연결은 되는데 소리가 없음 | NAT·SDP 주소, RTP 포트 | [04 문서 6절](04-sip-sdp-rtp.md#6-nat와-연결은-되는데-소리가-안-들려요)의 진단 순서 |
| 대표번호(5000)에 걸었는데 상담원 벨이 안 울림 | 멤버 없음, 모두 이석, 모두 통화 중 | `make rx c="queue show support"` |
| amicli: "5초 안에 로그인하지 못함" | Asterisk가 안 떠 있음, 계정 불일치 | `make ps`, `manager.conf`의 `[ipcc]`, `.env`의 `AMI_*` |
| amicli: `banner: EOF`가 몇 번 찍힘 | Asterisk 재시작 중 (Docker 포트포워더가 접속만 받고 끊음) | 정상. 자동 재접속을 기다린다 |
| `watch`에 기대한 이벤트가 안 보임 | 기본 필터가 Newexten 등을 숨긴다 | `-all` 또는 `-only 이벤트이름` |
| 로그 시각이 9시간 어긋남 | 시간대 | `.env`의 `TZ` 확인 후 `make restart` |
| 설정을 고쳤는데 반영이 안 됨 | reload 필요 | `make reload`. transport 템플릿·환경변수는 `make restart` |

## 7. 초기화

```bash
make down                                         # 컨테이너 삭제
make clean-data                                   # data/, recordings/ 삭제 (되돌릴 수 없음)
docker image rm ipcc-playground/asterisk:22.11.0  # 이미지까지 지우기
```
