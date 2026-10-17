// Package state는 콜·상담원·큐 상태머신이다 (Stage 2, docs/06-state-model.md).
//
// Rust 레거시(legacy-rust/src/engine.rs)를 옮긴 것이고, 같은 입력에 바이트 단위로 같은 출력을 내야 한다.
// `make diff-test`가 모든 녹화에서 두 구현의 출력을 비교한다. 포팅 메모는 docs/07-rust-to-go.md 10절.
//
// 코어는 I/O가 없다. 시간도 직접 읽지 않고 입력에 붙은 시각(마이크로초)만 쓴다.
// 타이머(후처리 끝, 콜 종료 확정)는 다음 입력의 시각이 마감을 지나면 그 입력보다 먼저 처리한다.
package state

import (
	"sort"
	"strings"

	"ipcc/engine/ami"
)

// 상담원 사유 코드
const (
	ReasonACW      = "후처리"
	ReasonNoAnswer = "무응답"
	ReasonLogin    = "로그인"
	ReasonAway     = "이석"
)

// AgentState는 상담원 상태다. Rust의 enum AgentState에 해당한다.
// Go의 switch는 빠진 case를 잡아 주지 않으므로 exhaustive 린터로 막는다 (make lint).
type AgentState int

const (
	LoggedOut AgentState = iota
	NotReady
	Ready
	Ringing
	Talking
	Hold
	ACW
)

func (s AgentState) String() string {
	switch s {
	case LoggedOut:
		return "LoggedOut"
	case NotReady:
		return "NotReady"
	case Ready:
		return "Ready"
	case Ringing:
		return "Ringing"
	case Talking:
		return "Talking"
	case Hold:
		return "Hold"
	case ACW:
		return "ACW"
	}
	return "?"
}

func (s AgentState) inCall() bool { return s == Ringing || s == Talking || s == Hold }

// CallState는 콜 상태다.
type CallState int

const (
	Entered CallState = iota
	Queued
	Alerting
	Connected
	Held
	Transferred
	Ended
)

func (s CallState) String() string {
	switch s {
	case Entered:
		return "Entered"
	case Queued:
		return "Queued"
	case Alerting:
		return "Alerting"
	case Connected:
		return "Connected"
	case Held:
		return "Held"
	case Transferred:
		return "Transferred"
	case Ended:
		return "Ended"
	}
	return "?"
}

// optTime은 Rust의 Option<i64>에 해당한다 (없을 수 있는 시각). 0을 '없음'으로 쓰지 않는 이유는
// 0도 올바른 값일 수 있고, '없음'을 실수로 계산에 넣는 버그를 타입으로 드러내기 위해서다.
type optTime struct {
	at  int64
	set bool
}

func some(t int64) optTime { return optTime{at: t, set: true} }

func (o optTime) or(d int64) int64 {
	if o.set {
		return o.at
	}
	return d
}

// Agent는 상담원 한 명이다.
type Agent struct {
	Iface, Name string
	State       AgentState
	Reason      string
	Call        string // 지금 맡은 콜. 후처리 중에는 방금 끝난 콜을 그대로 둔다
	missed      int
	reserved    string // 통화 중에 누른 이석
	acwDeadline optTime
}

func newAgent(iface, name string) *Agent { return &Agent{Iface: iface, Name: name, State: LoggedOut} }

// Call은 콜 하나다. ID는 Linkedid.
type Call struct {
	ID, Queue, Customer string
	State               CallState
	Agent               string // 지금 통화 중인 상담원
	handledBy           string // 이 콜을 받은 상담원 (전환 뒤에도 남는다)
	queuedAt            optTime
	connectedAt         optTime
	completeAt          optTime
	endedAt             optTime
	finalizeDeadline    optTime // 마지막 채널이 끊겼는데 AgentComplete가 아직 없을 때 종료를 확정할 시각
	outcome             string
	channels            map[string]bool
}

func newCall(id string) *Call { return &Call{ID: id, State: Entered, channels: map[string]bool{}} }

type channel struct {
	linkedid        string
	hangupRequested bool
}

// Policy는 센터마다 다른 정책 값이다.
type Policy struct {
	ACWMicros           int64 // 후처리 시간
	MaxMissed           int   // 무응답이 이만큼 연속되면 자동 이석 (0이면 끔)
	FinalizeGraceMicros int64 // 마지막 채널이 끊긴 뒤 AgentComplete를 기다리는 시간
}

// DefaultPolicy는 Rust의 Policy::default()와 같다.
func DefaultPolicy() Policy {
	return Policy{ACWMicros: 10_000_000, MaxMissed: 2, FinalizeGraceMicros: 2_000_000}
}

// CommandKind는 상담원이 화면에서 누른 버튼이다.
type CommandKind int

const (
	CmdLogin CommandKind = iota
	CmdLogout
	CmdReady
	CmdNotReady
	CmdWrapUp
)

// Command는 상담원 명령이다. Arg는 Login이면 이름, NotReady면 사유.
type Command struct {
	Kind  CommandKind
	Iface string
	Arg   string
}

// InputKind는 입력의 종류다.
type InputKind int

const (
	InputEvent           InputKind = iota // AMI 이벤트
	InputChannelSnapshot                  // CoreShowChannels 결과
	InputQueueSnapshot                    // QueueStatus 결과
	InputCommand                          // 상담원 명령
)

// Input은 상태머신 입력 하나다. Rust의 enum Input(데이터를 품는 열거형)을 종류 + 필드로 옮겼다.
type Input struct {
	Kind  InputKind
	Msg   ami.Message   // InputEvent
	Items []ami.Message // 스냅샷
	Cmd   Command       // InputCommand
}

// Engine은 상태머신이다. 고루틴 하나만 만진다 (docs/06 2절).
type Engine struct {
	Policy   Policy
	agents   map[string]*Agent
	calls    map[string]*Call
	channels map[string]*channel
	now      int64
}

// New는 빈 상태머신을 만든다.
func New(p Policy) *Engine {
	return &Engine{Policy: p, agents: map[string]*Agent{}, calls: map[string]*Call{}, channels: map[string]*channel{}}
}

// Apply는 입력 하나를 처리한다. 시각이 거꾸로 가면 지금까지의 가장 늦은 시각으로 본다.
func (e *Engine) Apply(ts int64, in Input) []*Out {
	var out []*Out
	t := max(ts, e.now)
	e.fireTimers(t, &out)
	e.now = t
	switch in.Kind {
	case InputEvent:
		e.onEvent(in.Msg, t, &out)
	case InputChannelSnapshot:
		e.onChannelSnapshot(in.Items, t, &out)
	case InputQueueSnapshot:
		e.onQueueSnapshot(in.Items, t, &out)
	case InputCommand:
		e.onCommand(in.Cmd, t, &out)
	}
	return out
}

// Advance는 입력 없이 시간만 ts까지 흘려서 마감이 지난 타이머를 처리한다.
// 실시간 엔진(live)이 주기적으로 부른다. 재생에서는 다음 입력이 시간을 흘리므로 필요 없다.
func (e *Engine) Advance(ts int64) []*Out {
	var out []*Out
	t := max(ts, e.now)
	e.fireTimers(t, &out)
	e.now = t
	return out
}

// Flush는 남은 타이머를 모두 처리한다 (녹화 재생의 끝에서 시간을 끝까지 흘린다).
func (e *Engine) Flush() []*Out {
	var out []*Out
	e.fireTimers(maxInt64, &out)
	return out
}

const maxInt64 = int64(^uint64(0) >> 1)

// Snapshot은 지금 상태를 한 줄로 낸다. 목록은 ID 순서다 (Go 맵 순서는 무작위라 반드시 정렬한다).
func (e *Engine) Snapshot() *Out {
	var agents, calls []string
	for _, id := range sortedKeys(e.agents) {
		a := e.agents[id]
		agents = append(agents, newOut().S("id", a.Iface).S("name", a.Name).S("state", a.State.String()).
			S("reason", a.Reason).S("call", a.Call).Line())
	}
	for _, id := range sortedKeys(e.calls) {
		c := e.calls[id]
		calls = append(calls, newOut().S("id", c.ID).S("state", c.State.String()).S("queue", c.Queue).
			S("customer", c.Customer).S("agent", c.Agent).Line())
	}
	return newOut().S("kind", "snapshot").
		Raw("agents", "["+strings.Join(agents, ",")+"]").
		Raw("calls", "["+strings.Join(calls, ",")+"]")
}

// Agent는 상담원 하나의 상태다 (테스트·엔진 껍데기용). 없으면 nil.
func (e *Engine) Agent(iface string) *Agent { return e.agents[iface] }

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// ── 타이머 ────────────────────────────────────────────────────────────────

type timer struct {
	deadline int64
	kind     int // 0 = 후처리 끝, 1 = 콜 종료 확정
	id       string
}

func (a timer) less(b timer) bool {
	if a.deadline != b.deadline {
		return a.deadline < b.deadline
	}
	if a.kind != b.kind {
		return a.kind < b.kind
	}
	return a.id < b.id
}

// fireTimers는 마감이 until 이하인 타이머를 (마감, 종류, ID) 순서로 하나씩 처리한다.
// 처리하면서 새 타이머가 생길 수 있어서 매번 다시 찾는다.
func (e *Engine) fireTimers(until int64, out *[]*Out) {
	for {
		var next *timer
		consider := func(c timer) {
			if c.deadline <= until && (next == nil || c.less(*next)) {
				next = &c
			}
		}
		for _, id := range sortedKeys(e.agents) {
			if d := e.agents[id].acwDeadline; d.set {
				consider(timer{d.at, 0, id})
			}
		}
		for _, id := range sortedKeys(e.calls) {
			if d := e.calls[id].finalizeDeadline; d.set {
				consider(timer{d.at, 1, id})
			}
		}
		if next == nil {
			return
		}
		if next.deadline > e.now {
			e.now = next.deadline
		}
		t := e.now
		if next.kind == 0 {
			e.endACW(next.id, t, out)
		} else {
			e.finalizeExpired(next.id, t, out)
		}
	}
}

// ── 이벤트 ────────────────────────────────────────────────────────────────

func (e *Engine) onEvent(m ami.Message, t int64, out *[]*Out) {
	switch m.Event() {
	case "Newchannel":
		e.addChannel(m.Get("Uniqueid"), m.Get("Linkedid"), m.Get("CallerIDNum"), t, out)
	case "NewCallerid":
		e.updateCustomer(m.Get("Uniqueid"), m.Get("Linkedid"), m.Get("CallerIDNum"))
	case "HangupRequest", "SoftHangupRequest":
		if ch := e.channels[m.Get("Uniqueid")]; ch != nil {
			ch.hangupRequested = true
		}
	case "Hangup":
		e.removeChannel(m.Get("Uniqueid"), t, out)
	case "QueueCallerJoin":
		e.onQueueJoin(m, t, out)
	case "QueueCallerAbandon":
		e.onAbandon(m, t, out)
	case "AgentCalled":
		e.onAgentCalled(m, t, out)
	case "AgentRingNoAnswer":
		e.onRingNoAnswer(m, t, out)
	case "AgentConnect":
		e.onAgentConnect(m, t, out)
	case "AgentComplete":
		e.onAgentComplete(m, t, out)
	case "Hold":
		e.onHold(m.Get("Linkedid"), true, t, out)
	case "Unhold":
		e.onHold(m.Get("Linkedid"), false, t, out)
	case "AttendedTransfer":
		e.onAttendedTransfer(m, t, out)
	case "QueueMemberAdded":
		e.memberSeen(m.Get("Interface"), m.Get("MemberName"), m.Get("Paused") == "1", m.Get("PausedReason"), t, out)
	case "QueueMemberRemoved":
		e.logout(m.Get("Interface"), t, out)
	case "QueueMemberPause":
		e.onMemberPause(m.Get("Interface"), m.Get("Paused") == "1", m.Get("PausedReason"), t, out)
	}
}

func (e *Engine) addChannel(uid, lid, num string, t int64, out *[]*Out) {
	if uid == "" || lid == "" {
		return
	}
	e.channels[uid] = &channel{linkedid: lid}
	c, ok := e.calls[lid]
	if !ok {
		c = newCall(lid)
		e.calls[lid] = c
	}
	c.channels[uid] = true
	if uid == lid && c.Customer == "" && validNum(num) {
		c.Customer = num
	}
	if !ok {
		*out = append(*out, callLine(t, c, ""))
	}
}

// updateCustomer는 콜을 만든 채널(Uniqueid = Linkedid)의 번호가 정해지면 고객 번호로 쓴다.
func (e *Engine) updateCustomer(uid, lid, num string) {
	if uid == "" || uid != lid || !validNum(num) {
		return
	}
	if c := e.calls[lid]; c != nil && c.Customer == "" {
		c.Customer = num
	}
}

func (e *Engine) removeChannel(uid string, t int64, out *[]*Out) {
	ch, ok := e.channels[uid]
	if !ok {
		return
	}
	delete(e.channels, uid)
	c := e.calls[ch.linkedid]
	if c == nil {
		return
	}
	delete(c.channels, uid)
	if len(c.channels) == 0 {
		e.lastChannelGone(ch.linkedid, t, out)
	}
}

// lastChannelGone: 콜의 마지막 채널이 끊겼다. AgentComplete가 아직 안 왔으면 잠깐 기다린다 (Hangup보다 늦게 올 수 있다).
func (e *Engine) lastChannelGone(lid string, t int64, out *[]*Out) {
	c := e.calls[lid]
	if c == nil {
		return
	}
	c.endedAt = some(t)
	if c.Agent != "" && !c.completeAt.set {
		if a := e.agents[c.Agent]; a != nil && (a.State == Talking || a.State == Hold) && a.Call == lid {
			c.finalizeDeadline = some(t + e.Policy.FinalizeGraceMicros)
			return
		}
	}
	// 이 콜로 벨이 울리던 상담원이 있으면 되돌린다 (콜이 사라졌다)
	for _, id := range sortedKeys(e.agents) {
		if a := e.agents[id]; a.State == Ringing && a.Call == lid {
			*out = append(*out, warnLine(t, "ring_ended_without_event", id))
			e.backFromRing(id, t, out)
		}
	}
	e.finalizeCall(lid, t, out)
}

func (e *Engine) finalizeExpired(lid string, t int64, out *[]*Out) {
	c := e.calls[lid]
	if c == nil {
		return
	}
	if a := e.agents[c.Agent]; a != nil && (a.State == Talking || a.State == Hold) && a.Call == lid {
		*out = append(*out, warnLine(t, "complete_missing", c.Agent))
		e.enterACW(c.Agent, t, out)
	}
	e.finalizeCall(lid, t, out)
}

func (e *Engine) finalizeCall(lid string, t int64, out *[]*Out) {
	c := e.calls[lid]
	if c == nil {
		return
	}
	delete(e.calls, lid)
	from := c.State
	c.State = Ended
	*out = append(*out, callLine(t, c, from.String()))

	outcome := c.outcome
	switch {
	case outcome != "":
	case c.connectedAt.set:
		outcome = "completed"
	case c.queuedAt.set:
		outcome = "left_queue"
	default:
		outcome = "no_queue"
	}
	end := c.endedAt.or(t)
	var waitMS, talkMS int64
	if c.queuedAt.set {
		waitMS = (c.connectedAt.or(end) - c.queuedAt.at) / 1000
	}
	if c.connectedAt.set {
		talkMS = (c.completeAt.or(end) - c.connectedAt.at) / 1000
	}
	*out = append(*out, newOut().S("t", fmtTS(t)).S("kind", "call_end").S("id", c.ID).S("outcome", outcome).
		S("queue", c.Queue).S("customer", c.Customer).S("agent", c.handledBy).I("wait_ms", waitMS).I("talk_ms", talkMS))
}

func (e *Engine) onQueueJoin(m ami.Message, t int64, out *[]*Out) {
	lid := m.Get("Linkedid")
	c := e.calls[lid]
	if c == nil {
		return
	}
	c.Queue = m.Get("Queue")
	if num := m.Get("CallerIDNum"); validNum(num) {
		c.Customer = num
	}
	c.queuedAt = some(t)
	e.setCall(lid, Queued, t, out)
}

// onAbandon: QueueCallerAbandon은 큐 타임아웃 때도 온다 (app_queue가 record_abandoned를 부른다).
// 고객 채널에 끊기 요청이 먼저 있었으면 포기, 없었으면 타임아웃이다.
//
// 벨이 울리던 상담원은 대기로 돌린다. 벨 중에 고객이 끊으면 상담원 쪽 이벤트(AgentRingNoAnswer 등)는
// 오지 않는다 (testdata/ring-abandon.ami). 상담원 잘못이 아니므로 무응답 횟수는 세지 않는다.
func (e *Engine) onAbandon(m ami.Message, t int64, out *[]*Out) {
	lid := m.Get("Linkedid")
	callerHungUp := true
	if ch := e.channels[m.Get("Uniqueid")]; ch != nil {
		callerHungUp = ch.hangupRequested
	}
	if c := e.calls[lid]; c != nil {
		if callerHungUp {
			c.outcome = "abandoned"
		} else {
			c.outcome = "timeout"
		}
	}
	for _, id := range sortedKeys(e.agents) {
		if a := e.agents[id]; a.State == Ringing && a.Call == lid {
			e.backFromRing(id, t, out)
		}
	}
}

func (e *Engine) onAgentCalled(m ami.Message, t int64, out *[]*Out) {
	iface := m.Get("Interface")
	if iface == "" {
		return
	}
	e.ensureAgent(iface, m.Get("MemberName"), t, out)
	lid := m.Get("Linkedid")
	a := e.agents[iface]
	switch a.State {
	case ACW:
		*out = append(*out, warnLine(t, "called_during_acw", iface))
		a.acwDeadline = optTime{}
	case NotReady:
		*out = append(*out, warnLine(t, "called_while_not_ready", iface))
	case LoggedOut, Ready, Ringing, Talking, Hold:
	}
	e.setAgent(iface, Ringing, "", lid, t, out)
	if c := e.calls[lid]; c != nil && c.State == Queued {
		e.setCall(lid, Alerting, t, out)
	}
}

func (e *Engine) onRingNoAnswer(m ami.Message, t int64, out *[]*Out) {
	iface := m.Get("Interface")
	a := e.agents[iface]
	if a == nil {
		*out = append(*out, warnLine(t, "unknown_agent", iface))
		return
	}
	if a.State != Ringing {
		return
	}
	lid := a.Call
	a.missed++
	if e.Policy.MaxMissed > 0 && a.missed >= e.Policy.MaxMissed {
		a.missed = 0
		e.goNotReady(iface, ReasonNoAnswer, t, out)
	} else {
		e.backFromRing(iface, t, out)
	}
	if c := e.calls[lid]; c != nil && c.State == Alerting {
		e.setCall(lid, Queued, t, out)
	}
}

func (e *Engine) onAgentConnect(m ami.Message, t int64, out *[]*Out) {
	iface := m.Get("Interface")
	if iface == "" {
		return
	}
	e.ensureAgent(iface, m.Get("MemberName"), t, out)
	a := e.agents[iface]
	lid := m.Get("Linkedid")
	if lid == "" {
		lid = a.Call
	}
	a.missed = 0
	a.acwDeadline = optTime{}
	e.setAgent(iface, Talking, "", lid, t, out)
	if c := e.calls[lid]; c != nil {
		c.Agent = iface
		c.handledBy = iface
		c.connectedAt = some(t)
	}
	e.setCall(lid, Connected, t, out)
}

// onAgentComplete: AgentComplete는 채널 정보 없이 올 수 있다. 상담원(Interface)으로 찾고, 콜은 그 상담원의 Call로 찾는다.
func (e *Engine) onAgentComplete(m ami.Message, t int64, out *[]*Out) {
	iface := m.Get("Interface")
	a := e.agents[iface]
	if a == nil {
		*out = append(*out, warnLine(t, "unknown_agent", iface))
		return
	}
	lid := a.Call
	if a.State == Talking || a.State == Hold {
		e.enterACW(iface, t, out)
	} else {
		*out = append(*out, warnLine(t, "complete_unexpected", iface))
	}
	reason := m.Get("Reason")
	finalize, transferred := false, false
	if c := e.calls[lid]; c != nil {
		c.completeAt = some(t)
		if reason == "" {
			c.outcome = "completed"
		} else {
			c.outcome = "completed:" + reason
		}
		finalize = c.finalizeDeadline.set
		if reason == "transfer" && !finalize {
			c.Agent = ""
			transferred = true
		}
	}
	if transferred {
		e.setCall(lid, Transferred, t, out)
	}
	if finalize {
		e.finalizeCall(lid, t, out)
	}
}

// onHold: Hold/Unhold는 보류를 건 쪽(상담원 채널)에 온다. 콜의 상담원이 통화 중일 때만 반영한다.
// 블라인드 전환 때는 Hold 없이 Unhold만 오기도 해서, 보류 중이 아니면 무시한다.
func (e *Engine) onHold(lid string, hold bool, t int64, out *[]*Out) {
	c := e.calls[lid]
	if c == nil || c.Agent == "" {
		return
	}
	a := e.agents[c.Agent]
	if a == nil {
		return
	}
	switch {
	case hold && a.State == Talking:
		e.setAgent(c.Agent, Hold, "", lid, t, out)
		e.setCall(lid, Held, t, out)
	case !hold && a.State == Hold:
		e.setAgent(c.Agent, Talking, "", lid, t, out)
		e.setCall(lid, Connected, t, out)
	}
}

// onAttendedTransfer: 어텐디드 전환 중에 *3(atxferthreeway)을 누르면 3자통화가 되고 AttendedTransfer DestType=Threeway가 온다.
// 전환을 시작할 때 온 Hold의 짝인 Unhold는 오지 않으므로 이 이벤트로 보류를 푼다 (testdata/three-way.ami).
// 일반 어텐디드 전환(DestType=Bridge)은 상담원이 빠지면서 AgentComplete Reason=transfer로 끝난다.
func (e *Engine) onAttendedTransfer(m ami.Message, t int64, out *[]*Out) {
	if m.Get("Result") == "Success" && m.Get("DestType") == "Threeway" {
		e.onHold(m.Get("OrigTransfererLinkedid"), false, t, out)
	}
}

// memberSeen: QueueMemberAdded 또는 스냅샷에서 상담원을 본다. 엔진이 모르는 로그인이면 Asterisk 쪽 상태를 따른다.
func (e *Engine) memberSeen(iface, name string, paused bool, reason string, t int64, out *[]*Out) {
	if iface == "" {
		return
	}
	a := e.agents[iface]
	if a == nil {
		a = newAgent(iface, name)
		e.agents[iface] = a
	}
	if name != "" {
		a.Name = name
	}
	if a.State != LoggedOut {
		return
	}
	if paused {
		if reason == "" {
			reason = ReasonLogin
		}
		e.setAgent(iface, NotReady, reason, "", t, out)
	} else {
		e.setAgent(iface, Ready, "", "", t, out)
	}
}

func (e *Engine) logout(iface string, t int64, out *[]*Out) {
	a := e.agents[iface]
	if a == nil || a.State == LoggedOut {
		return
	}
	if a.State.inCall() {
		*out = append(*out, warnLine(t, "logout_in_call", iface))
	}
	a.acwDeadline = optTime{}
	a.reserved = ""
	a.missed = 0
	e.setAgent(iface, LoggedOut, "", "", t, out)
}

// onMemberPause: Asterisk 쪽의 이석/대기 변화. 엔진이 보낸 QueuePause의 메아리면 상태가 이미 같아서 아무 일도 없다.
func (e *Engine) onMemberPause(iface string, paused bool, reason string, t int64, out *[]*Out) {
	a := e.agents[iface]
	if a == nil {
		return
	}
	if paused {
		if reason == "" {
			reason = ReasonAway
		}
		switch a.State {
		case Ready, NotReady:
			e.setAgent(iface, NotReady, reason, "", t, out)
		case Ringing, Talking, Hold:
			a.reserved = reason
		case ACW:
			if reason != ReasonACW {
				a.reserved = reason
			}
		case LoggedOut:
		}
		return
	}
	switch a.State {
	case NotReady:
		e.setAgent(iface, Ready, "", "", t, out)
	case ACW:
		a.acwDeadline = optTime{}
		a.reserved = ""
		e.setAgent(iface, Ready, "", "", t, out)
	case Ringing, Talking, Hold:
		a.reserved = ""
	case Ready, LoggedOut:
	}
}

// ── 스냅샷 (재동기화) ─────────────────────────────────────────────────────

// onChannelSnapshot은 CoreShowChannels 결과와 메모리의 채널을 맞춘다.
func (e *Engine) onChannelSnapshot(items []ami.Message, t int64, out *[]*Out) {
	seen := map[string]bool{}
	for _, m := range items {
		if uid := m.Get("Uniqueid"); uid != "" {
			seen[uid] = true
		}
	}
	for _, uid := range sortedKeys(e.channels) {
		if !seen[uid] {
			*out = append(*out, warnLine(t, "resync_lost_channel", uid))
			e.removeChannel(uid, t, out)
		}
	}
	for _, m := range items {
		uid := m.Get("Uniqueid")
		if uid == "" || e.channels[uid] != nil {
			continue
		}
		*out = append(*out, warnLine(t, "resync_new_channel", uid))
		e.addChannel(uid, m.Get("Linkedid"), m.Get("CallerIDNum"), t, out)
	}
}

// onQueueSnapshot은 QueueStatus 결과와 상담원을 맞춘다. 상담원 상태의 기준은 엔진이라, 어긋나면 Asterisk 쪽을 고친다.
func (e *Engine) onQueueSnapshot(items []ami.Message, t int64, out *[]*Out) {
	members := map[string]ami.Message{}
	for _, m := range items {
		// 스냅샷의 QueueMember는 실시간 이벤트와 필드 이름이 다르다 (Name/Location)
		if m.Event() == "QueueMember" && m.Get("Location") != "" {
			members[m.Get("Location")] = m
		}
	}
	for _, id := range sortedKeys(e.agents) {
		a := e.agents[id]
		if _, ok := members[id]; ok || a.State == LoggedOut {
			continue
		}
		*out = append(*out, warnLine(t, "resync_readd", id))
		*out = append(*out, actionLine(t, "QueueAdd", id, a.State == NotReady || a.State == ACW, a.Reason))
	}
	for _, iface := range sortedKeys(members) {
		m := members[iface]
		a := e.agents[iface]
		if a == nil || a.State == LoggedOut {
			*out = append(*out, warnLine(t, "resync_adopt", iface))
			e.memberSeen(iface, m.Get("Name"), m.Get("Paused") == "1", m.Get("PausedReason"), t, out)
			continue
		}
		if a.State.inCall() {
			continue
		}
		want := a.State == NotReady || a.State == ACW
		if want != (m.Get("Paused") == "1") {
			reason := ""
			if want {
				reason = a.Reason
			}
			*out = append(*out, warnLine(t, "resync_pause", iface))
			*out = append(*out, actionLine(t, "QueuePause", iface, want, reason))
		}
	}
}

// ── 명령 ──────────────────────────────────────────────────────────────────

func (e *Engine) onCommand(c Command, t int64, out *[]*Out) {
	iface := c.Iface
	a := e.agents[iface]
	switch c.Kind {
	case CmdLogin:
		if a == nil {
			a = newAgent(iface, c.Arg)
			e.agents[iface] = a
		}
		if c.Arg != "" {
			a.Name = c.Arg
		}
		if a.State != LoggedOut {
			return
		}
		e.setAgent(iface, NotReady, ReasonLogin, "", t, out)
		*out = append(*out, actionLine(t, "QueueAdd", iface, true, ReasonLogin))
	case CmdLogout:
		if a == nil || a.State == LoggedOut {
			return
		}
		e.logout(iface, t, out)
		*out = append(*out, actionLine(t, "QueueRemove", iface, false, ""))
	case CmdReady:
		if a == nil {
			return
		}
		switch a.State {
		case NotReady, ACW:
			a.acwDeadline = optTime{}
			a.reserved = ""
			e.setAgent(iface, Ready, "", "", t, out)
			*out = append(*out, actionLine(t, "QueuePause", iface, false, ""))
		case Ringing, Talking, Hold:
			a.reserved = ""
		case LoggedOut, Ready:
		}
	case CmdNotReady:
		if a == nil {
			return
		}
		reason := c.Arg
		if reason == "" {
			reason = ReasonAway
		}
		switch a.State {
		case Ready, NotReady, ACW:
			e.goNotReady(iface, reason, t, out)
		case Ringing, Talking, Hold:
			// 통화를 끊지 않는다: 이석을 예약해 두었다가 통화·후처리가 끝나면 이석으로 간다
			a.reserved = reason
		case LoggedOut:
		}
	case CmdWrapUp:
		e.endACW(iface, t, out)
	}
}

// ── 상태 바꾸기 ───────────────────────────────────────────────────────────

// ensureAgent: 엔진이 모르는 상담원이 큐 이벤트에 나오면 대기 상태로 만든다 (녹화가 로그인 뒤에 시작된 경우 등).
func (e *Engine) ensureAgent(iface, name string, t int64, out *[]*Out) {
	a := e.agents[iface]
	if a == nil {
		a = newAgent(iface, name)
		e.agents[iface] = a
	}
	if a.Name == "" {
		a.Name = name
	}
	if a.State == LoggedOut {
		a.State = Ready
		*out = append(*out, warnLine(t, "unknown_agent", iface))
	}
}

func (e *Engine) setAgent(iface string, s AgentState, reason, call string, t int64, out *[]*Out) {
	a := e.agents[iface]
	if a == nil {
		return
	}
	from := a.State
	changed := from != s || a.Reason != reason
	a.State, a.Reason, a.Call = s, reason, call
	if changed {
		*out = append(*out, newOut().S("t", fmtTS(t)).S("kind", "agent").S("id", a.Iface).S("name", a.Name).
			S("from", from.String()).S("to", a.State.String()).S("reason", a.Reason).S("call", a.Call))
	}
}

func (e *Engine) setCall(id string, s CallState, t int64, out *[]*Out) {
	c := e.calls[id]
	if c == nil || c.State == s {
		return
	}
	from := c.State
	c.State = s
	*out = append(*out, callLine(t, c, from.String()))
}

// enterACW: Rust는 self.agents[iface]로 꺼내서 없으면 panic하지만(부르는 쪽이 늘 있는 상담원만 넘긴다),
// Go 라이브러리 코드는 panic하지 않는다는 규칙이라 nil이면 그냥 돌아간다.
func (e *Engine) enterACW(iface string, t int64, out *[]*Out) {
	a := e.agents[iface]
	if a == nil {
		return
	}
	e.setAgent(iface, ACW, ReasonACW, a.Call, t, out)
	a.acwDeadline = some(t + e.Policy.ACWMicros)
	*out = append(*out, actionLine(t, "QueuePause", iface, true, ReasonACW))
}

// endACW: 후처리가 끝났다 (타이머 또는 상담원 버튼). 예약된 이석이 있으면 이석으로, 없으면 대기로.
func (e *Engine) endACW(iface string, t int64, out *[]*Out) {
	a := e.agents[iface]
	if a == nil {
		return
	}
	a.acwDeadline = optTime{}
	if a.State != ACW {
		return
	}
	reserved := a.reserved
	a.reserved = ""
	if reserved == "" {
		e.setAgent(iface, Ready, "", "", t, out)
		*out = append(*out, actionLine(t, "QueuePause", iface, false, ""))
	} else {
		e.goNotReady(iface, reserved, t, out)
	}
}

// backFromRing: 벨이 울리다 끝났다 (무응답, 콜 사라짐). 예약된 이석이 있으면 이석으로.
func (e *Engine) backFromRing(iface string, t int64, out *[]*Out) {
	a := e.agents[iface]
	if a == nil {
		return
	}
	reserved := a.reserved
	a.reserved = ""
	if reserved == "" {
		e.setAgent(iface, Ready, "", "", t, out)
	} else {
		e.goNotReady(iface, reserved, t, out)
	}
}

func (e *Engine) goNotReady(iface, reason string, t int64, out *[]*Out) {
	a := e.agents[iface]
	if a == nil || (a.State == NotReady && a.Reason == reason) {
		return
	}
	a.acwDeadline = optTime{}
	a.reserved = ""
	e.setAgent(iface, NotReady, reason, "", t, out)
	*out = append(*out, actionLine(t, "QueuePause", iface, true, reason))
}

func validNum(n string) bool { return n != "" && n != "<unknown>" }

func callLine(t int64, c *Call, from string) *Out {
	return newOut().S("t", fmtTS(t)).S("kind", "call").S("id", c.ID).S("from", from).S("to", c.State.String()).
		S("queue", c.Queue).S("customer", c.Customer).S("agent", c.Agent)
}

func actionLine(t int64, name, iface string, paused bool, reason string) *Out {
	return newOut().S("t", fmtTS(t)).S("kind", "action").S("action", name).S("iface", iface).
		B("paused", paused).S("reason", reason)
}

func warnLine(t int64, code, id string) *Out {
	return newOut().S("t", fmtTS(t)).S("kind", "warn").S("code", code).S("id", id)
}
