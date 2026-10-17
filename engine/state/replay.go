package state

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"ipcc/engine/ami"
)

// Timed는 시각(마이크로초)이 붙은 입력이다.
type Timed struct {
	At    int64
	Input Input
}

// ErrNotUTF8은 녹화나 명령 파일이 UTF-8이 아닐 때 돌려준다 (Rust의 read_to_string이 실패하는 경우와 맞춘다).
var ErrNotUTF8 = errors.New("state: UTF-8이 아님")

// ParseRecording은 녹화(받은 바이트 그대로)를 메시지 목록으로 자른다.
// 첫 줄의 배너는 건너뛰고, 빈 줄로 끝나지 않은 마지막 메시지(잘린 녹화)는 버린다.
func ParseRecording(data []byte) ([]ami.Message, error) {
	if !utf8.Valid(data) {
		return nil, ErrNotUTF8
	}
	r := ami.NewReader(bytes.NewReader(data))
	if _, _, err := r.SkipBanner(); err != nil {
		if errors.Is(err, io.ErrUnexpectedEOF) {
			return nil, nil // 배너 줄조차 끝나지 않은 녹화
		}
		return nil, err
	}
	var msgs []ami.Message
	for {
		m, err := r.ReadMessage()
		switch {
		case err == nil:
			msgs = append(msgs, m)
		case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF):
			return msgs, nil
		default:
			return msgs, err
		}
	}
}

// Grouper는 AMI 메시지를 받은 순서대로 넣으면 상태머신 입력으로 바꾼다. 녹화 재생과 실시간 엔진(live)이 같이 쓴다.
// 응답은 버리고, 목록형 액션의 결과(CoreShowChannels, QueueStatus)는 Complete가 온 자리에서 스냅샷 입력 하나로 묶는다.
// Timestamp가 없는 메시지는 바로 앞 메시지의 시각을 쓴다.
type Grouper struct {
	lists map[string][]ami.Message // 진행 중인 목록 (ActionID → 항목)
	last  int64
}

// Reset은 진행 중인 목록을 버린다 (녹화 파일이 바뀔 때, 연결이 끊겼을 때). 마지막 시각은 남긴다.
func (g *Grouper) Reset() { g.lists = nil }

// Push는 메시지 하나를 넣는다. 상태머신에 넣을 입력이 생기면 ok = true.
func (g *Grouper) Push(m ami.Message) (Timed, bool) {
	if g.lists == nil {
		g.lists = map[string][]ami.Message{}
	}
	if m.IsResponse() {
		if ami.EqualFoldASCII(m.Get("EventList"), "start") {
			g.lists[m.ActionID()] = []ami.Message{}
		}
		return Timed{}, false
	}
	if !m.IsEvent() {
		return Timed{}, false
	}
	at, ok := parseTS(m.Get("Timestamp"))
	if !ok {
		at = g.last
	}
	g.last = at
	aid := m.ActionID()
	if items, listed := g.lists[aid]; aid != "" && listed {
		if !ami.EqualFoldASCII(m.Get("EventList"), "Complete") {
			g.lists[aid] = append(items, m)
			return Timed{}, false
		}
		delete(g.lists, aid)
		switch m.Event() {
		case "CoreShowChannelsComplete":
			return Timed{at, Input{Kind: InputChannelSnapshot, Items: items}}, true
		case "QueueStatusComplete":
			return Timed{at, Input{Kind: InputQueueSnapshot, Items: items}}, true
		}
		return Timed{}, false
	}
	return Timed{at, Input{Kind: InputEvent, Msg: m}}, true
}

// InputsFromRecordings는 녹화들(파일마다 메시지 목록)을 입력 목록으로 바꾼다.
func InputsFromRecordings(files [][]ami.Message) []Timed {
	var out []Timed
	var g Grouper
	for _, msgs := range files {
		g.Reset() // 목록은 파일(연결) 안에서만 이어진다
		for _, m := range msgs {
			if ti, ok := g.Push(m); ok {
				out = append(out, ti)
			}
		}
	}
	return out
}

// ParseCommands는 명령 파일을 읽는다. 한 줄에 "<시각> <명령> <인터페이스> [인자…]".
// 빈 줄과 #으로 시작하는 줄은 건너뛴다. 명령은 login(인자 = 이름), logout, ready, notready(인자 = 사유), wrapup.
func ParseCommands(text string) ([]Timed, error) {
	var out []Timed
	for n, rawLine := range strings.Split(text, "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		f := strings.Fields(line)
		if len(f) < 3 {
			return nil, fmt.Errorf("%d번째 줄: <시각> <명령> <인터페이스>가 필요함", n+1)
		}
		at, ok := parseTS(f[0])
		if !ok {
			return nil, fmt.Errorf("%d번째 줄: 시각이 아님: %s", n+1, f[0])
		}
		var kind CommandKind
		switch f[1] {
		case "login":
			kind = CmdLogin
		case "logout":
			kind = CmdLogout
		case "ready":
			kind = CmdReady
		case "notready":
			kind = CmdNotReady
		case "wrapup":
			kind = CmdWrapUp
		default:
			return nil, fmt.Errorf("%d번째 줄: 모르는 명령: %s", n+1, f[1])
		}
		out = append(out, Timed{at, Input{Kind: InputCommand, Cmd: Command{Kind: kind, Iface: f[2], Arg: strings.Join(f[3:], " ")}}})
	}
	return out, nil
}

// Merge는 명령을 이벤트 사이에 끼운다. 명령은 자기 시각보다 '뒤'인 첫 이벤트 바로 앞에 들어간다 (같은 시각이면 이벤트가 먼저).
func Merge(events, commands []Timed) []Timed {
	out := make([]Timed, 0, len(events)+len(commands))
	i := 0
	for _, e := range events {
		for i < len(commands) && commands[i].At < e.At {
			out = append(out, commands[i])
			i++
		}
		out = append(out, e)
	}
	return append(out, commands[i:]...)
}

// Run은 입력을 순서대로 넣고, 끝에서 남은 타이머를 흘린 뒤 스냅샷 한 줄을 붙인다.
func Run(inputs []Timed, p Policy) []string {
	e := New(p)
	var lines []string
	for _, ti := range inputs {
		for _, o := range e.Apply(ti.At, ti.Input) {
			lines = append(lines, o.Line())
		}
	}
	for _, o := range e.Flush() {
		lines = append(lines, o.Line())
	}
	return append(lines, e.Snapshot().Line())
}
