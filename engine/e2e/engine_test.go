//go:build e2e

package e2e

import (
	"context"
	"log/slog"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"ipcc/engine/ami"
	"ipcc/engine/live"
	"ipcc/engine/state"
)

// 상태머신 엔진(Stage 2) e2e: 엔진이 상담원 상태의 기준이 되어 Asterisk 큐 멤버를 움직이는지 본다 (D-008).
// 하네스의 AMI 연결은 엔진과 별개라서, 엔진이 보낸 액션의 '결과'를 Asterisk 이벤트로 확인할 수 있다.

const engineACW = 2 * time.Second

// engineUnderTest는 e2e 안에서 띄운 실시간 엔진이다.
type engineUnderTest struct {
	r     *live.Runner
	mu    sync.Mutex
	lines []string
	stop  func()
}

func startEngine(t *testing.T) *engineUnderTest {
	t.Helper()
	c := ami.New(ami.Config{
		Addr:       envOr("AMI_ADDR", "127.0.0.1:5038"),
		Username:   envOr("AMI_USER", "ipcc"),
		Secret:     envOr("AMI_SECRET", "ipcc-secret"),
		BackoffMax: 2 * time.Second, // 재시작 단계에서 빨리 다시 붙도록
		Logger:     slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn})),
	})
	e := &engineUnderTest{}
	policy := state.DefaultPolicy()
	policy.ACWMicros = engineACW.Microseconds()
	e.r = live.New(c, live.Config{
		Queue:  queueName,
		Policy: policy,
		Tick:   50 * time.Millisecond,
		Output: func(l string) {
			e.mu.Lock()
			e.lines = append(e.lines, l)
			e.mu.Unlock()
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); _ = c.Run(ctx) }()
	go func() { defer wg.Done(); _ = e.r.Run(ctx) }()
	var once sync.Once
	e.stop = func() { once.Do(func() { cancel(); wg.Wait() }) }
	t.Cleanup(e.stop)

	select {
	case <-e.r.Synced():
	case <-time.After(30 * time.Second):
		t.Fatal("엔진이 30초 안에 첫 재동기화를 마치지 못함")
	}
	return e
}

func (e *engineUnderTest) cmd(t *testing.T, kind state.CommandKind, iface, arg string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := e.r.Command(ctx, state.Command{Kind: kind, Iface: iface, Arg: arg}); err != nil {
		t.Fatalf("명령: %v", err)
	}
}

// count는 엔진 출력 중 sub가 모두 들어간 줄의 개수다.
func (e *engineUnderTest) count(subs ...string) int {
	e.mu.Lock()
	defer e.mu.Unlock()
	n := 0
next:
	for _, l := range e.lines {
		for _, s := range subs {
			if !strings.Contains(l, s) {
				continue next
			}
		}
		n++
	}
	return n
}

func (e *engineUnderTest) dump() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return strings.Join(e.lines, "\n")
}

// stepEngine: 로그인 → 대기 → 콜 → 후처리(엔진 타이머) → 대기 → 통화 중 이석 예약 → 후처리 뒤 이석 → 로그아웃.
// 상담원 상태는 엔진이 정하고 Asterisk에는 QueueAdd/QueuePause/QueueRemove로 반영한다.
func (h *harness) stepEngine(t *testing.T) {
	e := startEngine(t)
	defer func() {
		if t.Failed() {
			t.Logf("엔진 출력:\n%s", e.dump())
		}
	}()
	iface, name := simIface("1001"), "SIM-1001"
	isAgent := field("Interface", iface)

	mark := h.rec.mark()
	e.cmd(t, state.CmdLogin, iface, name)
	h.rec.waitCount(t, mark, 1, "QueueMemberAdded", all(isAgent, field("Paused", "1"), field("PausedReason", "로그인")))
	e.cmd(t, state.CmdReady, iface, "")
	h.rec.waitCount(t, mark, 1, "QueueMemberPause", all(isAgent, field("Paused", "0")))

	// 콜 1: 끝나면 엔진이 후처리로 이석시키고, 후처리 시간이 지나면 대기로 돌린다
	mark = h.rec.mark()
	h.originateCustomer(t, h.number(), 30)
	h.rec.waitCount(t, mark, 1, "AgentComplete", isAgent)
	acw := h.rec.waitCount(t, mark, 1, "QueueMemberPause", all(isAgent, field("Paused", "1"), field("PausedReason", "후처리")))
	acwAt := time.Now()
	mark = h.rec.mark()
	h.rec.waitCount(t, mark, 1, "QueueMemberPause", all(isAgent, field("Paused", "0")))
	if d := time.Since(acwAt); d < engineACW/2 {
		t.Errorf("후처리가 %v 만에 끝남 (정책 %v)", d, engineACW)
	}
	t.Logf("후처리 이석: %s → %v 뒤 대기", acw[0].Get("PausedReason"), time.Since(acwAt).Round(100*time.Millisecond))

	// 콜 2: 통화 중에 이석(식사)을 누른다 → 통화는 그대로, 후처리가 끝나면 대기 대신 식사 이석
	mark = h.rec.mark()
	h.originate(t, "customer", h.number(), 30, 4)
	h.rec.waitCount(t, mark, 1, "AgentConnect", isAgent)
	e.cmd(t, state.CmdNotReady, iface, "식사")
	h.rec.waitCount(t, mark, 1, "AgentComplete", isAgent)
	h.rec.waitCount(t, mark, 1, "QueueMemberPause", all(isAgent, field("Paused", "1"), field("PausedReason", "식사")))
	if n := len(h.rec.since(mark, "QueueMemberPause", all(isAgent, field("Paused", "0")))); n != 0 {
		t.Errorf("예약한 이석이 있는데 대기로 돌아감 (%d번)", n)
	}

	mark = h.rec.mark()
	e.cmd(t, state.CmdLogout, iface, "")
	h.rec.waitCount(t, mark, 1, "QueueMemberRemoved", isAgent)
	h.waitIdle(t)

	if n := e.count(`"kind":"call_end"`, `"outcome":"completed:agent"`, `"agent":"`+iface+`"`); n != 2 {
		t.Errorf("엔진의 call_end completed:agent = %d건, 기대 2건", n)
	}
	if n := e.count(`"kind":"warn"`); n != 0 {
		t.Errorf("엔진 경고 %d건", n)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	snap, err := e.r.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(snap, `"state":"LoggedOut"`) || !strings.Contains(snap, `"calls":[]`) {
		t.Errorf("마지막 스냅샷 = %s", snap)
	}
}

// stepEngineResync: Asterisk를 재시작하면 큐 멤버가 비워진다 (persistentmembers = no).
// 엔진이 재접속 후 QueueStatus 스냅샷과 자기 상태를 비교해서 상담원을 다시 넣는지 본다.
func (h *harness) stepEngineResync(t *testing.T) {
	e := startEngine(t)
	defer func() {
		if t.Failed() {
			t.Logf("엔진 출력:\n%s", e.dump())
		}
	}()
	iface := simIface("1001")
	isAgent := field("Interface", iface)

	mark := h.rec.mark()
	e.cmd(t, state.CmdLogin, iface, "SIM-1001")
	e.cmd(t, state.CmdReady, iface, "")
	h.rec.waitCount(t, mark, 1, "QueueMemberPause", all(isAgent, field("Paused", "0")))

	before := h.rec.connects()
	if out, err := compose(h.root, "restart", "asterisk"); err != nil {
		t.Fatalf("docker compose restart: %v\n%s", err, out)
	}
	h.rec.waitFor(t, 90*time.Second, "하네스 재접속", func() bool { return h.rec.connects() > before })
	// 엔진이 하네스보다 먼저 붙어 다시 넣었을 수 있다 (그러면 하네스는 QueueMemberAdded를 못 본다).
	// 그래서 이벤트 대신 QueueStatus로 멤버가 돌아왔는지 본다.
	h.rec.waitFor(t, 30*time.Second, "엔진이 상담원을 큐에 다시 넣음", func() bool {
		_, items := h.sendList(t, ami.NewAction("QueueStatus").With("Queue", queueName))
		for _, m := range items {
			if m.Event() == "QueueMember" && m.Get("Location") == iface && m.Get("Paused") == "0" {
				return true
			}
		}
		return false
	})
	if e.count(`"code":"resync_readd"`, iface) == 0 {
		t.Error("엔진 출력에 resync_readd 경고가 없음")
	}

	mark = h.rec.mark()
	e.cmd(t, state.CmdLogout, iface, "")
	h.rec.waitCount(t, mark, 1, "QueueMemberRemoved", isAgent)
}
