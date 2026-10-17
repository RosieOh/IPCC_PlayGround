package live

import (
	"context"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"ipcc/engine/ami"
	"ipcc/engine/state"
)

const (
	testTimeout = 3 * time.Second
	agent       = "Local/1001@sim-agent"
)

// fakeAsterisk는 접속 하나를 받아 주는 가짜 AMI 서버다.
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

// event는 지금 시각을 Timestamp로 단 이벤트를 보낸다 (manager.conf의 timestampevents = yes와 같다).
func (f *fakeAsterisk) event(name string, kv ...string) {
	f.t.Helper()
	now := time.Now().UnixMicro()
	f.send(append([]string{"Event", name, "Timestamp", fmt.Sprintf("%d.%06d", now/1e6, now%1e6)}, kv...)...)
}

// expect는 클라이언트가 보낸 다음 액션을 읽고 이름을 확인한 뒤 Success로 답한다.
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

func (f *fakeAsterisk) ok(m ami.Message, kv ...string) {
	f.t.Helper()
	f.send(append([]string{"Response", "Success", "ActionID", m.ActionID()}, kv...)...)
}

func start(t *testing.T) (*Runner, *fakeAsterisk, <-chan string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })

	ctx, cancel := context.WithCancel(context.Background())
	c := ami.New(ami.Config{Addr: ln.Addr().String(), Username: "test", Secret: "s", PingInterval: -1, ActionTimeout: testTimeout})
	lines := make(chan string, 256)
	r := New(c, Config{
		Policy: state.Policy{ACWMicros: 300_000, MaxMissed: 2, FinalizeGraceMicros: 2_000_000},
		Tick:   20 * time.Millisecond,
		Output: func(l string) { lines <- l },
	})
	done := make(chan struct{}, 2)
	go func() { _ = c.Run(ctx); done <- struct{}{} }()
	go func() { _ = r.Run(ctx); done <- struct{}{} }()
	t.Cleanup(func() { cancel(); <-done; <-done })

	_ = ln.(*net.TCPListener).SetDeadline(time.Now().Add(testTimeout))
	conn, err := ln.Accept()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	f := &fakeAsterisk{t: t, conn: conn, r: ami.NewReader(conn)}
	if _, err := conn.Write([]byte("Asterisk Call Manager/11.0.0\r\n")); err != nil {
		t.Fatal(err)
	}
	f.ok(f.expect("Login"), "Message", "Authentication accepted")
	return r, f, lines
}

// booted는 부팅이 끝났다고 알린다. 실제 Asterisk는 이미 부팅된 상태면 로그인 응답 바로 뒤에 보낸다 (Timestamp 없음).
func (f *fakeAsterisk) booted() {
	f.t.Helper()
	f.send("Event", "FullyBooted", "Privilege", "system,all", "Uptime", "1", "LastReload", "1", "Status", "Fully Booted")
}

// waitLine은 sub가 들어간 출력 줄을 기다린다.
func waitLine(t *testing.T, lines <-chan string, sub string) string {
	t.Helper()
	deadline := time.After(testTimeout)
	for {
		select {
		case l := <-lines:
			if strings.Contains(l, sub) {
				return l
			}
		case <-deadline:
			t.Fatalf("출력에 %s가 나오지 않았다", sub)
			return ""
		}
	}
}

func TestResyncThenWrapUpTimerUnpauses(t *testing.T) {
	r, f, lines := start(t)

	// 부팅이 끝나기 전에는 재동기화하지 않는다 (app_queue가 아직 없을 수 있다)
	_ = f.conn.SetReadDeadline(time.Now().Add(150 * time.Millisecond))
	if m, err := f.r.ReadMessage(); err == nil {
		t.Fatalf("FullyBooted 전에 액션을 보냈다: %v", m)
	}
	f.booted()

	// 부팅이 끝났으면 재동기화: 채널 목록(비어 있음) → 큐 상태(상담원 1명, 대기 중)
	m := f.expect("CoreShowChannels")
	f.ok(m, "EventList", "start")
	f.send("Event", "CoreShowChannelsComplete", "ActionID", m.ActionID(), "EventList", "Complete", "ListItems", "0")
	m = f.expect("QueueStatus")
	if m.Get("Queue") != "support" {
		t.Fatalf("QueueStatus Queue = %q", m.Get("Queue"))
	}
	f.ok(m, "EventList", "start")
	f.send("Event", "QueueMember", "ActionID", m.ActionID(), "Queue", "support", "Name", "SIM-1001", "Location", agent, "Paused", "0")
	f.send("Event", "QueueStatusComplete", "ActionID", m.ActionID(), "EventList", "Complete", "ListItems", "1")
	waitLine(t, lines, `"code":"resync_adopt"`)
	waitLine(t, lines, `"to":"Ready"`)
	select {
	case <-r.Synced():
	case <-time.After(testTimeout):
		t.Fatal("Synced가 닫히지 않았다")
	}

	// 콜 하나: 연결 → 상담원이 끊음
	f.event("Newchannel", "Uniqueid", "L", "Linkedid", "L", "CallerIDNum", "01012345678")
	f.event("QueueCallerJoin", "Queue", "support", "Uniqueid", "L", "Linkedid", "L", "CallerIDNum", "01012345678")
	f.event("AgentCalled", "Interface", agent, "MemberName", "SIM-1001", "Linkedid", "L")
	f.event("AgentConnect", "Interface", agent, "MemberName", "SIM-1001", "Linkedid", "L")
	f.event("AgentComplete", "Interface", agent, "MemberName", "SIM-1001", "Linkedid", "L", "Reason", "agent")
	f.event("Hangup", "Uniqueid", "L")
	waitLine(t, lines, `"outcome":"completed:agent"`)

	// 엔진이 후처리로 이석시키고, 타이머가 끝나면 대기로 돌린다
	m = f.expect("QueuePause")
	if m.Get("Interface") != agent || m.Get("Paused") != "true" || m.Get("Reason") != "후처리" {
		t.Fatalf("후처리 QueuePause = %v", m)
	}
	f.ok(m)
	began := time.Now()
	m = f.expect("QueuePause")
	if m.Get("Paused") != "false" {
		t.Fatalf("후처리 끝 QueuePause = %v", m)
	}
	if d := time.Since(began); d < 200*time.Millisecond {
		t.Fatalf("후처리 300ms보다 일찍 끝났다: %v", d)
	}
	f.ok(m)

	// 로그아웃 명령 → QueueRemove
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	if err := r.Command(ctx, state.Command{Kind: state.CmdLogout, Iface: agent}); err != nil {
		t.Fatal(err)
	}
	f.ok(f.expect("QueueRemove"))
	snap, err := r.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(snap, `"state":"LoggedOut"`) || !strings.Contains(snap, `"calls":[]`) {
		t.Fatalf("스냅샷 = %s", snap)
	}
}

func TestLoginAddsMemberPausedWithName(t *testing.T) {
	r, f, _ := start(t)
	f.booted()
	for _, name := range []string{"CoreShowChannels", "QueueStatus"} {
		m := f.expect(name)
		f.ok(m, "EventList", "start")
		f.send("Event", name+"Complete", "ActionID", m.ActionID(), "EventList", "Complete", "ListItems", "0")
	}
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	if err := r.Command(ctx, state.Command{Kind: state.CmdLogin, Iface: agent, Arg: "상담원1"}); err != nil {
		t.Fatal(err)
	}
	m := f.expect("QueueAdd")
	if m.Get("Paused") != "true" || m.Get("Reason") != "로그인" || m.Get("MemberName") != "상담원1" {
		t.Fatalf("QueueAdd = %v", m)
	}
	f.ok(m)
}
