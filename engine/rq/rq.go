// Package rq는 "룸 큐"의 상태머신 코어다: app_queue 없이 엔진이 직접 대기열을 갖고 상담원을 고른다 (docs/12-room-queue.md).
//
//   - 고객은 통화마다 생기는 회의방(ConfBridge, 이름 rq-<큐>-<Linkedid>)에서 대기음을 들으며 기다린다.
//   - 엔진이 상담원을 골라 AMI Originate로 같은 방에 부른다. 상담원을 둘 부르면 3자통화, 한 명을 빼면 전환이다.
//   - 보류는 고객만 보류 방으로 옮기는 것이다. 그 사이 다른 상담원을 원래 방에 부르면 협의 전환이 된다.
//   - dialplan은 상태를 저장하지 않고 UserEvent(RQEnter, RQAgentIn, RQAgentMissed, RQAgentOut, RQCustomerOut,
//     RQHold, RQUnhold)로 일어난 일만 알린다. 상태의 주인은 이 코어 하나다.
//   - 상담원 상태는 두 축이다: 업무 모드(Mode: 로그아웃·대기·이석·후처리)와 통화 단계(Phase: 없음·벨·통화).
//     콜은 Mode=Inbound이고 Phase=Idle이며 그 큐의 스킬이 있는 상담원에게만 간다.
//   - 엔진이 다시 시작하면 업무 상태는 저장해 둔 것(Restore)으로, 통화 상태는 Asterisk의 채널 목록(Resync)으로 되살린다.
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
	"strconv"
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

func parseMode(s string) (Mode, bool) {
	for _, m := range []Mode{Logout, Inbound, NotReady, ACW} {
		if m.String() == s {
			return m, true
		}
	}
	return Logout, false
}

// Phase는 상담원의 통화 단계다. 교환기에서 일어난 일(UserEvent·OriginateResponse·채널 목록)로만 바뀐다.
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

// CallState는 콜(방)의 상태다. 보류는 상태가 아니라 콜의 Held 표시다 (보류 중에도 상담원은 방에서 통화 중이다).
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

// noSkillLevel은 스킬을 정하지 않은 상담원의 숙련도다. 모든 큐를 받되, 그 큐의 스킬이 있는 상담원보다 뒤에 고른다.
const noSkillLevel = 100

// Agent는 상담원 한 명이다.
type Agent struct {
	Iface, Name string
	Mode        Mode
	Reason      string
	Phase       Phase
	Room        string         // 지금 부르는 중이거나 들어가 있는 방 (후처리 중에는 방금 끝난 방)
	Skills      map[string]int // 큐 → 숙련도(1이 가장 높다). 비어 있으면 모든 큐를 받는다
	channel     string         // 방에 들어온 상담원 채널 (전환·협의 완료 때 엔진이 끊는다)
	ringID      string         // 벨 울리는 채널의 Uniqueid(Originate ChannelId). OriginateResponse를 맞추고, 벨을 끊을 때 쓴다
	invite      string         // 이미 통화 중인 방으로 부른 이유: "conference", "transfer:<원래 상담원>", "consult:<원래 상담원>"
	missed      int
	idleSince   int64 // 가장 오래 쉰 상담원부터 고른다
	offerUntil  int64 // 이때까지 응답(OriginateResponse·RQAgentIn)이 없으면 무응답으로 본다
	acwUntil    int64
}

// Call은 콜 하나(=회의방 하나)다.
type Call struct {
	ID, Queue, Customer string
	State               CallState
	Held                bool            // 고객이 보류 방에 있다 (RQHold ~ RQUnhold)
	channel             string          // 고객 채널. 보류·해제 때 Redirect한다
	holdReq             string          // 보내 놓고 아직 확인(RQHold/RQUnhold)을 받지 못한 요청: "hold", "unhold"
	consult             string          // 협의 중이면 넘겨받을 상담원
	consultFrom         string          // 협의를 건 상담원
	recording           bool            // 이 방에서 녹음을 시작했다 (상담원이 모두 나가면 방이 닫히며 녹음도 끝난다)
	offered             string          // 지금 부르고 있는 상담원 (분배)
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
	Record        bool  // 상담원이 방에 들어오면 녹음을 시작한다
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
	Skills   string `json:"skills,omitempty"`   // 상담원 스킬 "sales:1,support:2" (비면 모든 큐)
	State    string `json:"state,omitempty"`    // 콜 상태
	Hold     bool   `json:"hold,omitempty"`     // 콜: 고객이 보류 중
	Consult  string `json:"consult,omitempty"`  // 콜: 협의 중이면 넘겨받을 상담원
	Room     string `json:"room,omitempty"`     // 상담원이 있는 방, 액션 대상 방
	Queue    string `json:"queue,omitempty"`    // 콜
	Customer string `json:"customer,omitempty"` // 콜
	Entered  string `json:"entered,omitempty"`  // 콜: 고객이 방에 들어온 시각 (재동기화로 알게 된 콜은 채널이 생긴 시각)
	Agents   string `json:"agents,omitempty"`   // 콜에 들어와 있는 상담원 (쉼표)
	Outcome  string `json:"outcome,omitempty"`  // call_end: completed, abandoned
	WaitMS   *int64 `json:"wait_ms,omitempty"`  // call_end: 대기 시간
	TalkMS   *int64 `json:"talk_ms,omitempty"`  // call_end: 통화 시간
	Action   string `json:"action,omitempty"`   // action: Originate, Hangup, Hold, Unhold, Record
	Iface    string `json:"iface,omitempty"`    // action: 대상 상담원
	Channel  string `json:"channel,omitempty"`  // action: Originate면 만들 채널의 Uniqueid, 나머지는 대상 채널(이름 또는 Uniqueid)
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
	CmdSkills                        // Arg = "support:1,sales:2" (숙련도 생략 시 1, 비우면 모든 큐)
	CmdTransfer                      // Arg = 넘겨받을 상담원. 그 상담원이 방에 들어오면 원래 상담원을 뺀다
	CmdConference                    // Arg = 부를 상담원. 셋이 함께 통화한다
	CmdHold                          // 고객을 보류 방으로 (상담원은 원래 방에 남는다)
	CmdUnhold                        // 고객을 원래 방으로. 협의 중이면 셋이 함께 통화하게 된다
	CmdConsult                       // Arg = 넘겨받을 상담원. 고객을 보류하고 그 상담원을 방에 불러 먼저 통화한다
	CmdComplete                      // 협의 전환 완료: 원래 상담원이 빠지고 고객이 넘겨받은 상담원과 통화한다
	CmdCancel                        // 협의 취소: 넘겨받을 상담원을 빼고 고객을 돌려받는다
)

// Command는 명령 하나다.
type Command struct {
	Kind  CommandKind
	Iface string
	Arg   string
}

// CommandNames는 명령 이름이다 (표준입력·HTTP). 순서는 CommandKind 순서와 같다.
var CommandNames = []string{"login", "logout", "ready", "notready", "wrapup", "skills",
	"transfer", "conference", "hold", "unhold", "consult", "complete", "cancel"}

// ParseCommand는 명령 이름과 인자로 명령을 만든다. 상대 상담원이 필요한 명령(transfer, conference, consult)은 arg가 그 상담원이다.
func ParseCommand(name, iface, arg string) (Command, error) {
	for i, n := range CommandNames {
		if n != name {
			continue
		}
		k := CommandKind(i)
		if iface == "" {
			return Command{}, fmt.Errorf("%s: 상담원 인터페이스가 필요함", name)
		}
		if (k == CmdTransfer || k == CmdConference || k == CmdConsult) && arg == "" {
			return Command{}, fmt.Errorf("%s: 상대 상담원 인터페이스가 필요함", name)
		}
		return Command{Kind: k, Iface: iface, Arg: arg}, nil
	}
	return Command{}, fmt.Errorf("모르는 명령 %q (%s)", name, strings.Join(CommandNames, "|"))
}

// Event는 코어에 들어오는 AMI 이벤트에서 쓰는 필드만 모은 것이다 (껍데기가 ami.Message에서 뽑는다).
type Event struct {
	Name      string // UserEvent면 UserEvent 이름(RQEnter…), 아니면 이벤트 이름(OriginateResponse)
	Room      string
	Agent     string
	Queue     string
	Customer  string
	Channel   string
	Uniqueid  string // OriginateResponse: Originate에 붙인 ChannelId (실패여도 온다)
	Response  string // OriginateResponse: Success / Failure
	Timestamp int64  // 마이크로초 (없으면 0)
}

// Chan은 재동기화 때 쓰는 채널 하나다 (CoreShowChannels의 CoreShowChannel 이벤트에서 뽑는다).
type Chan struct {
	Channel         string
	Uniqueid        string
	CallerIDNum     string
	Application     string // ConfBridge, AppDial2(벨 울리는 Originate 채널) …
	ApplicationData string // ConfBridge면 "방,브리지 프로필,사용자 프로필"
	Seconds         int64  // 채널이 생긴 뒤 지난 시간 (Duration)
}

// AgentRecord는 엔진이 다시 시작해도 남아야 하는 상담원 업무 상태다. 통화 단계는 넣지 않는다 (Asterisk에서 다시 읽는다).
type AgentRecord struct {
	Iface    string         `json:"iface"`
	Name     string         `json:"name,omitempty"`
	Mode     string         `json:"mode"`
	Reason   string         `json:"reason,omitempty"`
	Skills   map[string]int `json:"skills,omitempty"`
	ACWUntil int64          `json:"acw_until,omitempty"` // 후처리 끝 시각 (마이크로초)
}

// Engine은 룸 큐 상태머신이다. 고루틴 하나만 만진다.
type Engine struct {
	Policy   Policy
	agents   map[string]*Agent
	calls    map[string]*Call
	now      int64
	lastRing int64 // ringID를 겹치지 않게 만든다
	synced   bool  // false면 분배하지 않는다 (연결 직후 재동기화 전에는 누가 통화 중인지 모른다)
}

// New는 빈 상태머신을 만든다.
func New(p Policy) *Engine {
	return &Engine{Policy: p, agents: map[string]*Agent{}, calls: map[string]*Call{}, synced: true}
}

// Agent는 상담원 상태다 (없으면 nil). 껍데기·테스트용.
func (e *Engine) Agent(iface string) *Agent { return e.agents[iface] }

// Call은 콜 상태다 (없거나 끝났으면 nil).
func (e *Engine) Call(room string) *Call { return e.calls[room] }

// Unsync는 AMI 연결이 새로 맺어졌다는 뜻이다. 끊긴 동안의 일을 모르므로 Resync 전까지 분배하지 않는다.
func (e *Engine) Unsync() { e.synced = false }

// Synced는 재동기화를 마쳤는지다.
func (e *Engine) Synced() bool { return e.synced }

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
	case "RQHold":
		e.onHold(ev, t, &out)
	case "RQUnhold":
		e.onUnhold(ev, t, &out)
	case "OriginateResponse":
		if ev.Response == "Failure" {
			if a := e.ringingAgent(ev); a != nil {
				e.offerFailed(a, "no_answer", t, &out)
			}
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
			e.offerFailed(a, "offer_timeout", t, out)
		}
		if a.Mode == ACW && a.acwUntil > 0 && a.acwUntil <= t {
			e.endACW(a, t, out)
		}
	}
	return t
}

// ringingAgent는 실패한 OriginateResponse가 어느 벨에 대한 것인지 찾는다.
// Originate에 ChannelId를 붙였으므로 실패여도 Uniqueid에 그 값이 온다 (녹화로 확인). 이미 끊은 벨이면 nil.
// Uniqueid가 없을 때만 채널 이름(실패면 요청한 문자열 그대로)으로 찾는다.
func (e *Engine) ringingAgent(ev Event) *Agent {
	if ev.Uniqueid != "" && ev.Uniqueid != "<unknown>" {
		for _, id := range sortedKeys(e.agents) {
			if a := e.agents[id]; a.Phase == Ring && a.ringID == ev.Uniqueid {
				return a
			}
		}
		return nil
	}
	if a := e.agents[baseIface(ev.Channel)]; a != nil && a.Phase == Ring {
		return a
	}
	return nil
}

// ── 이벤트 처리 ───────────────────────────────────────────────────────────

func (e *Engine) onEnter(ev Event, t int64, out *[]Line) {
	if ev.Room == "" || e.calls[ev.Room] != nil {
		return
	}
	c := &Call{ID: ev.Room, Queue: ev.Queue, Customer: ev.Customer, State: Waiting, channel: ev.Channel,
		agents: map[string]bool{}, enteredAt: t}
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
	a.ringID = ""
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
	c.State = Talking
	*out = append(*out, callLine(t, c))
	if e.Policy.Record && !c.recording {
		c.recording = true
		*out = append(*out, Line{T: fmtTS(t), Kind: "action", Action: "Record", Room: c.ID})
	}
	if from, ok := strings.CutPrefix(invite, "transfer:"); ok {
		// 넘겨받을 상담원이 들어왔다 → 원래 상담원을 뺀다. 먼저 빼면 고객도 방에서 나간다(end_marked)
		if fa := e.agents[from]; fa != nil && fa.Phase == Talk && fa.Room == c.ID && fa.channel != "" {
			*out = append(*out, hangupLine(t, fa.channel, c.ID, from))
		}
	}
}

// onAgentMissed: 상담원이 받았지만 고객이 이미 나가서 방에 들어가지 않았다. 상담원 잘못이 아니다.
// 고객이 끊을 때 벨을 끊으므로 보통은 오지 않는다 (벨을 끊기 직전에 받은 경우).
func (e *Engine) onAgentMissed(ev Event, t int64, out *[]Line) {
	a := e.agents[ev.Agent]
	if a == nil || a.Phase != Ring {
		return
	}
	a.offerUntil = 0
	a.invite = ""
	a.ringID = ""
	a.idleSince = t
	e.setPhase(a, Idle, "", t, out)
}

// onAgentOut: 상담원 채널이 끊겼다. 대기 중이었으면 후처리로, 이석 중이었으면 그대로 이석.
func (e *Engine) onAgentOut(ev Event, t int64, out *[]Line) {
	a := e.agents[ev.Agent]
	if a == nil || a.Phase != Talk || a.Room != ev.Room {
		return // RQAgentMissed 뒤의 끊김 등
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
	c := e.calls[ev.Room]
	if c == nil {
		return
	}
	delete(c.agents, a.Iface)
	if len(c.agents) == 0 {
		c.recording = false // 상담원이 모두 나가면 방이 닫히고 녹음도 끝난다 (ConfbridgeStopRecord)
	}
	if c.consultFrom == a.Iface || c.consult == a.Iface {
		c.consult, c.consultFrom = "", "" // 협의가 끝났다 (완료·취소·한쪽이 끊음)
	}
	if c.Held && c.holdReq != "unhold" {
		// 보류 중에 상담원이 나갔다 → 고객을 원래 방으로 돌려보낸다.
		// 남은 상담원이 있으면 그 상담원과 이어지고(협의 중 원래 상담원이 끊으면 전환 완료), 아무도 없으면 다시 대기열로
		e.unhold(c, t, out)
	}
	*out = append(*out, callLine(t, c))
}

// endACW: 후처리가 끝났다 (타이머, wrapup, ready). 대기로 돌아가며 방을 잊는다.
func (e *Engine) endACW(a *Agent, t int64, out *[]Line) {
	a.acwUntil = 0
	if a.Phase == Idle {
		a.Room = ""
	}
	e.setMode(a, Inbound, "", t, out)
}

// onCustomerOut: 고객 채널이 끊겼다 → 콜 끝. 방에 남은 상담원은 끊고, 이 방으로 벨이 울리는 상담원은 벨을 끊는다.
func (e *Engine) onCustomerOut(ev Event, t int64, out *[]Line) {
	c := e.calls[ev.Room]
	if c == nil {
		return
	}
	for _, id := range sortedKeys(c.agents) {
		if a := e.agents[id]; a != nil && a.channel != "" {
			*out = append(*out, hangupLine(t, a.channel, c.ID, id))
		}
	}
	for _, id := range sortedKeys(e.agents) {
		if a := e.agents[id]; a.Phase == Ring && a.Room == c.ID {
			e.cancelRing(a, t, out)
		}
	}
	e.endCall(c, t, out)
}

// cancelRing은 벨을 끊는다. 상담원 잘못이 아니므로 무응답으로 세지 않는다.
// 뒤따라 오는 OriginateResponse(Failure, Reason 0)는 ringID가 지워져 있어 무시된다.
func (e *Engine) cancelRing(a *Agent, t int64, out *[]Line) {
	if a.ringID != "" {
		*out = append(*out, hangupLine(t, a.ringID, a.Room, a.Iface))
	}
	a.offerUntil = 0
	a.invite = ""
	a.ringID = ""
	a.idleSince = t
	e.setPhase(a, Idle, "", t, out)
}

// endCall은 콜을 끝내고 call_end를 낸다.
func (e *Engine) endCall(c *Call, t int64, out *[]Line) {
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

// onHold: 고객이 보류 방에 들어갔다.
func (e *Engine) onHold(ev Event, t int64, out *[]Line) {
	c := e.calls[ev.Room]
	if c == nil {
		return
	}
	c.Held = true
	if c.holdReq == "hold" {
		c.holdReq = ""
	}
	*out = append(*out, callLine(t, c))
}

// onUnhold: 고객이 원래 방으로 돌아왔다. 상담원이 아무도 없으면 다시 대기열로.
func (e *Engine) onUnhold(ev Event, t int64, out *[]Line) {
	c := e.calls[ev.Room]
	if c == nil {
		return
	}
	c.Held = false
	if c.holdReq == "unhold" {
		c.holdReq = ""
	}
	if len(c.agents) == 0 && c.State == Talking {
		c.State = Waiting
	}
	*out = append(*out, callLine(t, c))
}

// offerFailed: 부른 상담원이 받지 않았다 (OriginateResponse Failure 또는 벨 시간 초과). 콜은 다시 대기로.
func (e *Engine) offerFailed(a *Agent, code string, t int64, out *[]Line) {
	room, invite := a.Room, a.invite
	a.offerUntil = 0
	a.invite = ""
	a.ringID = ""
	a.idleSince = t
	*out = append(*out, warnLine(t, code, a.Iface))
	e.setPhase(a, Idle, "", t, out)
	if invite == "" { // 전환·3자통화·협의로 부른 상담원은 무응답으로 세지 않는다
		a.missed++
		if e.Policy.MaxMissed > 0 && a.missed >= e.Policy.MaxMissed && a.Mode == Inbound {
			a.missed = 0
			e.setMode(a, NotReady, "무응답", t, out)
		}
	}
	c := e.calls[room]
	if c == nil {
		return
	}
	if c.offered == a.Iface {
		c.offered = ""
		if c.State == Offering {
			e.setCall(c, Waiting, t, out)
		}
	}
	if strings.HasPrefix(invite, "consult:") && c.consult == a.Iface {
		// 협의할 상담원이 받지 않았다 → 고객을 원래 상담원에게 돌려보낸다
		c.consult, c.consultFrom = "", ""
		e.unhold(c, t, out)
		*out = append(*out, callLine(t, c))
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
	case CmdSkills:
		if a == nil {
			*out = append(*out, warnLine(t, "unknown_agent", c.Iface))
			return
		}
		skills, err := ParseSkills(c.Arg)
		if err != nil {
			*out = append(*out, warnLine(t, "bad_skills", c.Iface))
			return
		}
		a.Skills = skills
		*out = append(*out, agentLine(t, a))
	case CmdTransfer, CmdConference:
		e.invite(c, t, out)
	case CmdHold, CmdUnhold:
		call := e.callOf(a)
		if call == nil {
			*out = append(*out, warnLine(t, "not_in_call", c.Iface))
			return
		}
		if c.Kind == CmdHold && !call.Held && call.holdReq != "hold" {
			e.hold(call, t, out)
		}
		if c.Kind == CmdUnhold && (call.Held || call.holdReq == "hold") {
			call.consult, call.consultFrom = "", "" // 협의 중이었으면 셋이 함께 통화하게 된다
			e.unhold(call, t, out)
			*out = append(*out, callLine(t, call))
		}
	case CmdConsult:
		e.consult(c, t, out)
	case CmdComplete, CmdCancel:
		e.finishConsult(c, t, out)
	}
}

// callOf는 상담원이 들어가 있는 콜이다 (없으면 nil).
func (e *Engine) callOf(a *Agent) *Call {
	if a == nil || a.Phase != Talk {
		return nil
	}
	return e.calls[a.Room]
}

// invite: 통화 중인 방에 다른 상담원을 부른다 (전환·3자통화).
func (e *Engine) invite(c Command, t int64, out *[]Line) {
	from := e.agents[c.Iface]
	call := e.callOf(from)
	if call == nil {
		*out = append(*out, warnLine(t, "invite_not_in_call", c.Iface))
		return
	}
	to := e.agents[c.Arg]
	if to == nil || to == from || to.Mode == Logout || to.Phase != Idle {
		*out = append(*out, warnLine(t, "invite_target_busy", c.Arg))
		return
	}
	to.invite = "conference"
	if c.Kind == CmdTransfer {
		to.invite = "transfer:" + from.Iface
	}
	e.offer(call, to, t, out)
}

// consult: 협의 전환을 시작한다. 고객을 보류 방으로 보내고, 넘겨받을 상담원을 원래 방에 부른다.
// 상담원 둘은 고객이 듣지 못하는 곳에서 먼저 통화한 뒤 complete(넘김) / cancel(취소) / unhold(셋이 통화) 중 하나를 한다.
func (e *Engine) consult(c Command, t int64, out *[]Line) {
	from := e.agents[c.Iface]
	call := e.callOf(from)
	if call == nil {
		*out = append(*out, warnLine(t, "invite_not_in_call", c.Iface))
		return
	}
	if call.consult != "" {
		*out = append(*out, warnLine(t, "consult_in_progress", c.Iface))
		return
	}
	to := e.agents[c.Arg]
	if to == nil || to == from || to.Mode == Logout || to.Phase != Idle {
		*out = append(*out, warnLine(t, "invite_target_busy", c.Arg))
		return
	}
	if !call.Held && call.holdReq != "hold" {
		e.hold(call, t, out)
	}
	call.consult, call.consultFrom = to.Iface, from.Iface
	to.invite = "consult:" + from.Iface
	*out = append(*out, callLine(t, call))
	e.offer(call, to, t, out)
}

// finishConsult: 협의 전환을 끝낸다.
// complete면 원래 상담원을 빼고 고객을 돌려보낸다 → 고객은 넘겨받은 상담원과 통화한다.
// cancel이면 넘겨받을 상담원을 빼고(벨이면 벨을 끊고) 고객을 돌려보낸다 → 원래 상담원과 다시 통화한다.
func (e *Engine) finishConsult(c Command, t int64, out *[]Line) {
	a := e.agents[c.Iface]
	call := e.callOf(a)
	if call == nil || call.consultFrom != a.Iface {
		*out = append(*out, warnLine(t, "no_consult", c.Iface))
		return
	}
	b := e.agents[call.consult]
	bIn := b != nil && b.Phase == Talk && b.Room == call.ID
	if c.Kind == CmdComplete {
		if !bIn || a.channel == "" {
			*out = append(*out, warnLine(t, "consult_not_ready", c.Iface)) // 넘겨받을 상담원이 아직 방에 없다
			return
		}
		*out = append(*out, hangupLine(t, a.channel, call.ID, a.Iface))
	} else {
		switch {
		case bIn:
			*out = append(*out, hangupLine(t, b.channel, call.ID, b.Iface))
		case b != nil && b.Phase == Ring && b.Room == call.ID:
			e.cancelRing(b, t, out)
		}
	}
	call.consult, call.consultFrom = "", ""
	e.unhold(call, t, out)
	*out = append(*out, callLine(t, call))
}

// hold / unhold는 고객 채널을 보류 방 / 원래 방으로 옮기는 액션을 낸다 (껍데기가 AMI Redirect로 바꾼다).
// 상태(Held)는 dialplan이 RQHold/RQUnhold로 알려 줄 때 바뀐다.
func (e *Engine) hold(c *Call, t int64, out *[]Line) {
	if c.channel == "" {
		*out = append(*out, warnLine(t, "no_customer_channel", c.ID))
		return
	}
	c.holdReq = "hold"
	*out = append(*out, Line{T: fmtTS(t), Kind: "action", Action: "Hold", Channel: c.channel, Room: c.ID})
}

func (e *Engine) unhold(c *Call, t int64, out *[]Line) {
	if c.channel == "" {
		*out = append(*out, warnLine(t, "no_customer_channel", c.ID))
		return
	}
	c.holdReq = "unhold"
	*out = append(*out, Line{T: fmtTS(t), Kind: "action", Action: "Unhold", Channel: c.channel, Room: c.ID})
}

// ── 분배 ──────────────────────────────────────────────────────────────────

// dispatch: 기다리는 콜(먼저 온 순서)마다 그 큐를 받을 수 있는 대기 중인 상담원(숙련도, 가장 오래 쉰 순서)을 붙인다.
func (e *Engine) dispatch(t int64, out *[]Line) {
	if !e.synced {
		return
	}
	waiting := make([]*Call, 0, len(e.calls))
	for _, c := range e.calls {
		if c.State == Waiting && !c.Held {
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
		if a := e.pickAgent(c.Queue); a != nil {
			e.offer(c, a, t, out)
		}
	}
}

func (e *Engine) pickAgent(queue string) *Agent {
	var best *Agent
	bestLevel := 0
	for _, id := range sortedKeys(e.agents) {
		a := e.agents[id]
		if a.Mode != Inbound || a.Phase != Idle {
			continue
		}
		level := noSkillLevel
		if len(a.Skills) > 0 {
			l, ok := a.Skills[queue]
			if !ok {
				continue
			}
			level = l
		}
		if best == nil || level < bestLevel || (level == bestLevel && a.idleSince < best.idleSince) {
			best, bestLevel = a, level
		}
	}
	return best
}

// offer: 상담원을 방으로 부른다 → 껍데기가 AMI Originate를 보낸다.
// 만들 채널의 Uniqueid(ringID)를 엔진이 정한다: 벨을 끊을 때(Hangup)와 OriginateResponse를 맞출 때 쓰고,
// 엔진이 다시 시작했을 때 채널 목록에서 "이 방으로 벨이 울리는 중"임을 알아보는 데도 쓴다.
func (e *Engine) offer(c *Call, a *Agent, t int64, out *[]Line) {
	e.lastRing = max(t, e.lastRing+1)
	a.ringID = fmt.Sprintf("%s-a%d", c.ID, e.lastRing)
	a.offerUntil = t + e.Policy.OfferMicros + e.Policy.OfferGraceMic
	e.setPhase(a, Ring, c.ID, t, out)
	if c.State == Waiting {
		c.offered = a.Iface
		e.setCall(c, Offering, t, out)
	}
	*out = append(*out, Line{T: fmtTS(t), Kind: "action", Action: "Originate", Iface: a.Iface, Room: c.ID,
		Customer: c.Customer, Channel: a.ringID})
}

// ── 재시작·재접속 ─────────────────────────────────────────────────────────

// Records는 저장해 둘 상담원 업무 상태다 (인터페이스 순).
func (e *Engine) Records() []AgentRecord {
	recs := make([]AgentRecord, 0, len(e.agents))
	for _, id := range sortedKeys(e.agents) {
		a := e.agents[id]
		recs = append(recs, AgentRecord{Iface: a.Iface, Name: a.Name, Mode: a.Mode.String(), Reason: a.Reason,
			Skills: a.Skills, ACWUntil: a.acwUntil})
	}
	return recs
}

// Restore는 저장해 둔 업무 상태를 되살린다. 통화 단계는 건드리지 않는다 (Resync가 Asterisk에서 다시 읽는다).
func (e *Engine) Restore(ts int64, recs []AgentRecord) []Line {
	var out []Line
	t := e.tick(ts, &out)
	for _, r := range recs {
		m, ok := parseMode(r.Mode)
		if r.Iface == "" || !ok {
			out = append(out, warnLine(t, "bad_record", r.Iface))
			continue
		}
		a := e.agents[r.Iface]
		if a == nil {
			a = &Agent{Iface: r.Iface, idleSince: t}
			e.agents[r.Iface] = a
		}
		a.Name, a.Mode, a.Reason, a.Skills = r.Name, m, r.Reason, r.Skills
		if m == ACW {
			a.acwUntil = r.ACWUntil
		}
		out = append(out, agentLine(t, a))
	}
	e.tick(t, &out) // 엔진이 꺼진 사이 끝난 후처리
	return out
}

// Resync는 Asterisk의 채널 목록으로 통화 상태를 다시 맞춘다 (엔진 재시작, AMI 재접속).
// 고객이 있는 방 = 진행 중인 콜, 방에 있는 상담원 = 통화 중, ringID 모양의 채널 = 벨 울리는 중이다.
// 목록에 없는 콜은 엔진이 보지 못하는 사이 끝난 것이다. 보류·협의 같은 '진행 중인 의도'는 되살리지 못한다.
func (e *Engine) Resync(ts int64, chans []Chan) []Line {
	var out []Line
	t := e.tick(ts, &out)
	type seen struct {
		customer *Chan
		held     bool
		agents   map[string]*Chan
		ringing  map[string]*Chan
	}
	rooms := map[string]*seen{}
	room := func(id string) *seen {
		if rooms[id] == nil {
			rooms[id] = &seen{agents: map[string]*Chan{}, ringing: map[string]*Chan{}}
		}
		return rooms[id]
	}
	type where struct {
		room    string
		ch      *Chan
		ringing bool
	}
	agentAt := map[string]where{}
	for i := range chans {
		ch := &chans[i]
		if ch.Application == "ConfBridge" {
			args := strings.Split(ch.ApplicationData, ",")
			if len(args) < 3 {
				continue
			}
			conf, profile := args[0], args[2]
			switch {
			case profile == "rq_customer" && isRoom(conf):
				room(conf).customer = ch
			case profile == "rq_hold" && isRoom(strings.TrimSuffix(conf, "-hold")):
				s := room(strings.TrimSuffix(conf, "-hold"))
				s.customer, s.held = ch, true
			case profile == "rq_agent" && isRoom(conf):
				iface := baseIface(ch.Channel)
				room(conf).agents[iface] = ch
				agentAt[iface] = where{conf, ch, false}
			}
			continue
		}
		if r, ok := ringRoom(ch.Uniqueid); ok {
			iface := baseIface(ch.Channel)
			room(r).ringing[iface] = ch
			if _, in := agentAt[iface]; !in {
				agentAt[iface] = where{r, ch, true}
			}
		}
	}

	// 1) 목록에 없는 콜은 끝났다
	for _, id := range sortedKeys(e.calls) {
		if s := rooms[id]; s == nil || s.customer == nil {
			out = append(out, warnLine(t, "call_gone", id))
			e.endCall(e.calls[id], t, &out)
		}
	}
	// 2) 상담원의 통화 단계는 Asterisk가 기준이다
	for _, id := range sortedKeys(e.agents) {
		a := e.agents[id]
		if _, ok := agentAt[id]; !ok && a.Phase != Idle {
			a.channel, a.ringID, a.invite, a.offerUntil, a.idleSince = "", "", "", 0, t
			e.setPhase(a, Idle, "", t, &out)
		}
	}
	for _, id := range sortedKeys(agentAt) {
		w := agentAt[id]
		a := e.agents[id]
		if a == nil { // 저장된 기록에 없는 상담원이 통화 중이다 → 이석으로 두고 관리자가 정하게 한다
			a = &Agent{Iface: id, Name: id, Mode: NotReady, Reason: "재동기화", idleSince: t}
			e.agents[id] = a
			out = append(out, agentLine(t, a))
		}
		a.invite = ""
		if w.ringing {
			a.channel, a.ringID, a.offerUntil = "", w.ch.Uniqueid, t+e.Policy.OfferMicros+e.Policy.OfferGraceMic
			e.setPhase(a, Ring, w.room, t, &out)
		} else {
			a.channel, a.ringID, a.offerUntil = w.ch.Channel, "", 0
			e.setPhase(a, Talk, w.room, t, &out)
		}
	}
	// 3) 고객이 있는 방 = 콜
	for _, id := range sortedKeys(rooms) {
		s := rooms[id]
		if s.customer == nil {
			continue // 고객이 이미 나간 방의 상담원은 곧 끝난다 (RQAgentOut)
		}
		c := e.calls[id]
		if c == nil {
			c = &Call{ID: id, Queue: queueOf(id), Customer: s.customer.CallerIDNum, agents: map[string]bool{},
				enteredAt: t - s.customer.Seconds*1_000_000}
			e.calls[id] = c
		}
		c.channel, c.Held, c.holdReq, c.consult, c.consultFrom, c.offered = s.customer.Channel, s.held, "", "", "", ""
		c.agents = map[string]bool{}
		var longest int64
		for iface, ch := range s.agents {
			c.agents[iface] = true
			longest = max(longest, ch.Seconds)
		}
		switch {
		case len(c.agents) > 0:
			c.State = Talking
			if c.answeredAt == 0 {
				c.answeredAt = t - longest*1_000_000
				c.handledBy = sortedKeys(c.agents)[0]
			}
			c.recording = e.Policy.Record // 녹음 중인지는 목록에 나오지 않는다. 시작했던 것으로 본다
		case len(s.ringing) > 0:
			c.State = Offering
			c.offered = sortedKeys(s.ringing)[0]
		default:
			c.State = Waiting
		}
		if s.held && len(c.agents) == 0 {
			e.unhold(c, t, &out) // 보류 중 상담원이 모두 나갔다 → 고객을 돌려보내 다시 분배한다
		}
		out = append(out, callLine(t, c))
	}
	e.synced = true
	e.dispatch(t, &out)
	return out
}

// isRoom은 룸 큐의 방 이름(rq-<큐>-<Linkedid>)인지다. 보류 방(…-hold)은 아니다.
func isRoom(conf string) bool {
	return strings.HasPrefix(conf, "rq-") && !strings.HasSuffix(conf, "-hold")
}

// queueOf는 방 이름에서 큐를 꺼낸다: rq-support-1792991351.8 → support.
func queueOf(room string) string {
	q, _, _ := strings.Cut(strings.TrimPrefix(room, "rq-"), "-")
	return q
}

// ringRoom은 Uniqueid가 엔진이 붙인 ringID(<방>-a<숫자>)이면 그 방을 돌려준다.
// Local 채널의 반대쪽(;2)은 "<ringID>;2"라서 여기에 걸리지 않는다 (녹화로 확인).
func ringRoom(uid string) (string, bool) {
	i := strings.LastIndex(uid, "-a")
	if i <= 0 {
		return "", false
	}
	if _, err := strconv.ParseInt(uid[i+2:], 10, 64); err != nil || !isRoom(uid[:i]) {
		return "", false
	}
	return uid[:i], true
}

// ParseSkills는 "support:1,sales:2" 또는 "support,sales"(숙련도 1)를 읽는다. 빈 문자열이면 nil(모든 큐).
func ParseSkills(s string) (map[string]int, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	skills := map[string]int{}
	for _, part := range strings.Split(s, ",") {
		q, lv, hasLevel := strings.Cut(strings.TrimSpace(part), ":")
		if q == "" {
			return nil, fmt.Errorf("큐 이름이 비었음: %q", s)
		}
		level := 1
		if hasLevel {
			n, err := strconv.Atoi(lv)
			if err != nil || n < 1 {
				return nil, fmt.Errorf("숙련도는 1 이상의 정수: %q", part)
			}
			level = n
		}
		skills[q] = level
	}
	return skills, nil
}

func formatSkills(skills map[string]int) string {
	parts := make([]string, 0, len(skills))
	for _, q := range sortedKeys(skills) {
		parts = append(parts, fmt.Sprintf("%s:%d", q, skills[q]))
	}
	return strings.Join(parts, ",")
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
		Reason: a.Reason, Skills: formatSkills(a.Skills), Room: a.Room}
}

func callLine(t int64, c *Call) Line {
	return Line{T: fmtTS(t), Kind: "call", ID: c.ID, State: c.State.String(), Hold: c.Held, Consult: c.consult,
		Queue: c.Queue, Customer: c.Customer, Entered: fmtTS(c.enteredAt), Agents: strings.Join(sortedKeys(c.agents), ",")}
}

func hangupLine(t int64, channel, room, iface string) Line {
	return Line{T: fmtTS(t), Kind: "action", Action: "Hangup", Channel: channel, Room: room, Iface: iface}
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
