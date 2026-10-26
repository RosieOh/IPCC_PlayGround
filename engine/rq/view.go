package rq

import (
	"encoding/json"
	"fmt"
	"time"
)

// 화면(web/의 룸 큐 화면)에 보낼 상태. 코어의 출력 줄을 그대로 쓰고, 화면에만 필요한 값(두 축 각각이 바뀐 시각,
// 누적 결과, 최근 줄)은 껍데기가 출력 줄을 보며 따로 모은다. 모든 필드는 actor 고루틴만 만진다.

const recentMax = 200 // 화면이 처음 붙을 때 보여 줄 최근 출력 줄 수

type totals struct {
	Handled   int `json:"handled"`   // completed
	Abandoned int `json:"abandoned"` // 상담원과 연결되기 전에 끊음
}

type board struct {
	last       map[string]Line   // 상담원 → 마지막 agent 줄 (어느 축이 바뀌었는지 보려고)
	modeSince  map[string]string // 상담원 → 지금 업무 모드(사유 포함)가 된 시각
	phaseSince map[string]string // 상담원 → 지금 통화 단계가 된 시각
	callState  map[string]string
	callSince  map[string]string // 콜 → 지금 상태가 된 시각
	recent     []string
	totals     totals
	connected  bool // AMI 연결
	synced     bool // 이 연결에서 재동기화(CoreShowChannels)를 마쳤는가
}

func newBoard() *board {
	return &board{last: map[string]Line{}, modeSince: map[string]string{}, phaseSince: map[string]string{},
		callState: map[string]string{}, callSince: map[string]string{}}
}

// track은 코어가 낸 줄 하나를 화면용 상태에 반영한다.
func (b *board) track(l Line, line string) {
	switch l.Kind {
	case "agent":
		prev, seen := b.last[l.ID]
		if !seen || prev.Mode != l.Mode || prev.Reason != l.Reason {
			b.modeSince[l.ID] = l.T
		}
		if !seen || prev.Phase != l.Phase {
			b.phaseSince[l.ID] = l.T
		}
		b.last[l.ID] = l
	case "call":
		if l.State == Ended.String() {
			delete(b.callState, l.ID)
			delete(b.callSince, l.ID)
			break
		}
		if b.callState[l.ID] != l.State {
			b.callState[l.ID] = l.State
			b.callSince[l.ID] = l.T
		}
	case "call_end":
		if l.Outcome == "completed" {
			b.totals.Handled++
		} else {
			b.totals.Abandoned++
		}
	}
	b.recent = append(b.recent, line)
	if len(b.recent) >= 2*recentMax { // 가끔 한 번씩만 복사해서 앞부분을 버린다
		b.recent = append([]string(nil), b.recent[len(b.recent)-recentMax:]...)
	}
}

type stateView struct {
	Kind      string            `json:"kind"` // "state"
	Now       string            `json:"now"`
	Connected bool              `json:"connected"`
	Synced    bool              `json:"synced"`
	Policy    policyView        `json:"policy"`
	Agents    []json.RawMessage `json:"agents"` // agent 줄
	Calls     []json.RawMessage `json:"calls"`  // call 줄
	Since     sinceView         `json:"since"`
	Totals    totals            `json:"totals"`
	Recent    []json.RawMessage `json:"recent"`
}

type policyView struct {
	ACWMs     int64 `json:"acw_ms"`
	RingMs    int64 `json:"ring_ms"`
	MaxMissed int   `json:"max_missed"`
	Record    bool  `json:"record"`
}

type sinceView struct {
	Mode  map[string]string `json:"mode"`
	Phase map[string]string `json:"phase"`
	Calls map[string]string `json:"calls"`
}

// stateJSON은 화면이 처음 붙을 때 받는 전체 상태다.
func (r *Runner) stateJSON() string {
	b := r.board
	agents, calls := r.core.Snapshot()
	recent := b.recent
	if len(recent) > recentMax {
		recent = recent[len(recent)-recentMax:]
	}
	p := r.cfg.Policy
	v := stateView{
		Kind:      "state",
		Now:       fmtTS(time.Now().UnixMicro()),
		Connected: b.connected,
		Synced:    b.synced,
		Policy:    policyView{ACWMs: p.ACWMicros / 1000, RingMs: p.OfferMicros / 1000, MaxMissed: p.MaxMissed, Record: p.Record},
		Agents:    rawLines(agents),
		Calls:     rawLines(calls),
		Since:     sinceView{Mode: b.modeSince, Phase: b.phaseSince, Calls: b.callSince},
		Totals:    b.totals,
		Recent:    make([]json.RawMessage, len(recent)),
	}
	for i, l := range recent {
		v.Recent[i] = json.RawMessage(l)
	}
	data, err := json.Marshal(v)
	if err != nil { // 위 값들은 모두 직렬화할 수 있다. 그래도 panic 대신 빈 상태를 보낸다
		r.log.Error("상태 직렬화 실패", "err", err)
		return `{"kind":"state"}`
	}
	return string(data)
}

func rawLines(lines []Line) []json.RawMessage {
	out := make([]json.RawMessage, len(lines))
	for i, l := range lines {
		out[i] = json.RawMessage(EncodeLine(l))
	}
	return out
}

// statusJSON은 연결 상태가 바뀔 때 화면에 보내는 줄이다 (코어 출력이 아니라 껍데기의 줄).
func (r *Runner) statusJSON() string {
	return fmt.Sprintf(`{"kind":"status","t":%q,"connected":%t,"synced":%t}`,
		fmtTS(time.Now().UnixMicro()), r.board.connected, r.board.synced)
}

// setConn은 AMI 연결·재동기화 상태를 바꾸고 화면에 알린다.
func (r *Runner) setConn(connected, synced bool) {
	if r.board.connected == connected && r.board.synced == synced {
		return
	}
	r.board.connected, r.board.synced = connected, synced
	r.broadcast(r.statusJSON())
}

// broadcast는 화면 구독자에게 줄 하나를 보낸다. 받지 못할 만큼 밀린 화면은 끊는다
// (actor를 기다리게 하지 않는다. 화면은 다시 붙어서 전체 상태부터 받는다).
func (r *Runner) broadcast(line string) {
	for sub := range r.subs {
		select {
		case sub <- line:
		default:
			r.log.Warn("밀린 화면 연결을 끊음")
			r.drop(sub)
		}
	}
}

func (r *Runner) drop(sub chan string) {
	if _, ok := r.subs[sub]; ok {
		delete(r.subs, sub)
		close(sub)
	}
}
