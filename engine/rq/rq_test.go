package rq

import (
	"strings"
	"testing"
)

// 이벤트 순서와 필드는 실제 Asterisk 녹화에서 가져왔다 (docs/12-room-queue.md 3절).

const (
	agentA = "Local/1001@sim-agent"
	agentB = "Local/1002@sim-agent"
	room   = "rq-1792983005.18"
)

func sec(s float64) int64 { return int64(s * 1_000_000) }

func login(e *Engine, ifaces ...string) {
	for i, f := range ifaces {
		e.HandleCommand(sec(float64(i)*0.1), Command{Kind: CmdLogin, Iface: f, Arg: f})
		e.HandleCommand(sec(float64(i)*0.1+0.01), Command{Kind: CmdReady, Iface: f})
	}
}

func enter(e *Engine, t float64, r, cust string) []Line {
	return e.HandleEvent(sec(t), Event{Name: "RQEnter", Room: r, Queue: "support", Customer: cust})
}
func agentIn(e *Engine, t float64, r, a, ch string) []Line {
	return e.HandleEvent(sec(t), Event{Name: "RQAgentIn", Room: r, Agent: a, Channel: ch})
}
func agentOut(e *Engine, t float64, r, a string) []Line {
	return e.HandleEvent(sec(t), Event{Name: "RQAgentOut", Room: r, Agent: a})
}
func customerOut(e *Engine, t float64, r string) []Line {
	return e.HandleEvent(sec(t), Event{Name: "RQCustomerOut", Room: r})
}
func originateFailed(e *Engine, t float64, ch string) []Line {
	return e.HandleEvent(sec(t), Event{Name: "OriginateResponse", Channel: ch, Response: "Failure"})
}

// find는 조건에 맞는 첫 줄을 돌려준다.
func find(out []Line, kind string, pred func(Line) bool) *Line {
	for i := range out {
		if out[i].Kind == kind && (pred == nil || pred(out[i])) {
			return &out[i]
		}
	}
	return nil
}

func originateTo(iface string) func(Line) bool {
	return func(l Line) bool { return l.Action == "Originate" && l.Iface == iface }
}

func mustState(t *testing.T, e *Engine, iface string, m Mode, p Phase) {
	t.Helper()
	a := e.Agent(iface)
	if a == nil || a.Mode != m || a.Phase != p {
		t.Fatalf("%s = %+v, 기대 %v/%v", iface, a, m, p)
	}
}

func TestNormalCall(t *testing.T) {
	e := New(DefaultPolicy())
	login(e, agentA)

	out := enter(e, 10, room, "01011110001")
	if find(out, "action", originateTo(agentA)) == nil {
		t.Fatalf("대기 중인 상담원을 부르지 않았다: %+v", out)
	}
	mustState(t, e, agentA, Inbound, Ring)
	if c := e.Call(room); c.State != Offering {
		t.Fatalf("콜 상태 %v", c.State)
	}

	agentIn(e, 12.5, room, agentA, "Local/1001@sim-agent-0000000b;1")
	mustState(t, e, agentA, Inbound, Talk)
	if c := e.Call(room); c.State != Talking {
		t.Fatalf("콜 상태 %v", c.State)
	}

	agentOut(e, 15.5, room, agentA) // 상담원이 끊음 → 후처리
	mustState(t, e, agentA, ACW, Idle)
	out = customerOut(e, 15.51, room)
	end := find(out, "call_end", nil)
	if end == nil || end.Outcome != "completed" || *end.WaitMS != 2500 || *end.TalkMS != 3010 || end.Agents != agentA {
		t.Fatalf("call_end = %+v", end)
	}
	if find(out, "action", func(l Line) bool { return l.Action == "Hangup" }) != nil {
		t.Fatal("상담원이 이미 나갔는데 Hangup을 보냈다")
	}

	e.Advance(sec(25.5)) // 후처리 10초
	mustState(t, e, agentA, Inbound, Idle)
}

func TestAbandonWithoutAgents(t *testing.T) {
	e := New(DefaultPolicy())
	enter(e, 1, room, "010")
	if c := e.Call(room); c.State != Waiting {
		t.Fatalf("콜 상태 %v", c.State)
	}
	out := customerOut(e, 5, room)
	if end := find(out, "call_end", nil); end == nil || end.Outcome != "abandoned" || *end.WaitMS != 4000 {
		t.Fatalf("call_end = %+v", end)
	}
}

func TestNoAnswerGoesToNextAgent(t *testing.T) {
	e := New(DefaultPolicy())
	login(e, agentA, agentB) // A가 먼저 대기 → A를 먼저 부른다
	out := enter(e, 10, room, "010")
	if find(out, "action", originateTo(agentA)) == nil {
		t.Fatalf("A를 먼저 불러야 한다: %+v", out)
	}
	// 실패 OriginateResponse의 Channel에는 접미사가 없다 (녹화로 확인)
	out = originateFailed(e, 25, agentA)
	if find(out, "warn", func(l Line) bool { return l.Code == "no_answer" }) == nil || find(out, "action", originateTo(agentB)) == nil {
		t.Fatalf("A 무응답 뒤 B를 불러야 한다: %+v", out)
	}
	mustState(t, e, agentA, Inbound, Idle)
	mustState(t, e, agentB, Inbound, Ring)
	if e.Agent(agentA).missed != 1 {
		t.Fatalf("무응답 %d", e.Agent(agentA).missed)
	}
}

func TestRepeatedNoAnswerSendsAgentToNotReady(t *testing.T) {
	e := New(DefaultPolicy())
	login(e, agentA)
	enter(e, 10, room, "010")
	originateFailed(e, 25, agentA) // 1번째: 다시 대기 → 같은 콜을 다시 받음
	originateFailed(e, 40, agentA) // 2번째: 자동 이석
	mustState(t, e, agentA, NotReady, Idle)
	if a := e.Agent(agentA); a.Reason != "무응답" {
		t.Fatalf("사유 %q", a.Reason)
	}
	if c := e.Call(room); c.State != Waiting {
		t.Fatalf("콜은 다시 기다려야 한다: %v", c.State)
	}
}

// 벨이 울리는 사이 고객이 끊고, 상담원이 뒤늦게 받으면 RQAgentMissed → RQAgentOut 순서로 온다 (녹화로 확인)
func TestLateAgentIsNotPenalized(t *testing.T) {
	e := New(DefaultPolicy())
	login(e, agentA)
	enter(e, 10, room, "010")
	customerOut(e, 12, room)
	e.HandleEvent(sec(15), Event{Name: "RQAgentMissed", Room: room, Agent: agentA})
	agentOut(e, 15.001, room, agentA)
	mustState(t, e, agentA, Inbound, Idle) // 후처리로 가지 않는다
	if e.Agent(agentA).missed != 0 {
		t.Fatal("고객이 먼저 끊은 것은 무응답이 아니다")
	}
}

func TestTransferKicksOriginalAgentAfterTargetJoins(t *testing.T) {
	e := New(DefaultPolicy())
	login(e, agentA)
	e.HandleCommand(sec(1), Command{Kind: CmdLogin, Iface: agentB, Arg: "B"}) // B는 이석(로그인) 상태여도 전환은 받을 수 있다
	enter(e, 10, room, "010")
	agentIn(e, 12, room, agentA, "Local/1001@sim-agent-00000014;1")

	out := e.HandleCommand(sec(14), Command{Kind: CmdTransfer, Iface: agentA, Arg: agentB})
	if find(out, "action", originateTo(agentB)) == nil {
		t.Fatalf("B를 불러야 한다: %+v", out)
	}
	if find(out, "action", func(l Line) bool { return l.Action == "Hangup" }) != nil {
		t.Fatal("B가 들어오기 전에 A를 빼면 고객도 방에서 나간다 (end_marked)")
	}

	out = agentIn(e, 16, room, agentB, "Local/1002@sim-agent-00000015;1")
	h := find(out, "action", func(l Line) bool { return l.Action == "Hangup" })
	if h == nil || h.Channel != "Local/1001@sim-agent-00000014;1" {
		t.Fatalf("B가 들어오면 A 채널을 끊어야 한다: %+v", out)
	}
	agentOut(e, 16.2, room, agentA)
	mustState(t, e, agentA, ACW, Idle)
	mustState(t, e, agentB, NotReady, Talk)
	if c := e.Call(room); c.State != Talking || strings.Join(sortedKeys(c.agents), ",") != agentB {
		t.Fatalf("콜 = %+v", c)
	}
	out = customerOut(e, 30, room)
	if end := find(out, "call_end", nil); end.Agents != agentA || end.Outcome != "completed" {
		t.Fatalf("call_end = %+v (받은 상담원은 A)", end)
	}
	if h := find(out, "action", func(l Line) bool { return l.Action == "Hangup" }); h == nil || h.Iface != agentB {
		t.Fatalf("고객이 끊으면 남은 상담원 B를 끊어야 한다: %+v", out)
	}
}

func TestConferenceKeepsBothAgents(t *testing.T) {
	e := New(DefaultPolicy())
	login(e, agentA)
	e.HandleCommand(sec(1), Command{Kind: CmdLogin, Iface: agentB})
	enter(e, 10, room, "010")
	agentIn(e, 12, room, agentA, "chA")
	e.HandleCommand(sec(13), Command{Kind: CmdConference, Iface: agentA, Arg: agentB})
	out := agentIn(e, 14, room, agentB, "chB")
	if find(out, "action", func(l Line) bool { return l.Action == "Hangup" }) != nil {
		t.Fatal("3자통화는 아무도 빼지 않는다")
	}
	if c := e.Call(room); len(c.agents) != 2 {
		t.Fatalf("방에 상담원 %d명", len(c.agents))
	}
}

// 두 축의 장점: 통화 중에 이석을 누르면 모드만 바뀌고, 통화가 끝나도 후처리·대기로 가지 않는다 (이석 예약이 따로 필요 없다)
func TestNotReadyDuringCall(t *testing.T) {
	e := New(DefaultPolicy())
	login(e, agentA)
	enter(e, 10, room, "010")
	agentIn(e, 12, room, agentA, "chA")
	e.HandleCommand(sec(13), Command{Kind: CmdNotReady, Iface: agentA, Arg: "식사"})
	mustState(t, e, agentA, NotReady, Talk)
	agentOut(e, 20, room, agentA)
	mustState(t, e, agentA, NotReady, Idle)
	customerOut(e, 20.01, room)
	out := enter(e, 30, "rq-2", "011")
	if find(out, "action", nil) != nil {
		t.Fatalf("이석 중인 상담원에게 콜을 주면 안 된다: %+v", out)
	}
}

func TestCustomerHangsUpFirstKicksAgent(t *testing.T) {
	e := New(DefaultPolicy())
	login(e, agentA)
	enter(e, 10, room, "010")
	agentIn(e, 12, room, agentA, "Local/1001@sim-agent-0000000b;1")
	out := customerOut(e, 20, room)
	if h := find(out, "action", func(l Line) bool { return l.Action == "Hangup" }); h == nil || h.Channel != "Local/1001@sim-agent-0000000b;1" {
		t.Fatalf("방에 남은 상담원을 끊어야 한다: %+v", out)
	}
	if end := find(out, "call_end", nil); end.Outcome != "completed" || *end.TalkMS != 8000 {
		t.Fatalf("call_end = %+v", end)
	}
}

func TestOfferTimeoutWithoutResponse(t *testing.T) {
	e := New(DefaultPolicy())
	login(e, agentA, agentB)
	enter(e, 10, room, "010")
	out := e.Advance(sec(10 + 15 + 5)) // 벨 15초 + 여유 5초가 지나도 아무 응답이 없다
	if find(out, "warn", func(l Line) bool { return l.Code == "offer_timeout" }) == nil || find(out, "action", originateTo(agentB)) == nil {
		t.Fatalf("벨 시간 초과 뒤 다음 상담원을 불러야 한다: %+v", out)
	}
}

func TestBaseIface(t *testing.T) {
	for in, want := range map[string]string{
		"Local/1001@sim-agent-0000000b;1": "Local/1001@sim-agent",
		"Local/1004@sim-noanswer":         "Local/1004@sim-noanswer",
		"PJSIP/9002-00000003":             "PJSIP/9002",
		"PJSIP/9002":                      "PJSIP/9002",
	} {
		if got := baseIface(in); got != want {
			t.Errorf("baseIface(%q) = %q, 기대 %q", in, got, want)
		}
	}
}

func TestRoomIsForgottenAfterWrapUp(t *testing.T) {
	e := New(DefaultPolicy())
	login(e, agentA)
	enter(e, 10, room, "010")
	agentIn(e, 12, room, agentA, "chA")
	agentOut(e, 15, room, agentA)
	if a := e.Agent(agentA); a.Room != room {
		t.Fatalf("후처리 중에는 방금 끝난 방을 기억한다: %q", a.Room)
	}
	e.Advance(sec(25))
	if a := e.Agent(agentA); a.Mode != Inbound || a.Room != "" {
		t.Fatalf("후처리가 끝나면 방을 잊는다: %+v", a)
	}
}
