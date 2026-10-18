package live

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"ipcc/engine/state"
)

// 화면(web/)에 보낼 상태. 상태머신 코어(state)의 출력 계약은 Rust 레거시와 바이트 단위로 맞춰 두었으므로
// 화면에만 필요한 값(상태가 바뀐 시각, 누적 결과, 최근 줄)은 코어를 고치지 않고 껍데기가 출력 줄을 보며 따로 모은다.
// 모든 필드는 actor 고루틴만 만진다.

const recentMax = 200 // 화면이 처음 붙을 때 보여 줄 최근 출력 줄 수

type totals struct {
	Handled   int `json:"handled"`   // completed, completed:* (전환 포함)
	Abandoned int `json:"abandoned"` // 대기 중 포기
	Timeout   int `json:"timeout"`   // 큐 타임아웃
	Other     int `json:"other"`     // left_queue, no_queue
}

type board struct {
	agentSince map[string]string // 상담원 → 지금 상태가 된 시각
	callSince  map[string]string // 콜 → 지금 상태가 된 시각
	callQueued map[string]string // 콜 → 큐에 처음 들어온 시각 (무응답으로 Queued·Alerting을 오가도 대기 시간은 여기서부터)
	recent     []string
	totals     totals
	connected  bool // AMI 연결
	synced     bool // 이 연결에서 재동기화(QueueStatus 스냅샷)를 반영했는가
}

func newBoard() *board {
	return &board{agentSince: map[string]string{}, callSince: map[string]string{}, callQueued: map[string]string{}}
}

// track은 상태머신이 낸 줄 하나를 화면용 상태에 반영한다.
func (b *board) track(o *state.Out, line string) {
	switch o.Get("kind") {
	case "agent":
		b.agentSince[o.Get("id")] = o.Get("t")
	case "call":
		id, to := o.Get("id"), o.Get("to")
		if to == "Ended" {
			delete(b.callSince, id)
			delete(b.callQueued, id)
			break
		}
		b.callSince[id] = o.Get("t")
		if _, ok := b.callQueued[id]; !ok && to == "Queued" {
			b.callQueued[id] = o.Get("t")
		}
	case "call_end":
		switch outcome := o.Get("outcome"); {
		case outcome == "completed" || strings.HasPrefix(outcome, "completed:"):
			b.totals.Handled++
		case outcome == "abandoned":
			b.totals.Abandoned++
		case outcome == "timeout":
			b.totals.Timeout++
		default:
			b.totals.Other++
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
	Queue     string            `json:"queue"`
	Connected bool              `json:"connected"`
	Synced    bool              `json:"synced"`
	Policy    policyView        `json:"policy"`
	Snapshot  json.RawMessage   `json:"snapshot"`
	Since     sinceView         `json:"since"`
	Totals    totals            `json:"totals"`
	Recent    []json.RawMessage `json:"recent"`
}

type policyView struct {
	ACWMs     int64 `json:"acw_ms"`
	MaxMissed int   `json:"max_missed"`
}

type sinceView struct {
	Agents map[string]string `json:"agents"`
	Calls  map[string]string `json:"calls"`
	Queued map[string]string `json:"queued"`
}

// stateJSON은 화면이 처음 붙을 때 받는 전체 상태다.
func (r *Runner) stateJSON() string {
	b := r.board
	recent := b.recent
	if len(recent) > recentMax {
		recent = recent[len(recent)-recentMax:]
	}
	raws := make([]json.RawMessage, len(recent))
	for i, l := range recent {
		raws[i] = json.RawMessage(l)
	}
	v := stateView{
		Kind:      "state",
		Now:       fmtMicros(time.Now().UnixMicro()),
		Queue:     r.cfg.Queue,
		Connected: b.connected,
		Synced:    b.synced,
		Policy:    policyView{ACWMs: r.cfg.Policy.ACWMicros / 1000, MaxMissed: r.cfg.Policy.MaxMissed},
		Snapshot:  json.RawMessage(r.core.Snapshot().Line()),
		Since:     sinceView{Agents: b.agentSince, Calls: b.callSince, Queued: b.callQueued},
		Totals:    b.totals,
		Recent:    raws,
	}
	data, err := json.Marshal(v)
	if err != nil { // 위 값들은 모두 직렬화할 수 있다. 그래도 panic 대신 빈 상태를 보낸다
		r.log.Error("상태 직렬화 실패", "err", err)
		return `{"kind":"state"}`
	}
	return string(data)
}

// statusJSON은 연결 상태가 바뀔 때 화면에 보내는 줄이다 (상태머신 출력이 아니라 껍데기의 줄).
func (r *Runner) statusJSON() string {
	return fmt.Sprintf(`{"kind":"status","t":%q,"connected":%t,"synced":%t}`,
		fmtMicros(time.Now().UnixMicro()), r.board.connected, r.board.synced)
}

func fmtMicros(us int64) string { return fmt.Sprintf("%d.%06d", us/1_000_000, us%1_000_000) }
