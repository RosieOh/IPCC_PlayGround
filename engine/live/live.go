// Package live는 상태머신(state)을 실제 Asterisk에 붙인다. 상태머신 코어는 I/O가 없고, 이 패키지가 껍데기다.
//
// 고루틴 구성 (docs/06-state-model.md 2절):
//
//	ami.Client.Run  접속·재접속·수신. 사이에 무제한 큐가 있어서 이 패키지가 늦어도 AMI 읽기 루프는 막히지 않는다
//	Runner.Run      상태머신을 혼자 만지는 고루틴(actor). AMI 이벤트·스냅샷·명령·시계를 한 줄로 세워 차례로 넣는다
//	 ├─ resync      FullyBooted를 받을 때마다 CoreShowChannels·QueueStatus를 보낸다. 결과는 이벤트 스트림으로 돌아온다
//	 └─ execute     상태머신이 낸 액션(QueuePause·QueueAdd·QueueRemove)을 AMI로 보낸다.
//	                응답을 기다리는 동안 actor가 멈추지 않게 따로 돈다
//
// 스냅샷은 ami.Client.SendListInline으로 받는다. 목록이 실시간 이벤트와 '받은 순서 그대로' 섞여서 오므로
// 녹화 재생과 같은 state.Grouper로 묶을 수 있고, 실시간과 재생의 처리 순서가 같아진다.
package live

import (
	"context"
	"log/slog"
	"strconv"
	"time"

	"ipcc/engine/ami"
	"ipcc/engine/state"
)

// Config는 Runner 설정이다. 0 값인 항목은 기본값으로 채운다.
type Config struct {
	Queue  string        // 상담원을 넣고 빼는 큐. 기본 "support"
	Policy state.Policy  // 0 값이면 state.DefaultPolicy()
	Tick   time.Duration // 타이머(후처리 끝 등)를 확인하는 간격. 기본 200ms

	// Output은 상태머신이 낸 줄(JSONL)을 받는다. actor 고루틴에서 부르므로 빨리 돌아와야 한다.
	Output func(line string)
	Logger *slog.Logger
}

// action은 Asterisk에 보낼 액션이다. 상태머신의 action 줄에서 만들고, actor가 아닌 고루틴으로 넘긴다.
type action struct {
	name, iface, member, reason string
	paused                      bool
}

// Runner는 상태머신 하나를 실제 AMI 연결에 붙여 돌린다.
type Runner struct {
	c    *ami.Client
	cfg  Config
	core *state.Engine // actor 고루틴만 만진다
	log  *slog.Logger

	cmds   chan state.Command
	snaps  chan chan string
	acts   chan action
	synced chan struct{} // 첫 QueueStatus 스냅샷을 반영하면 닫는다
}

// New는 Runner를 만든다. c.Run은 부르는 쪽이 따로 돌린다.
func New(c *ami.Client, cfg Config) *Runner {
	if cfg.Queue == "" {
		cfg.Queue = "support"
	}
	if cfg.Policy == (state.Policy{}) {
		cfg.Policy = state.DefaultPolicy()
	}
	if cfg.Tick <= 0 {
		cfg.Tick = 200 * time.Millisecond
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	return &Runner{
		c: c, cfg: cfg, core: state.New(cfg.Policy), log: cfg.Logger,
		cmds: make(chan state.Command), snaps: make(chan chan string), acts: make(chan action, 1024),
		synced: make(chan struct{}),
	}
}

// Synced는 첫 재동기화(QueueStatus 스냅샷)를 반영하면 닫힌다. 그 전에는 엔진이 상담원 상태를 모른다.
func (r *Runner) Synced() <-chan struct{} { return r.synced }

// Command는 상담원 명령(로그인·대기·이석·후처리 끝·로그아웃)을 넣는다. 여러 고루틴에서 불러도 된다.
func (r *Runner) Command(ctx context.Context, cmd state.Command) error {
	select {
	case r.cmds <- cmd:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Snapshot은 지금 상태 한 줄(JSON)이다. 여러 고루틴에서 불러도 된다.
func (r *Runner) Snapshot(ctx context.Context) (string, error) {
	reply := make(chan string, 1)
	select {
	case r.snaps <- reply:
	case <-ctx.Done():
		return "", ctx.Err()
	}
	select {
	case s := <-reply:
		return s, nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

// Run은 ctx가 끝나거나 AMI 클라이언트가 멈출 때까지 상태머신을 돌린다.
func (r *Runner) Run(ctx context.Context) error {
	go r.execute(ctx)
	tick := time.NewTicker(r.cfg.Tick)
	defer tick.Stop()

	var g state.Grouper
	events := r.c.Events()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case ev, ok := <-events:
			if !ok {
				return nil // 클라이언트의 Run이 끝났다
			}
			switch ev.Kind {
			case ami.KindConnected:
				// 끊긴 동안의 이벤트는 사라졌다. 지금 상태를 다시 받아 맞춰야 하는데(재동기화),
				// 바로 하지 않고 FullyBooted를 기다린다: Asterisk는 모듈을 다 올리기 전에도 AMI 로그인을 받아서,
				// 재시작 직후에는 QueueStatus가 "Invalid/unknown command"로 실패한다 (e2e에서 확인, docs/05 6절)
				g.Reset()
			case ami.KindDisconnected:
				g.Reset() // 받다 만 목록은 버린다
				r.log.Warn("AMI 끊김: 다시 연결되면 재동기화한다", "err", ev.Err)
			case ami.KindAMI:
				if ev.Msg.Event() == "FullyBooted" {
					// 로그인 때 이미 부팅이 끝났으면 응답 바로 뒤에, 아니면 부팅이 끝나는 순간 온다 (manager.c action_login)
					go r.resync(ctx)
				}
				if ti, ok := g.Push(ev.Msg); ok {
					r.emit(ctx, r.core.Apply(ti.At, ti.Input))
					if ti.Input.Kind == state.InputQueueSnapshot {
						r.markSynced()
					}
				}
			}
		case cmd := <-r.cmds:
			in := state.Input{Kind: state.InputCommand, Cmd: cmd}
			r.emit(ctx, r.core.Apply(time.Now().UnixMicro(), in))
		case now := <-tick.C:
			r.emit(ctx, r.core.Advance(now.UnixMicro()))
		case reply := <-r.snaps:
			reply <- r.core.Snapshot().Line()
		}
	}
}

func (r *Runner) markSynced() {
	select {
	case <-r.synced:
	default:
		close(r.synced)
	}
}

// emit은 상태머신이 낸 줄을 내보내고, 액션 줄은 execute로 넘긴다.
func (r *Runner) emit(ctx context.Context, outs []*state.Out) {
	for _, o := range outs {
		if r.cfg.Output != nil {
			r.cfg.Output(o.Line())
		}
		if o.Get("kind") != "action" {
			continue
		}
		a := action{name: o.Get("action"), iface: o.Get("iface"), reason: o.Get("reason"), paused: o.Bool("paused")}
		if ag := r.core.Agent(a.iface); ag != nil {
			a.member = ag.Name // QueueAdd의 MemberName. 상태머신은 actor만 읽을 수 있어서 여기서 꺼내 둔다
		}
		select {
		case r.acts <- a:
		case <-ctx.Done():
			return
		}
	}
}

// resync는 지금 상태를 다시 받는다. 결과(목록)는 이벤트 스트림으로 와서 Grouper가 스냅샷 입력으로 묶는다.
func (r *Runner) resync(ctx context.Context) {
	for _, act := range []ami.Message{
		ami.NewAction("CoreShowChannels"),
		ami.NewAction("QueueStatus").With("Queue", r.cfg.Queue),
	} {
		if _, err := r.c.SendListInline(ctx, act); err != nil {
			r.log.Warn("재동기화 요청 실패", "action", act.Get("Action"), "err", err)
		}
	}
}

// execute는 액션을 받은 순서대로 하나씩 보낸다. 실패하면 로그만 남긴다:
// 상태의 기준은 엔진이고, 다음 재동기화(QueueStatus 스냅샷)에서 어긋난 것을 다시 고친다.
func (r *Runner) execute(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case a := <-r.acts:
			if _, err := r.c.Send(ctx, r.amiAction(a)); err != nil {
				r.log.Warn("액션 실패", "action", a.name, "iface", a.iface, "err", err)
			}
		}
	}
}

func (r *Runner) amiAction(a action) ami.Message {
	m := ami.NewAction(a.name).With("Queue", r.cfg.Queue).With("Interface", a.iface)
	if a.name == "QueueRemove" {
		return m
	}
	m = m.With("Paused", strconv.FormatBool(a.paused))
	if a.reason != "" {
		m = m.With("Reason", a.reason)
	}
	if a.name == "QueueAdd" && a.member != "" {
		m = m.With("MemberName", a.member)
	}
	return m
}
