//go:build e2e

package e2e

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"ipcc/engine/ami"
	"ipcc/engine/rq"
)

// 룸 큐 e2e: app_queue 없이 엔진이 상담원을 고르고, 고객은 통화마다 생기는 회의방에서 기다린다 (docs/12-room-queue.md).
// 엔진 출력(rq.Line)과 함께 하네스가 녹화한 AMI 이벤트로 Asterisk에서 실제로 일어난 일도 확인한다.

type rqUnderTest struct {
	r     *rq.Runner
	stop  func()
	mu    sync.Mutex
	lines []rq.Line
	saved []rq.AgentRecord // 마지막으로 저장한 상담원 업무 상태
}

// startRQ는 룸 큐 엔진을 띄우고 첫 재동기화까지 기다린다. restore는 되살릴 상담원 업무 상태다.
func startRQ(t *testing.T, restore []rq.AgentRecord) *rqUnderTest {
	t.Helper()
	c := ami.New(ami.Config{
		Addr:     envOr("AMI_ADDR", "127.0.0.1:5038"),
		Username: envOr("AMI_USER", "ipcc"),
		Secret:   envOr("AMI_SECRET", "ipcc-secret"),
		Logger:   slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn})),
	})
	u := &rqUnderTest{}
	p := rq.DefaultPolicy()
	p.ACWMicros = (2 * time.Second).Microseconds()
	p.OfferMicros = (4 * time.Second).Microseconds()
	p.Record = true
	u.r = rq.NewRunner(c, rq.RunnerConfig{
		Policy: p,
		Tick:   50 * time.Millisecond,
		// 가짜 상담원은 1초 벨 뒤 받고 6초 통화한 뒤 끊는다
		AgentVars: map[string]string{"__SIM_RING": "1", "__SIM_TALK": "6"},
		Restore:   restore,
		Save: func(recs []rq.AgentRecord) {
			u.mu.Lock()
			u.saved = recs
			u.mu.Unlock()
		},
		Output: func(s string) {
			var l rq.Line
			if json.Unmarshal([]byte(s), &l) == nil {
				u.mu.Lock()
				u.lines = append(u.lines, l)
				u.mu.Unlock()
			}
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); _ = c.Run(ctx) }()
	go func() { defer wg.Done(); _ = u.r.Run(ctx) }()
	var once sync.Once
	u.stop = func() { once.Do(func() { cancel(); wg.Wait() }) }
	t.Cleanup(u.stop)
	select {
	case <-u.r.Synced():
	case <-time.After(30 * time.Second):
		t.Fatal("룸 큐 엔진이 30초 안에 재동기화하지 못함")
	}
	return u
}

func (u *rqUnderTest) cmd(t *testing.T, kind rq.CommandKind, iface, arg string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := u.r.Command(ctx, rq.Command{Kind: kind, Iface: iface, Arg: arg}); err != nil {
		t.Fatal(err)
	}
}

func (u *rqUnderTest) records() []rq.AgentRecord {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.saved
}

func (u *rqUnderTest) mark() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return len(u.lines)
}

// since는 mark 뒤의 줄 중 조건에 맞는 것이다.
func (u *rqUnderTest) since(mark int, pred func(rq.Line) bool) []rq.Line {
	u.mu.Lock()
	defer u.mu.Unlock()
	var out []rq.Line
	for _, l := range u.lines[mark:] {
		if pred(l) {
			out = append(out, l)
		}
	}
	return out
}

func (u *rqUnderTest) wait(t *testing.T, mark, n int, what string, pred func(rq.Line) bool) []rq.Line {
	t.Helper()
	deadline := time.Now().Add(40 * time.Second)
	for time.Now().Before(deadline) {
		if got := u.since(mark, pred); len(got) >= n {
			return got
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("%s %d건을 40초 안에 받지 못함. 엔진 출력:\n%s", what, n, u.dump(mark))
	return nil
}

func (u *rqUnderTest) dump(mark int) string {
	var b strings.Builder
	for _, l := range u.since(mark, func(rq.Line) bool { return true }) {
		b.WriteString(rq.EncodeLine(l) + "\n")
	}
	return b.String()
}

func callEnd(outcome string) func(rq.Line) bool {
	return func(l rq.Line) bool { return l.Kind == "call_end" && l.Outcome == outcome }
}

func agentIs(iface, phase string) func(rq.Line) bool {
	return func(l rq.Line) bool { return l.Kind == "agent" && l.ID == iface && l.Phase == phase }
}

func actionOn(action, iface string) func(rq.Line) bool {
	return func(l rq.Line) bool { return l.Kind == "action" && l.Action == action && l.Iface == iface }
}

func callIn(room string, pred func(rq.Line) bool) func(rq.Line) bool {
	return func(l rq.Line) bool { return l.Kind == "call" && l.ID == room && pred(l) }
}

func TestRoomQueue(t *testing.T) {
	root := repoRoot(t)
	waitFullyBooted(t, root)
	h := newHarness(t, root)
	h.requireNoRealActivity(t)
	u := startRQ(t, nil)
	a, b := simIface("1001"), simIface("1002")
	noAnswer := noAnswerIface

	t.Run("고객 셋·상담원 둘 — 먼저 온 고객부터, 셋째는 기다렸다가 받는다 (녹음 포함)", func(t *testing.T) {
		u.cmd(t, rq.CmdLogin, a, "SIM-1001")
		u.cmd(t, rq.CmdLogin, b, "SIM-1002")
		u.cmd(t, rq.CmdReady, a, "")
		u.cmd(t, rq.CmdReady, b, "")
		mark := u.mark()
		for range 3 {
			h.originateRing(t, "rq-customer", h.number(), 40, 1, 0)
			time.Sleep(400 * time.Millisecond)
		}
		ends := u.wait(t, mark, 3, "completed", callEnd("completed"))
		var longest int64
		for _, e := range ends {
			longest = max(longest, *e.WaitMS)
		}
		if longest < 3000 { // 상담원 둘이 6초씩 통화 + 후처리 2초 → 셋째는 몇 초 기다린다
			t.Errorf("셋째 고객이 기다리지 않았다: 최장 대기 %dms", longest)
		}
		if n := len(u.since(mark, func(l rq.Line) bool { return l.Kind == "warn" })); n != 0 {
			t.Errorf("경고 %d건:\n%s", n, u.dump(mark))
		}
		// 방마다 녹음 파일이 하나씩: <방>-<시각>.wav (시각은 Asterisk가 붙인다)
		for _, e := range ends {
			files, _ := filepath.Glob(filepath.Join(root, "data/asterisk/monitor/rq", e.ID+"-*.wav"))
			if len(files) != 1 {
				t.Errorf("%s 녹음 파일 %d개: %v", e.ID, len(files), files)
				continue
			}
			if st, err := os.Stat(files[0]); err != nil || st.Size() < 1000 {
				t.Errorf("녹음 파일 %s: %v (크기 %d)", files[0], err, st.Size())
			}
		}
		h.waitIdle(t)
	})

	t.Run("무응답 — 받지 않는 상담원 다음 상담원에게 간다", func(t *testing.T) {
		u.cmd(t, rq.CmdNotReady, b, "교육")
		u.cmd(t, rq.CmdLogin, noAnswer, "NOANSWER")
		u.cmd(t, rq.CmdReady, noAnswer, "")
		u.cmd(t, rq.CmdNotReady, a, "잠깐")
		u.cmd(t, rq.CmdReady, a, "") // 1004가 더 오래 쉬었으므로 먼저 불린다
		time.Sleep(2500 * time.Millisecond)
		mark := u.mark()
		h.originateRing(t, "rq-customer", h.number(), 40, 1, 0)
		u.wait(t, mark, 1, "no_answer 경고", func(l rq.Line) bool { return l.Kind == "warn" && l.Code == "no_answer" && l.ID == noAnswer })
		end := u.wait(t, mark, 1, "completed", callEnd("completed"))[0]
		if end.Agents != a {
			t.Errorf("1001이 받아야 한다: %+v", end)
		}
		u.cmd(t, rq.CmdLogout, noAnswer, "")
		h.waitIdle(t)
	})

	t.Run("벨 취소 — 벨이 울리는 사이 고객이 끊으면 엔진이 벨을 끊는다", func(t *testing.T) {
		u.cmd(t, rq.CmdNotReady, a, "회의")
		u.cmd(t, rq.CmdLogin, noAnswer, "NOANSWER")
		u.cmd(t, rq.CmdReady, noAnswer, "")
		mark, amark := u.mark(), h.rec.mark()
		h.originateRing(t, "rq-customer", h.number(), 2, 1, 0) // 고객은 2초 뒤 끊는다. 벨 시간(4초)보다 먼저
		orig := u.wait(t, mark, 1, "1004 Originate", actionOn("Originate", noAnswer))[0]
		hang := u.wait(t, mark, 1, "1004 벨 끊기", actionOn("Hangup", noAnswer))[0]
		if hang.Channel != orig.Channel || !strings.HasPrefix(orig.Channel, orig.Room+"-a") {
			t.Errorf("Originate에 붙인 ChannelId(%q)로 끊어야 한다: %q", orig.Channel, hang.Channel)
		}
		u.wait(t, mark, 1, "abandoned", callEnd("abandoned"))
		// Asterisk에서: 벨이 시간 초과(Reason 3)가 아니라 엔진의 Hangup(Reason 0)으로 끝났다
		resp := h.rec.waitCount(t, amark, 1, "OriginateResponse", field("Uniqueid", orig.Channel))[0]
		if resp.Get("Response") != "Failure" || resp.Get("Reason") != "0" {
			t.Errorf("OriginateResponse = %v", resp)
		}
		time.Sleep(300 * time.Millisecond)
		if w := u.since(mark, func(l rq.Line) bool { return l.Kind == "warn" && l.ID == noAnswer }); len(w) != 0 {
			t.Errorf("고객이 먼저 끊은 것은 무응답이 아니다: %+v", w)
		}
		u.cmd(t, rq.CmdLogout, noAnswer, "")
		h.waitIdle(t)
	})

	t.Run("전환 — 1002가 방에 들어온 뒤 엔진이 1001을 뺀다", func(t *testing.T) {
		u.cmd(t, rq.CmdReady, a, "")
		mark := u.mark()
		h.originateRing(t, "rq-customer", h.number(), 40, 1, 0)
		in := u.wait(t, mark, 1, "1001 통화", agentIs(a, "Talk"))
		u.cmd(t, rq.CmdTransfer, a, b) // 1002는 이석(교육) 중이어도 전환은 받는다
		hang := u.wait(t, mark, 1, "1001 Hangup 액션", actionOn("Hangup", a))
		u.wait(t, mark, 1, "1001 후처리", func(l rq.Line) bool { return l.Kind == "agent" && l.ID == a && l.Mode == "ACW" })
		end := u.wait(t, mark, 1, "completed", callEnd("completed"))[0]
		if end.Agents != a || *end.TalkMS < 6000 {
			t.Errorf("1001이 받고 1002가 이어서 통화해야 한다: %+v", end)
		}
		if !strings.HasPrefix(hang[0].Channel, a+"-") || in[0].Room != hang[0].Room {
			t.Errorf("끊은 채널 %q, 방 %q / %q", hang[0].Channel, in[0].Room, hang[0].Room)
		}
		h.waitIdle(t)
	})

	t.Run("보류 — 고객만 보류 방에서 대기음을 듣다가 돌아온다", func(t *testing.T) {
		u.cmd(t, rq.CmdWrapUp, a, "")
		mark, amark := u.mark(), h.rec.mark()
		h.originateRing(t, "rq-customer", h.number(), 40, 1, 0)
		room := u.wait(t, mark, 1, "1001 통화", agentIs(a, "Talk"))[0].Room
		u.cmd(t, rq.CmdHold, a, "")
		u.wait(t, mark, 1, "보류", callIn(room, func(l rq.Line) bool { return l.Hold }))
		h.rec.waitCount(t, amark, 1, "ConfbridgeJoin", field("Conference", room+"-hold")) // 고객이 보류 방에 들어갔다
		m2 := u.mark()
		u.cmd(t, rq.CmdUnhold, a, "")
		u.wait(t, m2, 1, "보류 해제", callIn(room, func(l rq.Line) bool { return !l.Hold && l.State == "Talking" }))
		// 원래 방에 고객 → 상담원 → 고객(돌아옴) 순서로 들어왔다
		joins := h.rec.waitCount(t, amark, 3, "ConfbridgeJoin", field("Conference", room))
		if !strings.HasPrefix(joins[2].Get("Channel"), "Local/rq-customer@sim-") {
			t.Errorf("세 번째로 원래 방에 들어온 것은 돌아온 고객이어야 한다: %v", joins[2])
		}
		u.wait(t, mark, 1, "completed", callEnd("completed"))
		h.waitIdle(t)
	})

	t.Run("보류 중 상담원이 끊으면 고객은 다시 대기열로", func(t *testing.T) {
		u.cmd(t, rq.CmdWrapUp, a, "")
		mark := u.mark()
		h.originateRing(t, "rq-customer", h.number(), 40, 1, 0)
		room := u.wait(t, mark, 1, "1001 통화", agentIs(a, "Talk"))[0].Room
		u.cmd(t, rq.CmdHold, a, "")
		u.wait(t, mark, 1, "보류", callIn(room, func(l rq.Line) bool { return l.Hold }))
		// 6초 통화가 끝나면 1001이 끊는다 → 엔진이 고객을 원래 방으로 → 다시 대기 → 후처리 2초 뒤 1001이 다시 받는다
		u.wait(t, mark, 1, "보류 해제 액션", func(l rq.Line) bool { return l.Kind == "action" && l.Action == "Unhold" && l.Room == room })
		u.wait(t, mark, 1, "다시 대기", callIn(room, func(l rq.Line) bool { return l.State == "Waiting" && !l.Hold }))
		u.wait(t, mark, 2, "두 번째 Originate", func(l rq.Line) bool { return l.Kind == "action" && l.Action == "Originate" && l.Room == room })
		end := u.wait(t, mark, 1, "completed", callEnd("completed"))[0]
		if end.ID != room || end.Agents != a {
			t.Errorf("call_end = %+v", end)
		}
		h.waitIdle(t)
	})

	t.Run("협의 전환 — 고객을 보류하고 1002와 먼저 통화한 뒤 넘긴다", func(t *testing.T) {
		u.cmd(t, rq.CmdWrapUp, a, "")
		mark, amark := u.mark(), h.rec.mark()
		h.originateRing(t, "rq-customer", h.number(), 40, 1, 0)
		room := u.wait(t, mark, 1, "1001 통화", agentIs(a, "Talk"))[0].Room
		u.cmd(t, rq.CmdConsult, a, b)
		u.wait(t, mark, 1, "1002 통화", agentIs(b, "Talk"))
		u.wait(t, mark, 1, "협의 중 보류", callIn(room, func(l rq.Line) bool { return l.Hold && l.Consult == b }))
		m2 := u.mark()
		u.cmd(t, rq.CmdComplete, a, "")
		u.wait(t, m2, 1, "1001 Hangup 액션", actionOn("Hangup", a))
		u.wait(t, m2, 1, "고객이 1002와", callIn(room, func(l rq.Line) bool { return !l.Hold && l.Agents == b }))
		end := u.wait(t, mark, 1, "completed", callEnd("completed"))[0]
		if end.Agents != a {
			t.Errorf("처음 받은 상담원은 1001: %+v", end)
		}
		// Asterisk에서: 고객이 보류 방에 있는 동안 1002가 원래 방에 들어왔다 (고객 모르게 먼저 통화)
		joins := h.rec.since(amark, "ConfbridgeJoin", func(ami.Message) bool { return true })
		order := []string{}
		for _, j := range joins {
			switch {
			case j.Get("Conference") == room+"-hold":
				order = append(order, "고객→보류방")
			case j.Get("Conference") == room && strings.HasPrefix(j.Get("Channel"), b+"-"):
				order = append(order, "1002→방")
			case j.Get("Conference") == room && strings.HasPrefix(j.Get("Channel"), "Local/rq-customer@sim-"):
				order = append(order, "고객→방")
			}
		}
		if want := []string{"고객→방", "고객→보류방", "1002→방", "고객→방"}; !slices.Equal(order, want) {
			t.Errorf("방 입장 순서 %v, 기대 %v", order, want)
		}
		h.waitIdle(t)
	})

	t.Run("스킬 — sales 콜은 sales 스킬이 있는 상담원에게 간다", func(t *testing.T) {
		u.cmd(t, rq.CmdWrapUp, a, "")
		u.cmd(t, rq.CmdSkills, a, "support")
		u.cmd(t, rq.CmdSkills, b, "sales")
		u.cmd(t, rq.CmdReady, b, "") // 1001이 더 오래 쉬었지만 sales 스킬이 없다
		mark := u.mark()
		h.originateRing(t, "rq-sales", h.number(), 40, 1, 0)
		orig := u.wait(t, mark, 1, "Originate", func(l rq.Line) bool { return l.Kind == "action" && l.Action == "Originate" })[0]
		if orig.Iface != b || !strings.HasPrefix(orig.Room, "rq-sales-") {
			t.Errorf("sales 콜(방 rq-sales-…)은 1002에게: %+v", orig)
		}
		end := u.wait(t, mark, 1, "completed", callEnd("completed"))[0]
		if end.Queue != "sales" || end.Agents != b {
			t.Errorf("call_end = %+v", end)
		}
		u.cmd(t, rq.CmdSkills, a, "")
		u.cmd(t, rq.CmdSkills, b, "")
		h.waitIdle(t)
	})

	t.Run("포기 — 받을 상담원이 없는 동안 고객이 끊는다", func(t *testing.T) {
		u.cmd(t, rq.CmdNotReady, a, "회의")
		u.cmd(t, rq.CmdNotReady, b, "회의")
		mark := u.mark()
		h.originateRing(t, "rq-customer", h.number(), 3, 1, 0)
		end := u.wait(t, mark, 1, "abandoned", callEnd("abandoned"))[0]
		if *end.WaitMS < 2000 {
			t.Errorf("대기 시간 %dms", *end.WaitMS)
		}
		if n := len(u.since(mark, func(l rq.Line) bool { return l.Kind == "action" })); n != 0 {
			t.Errorf("이석 중인데 액션 %d건", n)
		}
		h.waitIdle(t)
	})

	t.Run("재동기화 — 통화 중에 엔진이 다시 시작해도 이어서 관리한다", func(t *testing.T) {
		u.cmd(t, rq.CmdReady, a, "")
		mark := u.mark()
		h.originateRing(t, "rq-customer", h.number(), 40, 1, 0)
		room := u.wait(t, mark, 1, "1001 통화", agentIs(a, "Talk"))[0].Room
		time.Sleep(300 * time.Millisecond) // 저장 고루틴이 마지막 상태를 받게
		recs := u.records()
		u.stop()

		// 엔진이 꺼진 사이 고객이 하나 더 들어온다. RQEnter를 아무도 받지 못한다
		late := h.number()
		h.originateRing(t, "rq-customer", late, 40, 1, 0)
		time.Sleep(time.Second)

		u2 := startRQ(t, recs)
		// 업무 상태는 저장한 것에서, 통화 상태는 채널 목록에서: 1번 콜은 1001과 통화 중, 늦게 온 콜은 대기 중
		u2.wait(t, 0, 1, "되살린 통화", callIn(room, func(l rq.Line) bool { return l.State == "Talking" && l.Agents == a }))
		u2.wait(t, 0, 1, "되살린 대기", func(l rq.Line) bool { return l.Kind == "call" && l.Customer == late && l.State == "Waiting" })
		if b2 := u2.since(0, func(l rq.Line) bool { return l.Kind == "agent" && l.ID == b }); len(b2) == 0 || b2[0].Mode != "NotReady" {
			t.Errorf("1002는 이석(회의)으로 되살아나야 한다: %+v", b2)
		}
		// 1001이 끊으면 1번 콜이 끝나고, 후처리 뒤 1001이 늦게 온 콜을 받는다
		u2.wait(t, 0, 1, "1번 콜 끝", func(l rq.Line) bool { return l.Kind == "call_end" && l.ID == room && l.Outcome == "completed" })
		end := u2.wait(t, 0, 1, "늦게 온 콜 끝", func(l rq.Line) bool { return l.Kind == "call_end" && l.Customer == late })[0]
		if end.Outcome != "completed" || end.Agents != a {
			t.Errorf("call_end = %+v", end)
		}
		if w := u2.since(0, func(l rq.Line) bool { return l.Kind == "warn" }); len(w) != 0 {
			t.Errorf("경고: %+v", w)
		}
		u2.cmd(t, rq.CmdLogout, a, "")
		u2.cmd(t, rq.CmdLogout, b, "")
		h.waitIdle(t)
	})
}
