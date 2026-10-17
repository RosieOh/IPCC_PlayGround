package state

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ipcc/engine/ami"
)

// TestGolden은 testdata의 모든 녹화를 재생해서 Rust 레거시가 만든 기대 출력(*.golden.jsonl)과 비교한다.
// 기대 출력은 Go가 아니라 Rust로 만든다 (make golden). Go 쪽에 -update 플래그를 두지 않는 이유는
// '기준'이 Rust이기 때문이다. Go가 다르면 Go를 고친다.
func TestGolden(t *testing.T) {
	// 시나리오 녹화(testdata/*.ami)와 Stage 1에서 만든 녹화(../testdata/*.log)
	recs, _ := filepath.Glob("testdata/*.ami")
	logs, _ := filepath.Glob("../testdata/*.log")
	recs = append(recs, logs...)
	if len(recs) == 0 {
		t.Fatal("녹화가 없다")
	}
	for _, rec := range recs {
		base := filepath.Base(rec)
		name := strings.TrimSuffix(base, filepath.Ext(base))
		t.Run(name, func(t *testing.T) {
			inputs := mustInputs(t, rec)
			compareGolden(t, "testdata/"+name+".golden.jsonl", Run(inputs, DefaultPolicy()))

			cmds := filepath.Join(filepath.Dir(rec), name+".cmds")
			if _, err := os.Stat(cmds); err != nil {
				return
			}
			text, err := os.ReadFile(cmds)
			if err != nil {
				t.Fatal(err)
			}
			c, err := ParseCommands(string(text))
			if err != nil {
				t.Fatal(err)
			}
			compareGolden(t, "testdata/"+name+".cmds.golden.jsonl", Run(Merge(inputs, c), DefaultPolicy()))
		})
	}
}

func mustInputs(t *testing.T, path string) []Timed {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	msgs, err := ParseRecording(data)
	if err != nil {
		t.Fatal(err)
	}
	return InputsFromRecordings([][]ami.Message{msgs})
}

func compareGolden(t *testing.T, path string, got []string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (make golden으로 만든다)", err)
	}
	want := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	for i := 0; i < max(len(want), len(got)); i++ {
		var w, g string
		if i < len(want) {
			w = want[i]
		}
		if i < len(got) {
			g = got[i]
		}
		if w != g {
			t.Fatalf("%s %d번째 줄이 다르다\n Rust: %s\n   Go: %s", path, i+1, w, g)
		}
	}
}

func TestParseRecording(t *testing.T) {
	for _, tc := range []struct {
		name, text string
		events     []string
	}{
		{"배너·CRLF·LF 섞임, 잘린 끝", "Asterisk Call Manager/11.0.0\r\nEvent: A\r\nX: 1\r\n\r\n\nEvent: B\nY:  two\n\nEvent: C\r\nZ: cut", []string{"A", "B"}},
		// Rust 레거시가 처음에 틀렸던 경우: 줄바꿈 하나로 끝났지만 빈 줄이 없다 → 메시지가 끝나지 않았다
		{"줄바꿈 하나로 끝난 잘린 메시지", "Event: A\r\n\r\nEvent: B\r\nInterface: x\r\n", []string{"A"}},
		{"배너만 있고 줄바꿈 없음", "Asterisk Call Manager/11.0.0", nil},
		{"빈 파일", "", nil},
	} {
		msgs, err := ParseRecording([]byte(tc.text))
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		var got []string
		for _, m := range msgs {
			got = append(got, m.Event())
		}
		if strings.Join(got, ",") != strings.Join(tc.events, ",") {
			t.Errorf("%s: 이벤트 = %v, 기대 %v", tc.name, got, tc.events)
		}
	}
	if msgs, _ := ParseRecording([]byte("Event: B\r\nY:  two\r\n\r\n")); msgs[0].Get("y") != " two" {
		t.Errorf("콜론 뒤 공백은 한 칸만 뗀다: %q", msgs[0].Get("y"))
	}
	if _, err := ParseRecording([]byte("Event: \xff\r\n\r\n")); !errors.Is(err, ErrNotUTF8) {
		t.Errorf("UTF-8이 아닌 녹화: err = %v", err)
	}
}

func TestInputsFromRecordingsGroupsSnapshots(t *testing.T) {
	text := "Response: Success\r\nActionID: q1\r\nEventList: start\r\n\r\n" +
		"Event: QueueMember\r\nActionID: q1\r\nLocation: Local/1@a\r\n\r\n" +
		"Event: Newchannel\r\nTimestamp: 7.5\r\nUniqueid: U\r\nLinkedid: U\r\n\r\n" +
		"Event: QueueStatusComplete\r\nActionID: q1\r\nEventList: Complete\r\n\r\n"
	msgs, err := ParseRecording([]byte(text))
	if err != nil {
		t.Fatal(err)
	}
	in := InputsFromRecordings([][]ami.Message{msgs})
	if len(in) != 2 || in[0].Input.Kind != InputEvent || in[1].Input.Kind != InputQueueSnapshot {
		t.Fatalf("입력 = %+v", in)
	}
	if len(in[1].Input.Items) != 1 || in[1].At != 7_500_000 {
		t.Fatalf("스냅샷 항목 %d개, 시각 %d (Timestamp 없는 메시지는 앞 메시지 시각)", len(in[1].Input.Items), in[1].At)
	}
}

func TestParseCommandsAndMerge(t *testing.T) {
	c, err := ParseCommands("# 주석\n\n 2.0 notready Local/1@a 점심 식사 \r\n3 wrapup Local/1@a\n")
	if err != nil {
		t.Fatal(err)
	}
	if len(c) != 2 || c[0].Input.Cmd.Arg != "점심 식사" || c[0].At != 2_000_000 || c[1].Input.Cmd.Kind != CmdWrapUp {
		t.Fatalf("명령 = %+v", c)
	}
	for _, bad := range []string{"1 login", "x login Local/1@a", "1 dance Local/1@a"} {
		if _, err := ParseCommands(bad); err == nil {
			t.Errorf("%q: 에러가 나야 한다", bad)
		}
	}

	ev := func(at int64) Timed { return Timed{At: at, Input: Input{Kind: InputEvent}} }
	merged := Merge([]Timed{ev(1_000_000), ev(2_000_000), ev(5_000_000)}, c)
	var kinds []InputKind
	for _, m := range merged {
		kinds = append(kinds, m.Input.Kind)
	}
	// 같은 시각(2.0)이면 이벤트가 먼저, 명령은 자기 시각보다 뒤인 첫 이벤트 바로 앞
	want := []InputKind{InputEvent, InputEvent, InputCommand, InputCommand, InputEvent}
	for i := range want {
		if i >= len(kinds) || kinds[i] != want[i] {
			t.Fatalf("순서 = %v, 기대 %v", kinds, want)
		}
	}
}

// TestInvariantsHoldOnEveryInput은 모든 녹화를 재생하면서 입력 하나를 처리할 때마다 불변조건을 검사한다 (docs/06 8절).
// 깨지면 그 입력이 버그를 일으킨 지점이다.
func TestInvariantsHoldOnEveryInput(t *testing.T) {
	recs, _ := filepath.Glob("testdata/*.ami")
	logs, _ := filepath.Glob("../testdata/*.log")
	for _, rec := range append(recs, logs...) {
		t.Run(filepath.Base(rec), func(t *testing.T) {
			inputs := mustInputs(t, rec)
			if c, err := os.ReadFile(strings.TrimSuffix(rec, ".ami") + ".cmds"); err == nil {
				cmds, err := ParseCommands(string(c))
				if err != nil {
					t.Fatal(err)
				}
				inputs = Merge(inputs, cmds)
			}
			e := New(DefaultPolicy())
			for i, ti := range inputs {
				e.Apply(ti.At, ti.Input)
				if err := e.checkInvariants(); err != nil {
					t.Fatalf("%d번째 입력(%s) 뒤: %v", i+1, describe(ti.Input), err)
				}
			}
			e.Flush()
			if err := e.checkInvariants(); err != nil {
				t.Fatalf("Flush 뒤: %v", err)
			}
		})
	}
}

func describe(in Input) string {
	switch in.Kind {
	case InputEvent:
		return in.Msg.Event()
	case InputChannelSnapshot:
		return "채널 스냅샷"
	case InputQueueSnapshot:
		return "큐 스냅샷"
	case InputCommand:
		return "명령"
	}
	return "?"
}

// checkInvariants는 상태가 서로 맞는지 본다.
func (e *Engine) checkInvariants() error {
	for _, id := range sortedKeys(e.agents) {
		a := e.agents[id]
		switch a.State {
		case Ringing, Talking, Hold:
			c := e.calls[a.Call]
			if c == nil {
				return fmt.Errorf("상담원 %s(%v)의 콜 %q가 없다", id, a.State, a.Call)
			}
			if a.State == Talking && (c.Agent != id || c.State != Connected) {
				return fmt.Errorf("통화 중 상담원 %s의 콜 %s: 상태 %v, 상담원 %q", id, c.ID, c.State, c.Agent)
			}
			if a.State == Hold && (c.Agent != id || c.State != Held) {
				return fmt.Errorf("보류 중 상담원 %s의 콜 %s: 상태 %v, 상담원 %q", id, c.ID, c.State, c.Agent)
			}
		case LoggedOut, Ready, NotReady:
			if a.Call != "" {
				return fmt.Errorf("상담원 %s(%v)에게 콜 %q가 남아 있다", id, a.State, a.Call)
			}
		case ACW: // 후처리 중에는 방금 끝난 콜을 기억한다 (설계)
		}
		if a.acwDeadline.set != (a.State == ACW) {
			return fmt.Errorf("상담원 %s(%v): 후처리 타이머 %v", id, a.State, a.acwDeadline.set)
		}
	}
	for _, uid := range sortedKeys(e.channels) {
		ch := e.channels[uid]
		if c := e.calls[ch.linkedid]; c == nil || !c.channels[uid] {
			return fmt.Errorf("채널 %s의 콜 %s가 없거나 채널을 모른다", uid, ch.linkedid)
		}
	}
	for _, id := range sortedKeys(e.calls) {
		c := e.calls[id]
		if len(c.channels) == 0 && !c.finalizeDeadline.set {
			return fmt.Errorf("채널이 다 끊긴 콜 %s가 종료 대기도 없이 남아 있다", id)
		}
		if c.Agent != "" {
			if a := e.agents[c.Agent]; a == nil || a.Call != id {
				return fmt.Errorf("콜 %s의 상담원 %s가 이 콜을 맡고 있지 않다", id, c.Agent)
			}
		}
	}
	return nil
}
