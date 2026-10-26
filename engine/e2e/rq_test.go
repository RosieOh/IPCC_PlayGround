//go:build e2e

package e2e

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"ipcc/engine/ami"
	"ipcc/engine/rq"
)

// 룸 큐 e2e: app_queue 없이 엔진이 상담원을 고르고, 고객은 통화마다 생기는 회의방에서 기다린다 (docs/12-room-queue.md).

type rqUnderTest struct {
	r     *rq.Runner
	mu    sync.Mutex
	lines []rq.Line
}

func startRQ(t *testing.T) *rqUnderTest {
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
	u.r = rq.NewRunner(c, rq.RunnerConfig{
		Policy: p,
		Tick:   50 * time.Millisecond,
		// 가짜 상담원은 1초 벨 뒤 받고 4초 통화한 뒤 끊는다
		AgentVars: map[string]string{"__SIM_RING": "1", "__SIM_TALK": "4"},
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
	t.Cleanup(func() { cancel(); wg.Wait() })
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

func TestRoomQueue(t *testing.T) {
	root := repoRoot(t)
	waitFullyBooted(t, root)
	h := newHarness(t, root)
	h.requireNoRealActivity(t)
	u := startRQ(t)
	a, b := simIface("1001"), simIface("1002")
	noAnswer := noAnswerIface

	t.Run("고객 셋·상담원 둘 — 먼저 온 고객부터, 셋째는 기다렸다가 받는다", func(t *testing.T) {
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
		if longest < 3000 { // 상담원 둘이 4초씩 통화 + 후처리 2초 → 셋째는 몇 초 기다린다
			t.Errorf("셋째 고객이 기다리지 않았다: 최장 대기 %dms", longest)
		}
		if n := len(u.since(mark, func(l rq.Line) bool { return l.Kind == "warn" })); n != 0 {
			t.Errorf("경고 %d건:\n%s", n, u.dump(mark))
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

	t.Run("전환 — 1002가 방에 들어온 뒤 엔진이 1001을 뺀다", func(t *testing.T) {
		u.cmd(t, rq.CmdWrapUp, a, "")
		mark := u.mark()
		h.originateRing(t, "rq-customer", h.number(), 40, 1, 0)
		in := u.wait(t, mark, 1, "1001 통화", func(l rq.Line) bool { return l.Kind == "agent" && l.ID == a && l.Phase == "Talk" })
		u.cmd(t, rq.CmdTransfer, a, b) // 1002는 이석(교육) 중이어도 전환은 받는다
		hang := u.wait(t, mark, 1, "1001 Hangup 액션", func(l rq.Line) bool { return l.Kind == "action" && l.Action == "Hangup" && l.Iface == a })
		u.wait(t, mark, 1, "1001 후처리", func(l rq.Line) bool { return l.Kind == "agent" && l.ID == a && l.Mode == "ACW" })
		end := u.wait(t, mark, 1, "completed", callEnd("completed"))[0]
		if end.Agents != a || *end.TalkMS < 4000 {
			t.Errorf("1001이 받고 1002가 이어서 통화해야 한다: %+v", end)
		}
		if !strings.HasPrefix(hang[0].Channel, a+"-") || in[0].Room != hang[0].Room {
			t.Errorf("끊은 채널 %q, 방 %q / %q", hang[0].Channel, in[0].Room, hang[0].Room)
		}
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
		u.cmd(t, rq.CmdLogout, a, "")
		u.cmd(t, rq.CmdLogout, b, "")
		h.waitIdle(t)
	})
}
