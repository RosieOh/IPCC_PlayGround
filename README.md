# IPCC Playground

[![CI](https://github.com/RosieOh/IPCC_PlayGround/actions/workflows/ci.yml/badge.svg)](https://github.com/RosieOh/IPCC_PlayGround/actions/workflows/ci.yml)

오픈소스 Asterisk 기반 IPCC 엔진 개발(핵심 엔진 Rust → Go 전환)을 미리 익히기 위한 학습용 **미니 IPCC**다.

- **Asterisk 22 LTS** (Docker 소스 빌드) — SIP 내선, 다이얼플랜, 큐(ACD)
- **Go CTI 엔진** — AMI 클라이언트 (재접속, 하트비트, ActionID 매칭, 원본 녹화)
- 이후 단계: 상태머신(Rust → Go 포팅 리허설), Node.js 게이트웨이와 화면, MySQL 통계, ARS, 고객사 연동

## 빠른 시작

요구 사항: Docker Desktop, Go 1.24 이상

```bash
make up            # Asterisk 빌드·기동 (첫 실행은 몇 분)
make test          # 단위 테스트 (gofmt + vet + race)
make lint          # golangci-lint (Docker로 실행)
make e2e           # 실제 Asterisk로 콜 흐름 전체를 자동 검증 (약 40초)
make rtp-check     # 이 Mac에서 음성(RTP)이 Asterisk까지 왕복하는지 검사

make watch         # 터미널 1: AMI 이벤트 실시간 보기 (recordings/에 녹화)
make sim-agents    # 터미널 2: 가짜 상담원 3명 로그인
make sim-call n=3  # 가짜 고객 콜 3건 → 터미널 1에서 큐 인입 → 상담원 연결 → 종료 흐름 확인

make sip-echo      # SIP 인증·통화 자동 테스트 (SIPp)
make               # 전체 명령 목록
```

소프트폰(Linphone 등) 연결과 문제 해결은 [docs/08-runbook.md](docs/08-runbook.md)에 있다.

## 지금 있는 것

| Stage | 내용 | 상태 |
|---|---|---|
| 0 | Asterisk 22.11 + PJSIP 내선(소프트폰 1001~1003, 자동 테스트 9001·9002), 에코(600·602)·멘트(601)·대표번호(5000 → 큐), Mac Docker NAT 설정, SIPp 시나리오, 음성 경로 검사(`rtp-check`) | ✅ 동작 확인 |
| 1 | `engine/ami` AMI 클라이언트 + `amicli` (watch / action / replay / agent / sim-call), 단위 테스트, e2e, CI, 샘플 녹화 2종 | ✅ 동작 확인 |
| 2~6 | 상태머신, 실시간 화면, 통계, ARS, 연동·장애 대응 | 설계 문서 / 계획 |

실제 Asterisk로 확인한 것: 큐 인입 → 상담원 연결 → 종료(가짜 상담원, 실제 SIP 상담원), 대기 중 포기,
SIP 등록·인증·통화, Mac에서 Docker 포트포워딩을 거친 음성(RTP) 왕복, Asterisk 재시작 후 자동 재접속,
`docker pause` 때 하트비트로 끊김 감지 후 복구.
이 가운데 `docker pause`를 뺀 나머지는 `make e2e`로 자동화했고, CI가 PR과 `main` 푸시마다 돌린다.
소프트폰으로 실제 소리를 들어 보는 확인만 사람 몫으로 남아 있다.

## 문서

[docs/README.md](docs/README.md)에서 시작한다. 처음에는 [아키텍처](docs/01-architecture.md) → [로드맵](docs/02-roadmap.md) 순서로 읽는다.

## 폴더

```text
asterisk/     Dockerfile, entrypoint, conf/ (설정 파일)
engine/       Go 모듈 ipcc/engine — ami/, rtpcheck/, cmd/, e2e/, testdata/
tests/sipp/   SIPp 시나리오
docs/         설계 문서
data/         (git 제외) Asterisk 로그·CDR·queue_log
recordings/   (git 제외) AMI 원본 녹화
```

## 라이선스

[MIT](LICENSE)
