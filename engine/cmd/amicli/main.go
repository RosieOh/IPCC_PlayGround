// amicli는 AMI를 직접 만져보는 학습용 CLI다.
//
// Stage 2 이후의 CTI 엔진도 ami 패키지를 그대로 쓴다. 이 CLI는 그 사용 예이기도 하다.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"ipcc/engine/ami"
)

const usage = `amicli — AMI 학습용 CLI

사용법 (옵션은 위치 인자보다 앞에):
  amicli watch [-record DIR] [-json] [-all] [-only E1,E2] [-v]
      이벤트를 실시간으로 본다. 끊기면 재접속하고, 접속할 때마다 현재 상태(스냅샷)를 다시 받는다.
      수신 원본은 -record 디렉터리(기본 recordings)에 녹화된다.

  amicli action [-json] <Action> [Key=Value ...]
      액션 하나를 보내고 응답(목록형이면 목록까지)을 본다.
      예) amicli action QueueStatus
          amicli action Command Command="core show channels"

  amicli replay [-json] [-all] [-only E1,E2] [-v] <녹화파일>
      watch가 녹화한 원본 스트림을 다시 읽는다.

  amicli stats [-fields] [-md] <녹화파일 ...>
      녹화를 집계한다: 이벤트 종류별 개수, 콜(Linkedid)별 이벤트 수, 이벤트마다 본 필드(-fields).
      -md는 이벤트 사전(docs/10-event-dictionary.md) 형식으로 낸다.

  amicli agent <login|logout|pause|unpause> [-sim] [-queue Q] [-reason R] [내선 ...]
      상담원 큐 로그인/로그아웃/이석/대기 (내선 생략 시 1001 1002 1003).
      -sim이면 가짜 상담원(Local/<내선>@sim-agent)을 다룬다.

  amicli sim-call [-n N] [-gap 2s] [-patience 300] [-talk 0]
      가짜 고객 콜을 대표번호로 인입시킨다. -patience를 짧게 주면 대기 중 포기(abandon)가 된다.

공통 옵션: -addr (AMI_ADDR, 기본 127.0.0.1:5038) -user (AMI_USER) -secret (AMI_SECRET)
          -md5 (챌린지 방식 로그인, 비밀번호를 보내지 않는다) -debug
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var err error
	switch cmd, args := os.Args[1], os.Args[2:]; cmd {
	case "watch":
		err = runWatch(ctx, args)
	case "action":
		err = runAction(ctx, args)
	case "replay":
		err = runReplay(args)
	case "stats":
		err = runStats(args)
	case "agent":
		err = runAgent(ctx, args)
	case "sim-call":
		err = runSimCall(ctx, args)
	case "help", "-h", "--help":
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "알 수 없는 명령 %q\n\n%s", cmd, usage)
		os.Exit(2)
	}
	if err != nil && !errors.Is(err, context.Canceled) {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

// connFlags는 모든 명령이 쓰는 접속 옵션이다.
type connFlags struct {
	addr, user, secret string
	md5, debug         bool
}

func addConnFlags(fs *flag.FlagSet) *connFlags {
	o := &connFlags{}
	fs.StringVar(&o.addr, "addr", envOr("AMI_ADDR", "127.0.0.1:5038"), "AMI 주소")
	fs.StringVar(&o.user, "user", envOr("AMI_USER", "ipcc"), "AMI 사용자 (asterisk/conf/manager.conf)")
	fs.StringVar(&o.secret, "secret", envOr("AMI_SECRET", "ipcc-secret"), "AMI 비밀번호")
	fs.BoolVar(&o.md5, "md5", false, "MD5 챌린지 방식으로 로그인 (비밀번호를 그대로 보내지 않는다)")
	fs.BoolVar(&o.debug, "debug", false, "디버그 로그 (보내는 액션까지 표시)")
	return o
}

// config는 ami.Config를 만든다. quiet이면 경고 이상만 로그로 남긴다.
func (o *connFlags) config(events string, quiet bool) ami.Config {
	level := slog.LevelInfo
	if quiet {
		level = slog.LevelWarn
	}
	if o.debug {
		level = slog.LevelDebug
	}
	return ami.Config{
		Addr:     o.addr,
		Username: o.user,
		Secret:   o.secret,
		AuthMD5:  o.md5,
		Events:   events,
		Logger:   slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})),
	}
}

// connectOnce는 일회성 명령용이다. 로그인까지 마치면 Client와 정리 함수를 돌려준다.
// 이벤트는 받지 않는다(Events: off).
func connectOnce(ctx context.Context, o *connFlags) (*ami.Client, func(), error) {
	cfg := o.config("off", true)
	cfg.BackoffMax = time.Second
	c := ami.New(cfg)

	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = c.Run(runCtx)
	}()
	stop := func() {
		cancel()
		<-done
	}

	timeout := time.NewTimer(5 * time.Second)
	defer timeout.Stop()
	for {
		select {
		case ev, ok := <-c.Events():
			if !ok {
				stop()
				return nil, nil, context.Canceled
			}
			if ev.Kind == ami.KindConnected {
				go func() { // 이후 항목은 쓰지 않으므로 비워 준다
					for range c.Events() {
					}
				}()
				return c, stop, nil
			}
		case <-timeout.C:
			stop()
			return nil, nil, fmt.Errorf("%s 에 5초 안에 로그인하지 못함 — `make up`을 했는지, manager.conf 계정이 맞는지 확인", o.addr)
		case <-ctx.Done():
			stop()
			return nil, nil, ctx.Err()
		}
	}
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
