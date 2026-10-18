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
// -http 주소(기본 127.0.0.1:8090)에는 화면용 API를 연다 (live.Handler: 상태, SSE, 명령). 화면은 web/ (Next.js).
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
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
	httpAddr := flag.String("http", envOr("ENGINE_HTTP", "127.0.0.1:8090"), "화면용 HTTP API 주소 (빈 값이면 열지 않는다)")
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
	if *httpAddr != "" {
		go serveHTTP(ctx, *httpAddr, r.Handler(), log)
	}
	if err := r.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		log.Error("엔진 종료", "err", err)
		os.Exit(1)
	}
}

// serveHTTP는 화면용 API를 연다. SSE 연결이 오래 살아 있으므로 WriteTimeout을 두지 않고,
// 요청 컨텍스트를 ctx에서 파생시켜 엔진이 끝날 때 SSE 핸들러도 같이 끝나게 한다.
func serveHTTP(ctx context.Context, addr string, h http.Handler, log *slog.Logger) {
	srv := &http.Server{
		Addr: addr, Handler: h, ReadHeaderTimeout: 5 * time.Second,
		BaseContext: func(net.Listener) context.Context { return ctx },
	}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()
	log.Info("화면용 API", "addr", "http://"+addr)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Error("화면용 API를 열 수 없음", "addr", addr, "err", err)
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
