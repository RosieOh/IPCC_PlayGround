// engine은 상태머신을 실제 Asterisk에 붙여 돌린다 (Stage 2 엔진 껍데기).
//
// 상태 변화·액션·경고를 표준출력에 JSONL로 쓰고(형식은 bin/state replay와 같다),
// 상담원 명령은 표준입력에서 한 줄씩 받는다:
//
//	login Local/1001@sim-agent 상담원1
//	ready Local/1001@sim-agent
//	notready Local/1001@sim-agent 식사
//	wrapup Local/1001@sim-agent
//	logout Local/1001@sim-agent
//	snapshot
//
// 상담원 툴바용 명령 API(HTTP·WebSocket)는 Stage 3에서 만든다.
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
	"ipcc/engine/live"
	"ipcc/engine/state"
)

func main() {
	addr := flag.String("addr", envOr("AMI_ADDR", "127.0.0.1:5038"), "AMI 주소")
	user := flag.String("user", envOr("AMI_USER", "ipcc"), "AMI 사용자 (asterisk/conf/manager.conf)")
	secret := flag.String("secret", envOr("AMI_SECRET", "ipcc-secret"), "AMI 비밀번호")
	queue := flag.String("queue", "support", "상담원을 넣고 빼는 큐")
	acw := flag.Duration("acw", 10*time.Second, "후처리 시간")
	maxMissed := flag.Int("max-missed", 2, "무응답이 이만큼 연속되면 자동 이석 (0이면 끔)")
	debug := flag.Bool("debug", false, "디버그 로그 (보내는 액션까지 표시)")
	flag.Parse()

	level := slog.LevelInfo
	if *debug {
		level = slog.LevelDebug
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	client := ami.New(ami.Config{Addr: *addr, Username: *user, Secret: *secret, Logger: log})
	out := bufio.NewWriter(os.Stdout)
	policy := state.DefaultPolicy()
	policy.ACWMicros = acw.Microseconds()
	policy.MaxMissed = *maxMissed
	r := live.New(client, live.Config{
		Queue:  *queue,
		Policy: policy,
		Logger: log,
		Output: func(line string) {
			fmt.Fprintln(out, line)
			_ = out.Flush()
		},
	})

	go func() { _ = client.Run(ctx) }()
	go readCommands(ctx, r, log)
	if err := r.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		log.Error("엔진 종료", "err", err)
		os.Exit(1)
	}
}

// readCommands는 표준입력의 명령을 엔진에 넣는다. 형식은 명령 파일(bin/state replay --cmds)에서 시각을 뺀 것이다.
func readCommands(ctx context.Context, r *live.Runner, log *slog.Logger) {
	sc := bufio.NewScanner(os.Stdin)
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) == 0 || strings.HasPrefix(f[0], "#") {
			continue
		}
		if f[0] == "snapshot" {
			if s, err := r.Snapshot(ctx); err == nil {
				fmt.Fprintln(os.Stderr, s)
			}
			continue
		}
		// 명령 파일 파서를 그대로 쓴다 (시각 자리는 버린다)
		cmds, err := state.ParseCommands("0 " + strings.Join(f, " "))
		if err != nil {
			log.Warn("명령을 알아들을 수 없음", "line", sc.Text(), "err", err)
			continue
		}
		if err := r.Command(ctx, cmds[0].Input.Cmd); err != nil {
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
