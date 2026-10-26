package rq

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"ipcc/engine/ami"
)

// Runner는 룸 큐 코어를 실제 AMI 연결에 붙여 돌린다 (state 패키지의 live.Runner와 같은 구조).
//
//	ami.Client.Run  접속·재접속·수신 (사이에 무제한 큐가 있어 읽기 루프는 막히지 않는다)
//	Runner.Run      코어를 혼자 만지는 actor: AMI 이벤트·명령·시계를 한 줄로 세워 차례로 넣는다
//	 └─ execute     코어가 낸 액션(Originate·Hangup)을 AMI로 보낸다. 응답 대기가 actor를 막지 않게 따로 돈다
type Runner struct {
	c    *ami.Client
	cfg  RunnerConfig
	core *Engine
	log  *slog.Logger

	cmds  chan Command
	snaps chan chan string
	acts  chan Line
}

// RunnerConfig는 Runner 설정이다.
type RunnerConfig struct {
	Policy Policy
	Tick   time.Duration // 타이머 확인 간격. 기본 200ms
	// AgentVars는 상담원 Originate에 덧붙일 채널 변수다. 가짜 상담원(sim-agent)의 벨·통화 시간을 정할 때 쓴다.
	// 예: {"__SIM_RING": "1", "__SIM_TALK": "5"}
	AgentVars map[string]string
	Output    func(line string) // 코어가 낸 줄(JSONL). actor 고루틴에서 부른다
	Logger    *slog.Logger
}

// NewRunner는 Runner를 만든다. c.Run은 부르는 쪽이 따로 돌린다.
func NewRunner(c *ami.Client, cfg RunnerConfig) *Runner {
	if cfg.Policy == (Policy{}) {
		cfg.Policy = DefaultPolicy()
	}
	if cfg.Tick <= 0 {
		cfg.Tick = 200 * time.Millisecond
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	return &Runner{c: c, cfg: cfg, core: New(cfg.Policy), log: cfg.Logger,
		cmds: make(chan Command), snaps: make(chan chan string), acts: make(chan Line, 1024)}
}

// Command는 명령을 넣는다. 여러 고루틴에서 불러도 된다.
func (r *Runner) Command(ctx context.Context, c Command) error {
	select {
	case r.cmds <- c:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Snapshot은 지금 상태(상담원·진행 중인 콜)를 JSONL 여러 줄로 돌려준다.
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

// Run은 ctx가 끝나거나 AMI 클라이언트가 멈출 때까지 돈다.
func (r *Runner) Run(ctx context.Context) error {
	go r.execute(ctx)
	tick := time.NewTicker(r.cfg.Tick)
	defer tick.Stop()
	events := r.c.Events()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case ev, ok := <-events:
			if !ok {
				return nil
			}
			switch ev.Kind {
			case ami.KindAMI:
				if in, ok := toEvent(ev.Msg); ok {
					ts := in.Timestamp
					if ts == 0 {
						ts = ev.Received.UnixMicro()
					}
					r.emit(ctx, r.core.HandleEvent(ts, in))
				}
			case ami.KindConnected:
				r.log.Info("AMI 연결: 룸 큐 엔진은 재접속 시 진행 중인 방을 다시 맞추지 않는다 (docs/12 과제)")
			case ami.KindDisconnected:
				r.log.Warn("AMI 끊김", "err", ev.Err)
			}
		case c := <-r.cmds:
			r.emit(ctx, r.core.HandleCommand(time.Now().UnixMicro(), c))
		case now := <-tick.C:
			r.emit(ctx, r.core.Advance(now.UnixMicro()))
		case reply := <-r.snaps:
			agents, calls := r.core.Snapshot()
			var b strings.Builder
			for _, l := range append(agents, calls...) {
				b.WriteString(EncodeLine(l))
				b.WriteByte('\n')
			}
			reply <- b.String()
		}
	}
}

// toEvent는 AMI 메시지에서 코어가 쓰는 필드만 뽑는다. 관심 없는 이벤트면 false.
func toEvent(m ami.Message) (Event, bool) {
	var name string
	switch m.Event() {
	case "UserEvent":
		name = m.Get("UserEvent")
		if !strings.HasPrefix(name, "RQ") {
			return Event{}, false
		}
	case "OriginateResponse":
		name = "OriginateResponse"
	default:
		return Event{}, false
	}
	return Event{
		Name: name, Room: m.Get("Room"), Agent: m.Get("Agent"), Queue: m.Get("Queue"), Customer: m.Get("Customer"),
		Channel: m.Get("Channel"), Response: m.Get("Response"), Timestamp: parseTS(m.Get("Timestamp")),
	}, true
}

func (r *Runner) emit(ctx context.Context, out []Line) {
	for _, l := range out {
		if r.cfg.Output != nil {
			r.cfg.Output(EncodeLine(l))
		}
		if l.Kind != "action" {
			continue
		}
		select {
		case r.acts <- l:
		case <-ctx.Done():
			return
		}
	}
}

// execute는 액션을 받은 순서대로 보낸다. 실패하면 로그만 남긴다:
// Originate가 실패하면 OriginateResponse(Failure)나 벨 시간 초과로 코어가 알아서 다음 상담원을 부른다.
func (r *Runner) execute(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case l := <-r.acts:
			if _, err := r.c.Send(ctx, r.amiAction(l)); err != nil {
				r.log.Warn("액션 실패", "action", l.Action, "iface", l.Iface, "channel", l.Channel, "err", err)
			}
		}
	}
}

func (r *Runner) amiAction(l Line) ami.Message {
	if l.Action == "Hangup" {
		return ami.NewAction("Hangup").With("Channel", l.Channel)
	}
	m := ami.NewAction("Originate").
		With("Channel", l.Iface).
		With("Context", "rq-agent").With("Exten", "s").With("Priority", "1").
		With("Timeout", strconv.FormatInt(r.cfg.Policy.OfferMicros/1000, 10)).
		With("CallerID", fmt.Sprintf("%q <%s>", l.Customer, l.Customer)).
		With("Variable", "RQ_ROOM="+l.Room).
		With("Variable", "RQ_AGENT="+l.Iface)
	for _, k := range sortedKeys(r.cfg.AgentVars) {
		m = m.With("Variable", k+"="+r.cfg.AgentVars[k])
	}
	return m.With("Async", "true")
}

// parseTS는 "1792983005.509553" → 마이크로초. 형식이 아니면 0.
func parseTS(s string) int64 {
	sec, frac, _ := strings.Cut(s, ".")
	n, err := strconv.ParseInt(sec, 10, 64)
	if err != nil || n <= 0 {
		return 0
	}
	frac = (frac + "000000")[:6]
	f, err := strconv.ParseInt(frac, 10, 64)
	if err != nil {
		return 0
	}
	return n*1_000_000 + f
}
