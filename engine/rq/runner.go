package rq

import (
	"context"
	"errors"
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
//	Runner.Run      코어를 혼자 만지는 actor: AMI 이벤트·명령·시계·화면 요청을 한 줄로 세워 차례로 넣는다
//	 ├─ resync      FullyBooted를 받을 때마다 CoreShowChannels를 보낸다. 목록은 이벤트 스트림으로 돌아와 actor가 묶는다
//	 ├─ execute     코어가 낸 액션(Originate·Hangup·Redirect·녹음)을 AMI로 보낸다. 응답 대기가 actor를 막지 않게 따로 돈다
//	 ├─ save        상담원 업무 상태를 저장한다 (파일 쓰기가 actor를 막지 않게 따로 돈다. 밀리면 마지막 것만 쓴다)
//	 └─ HTTP        화면용 API (http.go)
type Runner struct {
	c    *ami.Client
	cfg  RunnerConfig
	core *Engine // actor 고루틴만 만진다
	log  *slog.Logger

	cmds  chan Command
	snaps chan chan string
	acts  chan Line
	saves chan []AgentRecord // 버퍼 1: 저장이 밀리면 새 것으로 바꿔 넣는다

	lists  map[string][]Chan // 받고 있는 CoreShowChannels 목록 (ActionID → 채널). actor만 만진다
	synced chan struct{}     // 첫 재동기화를 반영하면 닫는다

	// 화면 (view.go, http.go). board와 subs는 actor만 만진다
	board   *board
	subs    map[chan string]struct{}
	views   chan viewReq
	unsubs  chan chan string
	stopped chan struct{} // Run이 끝나면 닫는다
}

// RunnerConfig는 Runner 설정이다.
type RunnerConfig struct {
	Policy Policy
	Tick   time.Duration // 타이머 확인 간격. 기본 200ms
	// AgentVars는 상담원 Originate에 덧붙일 채널 변수다. 가짜 상담원(sim-agent)의 벨·통화 시간을 정할 때 쓴다.
	// 예: {"__SIM_RING": "1", "__SIM_TALK": "5"}
	AgentVars map[string]string
	// RecordDir는 녹음 파일을 둘 Asterisk 컨테이너 안의 디렉터리다. 기본 /var/spool/asterisk/monitor/rq
	// (compose가 data/asterisk/monitor로 마운트한다). 파일 이름은 <방>-<시각>.wav (시각은 Asterisk가 붙인다)
	RecordDir string
	Restore   []AgentRecord       // 시작할 때 되살릴 상담원 업무 상태 (저장해 둔 것)
	Save      func([]AgentRecord) // 업무 상태가 바뀔 때 부른다. save 고루틴에서 부르므로 오래 걸려도 된다
	Output    func(line string)   // 코어가 낸 줄(JSONL). actor 고루틴에서 부른다
	Logger    *slog.Logger
}

// viewReq는 화면의 상태 요청이다. sub가 있으면 구독자로 등록하고 이후 출력 줄을 보낸다.
type viewReq struct {
	sub   chan string
	reply chan string
}

// NewRunner는 Runner를 만든다. c.Run은 부르는 쪽이 따로 돌린다.
func NewRunner(c *ami.Client, cfg RunnerConfig) *Runner {
	if cfg.Policy == (Policy{}) {
		cfg.Policy = DefaultPolicy()
	}
	if cfg.Tick <= 0 {
		cfg.Tick = 200 * time.Millisecond
	}
	if cfg.RecordDir == "" {
		cfg.RecordDir = "/var/spool/asterisk/monitor/rq"
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	return &Runner{c: c, cfg: cfg, core: New(cfg.Policy), log: cfg.Logger,
		cmds: make(chan Command), snaps: make(chan chan string), acts: make(chan Line, 1024),
		saves: make(chan []AgentRecord, 1), lists: map[string][]Chan{}, synced: make(chan struct{}),
		board: newBoard(), subs: map[chan string]struct{}{},
		views: make(chan viewReq), unsubs: make(chan chan string), stopped: make(chan struct{})}
}

// Command는 명령을 넣는다. 여러 고루틴에서 불러도 된다.
func (r *Runner) Command(ctx context.Context, c Command) error {
	select {
	case r.cmds <- c:
		return nil
	case <-r.stopped:
		return errStopped
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Snapshot은 지금 상태(상담원·진행 중인 콜)를 JSONL 여러 줄로 돌려준다.
func (r *Runner) Snapshot(ctx context.Context) (string, error) {
	reply := make(chan string, 1)
	select {
	case r.snaps <- reply:
	case <-r.stopped:
		return "", errStopped
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

// Synced는 첫 재동기화(CoreShowChannels)를 반영하면 닫힌다. 그 전에는 누가 통화 중인지 몰라서 분배하지 않는다.
func (r *Runner) Synced() <-chan struct{} { return r.synced }

var errStopped = errors.New("rq: 엔진이 멈췄다")

// Run은 ctx가 끝나거나 AMI 클라이언트가 멈출 때까지 돈다.
func (r *Runner) Run(ctx context.Context) error {
	defer close(r.stopped)
	go r.execute(ctx)
	go r.save(ctx)
	if len(r.cfg.Restore) > 0 {
		r.emit(ctx, r.core.Restore(time.Now().UnixMicro(), r.cfg.Restore))
	}
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
				r.handleAMI(ctx, ev)
			case ami.KindConnected:
				// 끊긴 동안(또는 엔진이 꺼져 있던 동안)의 일을 모른다. FullyBooted 뒤 채널 목록으로 다시 맞출 때까지 분배를 멈춘다
				r.core.Unsync()
				r.lists = map[string][]Chan{}
				r.setConn(true, false)
			case ami.KindDisconnected:
				r.lists = map[string][]Chan{}
				r.setConn(false, false)
				r.log.Warn("AMI 끊김: 다시 연결되면 재동기화한다", "err", ev.Err)
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
		case req := <-r.views:
			if req.sub != nil {
				r.subs[req.sub] = struct{}{}
			}
			req.reply <- r.stateJSON()
		case sub := <-r.unsubs:
			r.drop(sub)
		}
	}
}

// handleAMI는 AMI 메시지 하나를 처리한다. CoreShowChannels 목록은 Complete가 온 자리에서 재동기화 입력 하나로 묶는다
// (목록이 실시간 이벤트와 받은 순서 그대로 섞여 온다: ami.Client.SendListInline).
func (r *Runner) handleAMI(ctx context.Context, ev ami.Event) {
	m := ev.Msg
	if m.IsResponse() {
		if ami.EqualFoldASCII(m.Get("EventList"), "start") {
			r.lists[m.ActionID()] = []Chan{}
		}
		return
	}
	ts := parseTS(m.Get("Timestamp"))
	if ts == 0 {
		ts = ev.Received.UnixMicro()
	}
	if items, listed := r.lists[m.ActionID()]; listed && m.ActionID() != "" {
		switch m.Event() {
		case "CoreShowChannel":
			r.lists[m.ActionID()] = append(items, toChan(m))
		case "CoreShowChannelsComplete":
			delete(r.lists, m.ActionID())
			r.emit(ctx, r.core.Resync(ts, items))
			r.setConn(true, true)
			select {
			case <-r.synced:
			default:
				close(r.synced)
			}
		}
		return
	}
	if m.Event() == "FullyBooted" {
		// 로그인 때 이미 부팅이 끝났으면 응답 바로 뒤에, 아니면 부팅이 끝나는 순간 온다.
		// 그 전에는 모듈이 덜 올라와 목록이 비어 있을 수 있다 (docs/05 6절)
		go r.resync(ctx)
	}
	if in, ok := toEvent(m); ok {
		if in.Timestamp == 0 {
			in.Timestamp = ts
		}
		r.emit(ctx, r.core.HandleEvent(in.Timestamp, in))
	}
}

func (r *Runner) resync(ctx context.Context) {
	if _, err := r.c.SendListInline(ctx, ami.NewAction("CoreShowChannels")); err != nil {
		r.log.Warn("재동기화 요청 실패", "err", err)
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
		Channel: m.Get("Channel"), Uniqueid: m.Get("Uniqueid"), Response: m.Get("Response"),
		Timestamp: parseTS(m.Get("Timestamp")),
	}, true
}

func toChan(m ami.Message) Chan {
	return Chan{Channel: m.Get("Channel"), Uniqueid: m.Get("Uniqueid"), CallerIDNum: m.Get("CallerIDNum"),
		Application: m.Get("Application"), ApplicationData: m.Get("ApplicationData"), Seconds: parseDuration(m.Get("Duration"))}
}

// emit은 코어가 낸 줄을 내보내고, 액션은 execute로, 상담원 상태가 바뀌었으면 저장을 save로 넘긴다.
func (r *Runner) emit(ctx context.Context, out []Line) {
	agentChanged := false
	for _, l := range out {
		line := EncodeLine(l)
		if r.cfg.Output != nil {
			r.cfg.Output(line)
		}
		r.board.track(l, line)
		r.broadcast(line)
		switch l.Kind {
		case "agent":
			agentChanged = true
		case "action":
			select {
			case r.acts <- l:
			case <-ctx.Done():
				return
			}
		}
	}
	if agentChanged && r.cfg.Save != nil {
		recs := r.core.Records()
		select { // 버퍼에 저장 못 한 것이 남아 있으면 버리고 새 것을 넣는다 (넣는 쪽은 actor 하나뿐이다)
		case <-r.saves:
		default:
		}
		r.saves <- recs
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

func (r *Runner) save(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case recs := <-r.saves:
			r.cfg.Save(recs)
		}
	}
}

func (r *Runner) amiAction(l Line) ami.Message {
	switch l.Action {
	case "Hangup": // Channel은 채널 이름 또는 Uniqueid (벨을 끊을 때는 ringID). AMI Hangup은 둘 다 받는다
		return ami.NewAction("Hangup").With("Channel", l.Channel)
	case "Hold":
		return redirect(l.Channel, "rq-hold")
	case "Unhold":
		return redirect(l.Channel, "rq-back")
	case "Record":
		return ami.NewAction("ConfbridgeStartRecord").With("Conference", l.Room).
			With("RecordFile", r.cfg.RecordDir+"/"+l.Room+".wav")
	}
	m := ami.NewAction("Originate").
		With("Channel", l.Iface).
		With("ChannelId", l.Channel). // 만들 채널의 Uniqueid를 엔진이 정한다 (Local이면 반대쪽은 "<값>;2")
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

// redirect는 고객 채널을 회의방에서 빼서 다른 컨텍스트로 보낸다. 끊김 처리기는 불리지 않는다 (녹화로 확인).
func redirect(channel, context string) ami.Message {
	return ami.NewAction("Redirect").With("Channel", channel).
		With("Context", context).With("Exten", "s").With("Priority", "1")
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

// parseDuration은 CoreShowChannel의 Duration("01:02:03")을 초로 바꾼다. 형식이 아니면 0.
func parseDuration(s string) int64 {
	var total int64
	for _, part := range strings.Split(s, ":") {
		n, err := strconv.ParseInt(part, 10, 64)
		if err != nil {
			return 0
		}
		total = total*60 + n
	}
	return total
}
