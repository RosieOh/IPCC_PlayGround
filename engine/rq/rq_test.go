package rq

import (
	"strings"
	"testing"
)

// 이벤트 순서와 필드는 실제 Asterisk 녹화에서 가져왔다 (docs/12-room-queue.md 2절).

const (
	agentA = "Local/1001@sim-agent"
	agentB = "Local/1002@sim-agent"
	room   = "rq-support-1792983005.18"
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
	out := enter(e, 30, "rq-support-2", "011")
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

// ── 벨 취소 ───────────────────────────────────────────────────────────────

// 벨이 울리는 사이 고객이 끊으면 엔진이 벨을 끊는다. Originate에 붙인 ChannelId(ringID)로 Hangup한다 (녹화로 확인:
// AMI Hangup은 Uniqueid도 받는다). 뒤늦게 오는 OriginateResponse(Failure, Reason 0, Uniqueid=ringID)는 무시한다.
func TestCustomerHangsUpWhileRingingCancelsRing(t *testing.T) {
	e := New(DefaultPolicy())
	login(e, agentA)
	orig := find(enter(e, 10, room, "010"), "action", originateTo(agentA))
	if orig == nil || !strings.HasPrefix(orig.Channel, room+"-a") {
		t.Fatalf("Originate에 ringID(ChannelId)가 있어야 한다: %+v", orig)
	}
	out := customerOut(e, 12, room)
	h := find(out, "action", func(l Line) bool { return l.Action == "Hangup" })
	if h == nil || h.Channel != orig.Channel || h.Iface != agentA {
		t.Fatalf("벨을 ringID로 끊어야 한다: %+v", out)
	}
	mustState(t, e, agentA, Inbound, Idle)

	// 곧바로 다음 콜이 A에게 간다. 그 뒤에 앞 벨의 실패 응답이 와도 새 벨을 실패로 보면 안 된다
	next := find(enter(e, 12.1, "rq-support-2", "011"), "action", originateTo(agentA))
	out = e.HandleEvent(sec(12.2), Event{Name: "OriginateResponse", Response: "Failure", Channel: agentA, Uniqueid: orig.Channel})
	if len(out) != 0 {
		t.Fatalf("끊은 벨의 응답은 무시해야 한다: %+v", out)
	}
	mustState(t, e, agentA, Inbound, Ring)
	if a := e.Agent(agentA); a.missed != 0 || a.ringID != next.Channel {
		t.Fatalf("상담원 = %+v", a)
	}
}

func TestOriginateFailureIsMatchedByUniqueid(t *testing.T) {
	e := New(DefaultPolicy())
	login(e, agentA, agentB)
	orig := find(enter(e, 10, room, "010"), "action", originateTo(agentA))
	out := e.HandleEvent(sec(25), Event{Name: "OriginateResponse", Response: "Failure", Channel: agentA, Uniqueid: orig.Channel})
	if find(out, "warn", func(l Line) bool { return l.Code == "no_answer" && l.ID == agentA }) == nil ||
		find(out, "action", originateTo(agentB)) == nil {
		t.Fatalf("A 무응답 뒤 B를 불러야 한다: %+v", out)
	}
}

// ── 보류 ──────────────────────────────────────────────────────────────────

const customerCh = "Local/rq-customer@sim-00000004;2"

func talking(t *testing.T, e *Engine) {
	t.Helper()
	e.HandleEvent(sec(10), Event{Name: "RQEnter", Room: room, Queue: "support", Customer: "010", Channel: customerCh})
	agentIn(e, 12, room, agentA, "Local/1001@sim-agent-00000006;1")
	mustState(t, e, agentA, Inbound, Talk)
}

func action(name string) func(Line) bool { return func(l Line) bool { return l.Action == name } }

func TestHoldAndUnhold(t *testing.T) {
	e := New(DefaultPolicy())
	login(e, agentA)
	talking(t, e)
	out := e.HandleCommand(sec(13), Command{Kind: CmdHold, Iface: agentA})
	if h := find(out, "action", action("Hold")); h == nil || h.Channel != customerCh || h.Room != room {
		t.Fatalf("고객 채널을 보류 방으로 옮겨야 한다: %+v", out)
	}
	if e.Call(room).Held {
		t.Fatal("RQHold가 오기 전에는 보류로 보지 않는다")
	}
	out = e.HandleEvent(sec(13.1), Event{Name: "RQHold", Room: room})
	if c := find(out, "call", nil); c == nil || !c.Hold {
		t.Fatalf("call 줄에 hold가 있어야 한다: %+v", out)
	}
	mustState(t, e, agentA, Inbound, Talk) // 상담원은 원래 방에 남아 있다
	out = e.HandleCommand(sec(20), Command{Kind: CmdUnhold, Iface: agentA})
	if find(out, "action", action("Unhold")) == nil {
		t.Fatalf("고객을 원래 방으로: %+v", out)
	}
	e.HandleEvent(sec(20.1), Event{Name: "RQUnhold", Room: room})
	if c := e.Call(room); c.Held || c.State != Talking {
		t.Fatalf("콜 = %+v", c)
	}
}

// 보류 중에 상담원이 끊으면 고객을 원래 방으로 돌려보내고, 고객이 돌아오면 다시 분배한다.
func TestAgentLeavesDuringHoldRequeues(t *testing.T) {
	e := New(DefaultPolicy())
	login(e, agentA, agentB)
	e.HandleCommand(sec(1), Command{Kind: CmdNotReady, Iface: agentB}) // B는 이석: A가 받게
	talking(t, e)
	e.HandleCommand(sec(13), Command{Kind: CmdHold, Iface: agentA})
	e.HandleEvent(sec(13.1), Event{Name: "RQHold", Room: room})
	e.HandleCommand(sec(14), Command{Kind: CmdReady, Iface: agentB})
	out := agentOut(e, 15, room, agentA)
	if find(out, "action", action("Unhold")) == nil || find(out, "action", action("Originate")) != nil {
		t.Fatalf("고객을 돌려보내되, 돌아오기 전에는 부르지 않는다: %+v", out)
	}
	out = e.HandleEvent(sec(15.2), Event{Name: "RQUnhold", Room: room})
	if find(out, "action", originateTo(agentB)) == nil {
		t.Fatalf("고객이 방에 돌아오면 B를 불러야 한다: %+v", out)
	}
}

// ── 협의 전환 ─────────────────────────────────────────────────────────────

func consulting(t *testing.T, e *Engine) {
	t.Helper()
	talking(t, e)
	out := e.HandleCommand(sec(13), Command{Kind: CmdConsult, Iface: agentA, Arg: agentB})
	if find(out, "action", action("Hold")) == nil || find(out, "action", originateTo(agentB)) == nil {
		t.Fatalf("고객을 보류하고 B를 불러야 한다: %+v", out)
	}
	if c := find(out, "call", func(l Line) bool { return l.Consult == agentB }); c == nil {
		t.Fatalf("call 줄에 consult가 있어야 한다: %+v", out)
	}
	e.HandleEvent(sec(13.1), Event{Name: "RQHold", Room: room})
}

func TestConsultThenComplete(t *testing.T) {
	e := New(DefaultPolicy())
	login(e, agentA)
	e.HandleCommand(sec(1), Command{Kind: CmdLogin, Iface: agentB, Arg: "B"})
	consulting(t, e)
	out := e.HandleCommand(sec(13.5), Command{Kind: CmdComplete, Iface: agentA})
	if find(out, "warn", func(l Line) bool { return l.Code == "consult_not_ready" }) == nil {
		t.Fatalf("B가 방에 들어오기 전에는 완료할 수 없다: %+v", out)
	}
	out = agentIn(e, 14, room, agentB, "Local/1002@sim-agent-00000008;1")
	if find(out, "action", action("Hangup")) != nil {
		t.Fatal("협의는 B가 들어와도 A를 빼지 않는다 (둘이 먼저 통화한다)")
	}
	out = e.HandleCommand(sec(20), Command{Kind: CmdComplete, Iface: agentA})
	h := find(out, "action", action("Hangup"))
	if h == nil || h.Channel != "Local/1001@sim-agent-00000006;1" || find(out, "action", action("Unhold")) == nil {
		t.Fatalf("A를 빼고 고객을 돌려보내야 한다: %+v", out)
	}
	out = agentOut(e, 20.1, room, agentA)
	if find(out, "action", action("Unhold")) != nil {
		t.Fatal("이미 보낸 보류 해제를 또 보내면 안 된다")
	}
	e.HandleEvent(sec(20.2), Event{Name: "RQUnhold", Room: room})
	mustState(t, e, agentA, ACW, Idle)
	mustState(t, e, agentB, NotReady, Talk)
	if c := e.Call(room); c.Held || c.consult != "" || strings.Join(sortedKeys(c.agents), ",") != agentB {
		t.Fatalf("콜 = %+v", c)
	}
	if end := find(customerOut(e, 30, room), "call_end", nil); end.Agents != agentA {
		t.Fatalf("call_end = %+v (받은 상담원은 A)", end)
	}
}

func TestConsultCancel(t *testing.T) {
	e := New(DefaultPolicy())
	login(e, agentA)
	e.HandleCommand(sec(1), Command{Kind: CmdLogin, Iface: agentB})
	consulting(t, e)
	agentIn(e, 14, room, agentB, "chB")
	out := e.HandleCommand(sec(16), Command{Kind: CmdCancel, Iface: agentA})
	if h := find(out, "action", action("Hangup")); h == nil || h.Channel != "chB" || find(out, "action", action("Unhold")) == nil {
		t.Fatalf("B를 빼고 고객을 돌려받아야 한다: %+v", out)
	}
	agentOut(e, 16.1, room, agentB)
	e.HandleEvent(sec(16.2), Event{Name: "RQUnhold", Room: room})
	mustState(t, e, agentA, Inbound, Talk)
	if c := e.Call(room); c.Held || strings.Join(sortedKeys(c.agents), ",") != agentA {
		t.Fatalf("콜 = %+v", c)
	}
}

func TestConsultCancelWhileRinging(t *testing.T) {
	e := New(DefaultPolicy())
	login(e, agentA)
	e.HandleCommand(sec(1), Command{Kind: CmdLogin, Iface: agentB})
	consulting(t, e)
	ring := e.Agent(agentB).ringID
	out := e.HandleCommand(sec(14), Command{Kind: CmdCancel, Iface: agentA})
	if h := find(out, "action", action("Hangup")); h == nil || h.Channel != ring {
		t.Fatalf("B의 벨을 끊어야 한다: %+v", out)
	}
	mustState(t, e, agentB, NotReady, Idle)
}

// 협의할 상담원이 받지 않으면 고객을 원래 상담원에게 돌려보낸다. 협의로 부른 상담원은 무응답으로 세지 않는다.
func TestConsultNoAnswerReturnsCustomer(t *testing.T) {
	e := New(DefaultPolicy())
	login(e, agentA, agentB)
	e.HandleCommand(sec(5), Command{Kind: CmdNotReady, Iface: agentB})
	consulting(t, e)
	out := e.HandleEvent(sec(28), Event{Name: "OriginateResponse", Response: "Failure", Uniqueid: e.Agent(agentB).ringID})
	if find(out, "action", action("Unhold")) == nil {
		t.Fatalf("고객을 돌려보내야 한다: %+v", out)
	}
	if e.Agent(agentB).missed != 0 || e.Call(room).consult != "" {
		t.Fatalf("B = %+v, 콜 = %+v", e.Agent(agentB), e.Call(room))
	}
}

// 협의 중 보류를 풀면 고객이 두 상담원이 있는 방으로 돌아온다 = 3자통화.
func TestUnholdDuringConsultMakesThreeWay(t *testing.T) {
	e := New(DefaultPolicy())
	login(e, agentA)
	e.HandleCommand(sec(1), Command{Kind: CmdLogin, Iface: agentB})
	consulting(t, e)
	agentIn(e, 14, room, agentB, "chB")
	out := e.HandleCommand(sec(16), Command{Kind: CmdUnhold, Iface: agentA})
	if find(out, "action", action("Hangup")) != nil || find(out, "action", action("Unhold")) == nil {
		t.Fatalf("아무도 빼지 않고 고객을 돌려보낸다: %+v", out)
	}
	e.HandleEvent(sec(16.1), Event{Name: "RQUnhold", Room: room})
	if c := e.Call(room); c.consult != "" || len(c.agents) != 2 {
		t.Fatalf("콜 = %+v", c)
	}
}

// 보류 중에 일반 전환을 해도 된다: B가 들어오면 A를 빼고, A가 나가면 보류를 풀어 고객을 B와 잇는다.
func TestBlindTransferWhileHeld(t *testing.T) {
	e := New(DefaultPolicy())
	login(e, agentA)
	e.HandleCommand(sec(1), Command{Kind: CmdLogin, Iface: agentB})
	talking(t, e)
	e.HandleCommand(sec(13), Command{Kind: CmdHold, Iface: agentA})
	e.HandleEvent(sec(13.1), Event{Name: "RQHold", Room: room})
	e.HandleCommand(sec(14), Command{Kind: CmdTransfer, Iface: agentA, Arg: agentB})
	agentIn(e, 15, room, agentB, "chB")
	out := agentOut(e, 15.1, room, agentA)
	if find(out, "action", action("Unhold")) == nil {
		t.Fatalf("A가 나가면 고객을 돌려보내 B와 잇는다: %+v", out)
	}
}

// ── 스킬 ──────────────────────────────────────────────────────────────────

func TestSkillsRouteCallsByQueue(t *testing.T) {
	e := New(DefaultPolicy())
	login(e, agentA, agentB) // A가 더 오래 쉬었다
	e.HandleCommand(sec(1), Command{Kind: CmdSkills, Iface: agentA, Arg: "support"})
	out := e.HandleCommand(sec(1.1), Command{Kind: CmdSkills, Iface: agentB, Arg: "sales:1,support:2"})
	if l := find(out, "agent", nil); l == nil || l.Skills != "sales:1,support:2" {
		t.Fatalf("agent 줄에 스킬: %+v", out)
	}
	out = e.HandleEvent(sec(10), Event{Name: "RQEnter", Room: "rq-sales-1", Queue: "sales"})
	if find(out, "action", originateTo(agentB)) == nil {
		t.Fatalf("sales 콜은 sales 스킬이 있는 B에게: %+v", out)
	}
	// B가 sales를 받는 동안 온 sales 콜은 기다리고, 뒤에 온 support 콜은 A가 받는다
	if find(e.HandleEvent(sec(11), Event{Name: "RQEnter", Room: "rq-sales-2", Queue: "sales"}), "action", nil) != nil {
		t.Fatal("support만 하는 A에게 sales 콜을 주면 안 된다")
	}
	if find(e.HandleEvent(sec(12), Event{Name: "RQEnter", Room: "rq-support-3", Queue: "support"}), "action", originateTo(agentA)) == nil {
		t.Fatal("앞의 콜을 받을 상담원이 없어도 뒤의 다른 큐 콜은 분배한다")
	}
}

func TestSkillLevelBeatsIdleTime(t *testing.T) {
	e := New(DefaultPolicy())
	login(e, agentA, agentB) // A가 더 오래 쉬었지만
	e.HandleCommand(sec(1), Command{Kind: CmdSkills, Iface: agentA, Arg: "support:3"})
	e.HandleCommand(sec(1), Command{Kind: CmdSkills, Iface: agentB, Arg: "support:1"})
	if find(enter(e, 10, room, "010"), "action", originateTo(agentB)) == nil {
		t.Fatal("숙련도가 높은(숫자가 작은) B가 먼저다")
	}
}

func TestAgentWithoutSkillsTakesAnyQueueLast(t *testing.T) {
	e := New(DefaultPolicy())
	login(e, agentA, agentB)
	e.HandleCommand(sec(1), Command{Kind: CmdSkills, Iface: agentB, Arg: "support:5"})
	if find(enter(e, 10, room, "010"), "action", originateTo(agentB)) == nil {
		t.Fatal("스킬이 있는 B가 스킬을 정하지 않은 A보다 먼저다")
	}
	if find(e.HandleEvent(sec(11), Event{Name: "RQEnter", Room: "rq-sales-1", Queue: "sales"}), "action", originateTo(agentA)) == nil {
		t.Fatal("스킬을 정하지 않은 A는 모든 큐를 받는다")
	}
}

func TestParseSkills(t *testing.T) {
	got, err := ParseSkills(" support:2, sales ")
	if err != nil || got["support"] != 2 || got["sales"] != 1 || formatSkills(got) != "sales:1,support:2" {
		t.Fatalf("%v %v", got, err)
	}
	if got, err := ParseSkills(""); got != nil || err != nil {
		t.Fatalf("빈 문자열은 모든 큐: %v %v", got, err)
	}
	for _, bad := range []string{"support:0", ":1", "support:x"} {
		if _, err := ParseSkills(bad); err == nil {
			t.Errorf("%q는 오류여야 한다", bad)
		}
	}
}

// ── 녹음 ──────────────────────────────────────────────────────────────────

func TestRecordStartsOncePerRoom(t *testing.T) {
	p := DefaultPolicy()
	p.Record = true
	e := New(p)
	login(e, agentA)
	e.HandleCommand(sec(1), Command{Kind: CmdLogin, Iface: agentB})
	enter(e, 10, room, "010")
	out := agentIn(e, 12, room, agentA, "chA")
	if r := find(out, "action", action("Record")); r == nil || r.Room != room {
		t.Fatalf("첫 상담원이 들어오면 녹음: %+v", out)
	}
	e.HandleCommand(sec(13), Command{Kind: CmdConference, Iface: agentA, Arg: agentB})
	if find(agentIn(e, 14, room, agentB, "chB"), "action", action("Record")) != nil {
		t.Fatal("같은 방에서 녹음을 두 번 시작하면 안 된다")
	}
}

// ── 재시작·재접속 ─────────────────────────────────────────────────────────

func TestRecordsRoundTrip(t *testing.T) {
	e := New(DefaultPolicy())
	login(e, agentA, agentB)
	e.HandleCommand(sec(1), Command{Kind: CmdSkills, Iface: agentA, Arg: "sales:2"})
	e.HandleCommand(sec(2), Command{Kind: CmdNotReady, Iface: agentB, Arg: "교육"})
	enter(e, 10, room, "010")
	agentIn(e, 12, room, agentA, "chA")
	agentOut(e, 15, room, agentA) // A 후처리, 25초에 끝난다
	recs := e.Records()

	e2 := New(DefaultPolicy())
	e2.Restore(sec(20), recs)
	if a := e2.Agent(agentA); a.Mode != ACW || a.Skills["sales"] != 2 || a.Phase != Idle {
		t.Fatalf("A = %+v", a)
	}
	if b := e2.Agent(agentB); b.Mode != NotReady || b.Reason != "교육" {
		t.Fatalf("B = %+v", b)
	}
	e2.Advance(sec(25))
	mustState(t, e2, agentA, Inbound, Idle)

	e3 := New(DefaultPolicy())
	e3.Restore(sec(40), recs) // 꺼져 있는 사이 후처리가 끝났다
	mustState(t, e3, agentA, Inbound, Idle)
}

// 재동기화 입력은 실제 CoreShowChannels 목록에서 가져왔다 (docs/12 2절): 방 안의 고객·상담원은 ConfBridge와
// "방,rq_bridge,프로필", 벨 울리는 상담원은 AppDial2와 Uniqueid=ringID, 보류 중인 고객은 "방-hold,rq_bridge,rq_hold".
func TestResyncRebuildsCallsFromChannels(t *testing.T) {
	const agentC = "PJSIP/9002"
	e := New(DefaultPolicy())
	e.Restore(sec(100), []AgentRecord{
		{Iface: agentA, Name: "A", Mode: "Inbound"},
		{Iface: agentB, Name: "B", Mode: "Inbound"},
		{Iface: agentC, Name: "C", Mode: "NotReady", Reason: "식사"},
	})
	e.HandleEvent(sec(100), Event{Name: "RQEnter", Room: "rq-support-9", Queue: "support"}) // 꺼진 사이 끝난 콜
	e.Unsync()
	if find(e.HandleEvent(sec(100.5), Event{Name: "RQEnter", Room: "rq-sales-7", Queue: "sales"}), "action", nil) != nil {
		t.Fatal("재동기화 전에는 분배하지 않는다 (누가 통화 중인지 모른다)")
	}
	out := e.Resync(sec(101), []Chan{
		// 통화 중: 고객 + 상담원 A
		{Channel: "Local/rq-customer@sim-00000004;1", Uniqueid: "1792991351.8", Application: "Wait", ApplicationData: "40", Seconds: 20},
		{Channel: "Local/rq-customer@sim-00000004;2", Uniqueid: "1792991351.9", CallerIDNum: "01071292759",
			Application: "ConfBridge", ApplicationData: "rq-support-1792991351.8,rq_bridge,rq_customer", Seconds: 20},
		{Channel: "Local/1001@sim-agent-00000006;1", Uniqueid: "rq-support-1792991351.8-a1792991353", Application: "ConfBridge",
			ApplicationData: "rq-support-1792991351.8,rq_bridge,rq_agent", Seconds: 18},
		{Channel: "Local/1001@sim-agent-00000006;2", Uniqueid: "rq-support-1792991351.8-a1792991353;2", Application: "Wait", Seconds: 18},
		// 벨 울리는 중: 고객 + 상담원 C(PJSIP)
		{Channel: "PJSIP/9001-00000010", Uniqueid: "1792991400.20", CallerIDNum: "9001",
			Application: "ConfBridge", ApplicationData: "rq-support-1792991400.20,rq_bridge,rq_customer", Seconds: 5},
		{Channel: "PJSIP/9002-00000011", Uniqueid: "rq-support-1792991400.20-a1792991403", Application: "AppDial2",
			ApplicationData: "(Outgoing Line)", Seconds: 2},
		// 보류 중: 고객은 보류 방, 상담원 B는 원래 방
		{Channel: "Local/rq-customer@sim-00000020;2", Uniqueid: "1792991500.30", CallerIDNum: "01022223333",
			Application: "ConfBridge", ApplicationData: "rq-sales-1792991500.30-hold,rq_bridge,rq_hold", Seconds: 60},
		{Channel: "Local/1002@sim-agent-00000021;1", Uniqueid: "rq-sales-1792991500.30-a1792991502", Application: "ConfBridge",
			ApplicationData: "rq-sales-1792991500.30,rq_bridge,rq_agent", Seconds: 58},
		// 엔진이 꺼진 사이 들어와 기다리는 콜 (RQEnter를 못 받았다)
		{Channel: "Local/rq-customer@sim-00000030;2", Uniqueid: "1792991600.40", CallerIDNum: "01044445555",
			Application: "ConfBridge", ApplicationData: "rq-sales-7,rq_bridge,rq_customer", Seconds: 3},
	})
	if find(out, "warn", func(l Line) bool { return l.Code == "call_gone" && l.ID == "rq-support-9" }) == nil ||
		find(out, "call_end", func(l Line) bool { return l.ID == "rq-support-9" }) == nil {
		t.Fatalf("목록에 없는 콜은 끝낸다: %+v", out)
	}
	mustState(t, e, agentA, Inbound, Talk)
	mustState(t, e, agentB, Inbound, Talk)
	mustState(t, e, agentC, NotReady, Ring)
	if a := e.Agent(agentA); a.channel != "Local/1001@sim-agent-00000006;1" || a.Room != "rq-support-1792991351.8" {
		t.Fatalf("A = %+v", a)
	}
	if c := e.Call("rq-support-1792991351.8"); c.State != Talking || c.Customer != "01071292759" || c.Queue != "support" ||
		c.channel != "Local/rq-customer@sim-00000004;2" || c.enteredAt != sec(81) || c.answeredAt != sec(83) {
		t.Fatalf("통화 중인 콜 = %+v", c)
	}
	if c := e.Call("rq-support-1792991400.20"); c.State != Offering || c.offered != agentC {
		t.Fatalf("벨 울리는 콜 = %+v", c)
	}
	if c := e.Call("rq-sales-1792991500.30"); !c.Held || c.State != Talking || c.Queue != "sales" {
		t.Fatalf("보류 중인 콜 = %+v", c)
	}
	if !e.Synced() || find(out, "action", func(l Line) bool { return l.Action == "Originate" }) != nil {
		t.Fatalf("대기 중인 상담원이 없으니 아무도 부르지 않는다: %+v", out)
	}
	// 재동기화로 알게 된 벨: 실패 응답이 오면 맞춰서 처리한다
	out = e.HandleEvent(sec(110), Event{Name: "OriginateResponse", Response: "Failure", Uniqueid: "rq-support-1792991400.20-a1792991403"})
	if find(out, "warn", func(l Line) bool { return l.Code == "no_answer" && l.ID == agentC }) == nil {
		t.Fatalf("재동기화로 알게 된 벨의 실패: %+v", out)
	}
}

func TestResyncAfterAsteriskRestartEndsEverything(t *testing.T) {
	e := New(DefaultPolicy())
	login(e, agentA)
	enter(e, 10, room, "010")
	agentIn(e, 12, room, agentA, "chA")
	e.Unsync()
	out := e.Resync(sec(30), nil) // Asterisk가 재시작했다: 채널이 하나도 없다
	if end := find(out, "call_end", nil); end == nil || end.Outcome != "completed" {
		t.Fatalf("call_end = %+v", out)
	}
	mustState(t, e, agentA, Inbound, Idle)
}

// 저장된 기록에 없는 상담원이 방에 있으면 이석(재동기화)으로 만들어 둔다.
func TestResyncUnknownAgentInRoom(t *testing.T) {
	e := New(DefaultPolicy())
	e.Resync(sec(10), []Chan{
		{Channel: "Local/rq-customer@sim-00000004;2", CallerIDNum: "010", Application: "ConfBridge",
			ApplicationData: room + ",rq_bridge,rq_customer", Seconds: 5},
		{Channel: "Local/1001@sim-agent-00000006;1", Application: "ConfBridge", ApplicationData: room + ",rq_bridge,rq_agent", Seconds: 3},
	})
	mustState(t, e, agentA, NotReady, Talk)
	if a := e.Agent(agentA); a.Reason != "재동기화" {
		t.Fatalf("A = %+v", a)
	}
}

func TestRoomNames(t *testing.T) {
	if q := queueOf("rq-sales-1792991500.30"); q != "sales" {
		t.Errorf("queueOf = %q", q)
	}
	for uid, want := range map[string]string{
		"rq-support-1792991351.8-a1792991353":   "rq-support-1792991351.8",
		"rq-support-1792991351.8-a1792991353;2": "",
		"1792991351.9":                          "",
		"rq-support-1792991351.8-hold-a12":      "",
	} {
		if got, _ := ringRoom(uid); got != want {
			t.Errorf("ringRoom(%q) = %q, 기대 %q", uid, got, want)
		}
	}
}

func TestParseCommand(t *testing.T) {
	if c, err := ParseCommand("consult", agentA, agentB); err != nil || c.Kind != CmdConsult || c.Arg != agentB {
		t.Fatalf("%+v %v", c, err)
	}
	if c, err := ParseCommand("cancel", agentA, ""); err != nil || c.Kind != CmdCancel {
		t.Fatalf("%+v %v", c, err)
	}
	for _, bad := range [][3]string{{"transfer", agentA, ""}, {"ready", "", ""}, {"dance", agentA, ""}} {
		if _, err := ParseCommand(bad[0], bad[1], bad[2]); err == nil {
			t.Errorf("%v는 오류여야 한다", bad)
		}
	}
	if len(CommandNames) != int(CmdCancel)+1 {
		t.Fatal("CommandNames와 CommandKind 개수가 다르다")
	}
}
