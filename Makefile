# IPCC Playground — 자주 쓰는 명령 모음. `make`로 목록을 본다.
#
# 사람도, 코딩 에이전트도 같은 명령으로 "띄우고 → 콜을 만들고 → 검증"할 수 있게 하는 것이 목적이다.

SHELL := /bin/bash
.DEFAULT_GOAL := help

# .env가 있으면 읽어서 하위 명령(amicli, docker compose)에 넘긴다
-include .env
export

COMPOSE := docker compose
AST     := $(COMPOSE) exec asterisk
AMICLI  := bin/amicli

# golangci-lint는 Docker 이미지로 실행해서 로컬·CI 버전을 똑같이 맞춘다 (.github/workflows/ci.yml과 같은 버전)
GOLANGCI_LINT_VERSION := v2.14.0
GOLANGCI := docker run --rm -v "$(CURDIR)/engine:/src" -v ipcc-golangci-cache:/root/.cache -w /src \
	golangci/golangci-lint:$(GOLANGCI_LINT_VERSION) golangci-lint

# SIPp는 Asterisk 컨테이너 안에서 127.0.0.1로 실행한다.
# Docker의 NAT를 거치지 않으므로 SIP 시그널링·다이얼플랜·AMI 이벤트 검증에 집중할 수 있다.
SIPP := $(COMPOSE) exec -T asterisk sipp 127.0.0.1:5060 -i 127.0.0.1 -p 5070 \
	-key user 9001 -au 9001 -ap sipp9001 -m 1 -nostdin
# 상담원 전화기 역할(9002). pjsip.conf에서 9002를 127.0.0.1:5072 고정 주소로 부르므로 그 포트에서 기다린다
SIPP_AGENT := $(COMPOSE) exec -T asterisk sipp -i 127.0.0.1 -p 5072 -mp 6100 -m 1 -nostdin -timeout 60s

# Rust 레거시(legacy-rust)도 Docker 이미지로 빌드·실행한다 (로컬에 Rust 설치 불필요, CI와 같은 버전).
# 저장소는 읽기 전용으로 마운트하고, 빌드 산출물·crate 캐시는 이름 있는 볼륨에 둔다.
RUST_IMAGE  := rust:1.98.1-slim-bookworm
RUST_DOCKER := docker run --rm -v "$(CURDIR):/repo:ro" -v ipcc-cargo-target:/target \
	-v ipcc-cargo-registry:/usr/local/cargo/registry -e CARGO_TARGET_DIR=/target -w /repo/legacy-rust
# 상태머신 녹화와 기대 출력. 재생하는 녹화는 STATE_DATA/*.ami(시나리오)와 engine/testdata/*.log(Stage 1 녹화)
STATE_DATA  := engine/state/testdata
STATE_RECS  := $(STATE_DATA) engine/testdata
RUST_REPLAY_ALL := cargo build --release -q && /repo/tests/state/replay-all.sh /target/release/ipcc-state /out $(addprefix /repo/,$(STATE_RECS))

.PHONY: help up up-lan down restart ps logs cli rx reload sngrep pcap sh \
	build watch agent-login agent-logout sim-agents sim-logout sim-call sim-abandon \
	test lint e2e rtp-check event-dict scenarios rust-test golden diff-test \
	sip-register sip-echo sip-queue sip-agent-call clean clean-data

help: ## 이 목록
	@awk 'BEGIN{FS=":.*## "} /^## /{printf "\n\033[1m%s\033[0m\n", substr($$0,4); next} /^[a-zA-Z_-]+:.*## /{printf "  \033[36m%-14s\033[0m %s\n", $$1, $$2}' $(MAKEFILE_LIST)

## Asterisk
up: ## 기동 (Mac 안에서만 접속). 처음에는 이미지 빌드로 몇 분 걸린다
	$(COMPOSE) up -d --build
	@echo "→ make logs 에서 'Asterisk Ready.'를 확인하고, make cli 로 콘솔에 들어간다"

up-lan: ## 기동 (같은 Wi-Fi의 휴대폰에서도 접속). EXTERNAL_IP=Mac의 LAN IP
	@ip="$${EXTERNAL_IP_LAN:-$$(ipconfig getifaddr en0 || ipconfig getifaddr en1)}"; \
	 test -n "$$ip" || { echo "LAN IP를 찾지 못함. 직접 지정: EXTERNAL_IP_LAN=192.168.0.10 make up-lan"; exit 1; }; \
	 echo "EXTERNAL_IP=$$ip PUBLISH_IP=0.0.0.0  (SIP/RTP 포트가 LAN에 열린다)"; \
	 EXTERNAL_IP=$$ip PUBLISH_IP=0.0.0.0 $(COMPOSE) up -d --build

down: ## 중지 (컨테이너 삭제. data/ 와 recordings/ 는 남는다)
	$(COMPOSE) down

restart: ## 재시작 (pjsip_transport.conf.tmpl이나 환경변수를 바꿨을 때)
	$(COMPOSE) restart asterisk

ps: ## 컨테이너 상태
	$(COMPOSE) ps

logs: ## Asterisk 콘솔 로그 따라가기 (Ctrl+C로 빠져나옴)
	$(COMPOSE) logs -f --tail=100 asterisk

cli: ## Asterisk CLI 접속 (asterisk -rvvv). 나가기: exit
	$(AST) asterisk -rvvv

rx: ## CLI 명령 한 번 실행. 예) make rx c="pjsip show endpoints"
	@$(COMPOSE) exec -T asterisk asterisk -rx "$(c)"

reload: ## 설정 다시 읽기 (extensions·pjsip·queues 등 conf를 고친 뒤)
	@$(COMPOSE) exec -T asterisk asterisk -rx "core reload"

sngrep: ## SIP 흐름을 래더 다이어그램으로 보기 (q로 종료)
	$(AST) sngrep

pcap: ## 패킷 캡처 → data/asterisk/log/capture.pcap (Ctrl+C로 종료 → Wireshark로 열기)
	$(AST) tcpdump -i any -s0 -w /var/log/asterisk/capture.pcap udp

sh: ## 컨테이너 셸
	$(AST) bash

## Go 엔진 / AMI
build: ## Go 빌드 → bin/amicli, bin/state(녹화 재생), bin/engine(실시간 엔진)
	cd engine && go build -o ../bin/ ./cmd/amicli ./cmd/state ./cmd/engine

watch: build ## AMI 이벤트 실시간 보기 + 원본 녹화 (recordings/)
	$(AMICLI) watch -record recordings

agent-login: build ## 소프트폰 상담원 큐 로그인. 예) make agent-login ext="1001 1002"
	$(AMICLI) agent login $(ext)

agent-logout: build ## 소프트폰 상담원 큐 로그아웃
	$(AMICLI) agent logout $(ext)

sim-agents: build ## 가짜 상담원 3명 로그인 (소프트폰 없이 큐 테스트)
	$(AMICLI) agent login -sim

sim-logout: build ## 가짜 상담원 로그아웃
	$(AMICLI) agent logout -sim

sim-call: build ## 가짜 고객 콜 인입. 예) make sim-call n=5
	$(AMICLI) sim-call -n $(or $(n),1)

sim-abandon: build ## 6초 뒤 끊는 고객 콜 (받을 상담원이 없을 때 포기호가 된다)
	$(AMICLI) sim-call -n $(or $(n),1) -patience 6

## 검증 (CI도 같은 명령을 쓴다)
test: ## 단위 테스트: gofmt 검사 + go vet + go test -race
	@cd engine && unformatted="$$(gofmt -l .)"; \
	 if [ -n "$$unformatted" ]; then echo "gofmt이 필요한 파일 (gofmt -w <파일>로 고친다):"; echo "$$unformatted"; exit 1; fi
	cd engine && go vet ./... && go test -race ./...

lint: ## golangci-lint: errcheck, exhaustive, errorlint 등 (Docker로 실행)
	$(GOLANGCI) run ./...

e2e: ## 실제 Asterisk로 콜 흐름 전체 검증. 재시작까지 보려면 E2E_RESTART=1 make e2e
	$(COMPOSE) up -d
	cd engine && go test -tags e2e -count=1 -v -timeout 5m ./e2e/

rtp-check: ## 음성(RTP) 경로 검사: 이 Mac에서 602(에코)로 걸어 RTP가 양방향으로 오가는지 센다
	cd engine && go run ./cmd/rtpcheck

event-dict: build ## AMI 이벤트 사전 다시 만들기 (engine/testdata, engine/state/testdata 녹화 → docs/10-event-dictionary.md)
	$(AMICLI) stats -md engine/testdata/*.log $(filter-out %/truncated.ami,$(wildcard $(STATE_DATA)/*.ami)) > docs/10-event-dictionary.md

## 상태머신 (Stage 2): Rust 레거시 → Go 포팅
scenarios: ## 상태머신 테스트 녹화 다시 만들기 (실제 Asterisk, 약 1분 30초 → engine/state/testdata/*.ami). 고르기: only=hold,three-way
	$(COMPOSE) up -d
	cd engine && RECORD_SCENARIOS=$(CURDIR)/$(STATE_DATA) SCENARIOS=$(only) go test -tags e2e -count=1 -v -timeout 5m -run TestRecordScenarios ./e2e/

rust-test: ## Rust 레거시 단위 테스트 (Docker)
	$(RUST_DOCKER) $(RUST_IMAGE) cargo test

golden: ## Rust 레거시로 기대 출력 다시 만들기 (engine/state/testdata/*.golden.jsonl). Go 테스트가 이것과 비교한다
	@tmp=$$(mktemp -d) && trap 'rm -rf "$$tmp"' EXIT && \
	 $(RUST_DOCKER) -v "$$tmp:/out" $(RUST_IMAGE) sh -c '$(RUST_REPLAY_ALL)' && \
	 cp "$$tmp"/*.golden.jsonl $(STATE_DATA)/ && ls $(STATE_DATA)/*.golden.jsonl

diff-test: ## 차분 테스트: 모든 녹화에서 Rust 레거시와 Go 포팅의 출력이 바이트 단위로 같은지 (Docker)
	@tmp=$$(mktemp -d) && trap 'rm -rf "$$tmp"' EXIT && mkdir "$$tmp/rust" "$$tmp/go" && \
	 $(RUST_DOCKER) -v "$$tmp/rust:/out" $(RUST_IMAGE) sh -c '$(RUST_REPLAY_ALL)' && \
	 (cd engine && go build -o "$$tmp/state" ./cmd/state) && \
	 tests/state/replay-all.sh "$$tmp/state" "$$tmp/go" $(STATE_RECS) && \
	 diff -ru "$$tmp/rust" "$$tmp/go" && \
	 for f in "$$tmp"/rust/*.jsonl; do \
	   cmp -s "$$f" "$(STATE_DATA)/$${f##*/}" || { echo "기대 출력이 Rust 출력과 다름: $${f##*/} (make golden)"; exit 1; }; \
	 done && \
	 echo "Rust = Go: $$(ls "$$tmp"/rust | wc -l | tr -d ' ')개 출력이 모두 같음"

## SIP 자동 테스트 (SIPp, 컨테이너 안에서 실행)
sip-register: ## 9001로 등록 (REGISTER → 401 → 인증 → 200)
	$(SIPP) -sf /sipp/register.xml

sip-echo: ## 9001 → 600(에코) 3초 통화
	$(SIPP) -sf /sipp/uac-call-hangup.xml -s 600 -d 3000

sip-queue: ## 9001 → 5000(대표번호) → 큐 → 상담원이 끊을 때까지 (먼저 make sim-agents)
	$(SIPP) -sf /sipp/uac-call-wait-bye.xml -s 5000

sip-agent-call: build ## 실제 SIP 상담원 흐름: 9001 → 5000 → PJSIP/9002(SIPp가 받고 2초 뒤 끊음). 가짜 상담원은 로그아웃 상태여야 한다
	@$(AMICLI) agent login 9002
	@$(SIPP_AGENT) -sf /sipp/uas-agent.xml -d 2000 > /dev/null & uas=$$!; sleep 1; \
	 $(SIPP) -sf /sipp/uac-call-wait-bye.xml -s 5000; rc=$$?; \
	 wait $$uas || { echo "상담원 SIPp(UAS) 실패"; rc=1; }; \
	 $(AMICLI) agent logout 9002; exit $$rc

## 정리
clean: ## 빌드 산출물 삭제 (bin/)
	rm -rf bin

clean-data: ## 로그·녹음·AMI 녹화 삭제 (data/ recordings/) — 되돌릴 수 없음
	rm -rf data recordings
