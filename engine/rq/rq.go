// Package rq는 "룸 큐"의 상태머신 코어다: app_queue 없이 엔진이 직접 대기열을 갖고 상담원을 고른다 (docs/12-room-queue.md).
//
//   - 고객은 통화마다 생기는 회의방(ConfBridge, 이름 rq-<Linkedid>)에서 대기음을 들으며 기다린다.
//   - 엔진이 상담원을 골라 AMI Originate로 같은 방에 부른다. 상담원을 둘 부르면 3자통화, 한 명을 빼면 전환이다.
//   - dialplan은 상태를 저장하지 않고 UserEvent(RQEnter, RQAgentIn, RQAgentMissed, RQAgentOut, RQCustomerOut)로
//     일어난 일만 알린다. 상태의 주인은 이 코어 하나다.
//   - 상담원 상태는 두 축이다: 업무 모드(Mode: 로그아웃·대기·이석·후처리)와 통화 단계(Phase: 없음·벨·통화).
//     콜은 Mode=Inbound이고 Phase=Idle인 상담원에게만 간다.
//
// Stage 2의 state 패키지(app_queue 이벤트 기반, Rust 레거시와 출력 계약)와는 별개다.
// 코어는 I/O가 없고 시간도 입력에 붙은 시각(마이크로초)만 쓴다.
package rq

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Mode는 상담원이 선언한 업무 상태다.
type Mode int

const (
	Logout   Mode = iota
	Inbound       // 대기: 콜을 받는다
	NotReady      // 이석 (사유)
	ACW           // 후처리: 시간이 지나면 Inbound로
)

func (m Mode) String() string {
	switch m {
	case Logout:
		return "Logout"
	case Inbound:
		return "Inbound"
	case NotReady:
		return "NotReady"
	case ACW:
		return "ACW"
	}
	return "?"
}

// Phase는 상담원의 통화 단계다. 교환기에서 일어난 일(UserEvent·OriginateResponse)로만 바뀐다.
type Phase int

const (
	Idle Phase = iota
	Ring       // 엔진이 Originate로 부르는 중
	Talk       // 방에 들어와 있음
)

func (p Phase) String() string {
	switch p {
	case Idle:
		return "Idle"
	case Ring:
		return "Ring"
	case Talk:
		return "Talk"
	}
	return "?"
}

// CallState는 콜(방)의 상태다.
type CallState int

const (
	Waiting  CallState = iota // 고객 혼자 방에서 대기음
	Offering                  // 엔진이 상담원을 부르는 중
	Talking                   // 상담원이 방에 있음
	Ended
)

func (s CallState) String() string {
	switch s {
	case Waiting:
		return "Waiting"
	case Offering:
		return "Offering"
	case Talking:
		return "Talking"
	case Ended:
		return "Ended"
	}
	return "?"
}

// Agent는 상담원 한 명이다.
type Agent struct {
	Iface, Name string
	Mode        Mode
	Reason      string
	Phase       Phase
	Room        string // 지금 부르는 중이거나 들어가 있는 방 (후처리 중에는 방금 끝난 방)
	channel     string // 방에 들어온 상담원 채널 (전환 때 엔진이 끊는다)
	invite      string // 이미 통화 중인 방으로 부른 이유: "conference" 또는 "transfer:<원래 상담원>"
	missed      int
	idleSince   int64 // 가장 오래 쉰 상담원부터 고른다
	offerUntil  int64 // 이때까지 응답(OriginateResponse·RQAgentIn)이 없으면 무응답으로 본다
	acwUntil    int64
}

// Call은 콜 하나(=회의방 하나)다.
type Call struct {
	ID, Queue, Customer string
	State               CallState
	offered             string          // 지금 부르고 있는 상담원
	agents              map[string]bool // 방에 들어와 있는 상담원
	handledBy           string          // 처음 받은 상담원
	enteredAt           int64
	answeredAt          int64
}

// Policy는 센터마다 다른 정책 값이다.
type Policy struct {
	ACWMicros     int64 // 후처리 시간
	MaxMissed     int   // 무응답이 이만큼 연속되면 자동 이석 (0이면 끔)
	OfferMicros   int64 // 상담원 벨 시간 (Originate Timeout)
	OfferGraceMic int64 // 벨 시간이 지나고도 OriginateResponse가 없을 때 더 기다리는 시간
}

// DefaultPolicy는 기본 정책이다.
func DefaultPolicy() Policy {
	return Policy{ACWMicros: 10_000_000, MaxMissed: 2, OfferMicros: 15_000_000, OfferGraceMic: 5_000_000}
}

// Line은 출력 한 줄(JSON 객체 하나)이다. 필드 순서는 구조체 정의 순서로 고정된다.
type Line struct {
	T        string `json:"t"`
	Kind     string `json:"kind"`               // agent, call, call_end, action, warn
	ID       string `json:"id,omitempty"`       // 상담원 인터페이스 또는 방
	Name     string `json:"name,omitempty"`     // 상담원 이름
	Mode     string `json:"mode,omitempty"`     // 상담원 업무 모드
	Phase    string `json:"phase,omitempty"`    // 상담원 통화 단계
	Reason   string `json:"reason,omitempty"`   // 이석 사유
	State    string `json:"state,omitempty"`    // 콜 상태
	Room     string `json:"room,omitempty"`     // 상담원이 있는 방, 액션 대상 방
	Queue    string `json:"queue,omitempty"`    // 콜
	Customer string `json:"customer,omitempty"` // 콜
	Agents   string `json:"agents,omitempty"`   // 콜에 들어와 있는 상담원 (쉼표)
	Outcome  string `json:"outcome,omitempty"`  // call_end: completed, abandoned
	WaitMS   *int64 `json:"wait_ms,omitempty"`  // call_end: 대기 시간
	TalkMS   *int64 `json:"talk_ms,omitempty"`  // call_end: 통화 시간
	Action   string `json:"action,omitempty"`   // action: Originate, Hangup
	Iface    string `json:"iface,omitempty"`    // action Originate: 부를 상담원
	Channel  string `json:"channel,omitempty"`  // action Hangup: 끊을 채널
	Code     string `json:"code,omitempty"`     // warn
}

// CommandKind는 상담원(또는 관리자) 명령이다.
type CommandKind int

const (
	CmdLogin      CommandKind = iota // Arg = 이름
	CmdLogout                        //
	CmdReady                         // 대기 (Inbound)
	CmdNotReady                      // Arg = 사유
	CmdWrapUp                        // 후처리 끝
	CmdTransfer                      // Arg = 넘겨받을 상담원. 그 상담원이 방에 들어오면 원래 상담원을 뺀다
	CmdConference                    // Arg = 부를 상담원. 셋이 함께 통화한다
)

// Command는 명령 하나다.
type Command struct {
	Kind  CommandKind
	Iface string
	Arg   string
}

// Event는 코어에 들어오는 AMI 이벤트에서 쓰는 필드만 모은 것이다 (껍데기가 ami.Message에서 뽑는다).
type Event struct {
	Name      string // UserEvent면 UserEvent 이름(RQEnter…), 아니면 이벤트 이름(OriginateResponse)
	Room      string
	Agent     string
	Queue     string
	Customer  string
	Channel   string
	Response  string // OriginateResponse: Success / Failure
	Timestamp int64  // 마이크로초 (없으면 0)
}

// Engine은 룸 큐 상태머신이다. 고루틴 하나만 만진다.
type Engine struct {
	Policy Policy
	agents map[string]*Agent
	calls  map[string]*Call
	now    int64
}

// New는 빈 상태머신을 만든다.
func New(p Policy) *Engine {
	return &Engine{Policy: p, agents: map[string]*Agent{}, calls: map[string]*Call{}}
}

// Agent는 상담원 상태다 (없으면 nil). 껍데기·테스트용.
func (e *Engine) Agent(iface string) *Agent { return e.agents[iface] }

// Call은 콜 상태다 (없거나 끝났으면 nil).
func (e *Engine) Call(room string) *Call { return e.calls[room] }

// ── 입력 ──────────────────────────────────────────────────────────────────

// HandleEvent는 AMI 이벤트 하나를 처리한다.
func (e *Engine) HandleEvent(ts int64, ev Event) []Line {
	var out []Line
	t := e.tick(ts, &out)
	switch ev.Name {
	case "RQEnter":
		e.onEnter(ev, t, &out)
	case "RQAgentIn":
		e.onAgentIn(ev, t, &out)
	case "RQAgentMissed":
		e.onAgentMissed(ev, t, &out)
	case "RQAgentOut":
		e.onAgentOut(ev, t, &out)
	case "RQCustomerOut":
		e.onCustomerOut(ev, t, &out)
	case "OriginateResponse":
		if ev.Response == "Failure" {
			e.offerFailed(baseIface(ev.Channel), "no_answer", t, &out)
		}
	}
	e.dispatch(t, &out)
	return out
}

// HandleCommand는 명령 하나를 처리한다.
func (e *Engine) HandleCommand(ts int64, c Command) []Line {
	var out []Line
	t := e.tick(ts, &out)
	e.onCommand(c, t, &out)
	e.dispatch(t, &out)
	return out
}

// Advance는 입력 없이 시간만 흘린다 (후처리 끝, 벨 시간 초과).
func (e *Engine) Advance(ts int64) []Line {
	var out []Line
	t := e.tick(ts, &out)
	e.dispatch(t, &out)
	return out
}

// tick은 시각을 앞으로만 움직이고, 마감이 지난 타이머를 처리한다.
func (e *Engine) tick(ts int64, out *[]Line) int64 {
	if ts > e.now {
		e.now = ts
	}
	t := e.now
	for _, id := range sortedKeys(e.agents) {
		a := e.agents[id]
		if a.Phase == Ring && a.offerUntil > 0 && a.offerUntil <= t {
			e.offerFailed(id, "offer_timeout", t, out)
		}
		if a.Mode == ACW && a.acwUntil > 0 && a.acwUntil <= t {
			e.endACW(a, t, out)
		}
	}
	return t
}

// ── 이벤트 처리 ───────────────────────────────────────────────────────────

func (e *Engine) onEnter(ev Event, t int64, out *[]Line) {
	if ev.Room == "" || e.calls[ev.Room] != nil {
		return
	}
	c := &Call{ID: ev.Room, Queue: ev.Queue, Customer: ev.Customer, State: Waiting, agents: map[string]bool{}, enteredAt: t}
	e.calls[ev.Room] = c
	*out = append(*out, callLine(t, c))
}

// onAgentIn: 상담원이 방에 들어왔다. 전환이면 원래 상담원을 뺀다 (Hangup).
func (e *Engine) onAgentIn(ev Event, t int64, out *[]Line) {
	a := e.agents[ev.Agent]
	c := e.calls[ev.Room]
	if a == nil {
		*out = append(*out, warnLine(t, "unknown_agent", ev.Agent))
		return
	}
	a.missed = 0
	a.offerUntil = 0
	a.channel = ev.Channel
	invite := a.invite
	a.invite = ""
	e.setPhase(a, Talk, ev.Room, t, out)
	if c == nil {
		*out = append(*out, warnLine(t, "unknown_room", ev.Room))
		return
	}
	if c.offered == a.Iface {
		c.offered = ""
	}
	c.agents[a.Iface] = true
	if c.answeredAt == 0 {
		c.answeredAt = t
		c.handledBy = a.Iface
	}
	e.setCall(c, Talking, t, out)
	if from, ok := strings.CutPrefix(invite, "transfer:"); ok {
		// 넘겨받을 상담원이 들어왔다 → 원래 상담원을 뺀다. 먼저 빼면 고객도 방에서 나간다(end_marked)
		if fa := e.agents[from]; fa != nil && fa.Phase == Talk && fa.Room == c.ID && fa.channel != "" {
			*out = append(*out, Line{T: fmtTS(t), Kind: "action", Action: "Hangup", Channel: fa.channel, Room: c.ID, Iface: from})
		}
	}
}

// onAgentMissed: 상담원이 받았지만 고객이 이미 나가서 방에 들어가지 않았다. 상담원 잘못이 아니다.
func (e *Engine) onAgentMissed(ev Event, t int64, out *[]Line) {
	a := e.agents[ev.Agent]
	if a == nil || a.Phase != Ring {
		return
	}
	a.offerUntil = 0
	a.invite = ""
	a.idleSince = t
	e.setPhase(a, Idle, "", t, out)
}

// onAgentOut: 상담원 채널이 끊겼다. 대기 중이었으면 후처리로, 이석 중이었으면 그대로 이석.
func (e *Engine) onAgentOut(ev Event, t int64, out *[]Line) {
	a := e.agents[ev.Agent]
	if a == nil || a.Phase != Talk || a.Room != ev.Room {
		return // RQAgentMissed 뒤의 끊김 등
	}
	if c := e.calls[ev.Room]; c != nil {
		delete(c.agents, a.Iface)
		*out = append(*out, callLine(t, c))
	}
	a.channel = ""
	a.idleSince = t
	if a.Mode == Inbound { // 후처리 동안은 방금 끝난 방을 기억한다
		e.setPhase(a, Idle, ev.Room, t, out)
		a.acwUntil = t + e.Policy.ACWMicros
		e.setMode(a, ACW, "후처리", t, out)
	} else {
		e.setPhase(a, Idle, "", t, out)
	}
}

// endACW: 후처리가 끝났다 (타이머, wrapup, ready). 대기로 돌아가며 방을 잊는다.
func (e *Engine) endACW(a *Agent, t int64, out *[]Line) {
	a.acwUntil = 0
	if a.Phase == Idle {
		a.Room = ""
	}
	e.setMode(a, Inbound, "", t, out)
}

// onCustomerOut: 고객 채널이 끊겼다 → 콜 끝. 방에 남은 상담원은 엔진이 끊는다.
func (e *Engine) onCustomerOut(ev Event, t int64, out *[]Line) {
	c := e.calls[ev.Room]
	if c == nil {
		return
	}
	for _, id := range sortedKeys(c.agents) {
		if a := e.agents[id]; a != nil && a.channel != "" {
			*out = append(*out, Line{T: fmtTS(t), Kind: "action", Action: "Hangup", Channel: a.channel, Room: c.ID, Iface: id})
		}
	}
	outcome := "completed"
	if c.answeredAt == 0 {
		outcome = "abandoned"
	}
	waitEnd := c.answeredAt
	if waitEnd == 0 {
		waitEnd = t
	}
	wait := (waitEnd - c.enteredAt) / 1000
	var talk int64
	if c.answeredAt > 0 {
		talk = (t - c.answeredAt) / 1000
	}
	delete(e.calls, c.ID)
	c.State = Ended
	*out = append(*out, callLine(t, c))
	*out = append(*out, Line{T: fmtTS(t), Kind: "call_end", ID: c.ID, Queue: c.Queue, Customer: c.Customer,
		Agents: c.handledBy, Outcome: outcome, WaitMS: &wait, TalkMS: &talk})
}

// offerFailed: 부른 상담원이 받지 않았다 (OriginateResponse Failure 또는 벨 시간 초과). 콜은 다시 대기로.
func (e *Engine) offerFailed(iface, code string, t int64, out *[]Line) {
	a := e.agents[iface]
	if a == nil || a.Phase != Ring {
		return
	}
	room := a.Room
	invited := a.invite != ""
	a.offerUntil = 0
	a.invite = ""
	a.idleSince = t
	*out = append(*out, warnLine(t, code, iface))
	e.setPhase(a, Idle, "", t, out)
	if !invited { // 전환·3자통화로 부른 상담원은 무응답으로 세지 않는다
		a.missed++
		if e.Policy.MaxMissed > 0 && a.missed >= e.Policy.MaxMissed && a.Mode == Inbound {
			a.missed = 0
			e.setMode(a, NotReady, "무응답", t, out)
		}
	}
	if c := e.calls[room]; c != nil && c.offered == iface {
		c.offered = ""
		if c.State == Offering {
			e.setCall(c, Waiting, t, out)
		}
	}
}

// ── 명령 ──────────────────────────────────────────────────────────────────

func (e *Engine) onCommand(c Command, t int64, out *[]Line) {
	a := e.agents[c.Iface]
	switch c.Kind {
	case CmdLogin:
		if a == nil {
			a = &Agent{Iface: c.Iface, Mode: Logout, idleSince: t}
			e.agents[c.Iface] = a
		}
		if c.Arg != "" {
			a.Name = c.Arg
		}
		if a.Mode == Logout {
			e.setMode(a, NotReady, "로그인", t, out)
		}
	case CmdLogout:
		if a != nil && a.Mode != Logout {
			a.acwUntil = 0
			e.setMode(a, Logout, "", t, out)
		}
	case CmdReady:
		if a != nil && (a.Mode == NotReady || a.Mode == ACW) {
			if a.Phase == Idle && a.Mode == NotReady {
				a.idleSince = t
			}
			e.endACW(a, t, out)
		}
	case CmdNotReady:
		// 두 축이라 통화 중에도 모드만 바꾸면 된다: 통화가 끝나도 Inbound가 아니니 콜이 오지 않는다 (이석 예약이 따로 필요 없다)
		if a != nil && a.Mode != Logout {
			reason := c.Arg
			if reason == "" {
				reason = "이석"
			}
			a.acwUntil = 0
			e.setMode(a, NotReady, reason, t, out)
		}
	case CmdWrapUp:
		if a != nil && a.Mode == ACW {
			e.endACW(a, t, out)
		}
	case CmdTransfer, CmdConference:
		e.invite(c, t, out)
	}
}

// invite: 통화 중인 방에 다른 상담원을 부른다 (전환·3자통화).
func (e *Engine) invite(c Command, t int64, out *[]Line) {
	from := e.agents[c.Iface]
	to := e.agents[c.Arg]
	if from == nil || from.Phase != Talk || e.calls[from.Room] == nil {
		*out = append(*out, warnLine(t, "invite_not_in_call", c.Iface))
		return
	}
	if to == nil || to.Mode == Logout || to.Phase != Idle {
		*out = append(*out, warnLine(t, "invite_target_busy", c.Arg))
		return
	}
	call := e.calls[from.Room]
	to.invite = "conference"
	if c.Kind == CmdTransfer {
		to.invite = "transfer:" + from.Iface
	}
	e.offer(call, to, t, out)
}

// ── 분배 ──────────────────────────────────────────────────────────────────

// dispatch: 기다리는 콜(먼저 온 순서)마다 대기 중인 상담원(가장 오래 쉰 순서)을 붙인다.
func (e *Engine) dispatch(t int64, out *[]Line) {
	waiting := make([]*Call, 0, len(e.calls))
	for _, c := range e.calls {
		if c.State == Waiting {
			waiting = append(waiting, c)
		}
	}
	sort.Slice(waiting, func(i, j int) bool {
		if waiting[i].enteredAt != waiting[j].enteredAt {
			return waiting[i].enteredAt < waiting[j].enteredAt
		}
		return waiting[i].ID < waiting[j].ID
	})
	for _, c := range waiting {
		a := e.pickAgent()
		if a == nil {
			return
		}
		e.offer(c, a, t, out)
	}
}

func (e *Engine) pickAgent() *Agent {
	var best *Agent
	for _, id := range sortedKeys(e.agents) {
		a := e.agents[id]
		if a.Mode != Inbound || a.Phase != Idle {
			continue
		}
		if best == nil || a.idleSince < best.idleSince {
			best = a
		}
	}
	return best
}

// offer: 상담원을 방으로 부른다 → 껍데기가 AMI Originate를 보낸다.
func (e *Engine) offer(c *Call, a *Agent, t int64, out *[]Line) {
	a.offerUntil = t + e.Policy.OfferMicros + e.Policy.OfferGraceMic
	e.setPhase(a, Ring, c.ID, t, out)
	if c.State == Waiting {
		c.offered = a.Iface
		e.setCall(c, Offering, t, out)
	}
	*out = append(*out, Line{T: fmtTS(t), Kind: "action", Action: "Originate", Iface: a.Iface, Room: c.ID, Customer: c.Customer})
}

// ── 상태 바꾸기 ───────────────────────────────────────────────────────────

func (e *Engine) setMode(a *Agent, m Mode, reason string, t int64, out *[]Line) {
	if a.Mode == m && a.Reason == reason {
		return
	}
	a.Mode, a.Reason = m, reason
	*out = append(*out, agentLine(t, a))
}

func (e *Engine) setPhase(a *Agent, p Phase, room string, t int64, out *[]Line) {
	if a.Phase == p && a.Room == room {
		return
	}
	a.Phase, a.Room = p, room
	*out = append(*out, agentLine(t, a))
}

func (e *Engine) setCall(c *Call, s CallState, t int64, out *[]Line) {
	if c.State == s {
		return
	}
	c.State = s
	*out = append(*out, callLine(t, c))
}

// Snapshot은 지금 상태 전체다 (상담원, 진행 중인 콜).
func (e *Engine) Snapshot() (agents []Line, calls []Line) {
	for _, id := range sortedKeys(e.agents) {
		agents = append(agents, agentLine(e.now, e.agents[id]))
	}
	for _, id := range sortedKeys(e.calls) {
		calls = append(calls, callLine(e.now, e.calls[id]))
	}
	return agents, calls
}

// EncodeLine은 출력 줄을 JSON 한 줄로 만든다 (한글과 <>는 그대로 둔다).
func EncodeLine(l Line) string {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(l); err != nil { // Line은 문자열·정수뿐이라 실패하지 않는다
		return `{"kind":"warn","code":"encode_failed"}`
	}
	return strings.TrimSuffix(b.String(), "\n")
}

func agentLine(t int64, a *Agent) Line {
	return Line{T: fmtTS(t), Kind: "agent", ID: a.Iface, Name: a.Name, Mode: a.Mode.String(), Phase: a.Phase.String(),
		Reason: a.Reason, Room: a.Room}
}

func callLine(t int64, c *Call) Line {
	return Line{T: fmtTS(t), Kind: "call", ID: c.ID, State: c.State.String(), Queue: c.Queue, Customer: c.Customer,
		Agents: strings.Join(sortedKeys(c.agents), ",")}
}

func warnLine(t int64, code, id string) Line {
	return Line{T: fmtTS(t), Kind: "warn", Code: code, ID: id}
}

// baseIface는 채널 이름에서 인터페이스를 뽑는다.
// OriginateResponse의 Channel은 성공이면 "Local/1001@sim-agent-0000000b;1"·"PJSIP/9002-00000003",
// 실패면 요청한 문자열 그대로 "Local/1004@sim-noanswer"다 (녹화로 확인).
func baseIface(ch string) string {
	if m := chanSuffix.FindStringSubmatch(ch); m != nil {
		return m[1]
	}
	return ch
}

var chanSuffix = regexp.MustCompile(`^(.+)-[0-9a-f]{8}(;[12])?$`)

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func fmtTS(us int64) string { return fmt.Sprintf("%d.%06d", us/1_000_000, us%1_000_000) }
