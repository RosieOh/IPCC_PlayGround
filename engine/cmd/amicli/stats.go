package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"ipcc/engine/ami"
)

// runStats는 녹화 파일들을 집계한다: 이벤트 종류별 개수, 콜(Linkedid)별 이벤트 수, 이벤트마다 본 필드.
// -md는 docs/10-event-dictionary.md 형식(이벤트 사전)으로 낸다 (make event-dict).
func runStats(args []string) error {
	fs := flag.NewFlagSet("stats", flag.ExitOnError)
	fields := fs.Bool("fields", false, "이벤트마다 본 필드 목록도 보기")
	md := fs.Bool("md", false, "이벤트 사전을 Markdown으로 출력")
	_ = fs.Parse(args)
	if fs.NArg() == 0 {
		return errors.New("녹화 파일이 필요함. 예) amicli stats recordings/ami-*.log")
	}

	st := newRecordingStats()
	for _, path := range fs.Args() {
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		err = st.add(ami.NewReader(f))
		f.Close()
		if err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		st.files = append(st.files, path)
	}
	if *md {
		st.writeMarkdown(os.Stdout)
	} else {
		st.writeText(os.Stdout, *fields)
	}
	return nil
}

type eventStat struct {
	name   string
	count  int
	fields []string // 처음 본 순서
	seen   map[string]bool
}

type callStat struct {
	id          string
	events      int
	channels    map[string]bool
	first, last time.Time
}

type recordingStats struct {
	files                       []string
	messages, events, responses int
	byEvent                     map[string]*eventStat
	calls                       map[string]*callStat
}

func newRecordingStats() *recordingStats {
	return &recordingStats{byEvent: map[string]*eventStat{}, calls: map[string]*callStat{}}
}

// add는 녹화 하나를 끝까지 읽어 집계에 더한다.
func (s *recordingStats) add(r *ami.Reader) error {
	if _, _, err := r.SkipBanner(); err != nil {
		return err
	}
	for {
		m, err := r.ReadMessage()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		s.messages++
		if !m.IsEvent() {
			s.responses++
			continue
		}
		s.events++

		e := s.byEvent[m.Event()]
		if e == nil {
			e = &eventStat{name: m.Event(), seen: map[string]bool{}}
			s.byEvent[m.Event()] = e
		}
		e.count++
		for _, f := range m.Fields {
			if f.Key != "Event" && f.Key != "" && !e.seen[f.Key] {
				e.seen[f.Key] = true
				e.fields = append(e.fields, f.Key)
			}
		}

		if id := m.Get("Linkedid"); id != "" {
			c := s.calls[id]
			if c == nil {
				c = &callStat{id: id, channels: map[string]bool{}}
				s.calls[id] = c
			}
			c.events++
			if ch := m.Get("Channel"); ch != "" {
				c.channels[ch] = true
			}
			if t := eventTime(m, time.Time{}); !t.IsZero() {
				if c.first.IsZero() || t.Before(c.first) {
					c.first = t
				}
				if t.After(c.last) {
					c.last = t
				}
			}
		}
	}
}

// eventsByCount는 많은 순, 같으면 이름순이다.
func (s *recordingStats) eventsByCount() []*eventStat {
	out := make([]*eventStat, 0, len(s.byEvent))
	for _, e := range s.byEvent {
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].count != out[j].count {
			return out[i].count > out[j].count
		}
		return out[i].name < out[j].name
	})
	return out
}

func (s *recordingStats) writeText(w io.Writer, withFields bool) {
	fmt.Fprintf(w, "── 녹화 %d개, 메시지 %d개 (이벤트 %d, 응답 %d)\n\n이벤트 종류\n",
		len(s.files), s.messages, s.events, s.responses)
	for _, e := range s.eventsByCount() {
		fmt.Fprintf(w, "  %5d  %s\n", e.count, e.name)
		if withFields {
			fmt.Fprintf(w, "         %s\n", strings.Join(compressSnapshots(e.fields), ", "))
		}
	}

	calls := make([]*callStat, 0, len(s.calls))
	for _, c := range s.calls {
		calls = append(calls, c)
	}
	sort.Slice(calls, func(i, j int) bool { return calls[i].first.Before(calls[j].first) })
	fmt.Fprintf(w, "\n콜(Linkedid) %d건\n", len(calls))
	for _, c := range calls {
		fmt.Fprintf(w, "  %-16s 이벤트 %3d  채널 %d  %s → %s (%.1f초)\n",
			c.id, c.events, len(c.channels), clock(c.first), clock(c.last), c.last.Sub(c.first).Seconds())
	}
}

func (s *recordingStats) writeMarkdown(w io.Writer) {
	names := make([]string, 0, len(s.byEvent))
	for n := range s.byEvent {
		names = append(names, n)
	}
	sort.Strings(names)

	fmt.Fprintln(w, "# 10. AMI 이벤트 사전 (자동 생성)")
	fmt.Fprintln(w)
	fmt.Fprintf(w, "> `make event-dict`로 다시 만든다. 손으로 고치지 않는다. 원본: 녹화 %d개 (%s), 이벤트 %d개.\n",
		len(s.files), filesByDir(s.files), s.events)
	fmt.Fprintln(w, "> 이 환경(Asterisk 22.11)에서 실제로 본 이벤트와 필드만 있다. 녹화에 없는 이벤트는 `make rx c=\"manager show event 이름\"`으로 본다.")
	fmt.Fprintln(w, ">")
	fmt.Fprintln(w, "> **{채널}** = 채널 스냅샷 필드 14개 (`Channel` `ChannelState` `ChannelStateDesc` `CallerIDNum` `CallerIDName`")
	fmt.Fprintln(w, "> `ConnectedLineNum` `ConnectedLineName` `Language` `AccountCode` `Context` `Exten` `Priority` `Uniqueid` `Linkedid`).")
	fmt.Fprintln(w, "> **{Dest 채널}** = 같은 필드 앞에 `Dest`가 붙은 것 (큐·Dial 이벤트에서 상담원·상대 쪽 채널).")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "| 이벤트 | 횟수 | 엔진 입장의 의미 | 필드 |")
	fmt.Fprintln(w, "|---|---|---|---|")
	for _, n := range names {
		e := s.byEvent[n]
		fmt.Fprintf(w, "| %s | %d | %s | %s |\n", n, e.count, eventMeanings[n], strings.Join(compressSnapshots(e.fields), ", "))
	}
}

// snapshotFields는 Asterisk 12+가 채널 정보를 이벤트에 붙일 때 쓰는 필드 묶음이다.
var snapshotFields = []string{
	"Channel", "ChannelState", "ChannelStateDesc", "CallerIDNum", "CallerIDName",
	"ConnectedLineNum", "ConnectedLineName", "Language", "AccountCode",
	"Context", "Exten", "Priority", "Uniqueid", "Linkedid",
}

// compressSnapshots는 필드 목록에 채널 스냅샷 14개가 다 있으면 {채널}/{Dest 채널} 하나로 줄인다.
func compressSnapshots(fields []string) []string {
	have := map[string]bool{}
	for _, f := range fields {
		have[f] = true
	}
	full := func(prefix string) bool {
		for _, f := range snapshotFields {
			if !have[prefix+f] {
				return false
			}
		}
		return true
	}
	groups := map[string]string{} // 필드 → 대표 표기
	for prefix, label := range map[string]string{"": "{채널}", "Dest": "{Dest 채널}"} {
		if full(prefix) {
			for _, f := range snapshotFields {
				groups[prefix+f] = label
			}
		}
	}
	var out []string
	shown := map[string]bool{}
	for _, f := range fields {
		label, grouped := groups[f]
		if !grouped {
			out = append(out, f)
			continue
		}
		if !shown[label] {
			shown[label] = true
			out = append(out, label)
		}
	}
	return out
}

// filesByDir는 파일 목록을 디렉터리별로 묶는다: "`a/` x, y · `b/` z".
func filesByDir(paths []string) string {
	var dirs []string
	byDir := map[string][]string{}
	for _, p := range paths {
		d := filepath.ToSlash(filepath.Dir(p)) + "/"
		if _, ok := byDir[d]; !ok {
			dirs = append(dirs, d)
		}
		byDir[d] = append(byDir[d], filepath.Base(p))
	}
	parts := make([]string, len(dirs))
	for i, d := range dirs {
		parts[i] = "`" + d + "` " + strings.Join(byDir[d], ", ")
	}
	return strings.Join(parts, " · ")
}

// eventMeanings는 이벤트 사전의 '엔진 입장의 의미' 칸이다. 근거는 docs/05-ami.md의 실측 내용이다.
var eventMeanings = map[string]string{
	"AgentCalled":          "큐가 상담원을 부른다 → 상담원 **벨울림**",
	"AgentComplete":        "상담원 통화가 끝났다 → 상담원 **후처리**. 채널 정보 없이 올 수 있어 `Interface`로 상담원을 찾는다",
	"AgentConnect":         "상담원이 받았다 → 상담원 **통화중**. 고객·상담원 채널 정보가 다 있어 '상담원 ↔ 콜' 짝을 여기서 잡는다",
	"AgentDump":            "안내멘트 중에 상담원이 끊었다",
	"AgentRingNoAnswer":    "상담원이 벨 시간 안에 받지 않았다 → 다시 **대기**, 무응답 횟수 +1. `RingTime`이 **밀리초**다 (AgentConnect는 초)",
	"BridgeCreate":         "채널을 이을 브리지가 생겼다",
	"BridgeDestroy":        "브리지가 사라졌다",
	"BridgeEnter":          "채널이 브리지에 들어갔다. 3개 이상이면 회의",
	"BridgeLeave":          "채널이 브리지에서 나갔다",
	"Cdr":                  "통화 구간 기록 (cdr_manager). 통계 대조용",
	"ChallengeSent":        "보안 이벤트: SIP 인증을 요구했다 (401)",
	"ContactStatus":        "PJSIP 등록 주소 상태 (Reachable, Unreachable 등)",
	"DeviceStateChange":    "장치 상태가 바뀌었다. 큐의 가상 장치까지 나와서 양이 많다. 상담원 상태 판단에 쓰지 않는다",
	"DialBegin":            "Dial(큐 포함)이 상대를 부르기 시작했다",
	"DialEnd":              "Dial이 끝났다 (`DialStatus`: ANSWER, NOANSWER, BUSY …)",
	"DialState":            "Dial 진행 중 상태 (RINGING 등)",
	"FullyBooted":          "Asterisk가 모듈을 다 올렸다. 이미 부팅됐으면 로그인 응답 바로 뒤에(Timestamp 없음), 부팅 중이면 끝날 때 온다. 엔진은 이걸 받고 재동기화한다",
	"Hangup":               "채널이 끊겼다 (`Cause`). 콜의 마지막 채널이면 콜 종료",
	"HangupRequest":        "끊으라는 요청이 왔다 (상대가 끊음 등)",
	"Hold":                 "보류. 보류를 건 쪽(상담원 채널)에 온다. 어텐디드 전환을 시작할 때도 온다 → 상담원 **보류**",
	"LocalBridge":          "Local 채널 쌍(;1/;2)이 서로 이어졌다. 가짜 상담원·가짜 고객 콜에서만 나온다",
	"MusicOnHoldStart":     "대기음 시작 (큐 대기, 보류)",
	"MusicOnHoldStop":      "대기음 끝",
	"NewCallerid":          "채널의 발신번호가 정해졌다. Local 채널은 Newchannel 때 `<unknown>`이라 여기서 확정",
	"NewConnectedLine":     "상대편 번호 정보가 바뀌었다",
	"Newchannel":           "채널(통화 구간)이 생겼다. `Linkedid`로 콜에 붙인다",
	"Newexten":             "다이얼플랜 한 줄을 실행했다. 흐름 추적용, 양이 많다",
	"Newstate":             "채널 상태가 바뀌었다 (`Up` = 응답)",
	"OriginateResponse":    "비동기 Originate의 결과. `Response` 필드가 있지만 **이벤트**다",
	"QueueCallerAbandon":   "고객이 기다리다 끊었다 → **포기호**. 큐 타임아웃 때도 온다: 고객 채널에 끊기 요청(HangupRequest)이 먼저 있었는지로 구분",
	"QueueCallerJoin":      "고객이 큐에 들어왔다 → 대기 시작",
	"QueueCallerLeave":     "고객이 큐에서 나갔다 (연결·포기·타임아웃 모두)",
	"QueueMemberAdded":     "상담원이 큐에 들어왔다 (로그인)",
	"QueueMemberPause":     "상담원 이석(`Paused=1`, `PausedReason`) / 대기(`Paused=0`)",
	"QueueMemberRemoved":   "상담원이 큐에서 빠졌다 (로그아웃)",
	"QueueMemberStatus":    "상담원 장치 상태 (`Status`). 벨이 울리는 중에도 BUSY(3)라 벨울림 판단에 쓰지 않는다",
	"SoftHangupRequest":    "Asterisk 내부에서 끊으라는 요청이 났다",
	"SuccessfulAuth":       "보안 이벤트: 로그인 성공 (다른 AMI 클라이언트 것도 보인다)",
	"Shutdown":             "Asterisk가 종료한다 → 곧 연결이 끊긴다",
	"Unhold":               "보류 해제. 블라인드 전환 때는 Hold 없이 Unhold만 오고, 3자통화로 합쳐질 때는 오지 않는다",
	"BlindTransfer":        "블라인드 호전환. `Linkedid`는 그대로이고 원래 상담원은 AgentComplete `Reason=transfer`",
	"AttendedTransfer":     "어텐디드 호전환. `DestType`: Bridge = 넘기고 빠짐(AgentComplete `Reason=transfer`), Threeway = *3으로 3자통화(엔진이 보류를 푼다)",
	"DTMFBegin":            "키 입력 시작",
	"DTMFEnd":              "키 입력 끝. AMI Atxfer와 PlayDTMF(`Receive=true`)도 DTMF를 흉내 내서 여기 나온다",
	"VarSet":               "채널 변수 설정 (manager.conf eventfilter로 거른다)",
	"QueueMemberPenalty":   "상담원 우선순위가 바뀌었다",
	"QueueMemberRinginuse": "상담원의 ringinuse 설정이 바뀌었다",

	"BridgeMerge":              "두 브리지가 합쳐졌다 (3자통화: 전환용 브리지가 고객이 있는 브리지로 들어간다)",
	"LocalOptimizationBegin":   "Local 채널 쌍을 빼고 양쪽을 직접 잇기 시작한다 (전환 뒤 등). 채널이 바뀌므로 Uniqueid로 따라가면 끊긴다",
	"LocalOptimizationEnd":     "Local 채널 최적화가 끝났다",
	"CoreShowChannel":          "CoreShowChannels(채널 목록)의 항목. 재동기화 스냅샷 재료",
	"CoreShowChannelsComplete": "채널 목록 끝 (`EventList: Complete`). 엔진은 이 자리에서 채널 스냅샷 하나로 묶는다",
	"QueueParams":              "QueueStatus의 큐 정보 항목 (대기 수, 응대·포기 수 등)",
	"QueueMember":              "QueueStatus의 상담원 항목. 실시간 이벤트와 달리 `Name`/`Location`이다",
	"QueueEntry":               "QueueStatus의 대기 고객 항목",
	"QueueStatusComplete":      "QueueStatus 목록 끝. 엔진은 이 자리에서 큐 스냅샷 하나로 묶는다",
}
