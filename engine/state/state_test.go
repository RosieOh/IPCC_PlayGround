package state

import (
	"testing"

	"ipcc/engine/ami"
)

// Rust 레거시(legacy-rust/src/engine.rs)의 단위 테스트를 그대로 옮겼다.

const agent = "Local/1001@sim-agent"

func ev(name string, kv ...string) Input {
	m := ami.Message{Fields: []ami.Field{{Key: "Event", Value: name}}}
	for i := 0; i+1 < len(kv); i += 2 {
		m.Fields = append(m.Fields, ami.Field{Key: kv[i], Value: kv[i+1]})
	}
	return Input{Kind: InputEvent, Msg: m}
}

func cmd(kind CommandKind, arg string) Input {
	return Input{Kind: InputCommand, Cmd: Command{Kind: kind, Iface: agent, Arg: arg}}
}

// connected는 로그인 → 고객 채널 2개(Linkedid L) → 큐 → 벨 → 연결까지 만든다.
func connected(e *Engine) {
	steps := []Input{
		ev("QueueMemberAdded", "Interface", agent, "MemberName", "SIM-1001", "Paused", "0"),
		ev("Newchannel", "Uniqueid", "L", "Linkedid", "L", "CallerIDNum", "01012345678"),
		ev("Newchannel", "Uniqueid", "A", "Linkedid", "L", "CallerIDNum", "<unknown>"),
		ev("QueueCallerJoin", "Queue", "support", "Uniqueid", "L", "Linkedid", "L", "CallerIDNum", "01012345678"),
		ev("AgentCalled", "Interface", agent, "MemberName", "SIM-1001", "Linkedid", "L"),
		ev("AgentConnect", "Interface", agent, "MemberName", "SIM-1001", "Linkedid", "L"),
	}
	for i, s := range steps {
		e.Apply(1_000_000*int64(i+1), s)
	}
}

func has(out []*Out, kind, key, val string) bool {
	for _, o := range out {
		if o.Get("kind") == kind && o.Get(key) == val {
			return true
		}
	}
	return false
}

func TestCompleteAfterAllHangupsStillGetsItsReason(t *testing.T) {
	e := New(DefaultPolicy())
	connected(e)
	e.Apply(10_000_000, ev("Hangup", "Uniqueid", "A"))
	out := e.Apply(10_000_100, ev("Hangup", "Uniqueid", "L"))
	if has(out, "call_end", "id", "L") {
		t.Fatal("AgentComplete를 기다려야 한다")
	}
	// 채널 정보 없는 AgentComplete (실측)
	out = e.Apply(10_000_200, ev("AgentComplete", "Interface", agent, "Reason", "agent"))
	if !has(out, "call_end", "outcome", "completed:agent") {
		t.Fatalf("call_end completed:agent가 없다: %v", lines(out))
	}
	if s := e.Agent(agent).State; s != ACW {
		t.Fatalf("상담원 상태 = %v, 기대 ACW", s)
	}
}

func TestMissingCompleteIsFinalizedByTimer(t *testing.T) {
	e := New(DefaultPolicy())
	connected(e)
	e.Apply(10_000_000, ev("Hangup", "Uniqueid", "A"))
	e.Apply(10_000_000, ev("Hangup", "Uniqueid", "L"))
	out := e.Flush()
	if !has(out, "warn", "code", "complete_missing") || !has(out, "call_end", "outcome", "completed") {
		t.Fatalf("타이머가 콜을 끝내지 않았다: %v", lines(out))
	}
}

func TestAbandonVersusTimeout(t *testing.T) {
	for _, tc := range []struct {
		hangupFirst bool
		want        string
	}{{true, "abandoned"}, {false, "timeout"}} {
		e := New(DefaultPolicy())
		e.Apply(1, ev("Newchannel", "Uniqueid", "L", "Linkedid", "L"))
		e.Apply(2, ev("QueueCallerJoin", "Queue", "support", "Uniqueid", "L", "Linkedid", "L"))
		if tc.hangupFirst {
			e.Apply(3, ev("HangupRequest", "Uniqueid", "L"))
		}
		e.Apply(4, ev("QueueCallerAbandon", "Uniqueid", "L", "Linkedid", "L"))
		out := e.Apply(5, ev("Hangup", "Uniqueid", "L"))
		if !has(out, "call_end", "outcome", tc.want) {
			t.Errorf("기대 %s: %v", tc.want, lines(out))
		}
	}
}

func TestCallerLeavingWhileRingingReturnsAgentWithoutWarning(t *testing.T) {
	e := New(DefaultPolicy())
	e.Apply(1, ev("QueueMemberAdded", "Interface", agent, "Paused", "0"))
	e.Apply(2, ev("Newchannel", "Uniqueid", "L", "Linkedid", "L"))
	e.Apply(3, ev("QueueCallerJoin", "Queue", "support", "Uniqueid", "L", "Linkedid", "L"))
	e.Apply(4, ev("AgentCalled", "Interface", agent, "Linkedid", "L"))
	e.Apply(5, ev("HangupRequest", "Uniqueid", "L"))
	out := e.Apply(6, ev("QueueCallerAbandon", "Uniqueid", "L", "Linkedid", "L"))
	out = append(out, e.Apply(7, ev("Hangup", "Uniqueid", "L"))...)
	if has(out, "warn", "kind", "warn") || !has(out, "call_end", "outcome", "abandoned") {
		t.Fatalf("실측으로 확인한 정상 흐름이다 (경고 없이 포기호): %v", lines(out))
	}
	if a := e.Agent(agent); a.State != Ready || a.missed != 0 {
		t.Fatalf("상태 = %v, 무응답 %d", a.State, a.missed)
	}
}

func TestNotReadyPressedDuringCallAppliesAfterWrapUp(t *testing.T) {
	e := New(DefaultPolicy())
	connected(e)
	e.Apply(7_000_000, cmd(CmdNotReady, "식사"))
	if s := e.Agent(agent).State; s != Talking {
		t.Fatalf("통화는 끊지 않는다: 상태 = %v", s)
	}
	e.Apply(8_000_000, ev("AgentComplete", "Interface", agent, "Reason", "caller"))
	out := e.Flush()
	if a := e.Agent(agent); a.State != NotReady || a.Reason != "식사" {
		t.Fatalf("상태 = %v(%s), 기대 NotReady(식사)", a.State, a.Reason)
	}
	if !has(out, "action", "reason", "식사") {
		t.Fatalf("QueuePause(식사) 액션이 없다: %v", lines(out))
	}
}

func TestRepeatedNoAnswerSendsAgentToNotReady(t *testing.T) {
	e := New(DefaultPolicy())
	e.Apply(1, ev("QueueMemberAdded", "Interface", agent, "Paused", "0"))
	for i := int64(0); i < 2; i++ {
		e.Apply(10+i*2, ev("AgentCalled", "Interface", agent, "Linkedid", "L"))
		e.Apply(11+i*2, ev("AgentRingNoAnswer", "Interface", agent))
	}
	if a := e.Agent(agent); a.State != NotReady || a.Reason != ReasonNoAnswer {
		t.Fatalf("상태 = %v(%s), 기대 NotReady(무응답)", a.State, a.Reason)
	}
}

func TestAdvanceEndsWrapUpWithoutInput(t *testing.T) {
	e := New(DefaultPolicy())
	connected(e)
	e.Apply(8_000_000, ev("AgentComplete", "Interface", agent, "Reason", "caller"))
	if out := e.Advance(17_999_999); len(out) != 0 {
		t.Fatalf("후처리 10초가 아직 안 지났다: %v", lines(out))
	}
	out := e.Advance(18_000_000)
	if !has(out, "action", "action", "QueuePause") || e.Agent(agent).State != Ready {
		t.Fatalf("후처리가 끝나지 않았다: %v", lines(out))
	}
}

func msg(kv ...string) ami.Message {
	var m ami.Message
	for i := 0; i+1 < len(kv); i += 2 {
		m.Fields = append(m.Fields, ami.Field{Key: kv[i], Value: kv[i+1]})
	}
	return m
}

func TestResyncCorrectsChannelsAndPause(t *testing.T) {
	e := New(DefaultPolicy())
	e.Apply(1, ev("QueueMemberAdded", "Interface", agent, "Paused", "0"))
	if out := e.Apply(2, cmd(CmdNotReady, "교육")); !has(out, "action", "reason", "교육") {
		t.Fatalf("대기 → 이석 명령은 QueuePause(true, 사유): %v", lines(out))
	}
	e.Apply(3, ev("Newchannel", "Uniqueid", "X", "Linkedid", "X"))

	// 끊긴 동안 X는 끝났고 Y가 생겼다
	snap := []ami.Message{msg("Event", "CoreShowChannel", "Uniqueid", "Y", "Linkedid", "Y")}
	out := e.Apply(4, Input{Kind: InputChannelSnapshot, Items: snap})
	if !has(out, "warn", "code", "resync_lost_channel") || !has(out, "call_end", "id", "X") ||
		!has(out, "warn", "code", "resync_new_channel") || !has(out, "call", "id", "Y") {
		t.Fatalf("채널 보정: %v", lines(out))
	}

	// Asterisk는 대기(Paused=0)로 알고 있다 → 엔진 상태(이석)대로 다시 보낸다
	snap = []ami.Message{msg("Event", "QueueMember", "Location", agent, "Name", "SIM-1001", "Paused", "0")}
	out = e.Apply(5, Input{Kind: InputQueueSnapshot, Items: snap})
	if !has(out, "warn", "code", "resync_pause") || !has(out, "action", "reason", "교육") {
		t.Fatalf("이석 보정: %v", lines(out))
	}
}

func TestResyncReAddsMemberLostByAsteriskRestart(t *testing.T) {
	e := New(DefaultPolicy())
	e.Apply(1, ev("QueueMemberAdded", "Interface", agent, "Paused", "0"))
	out := e.Apply(2, Input{Kind: InputQueueSnapshot})
	if !has(out, "warn", "code", "resync_readd") || !has(out, "action", "action", "QueueAdd") {
		t.Fatalf("재동기화가 QueueAdd를 내지 않았다: %v", lines(out))
	}
}

// ── Go 쪽에만 있는 테스트 (포팅하면서 생긴 차이를 막는다) ──────────────

// 시각이 거꾸로 가도 now를 되돌리지 않는다 (Rust: ts.max(self.now)).
func TestTimeNeverGoesBackwards(t *testing.T) {
	e := New(DefaultPolicy())
	e.Apply(5_000_000, ev("QueueMemberAdded", "Interface", agent, "Paused", "0"))
	out := e.Apply(1_000_000, cmd(CmdNotReady, ""))
	if len(out) == 0 || out[0].Get("t") != "5.000000" {
		t.Fatalf("과거 시각이 그대로 쓰였다: %v", lines(out))
	}
}

// 타이머는 (마감, 종류, ID) 순서로 처리한다. Go 맵은 순서가 무작위라 정렬을 빠뜨리면 여기서 가끔 깨진다.
func TestTimersFireInDeadlineKindIDOrder(t *testing.T) {
	for range 50 {
		e := New(DefaultPolicy())
		for _, id := range []string{"Local/3@a", "Local/1@a", "Local/2@a"} {
			e.Apply(1, Input{Kind: InputEvent, Msg: ami.Message{Fields: []ami.Field{
				{Key: "Event", Value: "QueueMemberAdded"}, {Key: "Interface", Value: id}, {Key: "Paused", Value: "0"},
			}}})
			e.Apply(2, Input{Kind: InputEvent, Msg: ami.Message{Fields: []ami.Field{
				{Key: "Event", Value: "AgentConnect"}, {Key: "Interface", Value: id},
			}}})
			e.Apply(3, Input{Kind: InputEvent, Msg: ami.Message{Fields: []ami.Field{
				{Key: "Event", Value: "AgentComplete"}, {Key: "Interface", Value: id},
			}}})
		}
		var got []string
		for _, o := range e.Flush() {
			if o.Get("kind") == "agent" {
				got = append(got, o.Get("id"))
			}
		}
		if len(got) != 3 || got[0] != "Local/1@a" || got[1] != "Local/2@a" || got[2] != "Local/3@a" {
			t.Fatalf("후처리 끝 순서 = %v", got)
		}
	}
}

func lines(out []*Out) []string {
	s := make([]string, len(out))
	for i, o := range out {
		s[i] = o.Line()
	}
	return s
}
