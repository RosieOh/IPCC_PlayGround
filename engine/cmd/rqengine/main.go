// rqengine은 룸 큐(app_queue 없이 엔진이 상담원을 고르는 큐)를 실제 Asterisk에 붙여 돌린다 (docs/12-room-queue.md).
//
// 상태 변화·액션을 표준출력에 JSONL로 쓰고, 명령은 표준입력에서 한 줄씩 받는다:
//
//	login Local/1001@sim-agent 상담원1
//	ready Local/1001@sim-agent
//	notready Local/1001@sim-agent 식사
//	wrapup Local/1001@sim-agent
//	transfer Local/1001@sim-agent Local/1002@sim-agent     (1002가 방에 들어오면 1001을 뺀다)
//	conference Local/1001@sim-agent Local/1002@sim-agent   (셋이 함께 통화)
//	logout Local/1001@sim-agent
//	snapshot
//
// 콜은 대표번호 5100 또는 가짜 고객(make sim-rq-call)으로 넣는다.
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"ipcc/engine/ami"
	"ipcc/engine/rq"
)

func main() {
	addr := flag.String("addr", envOr("AMI_ADDR", "127.0.0.1:5038"), "AMI 주소")
	user := flag.String("user", envOr("AMI_USER", "ipcc"), "AMI 사용자")
	secret := flag.String("secret", envOr("AMI_SECRET", "ipcc-secret"), "AMI 비밀번호")
	acw := flag.Duration("acw", 10*time.Second, "후처리 시간")
	ring := flag.Duration("ring", 15*time.Second, "상담원 벨 시간 (Originate Timeout)")
	maxMissed := flag.Int("max-missed", 2, "무응답이 이만큼 연속되면 자동 이석 (0이면 끔)")
	simTalk := flag.String("sim-talk", "", "가짜 상담원(sim-agent) 통화 시간(초). 비우면 5~15초 무작위")
	flag.Parse()

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	p := rq.DefaultPolicy()
	p.ACWMicros, p.OfferMicros, p.MaxMissed = acw.Microseconds(), ring.Microseconds(), *maxMissed
	vars := map[string]string{"__SIM_RING": "1"}
	if *simTalk != "" {
		vars["__SIM_TALK"] = *simTalk
	}
	client := ami.New(ami.Config{Addr: *addr, Username: *user, Secret: *secret, Logger: log})
	out := bufio.NewWriter(os.Stdout)
	r := rq.NewRunner(client, rq.RunnerConfig{Policy: p, AgentVars: vars, Logger: log, Output: func(l string) {
		fmt.Fprintln(out, l)
		_ = out.Flush()
	}})

	go func() { _ = client.Run(ctx) }()
	go readCommands(ctx, r, log)
	if err := r.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		log.Error("엔진 종료", "err", err)
		os.Exit(1)
	}
}

var kinds = map[string]rq.CommandKind{
	"login": rq.CmdLogin, "logout": rq.CmdLogout, "ready": rq.CmdReady, "notready": rq.CmdNotReady,
	"wrapup": rq.CmdWrapUp, "transfer": rq.CmdTransfer, "conference": rq.CmdConference,
}

func readCommands(ctx context.Context, r *rq.Runner, log *slog.Logger) {
	sc := bufio.NewScanner(os.Stdin)
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) == 0 || strings.HasPrefix(f[0], "#") {
			continue
		}
		if f[0] == "snapshot" {
			if s, err := r.Snapshot(ctx); err == nil {
				fmt.Fprint(os.Stderr, s)
			}
			continue
		}
		k, ok := kinds[f[0]]
		if !ok || len(f) < 2 {
			log.Warn("명령을 알아들을 수 없음 (login|logout|ready|notready|wrapup|transfer|conference <인터페이스> [인자])", "line", sc.Text())
			continue
		}
		if err := r.Command(ctx, rq.Command{Kind: k, Iface: f[1], Arg: strings.Join(f[2:], " ")}); err != nil {
			return
		}
	}
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
