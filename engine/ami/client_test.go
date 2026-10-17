package ami

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

const testTimeout = 3 * time.Second

// fakeAMI는 테스트용 가짜 Asterisk다. 접속을 받으면 배너를 보내고, 그다음은 테스트가 직접 조종한다.
type fakeAMI struct {
	t     *testing.T
	ln    net.Listener
	conns chan *fakeConn
}

type fakeConn struct {
	t    *testing.T
	conn net.Conn
	r    *Reader
}

func newFakeAMI(t *testing.T) *fakeAMI {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeAMI{t: t, ln: ln, conns: make(chan *fakeConn, 8)}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			if _, err := conn.Write([]byte("Asterisk Call Manager/11.0.0\r\n")); err != nil {
				conn.Close()
				continue
			}
			select {
			case f.conns <- &fakeConn{t: t, conn: conn, r: NewReader(conn)}:
			default:
				conn.Close()
			}
		}
	}()
	t.Cleanup(func() { ln.Close() })
	return f
}

func (f *fakeAMI) addr() string { return f.ln.Addr().String() }

// accept는 클라이언트의 다음 접속을 기다린다.
func (f *fakeAMI) accept() *fakeConn {
	f.t.Helper()
	select {
	case fc := <-f.conns:
		f.t.Cleanup(func() { fc.conn.Close() })
		return fc
	case <-time.After(testTimeout):
		f.t.Fatal("client did not connect")
		return nil
	}
}

// readAction은 클라이언트가 보낸 액션 하나를 읽는다.
func (fc *fakeConn) readAction() Message {
	fc.t.Helper()
	_ = fc.conn.SetReadDeadline(time.Now().Add(testTimeout))
	m, err := fc.r.ReadMessage()
	if err != nil {
		fc.t.Fatalf("read action: %v", err)
	}
	return m
}

// send는 "Key", "Value" 쌍으로 메시지 하나를 보낸다.
func (fc *fakeConn) send(kv ...string) {
	fc.t.Helper()
	var b strings.Builder
	for i := 0; i+1 < len(kv); i += 2 {
		b.WriteString(kv[i] + ": " + kv[i+1] + "\r\n")
	}
	b.WriteString("\r\n")
	if _, err := fc.conn.Write([]byte(b.String())); err != nil {
		fc.t.Fatalf("write: %v", err)
	}
}

// acceptLogin은 Login 액션을 받아 성공으로 답한다.
func (fc *fakeConn) acceptLogin() {
	fc.t.Helper()
	m := fc.readAction()
	if m.Get("Action") != "Login" || m.Get("Username") != "test" {
		fc.t.Fatalf("first action = %v, want Login", m)
	}
	fc.send("Response", "Success", "ActionID", m.ActionID(), "Message", "Authentication accepted")
}

// startClient는 Client를 띄우고, 멈추고 기다리는 함수를 돌려준다(여러 번 불러도 된다).
func startClient(t *testing.T, addr string, tweak func(*Config)) (*Client, func()) {
	t.Helper()
	cfg := Config{
		Addr:          addr,
		Username:      "test",
		Secret:        "secret",
		PingInterval:  -1,
		ActionTimeout: testTimeout,
		BackoffMin:    10 * time.Millisecond,
		BackoffMax:    50 * time.Millisecond,
	}
	if tweak != nil {
		tweak(&cfg)
	}
	c := New(cfg)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = c.Run(ctx)
	}()
	stop := func() {
		cancel()
		<-done
	}
	t.Cleanup(stop)
	return c, stop
}

// nextEvent는 Events()의 다음 항목을 기다린다.
func nextEvent(t *testing.T, c *Client) Event {
	t.Helper()
	select {
	case ev, ok := <-c.Events():
		if !ok {
			t.Fatal("events channel closed")
		}
		return ev
	case <-time.After(testTimeout):
		t.Fatal("timed out waiting for event")
		return Event{}
	}
}

func expectKind(t *testing.T, c *Client, kind EventKind, connID int) Event {
	t.Helper()
	ev := nextEvent(t, c)
	if ev.Kind != kind || ev.ConnID != connID {
		t.Fatalf("got %v (conn %d, msg %v, err %v), want %v (conn %d)", ev.Kind, ev.ConnID, ev.Msg, ev.Err, kind, connID)
	}
	return ev
}

func TestConnectedComesFirstThenEventsInOrder(t *testing.T) {
	f := newFakeAMI(t)
	c, _ := startClient(t, f.addr(), nil)
	fc := f.accept()
	fc.acceptLogin()
	// 로그인 직후 Asterisk처럼 바로 이벤트를 보낸다. 그래도 Connected가 먼저 나와야 한다.
	fc.send("Event", "FullyBooted", "Status", "Fully Booted")
	fc.send("Event", "Newchannel", "Channel", "PJSIP/1001-00000001")

	if ev := expectKind(t, c, KindConnected, 1); ev.Version != "11.0.0" {
		t.Fatalf("version = %q", ev.Version)
	}
	for _, want := range []string{"FullyBooted", "Newchannel"} {
		if ev := expectKind(t, c, KindAMI, 1); ev.Msg.Event() != want {
			t.Fatalf("event = %q, want %q", ev.Msg.Event(), want)
		}
	}
}

// 응답이 보낸 순서와 다르게 와도 ActionID로 제 짝을 찾아야 한다.
func TestConcurrentActionsMatchByActionID(t *testing.T) {
	f := newFakeAMI(t)
	c, _ := startClient(t, f.addr(), nil)
	fc := f.accept()
	fc.acceptLogin()
	expectKind(t, c, KindConnected, 1)

	const n = 5
	type result struct {
		tag, got string
		err      error
	}
	results := make(chan result, n)
	for i := range n {
		tag := strconv.Itoa(i)
		go func() {
			resp, err := c.Send(context.Background(), NewAction("Ping").With("X-Tag", tag))
			results <- result{tag: tag, got: resp.Get("X-Tag"), err: err}
		}()
	}

	actions := make([]Message, n)
	for i := range n {
		actions[i] = fc.readAction()
	}
	for i := n - 1; i >= 0; i-- { // 역순으로 답한다
		a := actions[i]
		fc.send("Response", "Success", "ActionID", a.ActionID(), "Ping", "Pong", "X-Tag", a.Get("X-Tag"))
	}
	for range n {
		select {
		case r := <-results:
			if r.err != nil {
				t.Fatal(r.err)
			}
			if r.got != r.tag {
				t.Errorf("request %s got the response for %s", r.tag, r.got)
			}
		case <-time.After(testTimeout):
			t.Fatal("Send did not return")
		}
	}
}

func TestSendListCollectsItemsAndKeepsOtherEvents(t *testing.T) {
	f := newFakeAMI(t)
	c, _ := startClient(t, f.addr(), nil)
	fc := f.accept()
	fc.acceptLogin()
	expectKind(t, c, KindConnected, 1)

	type listResult struct {
		items []Message
		err   error
	}
	done := make(chan listResult, 1)
	go func() {
		_, items, err := c.SendList(context.Background(), NewAction("CoreShowChannels"))
		done <- listResult{items, err}
	}()

	id := fc.readAction().ActionID()
	fc.send("Response", "Success", "ActionID", id, "EventList", "start", "Message", "Channels will follow")
	fc.send("Event", "CoreShowChannel", "ActionID", id, "Channel", "PJSIP/1001-00000001")
	fc.send("Event", "Newchannel", "Channel", "PJSIP/1002-00000002") // 목록 사이에 끼어든 실시간 이벤트
	fc.send("Event", "CoreShowChannel", "ActionID", id, "Channel", "Local/customer@sim-00000003;1")
	fc.send("Event", "CoreShowChannelsComplete", "ActionID", id, "EventList", "Complete", "ListItems", "2")

	var r listResult
	select {
	case r = <-done:
	case <-time.After(testTimeout):
		t.Fatal("SendList did not return")
	}
	if r.err != nil {
		t.Fatal(r.err)
	}
	if len(r.items) != 2 || r.items[1].Get("Channel") != "Local/customer@sim-00000003;1" {
		t.Fatalf("items = %v", r.items)
	}
	if ev := expectKind(t, c, KindAMI, 1); ev.Msg.Event() != "Newchannel" {
		t.Fatalf("event stream got %q, want Newchannel", ev.Msg.Event())
	}
	select { // 목록 항목은 이벤트 스트림으로 새면 안 된다
	case ev := <-c.Events():
		t.Fatalf("unexpected extra item %v %q", ev.Kind, ev.Msg.Event())
	case <-time.After(50 * time.Millisecond):
	}
}

// SendListInline은 응답·목록 항목·Complete를 실시간 이벤트와 섞인 '받은 순서 그대로' 이벤트 스트림으로 내보낸다.
// 상태머신이 스냅샷과 실시간 이벤트를 녹화 재생과 같은 순서로 처리하기 위해 필요하다.
func TestSendListInlineKeepsWireOrder(t *testing.T) {
	f := newFakeAMI(t)
	c, _ := startClient(t, f.addr(), nil)
	fc := f.accept()
	fc.acceptLogin()
	expectKind(t, c, KindConnected, 1)

	done := make(chan error, 1)
	go func() {
		resp, err := c.SendListInline(context.Background(), NewAction("CoreShowChannels"))
		if err == nil && resp.Get("EventList") != "start" {
			err = fmt.Errorf("resp = %v", resp)
		}
		done <- err
	}()

	id := fc.readAction().ActionID()
	fc.send("Response", "Success", "ActionID", id, "EventList", "start", "Message", "Channels will follow")
	fc.send("Event", "CoreShowChannel", "ActionID", id, "Uniqueid", "1.1")
	fc.send("Event", "Hangup", "Uniqueid", "1.2") // 목록 사이에 끼어든 실시간 이벤트
	fc.send("Event", "CoreShowChannelsComplete", "ActionID", id, "EventList", "Complete", "ListItems", "1")
	fc.send("Event", "Hangup", "Uniqueid", "1.1") // 목록 뒤의 실시간 이벤트

	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(testTimeout):
		t.Fatal("SendListInline did not return")
	}
	var got []string
	for range 5 {
		ev := expectKind(t, c, KindAMI, 1)
		got = append(got, ev.Msg.Get("Response")+ev.Msg.Event()+ev.Msg.Get("Uniqueid"))
	}
	want := "Success CoreShowChannel1.1 Hangup1.2 CoreShowChannelsComplete Hangup1.1"
	if strings.Join(got, " ") != want {
		t.Fatalf("stream = %q\n want %q", strings.Join(got, " "), want)
	}
}

// 비동기 Originate의 결과(OriginateResponse)는 ActionID가 달린 '이벤트'로 온다.
// 우리 액션이든 다른 세션의 액션이든 이벤트 스트림으로 나가야 한다.
func TestOriginateResponseReachesEventStream(t *testing.T) {
	f := newFakeAMI(t)
	c, _ := startClient(t, f.addr(), nil)
	fc := f.accept()
	fc.acceptLogin()
	expectKind(t, c, KindConnected, 1)

	respc := make(chan error, 1)
	go func() {
		_, err := c.Send(context.Background(), NewAction("Originate").With("Async", "true"))
		respc <- err
	}()
	id := fc.readAction().ActionID()
	fc.send("Response", "Success", "ActionID", id, "Message", "Originate successfully queued")
	if err := <-respc; err != nil {
		t.Fatal(err)
	}
	fc.send("Event", "OriginateResponse", "ActionID", id, "Response", "Success", "Reason", "4")
	fc.send("Event", "OriginateResponse", "ActionID", "other-session-7", "Response", "Failure", "Reason", "0")

	for _, want := range []string{id, "other-session-7"} {
		ev := expectKind(t, c, KindAMI, 1)
		if ev.Msg.Event() != "OriginateResponse" || ev.Msg.ActionID() != want {
			t.Fatalf("got %v, want OriginateResponse for %s", ev.Msg, want)
		}
	}
}

func TestResponseError(t *testing.T) {
	f := newFakeAMI(t)
	c, _ := startClient(t, f.addr(), nil)
	fc := f.accept()
	fc.acceptLogin()
	expectKind(t, c, KindConnected, 1)

	errc := make(chan error, 1)
	go func() {
		_, err := c.Send(context.Background(), NewAction("QueuePause").With("Interface", "PJSIP/9999"))
		errc <- err
	}()
	a := fc.readAction()
	fc.send("Response", "Error", "ActionID", a.ActionID(), "Message", "Interface not found")

	err := <-errc
	var re *ResponseError
	if !errors.As(err, &re) || re.Action != "QueuePause" || re.Message != "Interface not found" {
		t.Fatalf("err = %v, want ResponseError(QueuePause, Interface not found)", err)
	}
}

// 서버가 연결을 끊으면 Disconnected를 내보내고, 다시 접속해서 Connected(2)를 내보내야 한다.
func TestReconnectAfterServerDrop(t *testing.T) {
	f := newFakeAMI(t)
	c, _ := startClient(t, f.addr(), nil)
	fc := f.accept()
	fc.acceptLogin()
	expectKind(t, c, KindConnected, 1)

	fc.conn.Close()
	if ev := expectKind(t, c, KindDisconnected, 1); ev.Err == nil {
		t.Fatal("Disconnected must carry the cause")
	}
	fc2 := f.accept()
	fc2.acceptLogin()
	expectKind(t, c, KindConnected, 2)
}

func TestLoginFailureRetries(t *testing.T) {
	dir := t.TempDir()
	f := newFakeAMI(t)
	c, _ := startClient(t, f.addr(), func(cfg *Config) { cfg.RecordDir = dir })
	fc := f.accept()
	a := fc.readAction()
	fc.send("Response", "Error", "ActionID", a.ActionID(), "Message", "Authentication failed")

	// 로그인에 실패한 연결은 Connected를 내보내지 않고, 다시 접속을 시도한다
	fc2 := f.accept()
	fc2.acceptLogin()
	expectKind(t, c, KindConnected, 2)

	// 녹화 파일은 로그인에 성공한 연결(c2)에만 생겨야 한다
	files, _ := filepath.Glob(filepath.Join(dir, "ami-*.log"))
	if len(files) != 1 || !strings.HasSuffix(files[0], "-c2.log") {
		t.Fatalf("recordings = %v, want only the c2 file", files)
	}
}

// 응답을 기다리던 중에 끊기면 타임아웃까지 기다리지 말고 바로 실패해야 한다.
func TestPendingActionFailsFastOnDisconnect(t *testing.T) {
	f := newFakeAMI(t)
	c, _ := startClient(t, f.addr(), func(cfg *Config) { cfg.ActionTimeout = 30 * time.Second })
	fc := f.accept()
	fc.acceptLogin()
	expectKind(t, c, KindConnected, 1)

	errc := make(chan error, 1)
	go func() {
		_, err := c.Send(context.Background(), NewAction("Ping"))
		errc <- err
	}()
	fc.readAction()
	fc.conn.Close() // 응답하지 않고 끊는다

	select {
	case err := <-errc:
		if !errors.Is(err, ErrDisconnected) {
			t.Fatalf("err = %v, want ErrDisconnected", err)
		}
	case <-time.After(testTimeout):
		t.Fatal("Send waited for the full action timeout")
	}
}

// TCP는 살아 있는데 Asterisk가 답하지 않는 상황(docker pause 등)은 Ping으로만 알 수 있다.
func TestPingTimeoutForcesReconnect(t *testing.T) {
	f := newFakeAMI(t)
	c, _ := startClient(t, f.addr(), func(cfg *Config) {
		cfg.PingInterval = 30 * time.Millisecond
		cfg.PingTimeout = 50 * time.Millisecond
	})
	fc := f.accept()
	fc.acceptLogin()
	expectKind(t, c, KindConnected, 1)

	// 가짜 서버는 Ping에 답하지 않는다
	ev := expectKind(t, c, KindDisconnected, 1)
	if ev.Err == nil || !strings.Contains(ev.Err.Error(), "ping") {
		t.Fatalf("cause = %v, want ping failure", ev.Err)
	}
	fc2 := f.accept()
	fc2.acceptLogin()
	expectKind(t, c, KindConnected, 2)
}

// MD5 챌린지 로그인: 비밀번호 대신 MD5(challenge + secret)만 보낸다.
func TestMD5ChallengeLogin(t *testing.T) {
	f := newFakeAMI(t)
	c, _ := startClient(t, f.addr(), func(cfg *Config) { cfg.AuthMD5 = true })
	fc := f.accept()

	ch := fc.readAction()
	if ch.Get("Action") != "Challenge" || ch.Get("AuthType") != "MD5" {
		t.Fatalf("first action = %v, want Challenge AuthType=MD5", ch)
	}
	fc.send("Response", "Success", "ActionID", ch.ActionID(), "Challenge", "840415273")

	login := fc.readAction()
	if _, ok := login.Lookup("Secret"); ok {
		t.Fatal("MD5 login must not send the secret")
	}
	if login.Get("AuthType") != "MD5" || login.Get("Key") != md5hex("840415273secret") {
		t.Fatalf("login = %v, want AuthType=MD5 Key=md5(challenge+secret)", login)
	}
	fc.send("Response", "Success", "ActionID", login.ActionID(), "Message", "Authentication accepted")
	expectKind(t, c, KindConnected, 1)
}

// 소비자가 읽지 않으면 적체 경고를 한 번만 내고, 비워지면 해소를 한 번 알린다.
func TestQueueDepthWarning(t *testing.T) {
	logs := &captureHandler{}
	f := newFakeAMI(t)
	c, _ := startClient(t, f.addr(), func(cfg *Config) {
		cfg.QueueWarnDepth = 5
		cfg.Logger = slog.New(logs)
	})
	fc := f.accept()
	fc.acceptLogin()
	expectKind(t, c, KindConnected, 1)

	for i := range 20 { // 아무도 읽지 않는 동안 20개를 쌓는다
		fc.send("Event", "Newexten", "Priority", strconv.Itoa(i))
	}
	eventually(t, func() bool { return c.QueueDepth() == 20 })
	if got := logs.count("이벤트 적체: 소비자가 이벤트를 제때 가져가지 않는다"); got != 1 {
		t.Fatalf("warnings = %d, want exactly 1 while depth grew 5 → 20", got)
	}

	for range 20 {
		expectKind(t, c, KindAMI, 1)
	}
	eventually(t, func() bool { return logs.count("이벤트 적체 해소") == 1 })
}

// captureHandler는 테스트에서 로그 메시지를 모은다.
type captureHandler struct {
	mu   sync.Mutex
	msgs []string
}

func (h *captureHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h *captureHandler) WithAttrs([]slog.Attr) slog.Handler       { return h }
func (h *captureHandler) WithGroup(string) slog.Handler            { return h }
func (h *captureHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.msgs = append(h.msgs, r.Message)
	return nil
}

func (h *captureHandler) count(msg string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	n := 0
	for _, m := range h.msgs {
		if m == msg {
			n++
		}
	}
	return n
}

func TestSendWithoutConnection(t *testing.T) {
	c := New(Config{})
	if _, err := c.Send(context.Background(), NewAction("Ping")); !errors.Is(err, ErrNotConnected) {
		t.Fatalf("err = %v, want ErrNotConnected", err)
	}
	if _, err := c.Send(context.Background(), NewAction("Originate").With("CallerID", "x\r\nAction: Hangup")); !errors.Is(err, ErrInvalidField) {
		t.Fatalf("err = %v, want ErrInvalidField", err)
	}
}

// 녹화 파일은 배너부터 시작하는 원본 그대로여서, 같은 Reader로 다시 읽을 수 있어야 한다.
func TestRecordingIsReplayable(t *testing.T) {
	dir := t.TempDir()
	f := newFakeAMI(t)
	c, stop := startClient(t, f.addr(), func(cfg *Config) { cfg.RecordDir = dir })
	fc := f.accept()
	fc.acceptLogin()
	expectKind(t, c, KindConnected, 1)
	fc.send("Event", "Newchannel", "Channel", "PJSIP/1001-00000001", "Uniqueid", "1.1")
	fc.send("Event", "Hangup", "Channel", "PJSIP/1001-00000001", "Uniqueid", "1.1")
	expectKind(t, c, KindAMI, 1)
	expectKind(t, c, KindAMI, 1)
	stop()

	files, err := filepath.Glob(filepath.Join(dir, "ami-*-c1.log"))
	if err != nil || len(files) != 1 {
		t.Fatalf("recordings = %v, %v", files, err)
	}
	raw, err := os.Open(files[0])
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()

	r := NewReader(raw)
	if _, found, err := r.SkipBanner(); err != nil || !found {
		t.Fatalf("recording must start with the banner: found=%v err=%v", found, err)
	}
	var got []string
	for {
		m, err := r.ReadMessage()
		if err != nil {
			break
		}
		got = append(got, m.Get("Response")+m.Event())
	}
	want := []string{"Success", "Newchannel", "Hangup"} // 로그인 응답도 녹화된다 (보낸 Secret은 안 남는다)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("replayed %v, want %v", got, want)
	}
}
