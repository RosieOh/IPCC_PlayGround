// rqengine은 룸 큐(app_queue 없이 엔진이 상담원을 고르는 큐)를 실제 Asterisk에 붙여 돌린다 (docs/12-room-queue.md).
//
// 상태 변화·액션을 표준출력에 JSONL로 쓰고, 명령은 표준입력에서 한 줄씩 받는다 (-http를 주면 화면용 API도 연다):
//
//	login Local/1001@sim-agent 상담원1
//	skills Local/1001@sim-agent support:1,sales:2      (비우면 모든 큐)
//	ready Local/1001@sim-agent
//	notready Local/1001@sim-agent 식사
//	wrapup Local/1001@sim-agent
//	transfer Local/1001@sim-agent Local/1002@sim-agent     (1002가 방에 들어오면 1001을 뺀다)
//	conference Local/1001@sim-agent Local/1002@sim-agent   (셋이 함께 통화)
//	hold / unhold Local/1001@sim-agent                     (고객만 보류 방으로 / 원래 방으로)
//	consult Local/1001@sim-agent Local/1002@sim-agent      (고객을 보류하고 1002와 먼저 통화)
//	complete / cancel Local/1001@sim-agent                 (협의 전환 완료 / 취소. 협의 중 unhold는 3자통화)
//	logout Local/1001@sim-agent
//	snapshot
//
// 상담원 업무 상태(로그인·이석·스킬)는 -state 파일에 저장했다가 다시 시작할 때 되살리고,
// 진행 중인 통화는 Asterisk의 채널 목록으로 다시 맞춘다. 콜은 대표번호 5100(support)·5101(sales)
// 또는 가짜 고객(make sim-rq-call [q=sales])으로 넣는다.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
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
	record := flag.Bool("record", false, "상담원이 방에 들어오면 녹음 (data/asterisk/monitor/rq/<방>-<시각>.wav)")
	statePath := flag.String("state", "data/rqengine/agents.json", "상담원 업무 상태 파일 (비우면 저장하지 않음)")
	httpAddr := flag.String("http", "", "화면용 HTTP API 주소 (예: 127.0.0.1:8091). 비우면 열지 않음")
	simTalk := flag.String("sim-talk", "", "가짜 상담원(sim-agent) 통화 시간(초). 비우면 5~15초 무작위")
	flag.Parse()

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	p := rq.DefaultPolicy()
	p.ACWMicros, p.OfferMicros, p.MaxMissed, p.Record = acw.Microseconds(), ring.Microseconds(), *maxMissed, *record
	vars := map[string]string{"__SIM_RING": "1"}
	if *simTalk != "" {
		vars["__SIM_TALK"] = *simTalk
	}
	cfg := rq.RunnerConfig{Policy: p, AgentVars: vars, Logger: log}
	if *statePath != "" {
		recs, err := loadRecords(*statePath)
		if err != nil {
			log.Error("상담원 상태 파일을 읽을 수 없음", "path", *statePath, "err", err)
			os.Exit(1)
		}
		if len(recs) > 0 {
			log.Info("상담원 업무 상태를 되살림 (통화 상태는 Asterisk에서 다시 읽는다)", "path", *statePath, "agents", len(recs))
		}
		cfg.Restore = recs
		cfg.Save = func(recs []rq.AgentRecord) {
			if err := saveRecords(*statePath, recs); err != nil {
				log.Warn("상담원 상태 저장 실패", "path", *statePath, "err", err)
			}
		}
	}
	client := ami.New(ami.Config{Addr: *addr, Username: *user, Secret: *secret, Logger: log})
	out := bufio.NewWriter(os.Stdout)
	cfg.Output = func(l string) {
		fmt.Fprintln(out, l)
		_ = out.Flush()
	}
	r := rq.NewRunner(client, cfg)

	go func() { _ = client.Run(ctx) }()
	go readCommands(ctx, r, log)
	if *httpAddr != "" {
		srv := &http.Server{Addr: *httpAddr, Handler: r.Handler(), ReadHeaderTimeout: 5 * time.Second}
		go func() {
			log.Info("화면용 HTTP API", "addr", "http://"+*httpAddr)
			if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				log.Error("HTTP 서버 종료", "err", err)
				stop()
			}
		}()
		defer func() {
			sctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			_ = srv.Shutdown(sctx)
		}()
	}
	if err := r.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		log.Error("엔진 종료", "err", err)
		os.Exit(1)
	}
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
		var iface string
		if len(f) > 1 {
			iface = f[1]
		}
		cmd, err := rq.ParseCommand(f[0], iface, strings.Join(f[2:], " "))
		if err != nil {
			log.Warn("명령을 알아들을 수 없음", "line", sc.Text(), "err", err)
			continue
		}
		if err := r.Command(ctx, cmd); err != nil {
			return
		}
	}
}

// loadRecords는 저장해 둔 상담원 업무 상태를 읽는다. 파일이 없으면 빈 목록.
func loadRecords(path string) ([]rq.AgentRecord, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("읽기: %w", err)
	}
	var recs []rq.AgentRecord
	if err := json.Unmarshal(data, &recs); err != nil {
		return nil, fmt.Errorf("JSON: %w", err)
	}
	return recs, nil
}

// saveRecords는 임시 파일에 쓰고 이름을 바꾼다: 쓰다가 죽어도 이전 파일이 깨지지 않는다.
func saveRecords(path string, recs []rq.AgentRecord) error {
	data, err := json.MarshalIndent(recs, "", "  ")
	if err != nil {
		return fmt.Errorf("JSON: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("디렉터리: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o644); err != nil {
		return fmt.Errorf("쓰기: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("이름 바꾸기: %w", err)
	}
	return nil
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
