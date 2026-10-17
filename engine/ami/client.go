package ami

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

var (
	// ErrNotConnected는 로그인된 연결이 없을 때 액션을 보내려 하면 돌려준다.
	ErrNotConnected = errors.New("ami: not connected")
	// ErrDisconnected는 응답을 기다리는 중에 연결이 끊겼을 때 돌려준다.
	ErrDisconnected = errors.New("ami: disconnected while waiting for response")
)

// ResponseError는 Asterisk가 "Response: Error"로 답했을 때의 오류다.
type ResponseError struct {
	Action   string
	Message  string // Asterisk가 준 Message 필드 (예: "Interface not found")
	Response Message
}

func (e *ResponseError) Error() string {
	return fmt.Sprintf("ami: %s failed: %s", e.Action, e.Message)
}

// Config는 Client 설정이다. 0 값인 항목은 기본값으로 채운다.
type Config struct {
	Addr     string // 기본 "127.0.0.1:5038"
	Username string
	Secret   string

	// Events는 Login 때 받을 이벤트 범위다.
	// "on"(기본, 전부) / "off"(안 받음) / "call,agent"처럼 클래스 지정.
	Events string

	// AuthMD5면 비밀번호를 그대로 보내지 않는 챌린지 방식으로 로그인한다.
	// Action: Challenge로 받은 문자열에 비밀번호를 붙여 MD5한 값(Key)만 보낸다.
	AuthMD5 bool

	// QueueWarnDepth는 소비자가 가져가지 않은 이벤트가 이만큼 쌓이면 경고 로그를 남긴다.
	// 기본 1000, 음수면 끈다. 절반 아래로 내려오면 해소 로그를 남긴다.
	QueueWarnDepth int64

	DialTimeout   time.Duration // 기본 5s
	ActionTimeout time.Duration // ctx에 데드라인이 없을 때 응답을 기다리는 시간. 기본 10s
	PingInterval  time.Duration // 하트비트 간격. 기본 15s, 음수면 끈다
	PingTimeout   time.Duration // Ping 응답 대기. 기본 5s
	BackoffMin    time.Duration // 재접속 대기 시작값. 기본 500ms
	BackoffMax    time.Duration // 재접속 대기 최댓값. 기본 30s

	// RecordDir가 있으면 연결마다 '받은 원본 바이트'를 파일로 녹화한다.
	// 녹화 파일은 Reader로 그대로 다시 읽을 수 있다 (replay 테스트, 장애 재현의 재료).
	// 보낸 액션은 녹화하지 않으므로 Login의 Secret은 파일에 남지 않는다.
	RecordDir string

	// ActionIDPrefix는 이 클라이언트가 붙이는 ActionID의 앞부분이다. 기본 "<호스트>-<pid>".
	// OriginateResponse 같은 일부 이벤트는 ActionID를 단 채 다른 AMI 세션에도 전달되므로,
	// 클라이언트마다 겹치지 않는 접두사를 쓴다.
	ActionIDPrefix string

	Logger *slog.Logger
}

func (cfg Config) withDefaults() Config {
	if cfg.Addr == "" {
		cfg.Addr = "127.0.0.1:5038"
	}
	if cfg.Events == "" {
		cfg.Events = "on"
	}
	if cfg.DialTimeout == 0 {
		cfg.DialTimeout = 5 * time.Second
	}
	if cfg.ActionTimeout == 0 {
		cfg.ActionTimeout = 10 * time.Second
	}
	if cfg.PingInterval == 0 {
		cfg.PingInterval = 15 * time.Second
	}
	if cfg.PingTimeout == 0 {
		cfg.PingTimeout = 5 * time.Second
	}
	if cfg.BackoffMin == 0 {
		cfg.BackoffMin = 500 * time.Millisecond
	}
	if cfg.BackoffMax == 0 {
		cfg.BackoffMax = 30 * time.Second
	}
	if cfg.QueueWarnDepth == 0 {
		cfg.QueueWarnDepth = 1000
	}
	if cfg.ActionIDPrefix == "" {
		host, _ := os.Hostname()
		if host == "" {
			host = "ipcc"
		}
		cfg.ActionIDPrefix = fmt.Sprintf("%s-%d", host, os.Getpid())
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.DiscardHandler)
	}
	return cfg
}

// EventKind는 Client가 내보내는 항목의 종류다.
type EventKind int

const (
	// KindAMI는 Asterisk가 보낸 이벤트다.
	KindAMI EventKind = iota
	// KindConnected는 로그인 성공(재접속 포함)이다. 이 시점에 상태를 다시 맞춰야 한다.
	KindConnected
	// KindDisconnected는 연결 끊김이다. 끊긴 동안의 이벤트는 사라졌으므로 지금 상태를 믿으면 안 된다.
	KindDisconnected
)

func (k EventKind) String() string {
	switch k {
	case KindAMI:
		return "ami"
	case KindConnected:
		return "connected"
	case KindDisconnected:
		return "disconnected"
	}
	return "unknown"
}

// Event는 Client가 받은 순서대로 내보내는 항목이다.
// 연결 상태 변화도 같은 채널로 내보내서 AMI 이벤트와의 순서가 섞이지 않게 했다.
// 연결 하나의 항목은 항상 Connected → (AMI 이벤트들) → Disconnected 순서로 나온다.
type Event struct {
	Kind     EventKind
	Msg      Message   // KindAMI일 때
	ConnID   int       // 몇 번째 연결에서 생긴 일인지 (1부터)
	Received time.Time // 클라이언트가 받은 시각
	Version  string    // KindConnected일 때 AMI 프로토콜 버전 (배너)
	Err      error     // KindDisconnected일 때 끊긴 이유
}

// Client는 AMI 연결 하나를 관리한다.
//
// 고루틴 구성:
//
//	Run ─ 접속 → 로그인 → 수신을 반복하고, 끊기면 백오프 후 재접속
//	 ├─ readLoop  소켓에서 메시지를 읽어 ① 기다리는 액션에 응답을 넘기거나 ② 이벤트 큐에 넣는다
//	 ├─ pingLoop  주기적으로 Ping. 응답이 없으면 연결을 끊어 재접속하게 한다
//	 └─ pump      이벤트 큐 → Events() 채널. 소비자가 느려도 readLoop를 막지 않는다
//
// Send/SendList는 여러 고루틴에서 동시에 불러도 된다.
type Client struct {
	cfg Config
	log *slog.Logger

	in         chan Event // readLoop → pump
	out        chan Event // pump → 소비자
	depth      atomic.Int64
	backlogged bool // 적체 경고를 낸 상태인지. pump 고루틴에서만 읽고 쓴다

	seq     atomic.Uint64
	running atomic.Bool

	mu      sync.Mutex
	conn    net.Conn                  // 로그인까지 끝난 현재 연결. 없으면 nil
	pending map[string]*pendingAction // ActionID → 응답을 기다리는 액션
	connID  int

	writeMu sync.Mutex // 여러 고루틴의 Write가 한 메시지 안에서 섞이지 않게
}

// pendingAction은 응답을 기다리는 액션 하나다.
// resp/items/err는 mu를 잡고 쓰고, done을 닫아서 기다리는 쪽에 알린다.
type pendingAction struct {
	action string
	conn   net.Conn // 이 액션을 보낸 연결
	list   bool     // 목록형 응답(EventList)을 기다리는가
	resp   Message
	items  []Message
	err    error
	done   chan struct{}
}

func (p *pendingAction) finish(err error) {
	p.err = err
	close(p.done)
}

func (p *pendingAction) respErr() error {
	if strings.EqualFold(p.resp.Get("Response"), "Error") {
		return &ResponseError{Action: p.action, Message: p.resp.Get("Message"), Response: p.resp}
	}
	return nil
}

// New는 Client를 만든다. 접속은 Run이 한다.
func New(cfg Config) *Client {
	cfg = cfg.withDefaults()
	return &Client{
		cfg:     cfg,
		log:     cfg.Logger,
		in:      make(chan Event),
		out:     make(chan Event),
		pending: make(map[string]*pendingAction),
	}
}

// Events는 AMI 이벤트와 연결 상태 변화를 받은 순서대로 내보낸다. Run이 끝나면 닫힌다.
func (c *Client) Events() <-chan Event { return c.out }

// QueueDepth는 소비자가 아직 가져가지 않은 항목 수다. 계속 늘어나면 소비자가 느린 것이다.
func (c *Client) QueueDepth() int64 { return c.depth.Load() }

// Run은 ctx가 끝날 때까지 접속 → 로그인 → 수신을 반복한다. 끊기면 지수 백오프로 재접속한다.
// Client 하나에 한 번만 부를 수 있다.
func (c *Client) Run(ctx context.Context) error {
	if !c.running.CompareAndSwap(false, true) {
		return errors.New("ami: Run called twice")
	}
	go pump(c.in, c.out, &c.depth, ctx.Done(), c.observeDepth)
	defer close(c.in) // session은 readLoop가 끝난 뒤에만 돌아오므로, 여기서는 아무도 in에 쓰지 않는다

	backoff := c.cfg.BackoffMin
	for {
		loggedIn, err := c.session(ctx)
		if ctx.Err() != nil {
			return nil
		}
		if loggedIn {
			// 잘 돌다가 끊긴 경우(Asterisk 재시작 등)는 대기 시간을 처음부터 다시 센다
			backoff = c.cfg.BackoffMin
		}
		wait := jitter(backoff)
		c.log.Warn("AMI 연결 실패, 재접속 대기", "addr", c.cfg.Addr, "err", err, "retry_in", wait.Round(time.Millisecond))
		select {
		case <-time.After(wait):
		case <-ctx.Done():
			return nil
		}
		backoff = min(backoff*2, c.cfg.BackoffMax)
	}
}

// observeDepth는 이벤트 큐 깊이가 바뀔 때마다 pump 고루틴에서 불린다.
// 경고선을 넘으면 한 번만 경고하고, 절반 아래로 내려와야 해소를 알린다.
// 경고선 근처에서 깊이가 오르내릴 때 로그가 쏟아지지 않게 하려는 것이다.
func (c *Client) observeDepth(depth int64) {
	limit := c.cfg.QueueWarnDepth
	if limit <= 0 {
		return
	}
	switch {
	case !c.backlogged && depth >= limit:
		c.backlogged = true
		c.log.Warn("이벤트 적체: 소비자가 이벤트를 제때 가져가지 않는다", "depth", depth, "limit", limit)
	case c.backlogged && depth <= limit/2:
		c.backlogged = false
		c.log.Info("이벤트 적체 해소", "depth", depth)
	}
}

// jitter는 d를 ±20% 범위에서 흔든다. 여러 클라이언트가 같은 순간에 몰려서 재접속하는 걸 막는다.
func jitter(d time.Duration) time.Duration {
	return time.Duration(float64(d) * (0.8 + 0.4*rand.Float64()))
}

// session은 연결 하나의 수명이다. 로그인까지 성공했는지와 끝난 이유를 돌려준다.
func (c *Client) session(ctx context.Context) (loggedIn bool, err error) {
	dialer := net.Dialer{Timeout: c.cfg.DialTimeout, KeepAlive: 30 * time.Second}
	conn, err := dialer.DialContext(ctx, "tcp", c.cfg.Addr)
	if err != nil {
		return false, err
	}

	c.mu.Lock()
	c.connID++
	connID := c.connID
	c.mu.Unlock()

	var src io.Reader = conn
	rec := &recorder{}
	if c.cfg.RecordDir != "" {
		src = io.TeeReader(conn, rec)
	}
	r := NewReader(src)

	// 로그인은 readLoop를 띄우기 전에 동기로 처리한다. 로그인 전에는 이벤트가 오지 않으므로
	// 응답 하나만 읽으면 되고, 덕분에 Connected가 이 연결의 어떤 이벤트보다 먼저 나간다.
	version, err := c.handshake(conn, r)
	if err != nil {
		conn.Close()
		return false, err
	}

	// 녹화 파일은 로그인에 성공한 연결만 만든다. 재접속 시도마다 빈 파일이 생기지 않게
	// 그때까지 받은 바이트(배너, 로그인 응답)는 메모리에 모아 뒀다가 파일 맨 앞에 쓴다.
	if c.cfg.RecordDir != "" {
		f, err := c.openRecording(connID)
		if err != nil {
			c.log.Warn("녹화 파일을 열 수 없어 녹화 없이 진행", "err", err)
		}
		rec.start(f)
		defer rec.close()
	}

	c.mu.Lock()
	c.conn = conn
	c.mu.Unlock()
	c.log.Info("AMI 로그인", "addr", c.cfg.Addr, "version", version, "conn", connID)
	c.emit(ctx, Event{Kind: KindConnected, ConnID: connID, Received: time.Now(), Version: version})

	sessCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	var readErr error
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		readErr = c.readLoop(sessCtx, conn, r, connID)
	}()

	pingFailed := make(chan error, 1)
	go c.pingLoop(sessCtx, pingFailed)

	// stop은 연결을 닫고 readLoop가 끝날 때까지 기다린다. 응답을 기다리던 액션은 모두 실패한다.
	stop := func() {
		cancel()
		conn.Close()
		<-readerDone
		c.dropConn(conn)
	}

	var cause error
	select {
	case <-ctx.Done():
		c.logoff()
		stop()
		return true, nil
	case <-readerDone:
		cause = readErr
	case cause = <-pingFailed:
	}
	stop()
	c.emit(ctx, Event{Kind: KindDisconnected, ConnID: connID, Received: time.Now(), Err: cause})
	return true, cause
}

// handshake는 배너를 읽고 로그인한다.
func (c *Client) handshake(conn net.Conn, r *Reader) (version string, err error) {
	_ = conn.SetDeadline(time.Now().Add(c.cfg.DialTimeout + c.cfg.ActionTimeout))
	defer func() { _ = conn.SetDeadline(time.Time{}) }()

	version, err = r.ReadBanner()
	if err != nil {
		return "", fmt.Errorf("banner: %w", err)
	}

	login := NewAction("Login").With("Username", c.cfg.Username)
	if c.cfg.AuthMD5 {
		resp, err := c.request(conn, r, NewAction("Challenge").With("AuthType", "MD5"))
		if err != nil {
			return "", fmt.Errorf("challenge: %w", err)
		}
		login = login.With("AuthType", "MD5").With("Key", md5hex(resp.Get("Challenge")+c.cfg.Secret))
	} else {
		login = login.With("Secret", c.cfg.Secret)
	}
	if _, err := c.request(conn, r, login.With("Events", c.cfg.Events)); err != nil {
		return "", fmt.Errorf("login: %w", err)
	}
	return version, nil
}

// request는 readLoop가 뜨기 전(로그인 단계)에 쓰는 동기 요청이다. 짝이 맞는 응답이 올 때까지 읽는다.
// 로그인 전에는 이벤트가 오지 않으므로 다른 메시지를 버려도 잃는 것이 없다.
func (c *Client) request(conn net.Conn, r *Reader, act Message) (Message, error) {
	id := c.nextActionID()
	wire, err := act.withActionID(id).Encode()
	if err != nil {
		return Message{}, err
	}
	if _, err := conn.Write(wire); err != nil {
		return Message{}, err
	}
	for {
		msg, err := r.ReadMessage()
		if err != nil {
			return Message{}, err
		}
		if !msg.IsResponse() || msg.ActionID() != id {
			continue
		}
		if !strings.EqualFold(msg.Get("Response"), "Success") {
			return msg, &ResponseError{Action: act.Get("Action"), Message: msg.Get("Message"), Response: msg}
		}
		return msg, nil
	}
}

func md5hex(s string) string {
	sum := md5.Sum([]byte(s))
	return hex.EncodeToString(sum[:])
}

// readLoop는 연결이 끊길 때까지 메시지를 읽어서 나눠 준다.
func (c *Client) readLoop(ctx context.Context, conn net.Conn, r *Reader, connID int) error {
	defer c.dropConn(conn)
	for {
		msg, err := r.ReadMessage()
		if err != nil {
			return err
		}
		if id := msg.ActionID(); id != "" && c.deliver(id, msg) {
			continue
		}
		if msg.IsResponse() {
			// 기다리는 쪽이 없는 응답: 타임아웃으로 포기한 액션에 늦게 온 응답 등
			c.log.Debug("짝 없는 응답", "actionid", msg.ActionID(), "response", msg.Get("Response"))
			continue
		}
		select {
		case c.in <- Event{Kind: KindAMI, Msg: msg, ConnID: connID, Received: time.Now()}:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// deliver는 msg가 기다리는 액션의 응답(또는 목록 항목)이면 그쪽에 넘기고 true를 돌려준다.
func (c *Client) deliver(id string, msg Message) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	p, ok := c.pending[id]
	if !ok {
		return false
	}
	switch {
	case msg.IsResponse():
		p.resp = msg
		// 목록형이 아니거나, 오류라서 목록이 따라오지 않으면 여기서 끝
		if !p.list || !strings.EqualFold(msg.Get("EventList"), "start") {
			delete(c.pending, id)
			p.finish(nil)
		}
		return true
	case p.list:
		if strings.EqualFold(msg.Get("EventList"), "Complete") {
			if n, err := strconv.Atoi(msg.Get("ListItems")); err == nil && n != len(p.items) {
				c.log.Warn("목록 개수 불일치", "action", p.action, "ListItems", n, "received", len(p.items))
			}
			delete(c.pending, id)
			p.finish(nil)
		} else {
			p.items = append(p.items, msg)
		}
		return true
	default:
		// 목록을 기다리지 않는 액션의 ActionID가 달린 이벤트 → 일반 이벤트로 흘려보낸다
		return false
	}
}

// dropConn은 conn을 현재 연결에서 내리고, conn으로 보낸 액션을 모두 실패 처리한다.
func (c *Client) dropConn(conn net.Conn) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn == conn {
		c.conn = nil
	}
	for id, p := range c.pending {
		if p.conn == conn {
			delete(c.pending, id)
			p.finish(ErrDisconnected)
		}
	}
}

// Send는 액션을 보내고 응답 하나를 기다린다. "Response: Error"면 *ResponseError를 돌려준다.
// ActionID는 Client가 붙인다(이미 있으면 바꾼다).
func (c *Client) Send(ctx context.Context, action Message) (Message, error) {
	p, err := c.send(ctx, action, false)
	if err != nil {
		return Message{}, err
	}
	return p.resp, p.respErr()
}

// SendList는 목록형 액션(CoreShowChannels, QueueStatus 등)을 보내고, 응답 뒤에 오는
// 목록 이벤트를 "EventList: Complete"까지 모아서 돌려준다. 목록 이벤트는 Events()로 나가지 않는다.
// 목록형이 아닌 액션에 써도 응답 하나로 끝난다.
func (c *Client) SendList(ctx context.Context, action Message) (Message, []Message, error) {
	p, err := c.send(ctx, action, true)
	if err != nil {
		return Message{}, nil, err
	}
	if err := p.respErr(); err != nil {
		return p.resp, nil, err
	}
	return p.resp, p.items, nil
}

func (c *Client) send(ctx context.Context, action Message, list bool) (*pendingAction, error) {
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.cfg.ActionTimeout)
		defer cancel()
	}
	name := action.Get("Action")
	id := c.nextActionID()
	wire, err := action.withActionID(id).Encode()
	if err != nil {
		return nil, err
	}

	p := &pendingAction{action: name, list: list, done: make(chan struct{})}
	c.mu.Lock()
	conn := c.conn
	if conn == nil {
		c.mu.Unlock()
		return nil, ErrNotConnected
	}
	p.conn = conn
	c.pending[id] = p
	c.mu.Unlock()

	c.writeMu.Lock()
	_ = conn.SetWriteDeadline(time.Now().Add(c.cfg.ActionTimeout))
	_, err = conn.Write(wire)
	c.writeMu.Unlock()
	if err != nil {
		c.forget(id)
		return nil, fmt.Errorf("ami: write %s: %w", name, err)
	}
	c.log.Debug("AMI 액션", "action", name, "actionid", id)

	select {
	case <-p.done:
		if p.err != nil {
			return nil, fmt.Errorf("ami: %s: %w", name, p.err)
		}
		return p, nil
	case <-ctx.Done():
		c.forget(id)
		return nil, fmt.Errorf("ami: %s (ActionID %s): %w", name, id, ctx.Err())
	}
}

func (c *Client) forget(id string) {
	c.mu.Lock()
	delete(c.pending, id)
	c.mu.Unlock()
}

func (c *Client) nextActionID() string {
	return c.cfg.ActionIDPrefix + "-" + strconv.FormatUint(c.seq.Add(1), 10)
}

// pingLoop는 주기적으로 Ping을 보낸다. TCP 연결은 살아 있는데 Asterisk가 멈춘 경우
// (프로세스 정지, docker pause, 중간 네트워크 단절)에는 읽기 오류가 나지 않아서 이렇게 직접 확인해야 한다.
func (c *Client) pingLoop(ctx context.Context, failed chan<- error) {
	if c.cfg.PingInterval <= 0 {
		return
	}
	t := time.NewTicker(c.cfg.PingInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			pctx, cancel := context.WithTimeout(ctx, c.cfg.PingTimeout)
			_, err := c.Send(pctx, NewAction("Ping"))
			cancel()
			if err != nil && ctx.Err() == nil {
				failed <- fmt.Errorf("ping: %w", err)
				return
			}
		}
	}
}

// logoff는 정상 종료 때 Asterisk에 작별 인사를 한다. 응답은 1초만 기다린다.
func (c *Client) logoff() {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, _ = c.Send(ctx, NewAction("Logoff"))
}

func (c *Client) emit(ctx context.Context, ev Event) {
	select {
	case c.in <- ev:
	case <-ctx.Done():
	}
}

// recorder는 녹화 파일이 정해질 때까지 받은 바이트를 메모리에 모아 둔다.
// Write는 readLoop 한 고루틴에서만 불리고, start는 readLoop가 뜨기 전에 불리므로 잠금이 필요 없다.
type recorder struct {
	buf     bytes.Buffer
	file    *os.File
	started bool
}

func (r *recorder) Write(p []byte) (int, error) {
	switch {
	case r.file != nil:
		if _, err := r.file.Write(p); err != nil {
			// 디스크 오류 때문에 이벤트 처리까지 멈추면 안 되므로 녹화만 포기한다
			r.file.Close()
			r.file = nil
		}
	case !r.started:
		r.buf.Write(p)
	}
	return len(p), nil
}

// start는 모아 둔 바이트를 f에 쓰고 이후로는 f에 바로 쓴다. f가 nil이면 녹화를 끈다.
func (r *recorder) start(f *os.File) {
	r.started = true
	if f != nil {
		if _, err := f.Write(r.buf.Bytes()); err != nil {
			f.Close()
		} else {
			r.file = f
		}
	}
	r.buf = bytes.Buffer{}
}

func (r *recorder) close() {
	if r.file != nil {
		r.file.Close()
	}
}

func (c *Client) openRecording(connID int) (*os.File, error) {
	if err := os.MkdirAll(c.cfg.RecordDir, 0o755); err != nil {
		return nil, err
	}
	name := fmt.Sprintf("ami-%s-c%d.log", time.Now().Format("20060102-150405"), connID)
	path := filepath.Join(c.cfg.RecordDir, name)
	f, err := os.Create(path)
	if err != nil {
		return nil, err
	}
	c.log.Info("AMI 수신 원본 녹화", "file", path)
	return f, nil
}
