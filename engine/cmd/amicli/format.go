package main

import (
	"flag"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"ipcc/engine/ami"
)

// filterFlags는 어떤 이벤트를 보여 줄지 정한다.
type filterFlags struct {
	all     bool
	only    string
	verbose bool
	onlySet map[string]bool
}

// noisyEvents는 기본으로 숨기는 이벤트다. 콜 흐름을 보기 어렵게 만들 만큼 자주 나온다.
// (VarSet, RTCP는 manager.conf의 eventfilter로 서버에서 이미 거른다)
var noisyEvents = map[string]bool{
	"Newexten":     true, // 다이얼플랜 한 줄 실행될 때마다
	"VarSet":       true,
	"RTCPSent":     true,
	"RTCPReceived": true,
}

func addFilterFlags(fs *flag.FlagSet) *filterFlags {
	f := &filterFlags{}
	fs.BoolVar(&f.all, "all", false, "기본으로 숨기는 이벤트(Newexten 등)까지 모두 보기")
	fs.StringVar(&f.only, "only", "", "이 이벤트만 보기 (쉼표로 구분). 예) QueueCallerJoin,AgentConnect")
	fs.BoolVar(&f.verbose, "v", false, "모든 필드 보기")
	return f
}

func (f *filterFlags) init() {
	if f.only == "" {
		return
	}
	f.onlySet = map[string]bool{}
	for _, e := range strings.Split(f.only, ",") {
		f.onlySet[strings.TrimSpace(e)] = true
	}
}

func (f *filterFlags) show(event string) bool {
	if f.onlySet != nil {
		return f.onlySet[event]
	}
	return f.all || !noisyEvents[event]
}

// keyFields는 한 줄 요약에 보여 줄 필드다. 이벤트에 있는 것만 이 순서로 찍는다.
var keyFields = []string{
	"Queue", "Position", "Count", "HoldTime", "RingTime", "TalkTime", "Reason",
	"MemberName", "Interface", "Status", "Paused", "PausedReason", "InCall",
	"Channel", "ChannelStateDesc", "CallerIDNum", "CallerIDName", "ConnectedLineNum",
	"Context", "Exten", "Application", "AppData",
	"DestChannel", "DialStatus", "Cause", "Cause-txt",
	"BridgeUniqueid", "BridgeNumChannels",
	"Device", "State", "AOR", "ContactStatus",
	"Response", "Message",
	"Linkedid",
}

// printEvent는 이벤트를 한 줄로 요약한다. verbose면 모든 필드를 찍는다.
func printEvent(w io.Writer, m ami.Message, received time.Time, verbose bool) {
	name := m.Event()
	if name == "" {
		name = "Response:" + m.Get("Response")
	}
	fmt.Fprintf(w, "%s  %-26s", clock(eventTime(m, received)), name)

	if verbose {
		fmt.Fprintln(w)
		for _, f := range m.Fields {
			if strings.EqualFold(f.Key, "Event") {
				continue
			}
			fmt.Fprintf(w, "      %s: %s\n", f.Key, f.Value)
		}
		return
	}
	for _, k := range keyFields {
		v, ok := m.Lookup(k)
		if !ok || v == "" {
			continue
		}
		if k == "Status" && strings.HasPrefix(name, "QueueMember") {
			v = deviceState(v)
		}
		fmt.Fprintf(w, " %s=%s", k, v)
	}
	fmt.Fprintln(w)
}

// printMessage는 메시지를 원래 형식(Key: Value)대로 찍는다.
func printMessage(w io.Writer, m ami.Message) {
	for _, f := range m.Fields {
		if f.Key == "" {
			fmt.Fprintln(w, f.Value)
			continue
		}
		fmt.Fprintf(w, "%s: %s\n", f.Key, f.Value)
	}
	fmt.Fprintln(w)
}

func writeJSONLine(w io.Writer, m ami.Message) {
	// json.Marshal로 감싸면 <, >가 다시 <로 바뀌므로 MarshalJSON을 직접 부른다
	b, err := m.MarshalJSON()
	if err != nil {
		fmt.Fprintf(w, `{"_error":%q}`+"\n", err.Error())
		return
	}
	_, _ = w.Write(append(b, '\n')) // 출력이 파이프로 끊겨도(| head 등) CLI가 할 수 있는 일은 없다
}

// eventTime은 Asterisk가 붙인 Timestamp(유닉스 초.마이크로초)를 쓰고, 없으면 받은 시각을 쓴다.
// manager.conf의 timestampevents = yes 덕분에 모든 이벤트에 붙어 있다.
func eventTime(m ami.Message, received time.Time) time.Time {
	ts := m.Get("Timestamp")
	if ts == "" {
		return received
	}
	secPart, fracPart, _ := strings.Cut(ts, ".")
	sec, err := strconv.ParseInt(secPart, 10, 64)
	if err != nil {
		return received
	}
	var nsec int64
	if fracPart != "" {
		nsec, _ = strconv.ParseInt((fracPart + "000000000")[:9], 10, 64)
	}
	return time.Unix(sec, nsec)
}

func clock(t time.Time) string {
	if t.IsZero() {
		return "--:--:--.---"
	}
	return t.Format("15:04:05.000")
}

// deviceState는 큐 멤버 Status 숫자(enum ast_device_state)에 이름을 붙인다.
func deviceState(s string) string {
	names := map[string]string{
		"0": "UNKNOWN", "1": "NOT_INUSE", "2": "INUSE", "3": "BUSY", "4": "INVALID",
		"5": "UNAVAILABLE", "6": "RINGING", "7": "RINGINUSE", "8": "ONHOLD",
	}
	if n, ok := names[s]; ok {
		return s + "(" + n + ")"
	}
	return s
}

func first(vs ...string) string {
	for _, v := range vs {
		if v != "" {
			return v
		}
	}
	return ""
}
