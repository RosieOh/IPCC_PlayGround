package rq

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"ipcc/engine/ami"
)

// 껍데기(Runner) 테스트: 가짜 AMI 서버로 재동기화·액션 변환·저장·화면 API를 본다 (live 패키지의 테스트와 같은 방식).

const testTimeout = 3 * time.Second

type fakeAsterisk struct {
	t    *testing.T
	conn net.Conn
	r    *ami.Reader
}

func (f *fakeAsterisk) send(kv ...string) {
	f.t.Helper()
	var b strings.Builder
	for i := 0; i+1 < len(kv); i += 2 {
		b.WriteString(kv[i] + ": " + kv[i+1] + "\r\n")
	}
	b.WriteString("\r\n")
	if _, err := f.conn.Write([]byte(b.String())); err != nil {
		f.t.Fatalf("write: %v", err)
	}
}

// userEvent는 dialplan의 UserEvent처럼 보낸다 (Timestamp는 manager.conf의 timestampevents = yes).
func (f *fakeAsterisk) userEvent(name string, kv ...string) {
	f.t.Helper()
	now := time.Now().UnixMicro()
	f.send(append([]string{"Event", "UserEvent", "Timestamp", fmt.Sprintf("%d.%06d", now/1e6, now%1e6), "UserEvent", name}, kv...)...)
}

func (f *fakeAsterisk) expect(name string) ami.Message {
	f.t.Helper()
	_ = f.conn.SetReadDeadline(time.Now().Add(testTimeout))
	m, err := f.r.ReadMessage()
	if err != nil {
		f.t.Fatalf("%s를 기다리다: %v", name, err)
	}
	if m.Get("Action") != name {
		f.t.Fatalf("액션 = %v, 기대 %s", m, name)
	}
	return m
}

func (f *fakeAsterisk) expectNothing(within time.Duration, why string) {
	f.t.Helper()
	_ = f.conn.SetReadDeadline(time.Now().Add(within))
	if m, err := f.r.ReadMessage(); err == nil {
		f.t.Fatalf("%s: 액션을 보냈다 %v", why, m)
	}
}

func (f *fakeAsterisk) ok(m ami.Message, kv ...string) {
	f.t.Helper()
	f.send(append([]string{"Response", "Success", "ActionID", m.ActionID()}, kv...)...)
}

type started struct {
	r     *Runner
	f     *fakeAsterisk
	lines chan string
	mu    sync.Mutex
	saved []AgentRecord
}

func startRunner(t *testing.T, restore []AgentRecord) *started {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	s := &started{lines: make(chan string, 256)}
	p := DefaultPolicy()
	p.Record = true
	c := ami.New(ami.Config{Addr: ln.Addr().String(), Username: "test", Secret: "s", PingInterval: -1, ActionTimeout: testTimeout})
	s.r = NewRunner(c, RunnerConfig{
		Policy: p, Tick: 20 * time.Millisecond, Restore: restore,
		AgentVars: map[string]string{"__SIM_RING": "1"},
		Save: func(recs []AgentRecord) {
			s.mu.Lock()
			s.saved = recs
			s.mu.Unlock()
		},
		Output: func(l string) { s.lines <- l },
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{}, 2)
	go func() { _ = c.Run(ctx); done <- struct{}{} }()
	go func() { _ = s.r.Run(ctx); done <- struct{}{} }()
	t.Cleanup(func() { cancel(); <-done; <-done })

	_ = ln.(*net.TCPListener).SetDeadline(time.Now().Add(testTimeout))
	conn, err := ln.Accept()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	s.f = &fakeAsterisk{t: t, conn: conn, r: ami.NewReader(conn)}
	if _, err := conn.Write([]byte("Asterisk Call Manager/11.0.0\r\n")); err != nil {
		t.Fatal(err)
	}
	s.f.ok(s.f.expect("Login"), "Message", "Authentication accepted")
	return s
}

func (s *started) waitLine(t *testing.T, sub string) string {
	t.Helper()
	deadline := time.After(testTimeout)
	for {
		select {
		case l := <-s.lines:
			if strings.Contains(l, sub) {
				return l
			}
		case <-deadline:
			t.Fatalf("출력에 %s가 나오지 않았다", sub)
			return ""
		}
	}
}

// 엔진이 다시 시작한 상황: 저장된 업무 상태를 되살리고, FullyBooted 뒤 채널 목록으로 진행 중인 방을 읽은 다음에야 분배한다.
func TestRunnerRestoresResyncsThenDispatches(t *testing.T) {
	s := startRunner(t, []AgentRecord{{Iface: agentA, Name: "A", Mode: "Inbound", Skills: map[string]int{"support": 1}}})
	f := s.f
	s.waitLine(t, `"mode":"Inbound"`)

	// 재동기화 전에 들어온 고객: 누가 통화 중인지 아직 모르므로 부르지 않는다
	const cust = "Local/rq-customer@sim-00000004;2"
	f.userEvent("RQEnter", "Channel", cust, "Room", room, "Queue", "support", "Customer", "010")
	f.expectNothing(150*time.Millisecond, "재동기화 전 분배")

	f.send("Event", "FullyBooted", "Privilege", "system,all", "Status", "Fully Booted")
	m := f.expect("CoreShowChannels")
	f.ok(m, "EventList", "start")
	f.send("Event", "CoreShowChannel", "ActionID", m.ActionID(), "Channel", cust, "Uniqueid", "1792983005.19",
		"CallerIDNum", "010", "Application", "ConfBridge", "ApplicationData", room+",rq_bridge,rq_customer", "Duration", "00:00:07")
	f.send("Event", "CoreShowChannelsComplete", "ActionID", m.ActionID(), "EventList", "Complete", "ListItems", "1")
	select {
	case <-s.r.Synced():
	case <-time.After(testTimeout):
		t.Fatal("Synced가 닫히지 않았다")
	}

	orig := f.expect("Originate")
	if orig.Get("Channel") != agentA || !strings.HasPrefix(orig.Get("ChannelId"), room+"-a") ||
		orig.Get("Context") != "rq-agent" || orig.Get("Async") != "true" {
		t.Fatalf("Originate = %v", orig)
	}
	vars := strings.Join(orig.GetAll("Variable"), " ")
	if vars != "RQ_ROOM="+room+" RQ_AGENT="+agentA+" __SIM_RING=1" {
		t.Fatalf("Variable = %q", vars)
	}
	f.ok(orig)

	// 상담원이 방에 들어오면 녹음, 보류 명령은 고객 채널 Redirect
	f.userEvent("RQAgentIn", "Channel", "Local/1001@sim-agent-00000006;1", "Room", room, "Agent", agentA)
	rec := f.expect("ConfbridgeStartRecord")
	if rec.Get("Conference") != room || rec.Get("RecordFile") != "/var/spool/asterisk/monitor/rq/"+room+".wav" {
		t.Fatalf("ConfbridgeStartRecord = %v", rec)
	}
	f.ok(rec)
	if err := s.r.Command(context.Background(), Command{Kind: CmdHold, Iface: agentA}); err != nil {
		t.Fatal(err)
	}
	red := f.expect("Redirect")
	if red.Get("Channel") != cust || red.Get("Context") != "rq-hold" || red.Get("Exten") != "s" {
		t.Fatalf("Redirect = %v", red)
	}
	f.ok(red)

	// 업무 상태는 save 고루틴이 받았다
	deadline := time.Now().Add(testTimeout)
	for {
		s.mu.Lock()
		saved := s.saved
		s.mu.Unlock()
		if len(saved) == 1 && saved[0].Iface == agentA && saved[0].Skills["support"] == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("저장된 상태 = %+v", saved)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestRunnerHTTPEventsAndCommands(t *testing.T) {
	s := startRunner(t, nil)
	f := s.f
	f.send("Event", "FullyBooted", "Privilege", "system,all", "Status", "Fully Booted")
	m := f.expect("CoreShowChannels")
	f.ok(m, "EventList", "start")
	f.send("Event", "CoreShowChannelsComplete", "ActionID", m.ActionID(), "EventList", "Complete", "ListItems", "0")
	<-s.r.Synced()
	if err := s.r.Command(context.Background(), Command{Kind: CmdLogin, Iface: agentA, Arg: "A"}); err != nil {
		t.Fatal(err)
	}
	s.waitLine(t, `"reason":"로그인"`)

	srv := httptest.NewServer(s.r.Handler())
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/api/events", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	sse := bufio.NewReader(resp.Body)
	next := func() (event, data string) { // 빈 줄까지 읽어 SSE 메시지 하나를 돌려준다 (주석·retry는 건너뜀)
		t.Helper()
		for {
			l, err := sse.ReadString('\n')
			if err != nil {
				t.Fatalf("SSE 읽기: %v", err)
			}
			l = strings.TrimRight(l, "\n")
			switch {
			case strings.HasPrefix(l, "event: "):
				event = strings.TrimPrefix(l, "event: ")
			case strings.HasPrefix(l, "data: "):
				data = strings.TrimPrefix(l, "data: ")
			case l == "" && data != "":
				return event, data
			}
		}
	}

	ev, data := next()
	var st stateView
	if err := json.Unmarshal([]byte(data), &st); err != nil {
		t.Fatalf("상태 JSON: %v\n%s", err, data)
	}
	if ev != "state" || !st.Connected || !st.Synced || len(st.Agents) != 1 || st.Since.Mode[agentA] == "" || st.Since.Phase[agentA] == "" ||
		st.Policy.RingMs != 15000 || !st.Policy.Record {
		t.Fatalf("첫 이벤트 = %s %s", ev, data)
	}

	post := func(body string) int {
		t.Helper()
		pr, err := http.Post(srv.URL+"/api/commands", "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		pr.Body.Close()
		return pr.StatusCode
	}
	if code := post(`{"kind":"skills","iface":"` + agentA + `","arg":"sales:1,support:2"}`); code != http.StatusAccepted {
		t.Fatalf("명령 응답 %d", code)
	}
	for {
		if _, data = next(); strings.Contains(data, `"kind":"agent"`) {
			break
		}
	}
	if !strings.Contains(data, `"skills":"sales:1,support:2"`) {
		t.Fatalf("상담원 줄 = %s", data)
	}
	for body, want := range map[string]int{
		`{"kind":"dance","iface":"x"}`:                      http.StatusBadRequest,
		`{"kind":"consult","iface":"` + agentA + `"}`:       http.StatusBadRequest, // 상대 상담원이 없다
		`{"kind":"ready","iface":"` + agentA + `","arg":1}`: http.StatusBadRequest, // arg가 문자열이 아니다
	} {
		if code := post(body); code != want {
			t.Errorf("%s → %d, 기대 %d", body, code, want)
		}
	}
}

func TestParseDuration(t *testing.T) {
	for in, want := range map[string]int64{"00:00:07": 7, "01:02:03": 3723, "": 0, "x": 0} {
		if got := parseDuration(in); got != want {
			t.Errorf("parseDuration(%q) = %d, 기대 %d", in, got, want)
		}
	}
}
