//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"ipcc/engine/ami"
	"ipcc/engine/rtpcheck"
)

const (
	queueName = "support"
	stepWait  = 45 * time.Second // 한 단계에서 기대한 이벤트를 기다리는 최대 시간

	// 자동 테스트용 SIP 상담원 전화기. pjsip.conf에서 127.0.0.1:5072 고정 주소로 부른다.
	sipAgentIface = "PJSIP/9002"
	sipAgentName  = "SIPP-9002"

	// 벨만 울리고 받지 않는 가짜 상담원 (extensions.conf의 [sim-noanswer])
	noAnswerIface = "Local/1004@sim-noanswer"
)

var simExts = []string{"1001", "1002", "1003"}

// SIPp 역할별 옵션. 고객(9001)은 Makefile의 SIPP와 같고, 상담원(9002)은 고정 주소 5072에서 기다린다.
var (
	customerSIPp = []string{"127.0.0.1:5060", "-i", "127.0.0.1", "-p", "5070",
		"-key", "user", "9001", "-au", "9001", "-ap", "sipp9001"}
	agentSIPp = []string{"-i", "127.0.0.1", "-p", "5072", "-mp", "6100"}
)

type step struct {
	name string
	fn   func(t *testing.T)
}

// TestCallFlow는 실제 Asterisk로 상담원 로그인부터 로그아웃까지 한 바퀴를 돈다.
// 단계가 실패하면 그 뒤 단계는 건너뛴다 (앞 단계 상태에 기대기 때문).
func TestCallFlow(t *testing.T) {
	root := repoRoot(t)
	waitFullyBooted(t, root)

	h := newHarness(t, root)
	h.requireNoRealActivity(t)
	h.removeTestAgents() // 지난 실행이나 make sim-agents가 남긴 테스트용 상담원 정리
	t.Cleanup(h.removeTestAgents)

	steps := []step{
		{"상담원 3명 로그인", h.stepLogin},
		{"정상 콜 2건 — 큐 → 상담원 연결 → 상담원이 끊음", h.stepNormalCalls},
		{"대기 중 포기 — 모두 이석인 동안 고객이 끊음", h.stepAbandon},
		{"SIP — 등록, 에코 통화, 대표번호로 큐 통화 (SIPp)", h.stepSIP},
		{"상담원 3명 로그아웃", h.stepLogout},
		{"실제 SIP 상담원 — SIP 고객 → 대표번호 → PJSIP/9002가 받고 먼저 끊음", h.stepSIPAgent},
		{"상태머신 엔진 — 엔진이 후처리 타이머·이석 예약을 큐 멤버에 반영", h.stepEngine},
		{"음성(RTP) 경로 — 포트포워딩을 거쳐 602 에코로 RTP 왕복", h.stepRTP},
	}
	if os.Getenv("E2E_RESTART") == "1" {
		steps = append(steps,
			step{"Asterisk 재시작 → 자동 재접속", h.stepRestart},
			step{"Asterisk 재시작 → 엔진이 재동기화로 상담원을 다시 넣음", h.stepEngineResync})
	}
	for _, s := range steps {
		if !t.Run(s.name, s.fn) {
			return
		}
	}

	h.stop()
	t.Run("녹화 파일을 처음부터 끝까지 다시 읽을 수 있다", h.stepReplayRecordings)
	h.logSummary(t)
}

func (h *harness) stepLogin(t *testing.T) {
	mark := h.rec.mark()
	for _, ext := range simExts {
		h.send(t, ami.NewAction("QueueAdd").
			With("Queue", queueName).
			With("Interface", simIface(ext)).
			With("MemberName", "SIM-"+ext).
			With("Paused", "false"))
	}
	h.rec.waitCount(t, mark, len(simExts), "QueueMemberAdded", isSimMember)
}

func (h *harness) stepNormalCalls(t *testing.T) {
	mark := h.rec.mark()
	nums := []string{h.number(), h.number()}
	for _, n := range nums {
		h.originateCustomer(t, n, 60)
	}

	// AgentConnect에는 고객·상담원 채널 정보가 다 있다. 여기서 '어느 상담원이 어느 콜'인지 잡아 둔다.
	connects := h.rec.waitCount(t, mark, len(nums), "AgentConnect", callerIn(nums...))
	joins := h.rec.since(mark, "QueueCallerJoin", callerIn(nums...))
	callOf := map[string]ami.Message{} // MemberName → 그 상담원의 AgentConnect
	for _, c := range connects {
		num := c.Get("CallerIDNum")
		// 콜 ID = Linkedid: 큐에 들어갈 때와 상담원에 연결될 때의 Linkedid가 같아야 한다
		if lid := linkedidOf(joins, num); lid == "" || lid != c.Get("Linkedid") {
			t.Errorf("%s: QueueCallerJoin Linkedid=%q, AgentConnect Linkedid=%q", num, lid, c.Get("Linkedid"))
		}
		callOf[c.Get("MemberName")] = c
	}

	// AgentComplete는 채널이 먼저 정리되면 채널 정보(Channel, CallerIDNum, Linkedid) 없이 온다.
	// e2e에서 콜 2건이 동시에 끝날 때 재현됐다 (docs/05-ami.md 6절). 그래서 상담원으로 짝을 짓는다.
	completes := h.rec.waitCount(t, mark, len(nums), "AgentComplete", memberIn(callOf))
	missing := 0
	for _, c := range completes {
		member := c.Get("MemberName")
		if c.Get("Reason") != "agent" {
			t.Errorf("%s: Reason=%q, want agent (가짜 상담원이 먼저 끊는다)", member, c.Get("Reason"))
		}
		// Originate에 __SIM_TALK=2를 실었으므로 통화는 약 2초
		if talk, err := strconv.Atoi(c.Get("TalkTime")); err != nil || talk < 1 || talk > 5 {
			t.Errorf("%s: TalkTime=%q, want 약 2초", member, c.Get("TalkTime"))
		}
		lid, ok := c.Lookup("Linkedid")
		switch {
		case !ok:
			missing++
		case lid != callOf[member].Get("Linkedid"):
			t.Errorf("%s: AgentComplete Linkedid=%q, AgentConnect Linkedid=%q", member, lid, callOf[member].Get("Linkedid"))
		}
	}
	if missing > 0 {
		t.Logf("AgentComplete %d건 중 %d건은 채널 정보 없이 왔다 (채널이 먼저 정리된 경우의 Asterisk 동작)", len(completes), missing)
	}
}

func (h *harness) stepAbandon(t *testing.T) {
	mark := h.rec.mark()
	h.pauseSimAgents(t, true, "e2e")
	h.rec.waitCount(t, mark, len(simExts), "QueueMemberPause", all(isSimMember, field("Paused", "1")))

	num := h.number()
	h.originateCustomer(t, num, 4) // 4초 동안만 기다리는 고객
	// 이 단계의 콜은 하나뿐이라 큐 이름으로 센다 (채널 정보가 빠진 이벤트가 와도 놓치지 않게)
	ab := h.rec.waitCount(t, mark, 1, "QueueCallerAbandon", field("Queue", queueName))
	if got, ok := ab[0].Lookup("CallerIDNum"); ok && got != num {
		t.Errorf("포기한 고객 = %s, want %s", got, num)
	}
	if got := h.rec.since(mark, "AgentConnect", field("Queue", queueName)); len(got) > 0 {
		t.Errorf("모든 상담원이 이석인데 콜이 연결됨: %v", got[0])
	}

	mark = h.rec.mark()
	h.pauseSimAgents(t, false, "")
	h.rec.waitCount(t, mark, len(simExts), "QueueMemberPause", all(isSimMember, field("Paused", "0")))
}

func (h *harness) stepSIP(t *testing.T) {
	h.sipp(t, "-sf", "/sipp/register.xml")
	h.sipp(t, "-sf", "/sipp/uac-call-hangup.xml", "-s", "600", "-d", "2000")

	// 대표번호 통화는 상담원이 끊어야(BYE) SIPp가 성공으로 끝난다
	mark := h.rec.mark()
	h.sipp(t, "-sf", "/sipp/uac-call-wait-bye.xml", "-s", "5000")
	conn := h.rec.waitCount(t, mark, 1, "AgentConnect", channelPrefix("PJSIP/9001-"))[0]
	h.rec.waitCount(t, mark, 1, "AgentComplete", field("MemberName", conn.Get("MemberName")))
}

func (h *harness) stepLogout(t *testing.T) {
	mark := h.rec.mark()
	h.removeTestAgents()
	h.rec.waitCount(t, mark, len(simExts), "QueueMemberRemoved", isSimMember)
}

// stepSIPAgent는 소프트폰 상담원과 같은 PJSIP 채널 흐름을 SIPp 두 개로 만든다.
// 가짜 상담원(Local 채널)이 모두 로그아웃한 뒤라서 큐는 PJSIP/9002만 부른다.
func (h *harness) stepSIPAgent(t *testing.T) {
	uas := make(chan error, 1)
	go func() {
		out, err := h.sippRun(agentSIPp, "-sf", "/sipp/uas-agent.xml", "-d", "2000")
		if err != nil {
			err = fmt.Errorf("%w\n%s", err, lastLines(out, 20))
		}
		uas <- err
	}()
	time.Sleep(time.Second) // SIPp가 5072 포트를 열 시간

	mark := h.rec.mark()
	h.send(t, ami.NewAction("QueueAdd").
		With("Queue", queueName).
		With("Interface", sipAgentIface).
		With("MemberName", sipAgentName).
		With("Paused", "false"))
	h.rec.waitCount(t, mark, 1, "QueueMemberAdded", field("MemberName", sipAgentName))

	h.sipp(t, "-sf", "/sipp/uac-call-wait-bye.xml", "-s", "5000")
	select {
	case err := <-uas:
		if err != nil {
			t.Fatalf("상담원 SIPp(UAS) 실패: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("상담원 SIPp(UAS)가 30초 안에 끝나지 않음")
	}

	conn := h.rec.waitCount(t, mark, 1, "AgentConnect", field("MemberName", sipAgentName))[0]
	if !strings.HasPrefix(conn.Get("Channel"), "PJSIP/9001-") || !strings.HasPrefix(conn.Get("DestChannel"), "PJSIP/9002-") {
		t.Errorf("AgentConnect Channel=%q DestChannel=%q, want PJSIP/9001-… / PJSIP/9002-…", conn.Get("Channel"), conn.Get("DestChannel"))
	}
	// 고객 구간과 상담원 구간이 같은 콜로 묶여야 한다
	if conn.Get("Linkedid") == "" || conn.Get("Linkedid") != conn.Get("DestLinkedid") {
		t.Errorf("Linkedid=%q DestLinkedid=%q, want 같은 값", conn.Get("Linkedid"), conn.Get("DestLinkedid"))
	}
	done := h.rec.waitCount(t, mark, 1, "AgentComplete", field("MemberName", sipAgentName))[0]
	if done.Get("Reason") != "agent" {
		t.Errorf("Reason=%q, want agent (상담원 SIPp가 먼저 BYE)", done.Get("Reason"))
	}

	mark = h.rec.mark()
	h.send(t, ami.NewAction("QueueRemove").With("Queue", queueName).With("Interface", sipAgentIface))
	h.rec.waitCount(t, mark, 1, "QueueMemberRemoved", field("MemberName", sipAgentName))
}

// stepRTP는 이 테스트를 돌리는 컴퓨터(Mac 또는 CI 러너)에서 Docker 포트포워딩을 거쳐
// 602(에코)로 RTP를 보내 돌아오는지 본다. NAT 설정(local_net, EXTERNAL_IP)이 틀리면 여기서 실패한다.
func (h *harness) stepRTP(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	res, err := rtpcheck.Run(ctx, rtpcheck.Config{Server: envOr("SIP_ADDR", "127.0.0.1:5060")})
	t.Logf("SDP 음성 주소 %s (%s), RTP 보냄 %d / 받음 %d, 첫 에코 %s",
		res.RemoteRTP, res.Codec, res.Sent, res.Received, res.FirstEcho.Round(time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	if !res.OK() {
		t.Fatalf("보낸 음성이 돌아오지 않음 — SDP 음성 주소 %s가 이 컴퓨터에서 갈 수 있는 주소인지 확인 (docs/04-sip-sdp-rtp.md 6절)", res.RemoteRTP)
	}
}

func (h *harness) stepRestart(t *testing.T) {
	before := h.rec.connects()
	if out, err := compose(h.root, "restart", "asterisk"); err != nil {
		t.Fatalf("docker compose restart: %v\n%s", err, out)
	}
	h.rec.waitFor(t, 90*time.Second, "재접속", func() bool { return h.rec.connects() > before })
	h.send(t, ami.NewAction("Ping")) // 다시 붙은 연결로 액션이 오가는지

	// persistentmembers = no: 재시작하면 큐 멤버가 비어 있다 (엔진이 다시 넣어야 하는 이유)
	_, items := h.sendList(t, ami.NewAction("QueueStatus").With("Queue", queueName))
	for _, m := range items {
		if m.Event() == "QueueMember" {
			t.Errorf("재시작 뒤에도 멤버가 남아 있음: %s", m.Get("Name"))
		}
	}
}

func (h *harness) stepReplayRecordings(t *testing.T) {
	files, err := filepath.Glob(filepath.Join(h.recordDir, "ami-*.log"))
	if err != nil || len(files) == 0 {
		t.Fatalf("녹화 파일이 없음 (%s): %v", h.recordDir, err)
	}
	for _, path := range files {
		t.Logf("%s: 메시지 %d개", filepath.Base(path), replay(t, path))
	}
}

// ── 하네스 ───────────────────────────────────────────────────────────────

type harness struct {
	root      string
	recordDir string
	c         *ami.Client
	rec       *recorder
	stop      func()
	prefix    string // 이번 실행에서 만드는 고객번호의 앞부분
	seq       int
}

func newHarness(t *testing.T, root string) *harness {
	t.Helper()
	recordDir := os.Getenv("E2E_RECORD_DIR")
	if recordDir == "" {
		recordDir = t.TempDir()
	}
	c := ami.New(ami.Config{
		Addr:      envOr("AMI_ADDR", "127.0.0.1:5038"),
		Username:  envOr("AMI_USER", "ipcc"),
		Secret:    envOr("AMI_SECRET", "ipcc-secret"),
		RecordDir: recordDir,
		Logger:    slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn})),
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = c.Run(ctx)
	}()
	rec := newRecorder()
	go rec.run(c.Events())

	var once sync.Once
	h := &harness{
		root:      root,
		recordDir: recordDir,
		c:         c,
		rec:       rec,
		stop:      func() { once.Do(func() { cancel(); <-done }) },
		prefix:    fmt.Sprintf("0109%03d", rand.IntN(1000)),
	}
	t.Cleanup(h.stop)
	rec.waitFor(t, 30*time.Second, "AMI 로그인", func() bool { return rec.connects() >= 1 })
	return h
}

// requireNoRealActivity는 실제 통화나 실제 상담원이 있으면 멈춘다.
// e2e 콜이 소프트폰으로 가거나, 사람의 통화가 숫자를 흐트러뜨리지 않게 하기 위해서다.
func (h *harness) requireNoRealActivity(t *testing.T) {
	t.Helper()
	_, chans := h.sendList(t, ami.NewAction("CoreShowChannels"))
	if len(chans) > 0 {
		names := make([]string, len(chans))
		for i, ch := range chans {
			names[i] = ch.Get("Channel")
		}
		t.Fatalf("통화 중인 채널이 있어 e2e를 시작하지 않음 (끝난 뒤 다시 실행): %s", strings.Join(names, ", "))
	}
	// 스냅샷의 QueueMember는 실시간 이벤트와 필드 이름이 다르다: Name/Location (이벤트는 MemberName/Interface)
	_, items := h.sendList(t, ami.NewAction("QueueStatus").With("Queue", queueName))
	for _, m := range items {
		if m.Event() == "QueueMember" && !isTestInterface(m.Get("Location")) {
			t.Fatalf("실제 상담원 %s(%s)이 %s 큐에 로그인돼 있어 e2e 콜이 그 전화기로 갈 수 있음 — make agent-logout 후 다시 실행",
				m.Get("Name"), m.Get("Location"), queueName)
		}
	}
}

// isTestInterface는 테스트가 만들고 지우는 상담원(가짜 상담원, 받지 않는 가짜 상담원, SIPp 상담원)인지 본다.
func isTestInterface(iface string) bool {
	return iface == sipAgentIface || iface == noAnswerIface ||
		(strings.HasPrefix(iface, "Local/") && strings.HasSuffix(iface, "@sim-agent"))
}

func (h *harness) send(t *testing.T, act ami.Message) ami.Message {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	resp, err := h.c.Send(ctx, act)
	if err != nil {
		t.Fatalf("%s: %v", act.Get("Action"), err)
	}
	return resp
}

func (h *harness) sendList(t *testing.T, act ami.Message) (ami.Message, []ami.Message) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	resp, items, err := h.c.SendList(ctx, act)
	if err != nil {
		t.Fatalf("%s: %v", act.Get("Action"), err)
	}
	return resp, items
}

// removeTestAgents는 테스트용 상담원(가짜 상담원, SIPp 상담원)을 큐에서 뺀다. 없어서 실패하는 것은 무시한다.
func (h *harness) removeTestAgents() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ifaces := []string{sipAgentIface, noAnswerIface}
	for _, ext := range simExts {
		ifaces = append(ifaces, simIface(ext))
	}
	for _, iface := range ifaces {
		_, _ = h.c.Send(ctx, ami.NewAction("QueueRemove").With("Queue", queueName).With("Interface", iface))
	}
}

func (h *harness) pauseSimAgents(t *testing.T, paused bool, reason string) {
	t.Helper()
	for _, ext := range simExts {
		act := ami.NewAction("QueuePause").
			With("Queue", queueName).
			With("Interface", simIface(ext)).
			With("Paused", strconv.FormatBool(paused))
		if reason != "" {
			act = act.With("Reason", reason)
		}
		h.send(t, act)
	}
}

// originateCustomer는 가짜 고객 콜을 대표번호로 인입시킨다 (amicli sim-call과 같은 방식).
// patience초가 지나면 고객이 끊는다. 상담원은 1초 벨 뒤 받고 2초 통화한다.
func (h *harness) originateCustomer(t *testing.T, num string, patience int) {
	t.Helper()
	h.originate(t, "customer", num, patience, 2)
}

// originate는 [sim]의 exten으로 가짜 고객 콜을 만든다. talk는 가짜 상담원의 통화 시간(초)이다.
func (h *harness) originate(t *testing.T, exten, num string, patience, talk int) {
	t.Helper()
	h.originateRing(t, exten, num, patience, 1, talk)
}

// originateRing은 originate에 가짜 상담원의 벨 시간(초)까지 정한다.
func (h *harness) originateRing(t *testing.T, exten, num string, patience, ring, talk int) {
	t.Helper()
	h.send(t, ami.NewAction("Originate").
		With("Channel", "Local/"+exten+"@sim").
		With("Application", "Wait").
		With("Data", strconv.Itoa(patience)).
		With("CallerID", fmt.Sprintf(`"e2e" <%s>`, num)).
		With("Variable", "__SIM_RING="+strconv.Itoa(ring)).
		With("Variable", "__SIM_TALK="+strconv.Itoa(talk)).
		With("Async", "true"))
}

// waitIdle은 채널이 하나도 남지 않을 때까지 기다린다 (시나리오 사이를 깨끗하게 끊기 위해).
func (h *harness) waitIdle(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for {
		_, chans := h.sendList(t, ami.NewAction("CoreShowChannels"))
		if len(chans) == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("60초가 지나도 채널 %d개가 남아 있음", len(chans))
		}
		time.Sleep(500 * time.Millisecond)
	}
}

func (h *harness) number() string {
	h.seq++
	return fmt.Sprintf("%s%04d", h.prefix, h.seq)
}

// sipp는 고객(9001) 역할로 SIPp 시나리오 하나를 돌린다. 실패하면 테스트를 멈춘다.
func (h *harness) sipp(t *testing.T, args ...string) {
	t.Helper()
	if out, err := h.sippRun(customerSIPp, args...); err != nil {
		t.Fatalf("SIPp %v 실패: %v\n%s", args, err, lastLines(out, 30))
	}
}

// sippRun은 Asterisk 컨테이너 안에서 SIPp를 role 옵션으로 한 번 돌리고 출력을 돌려준다.
func (h *harness) sippRun(role []string, args ...string) (string, error) {
	cmd := append([]string{"exec", "-T", "asterisk", "sipp"}, role...)
	cmd = append(cmd, "-m", "1", "-nostdin", "-timeout", "60s")
	return compose(h.root, append(cmd, args...)...)
}

func (h *harness) logSummary(t *testing.T) {
	counts := h.rec.counts()
	names := make([]string, 0, len(counts))
	for n := range counts {
		names = append(names, n)
	}
	sort.Strings(names)
	var b strings.Builder
	for _, n := range names {
		fmt.Fprintf(&b, " %s=%d", n, counts[n])
	}
	t.Logf("받은 이벤트 %d개, 접속 %d회 —%s", h.rec.mark(), h.rec.connects(), b.String())
}

// ── 이벤트 수집 ──────────────────────────────────────────────────────────

// recorder는 Client가 내보내는 이벤트를 모두 모아 두고, 조건이 맞을 때까지 기다릴 수 있게 한다.
type recorder struct {
	mu       sync.Mutex
	msgs     []ami.Message
	nConnect int
	changed  chan struct{} // 새 항목이 오면 신호 (버퍼 1, 넘치면 버린다)
}

func newRecorder() *recorder {
	return &recorder{changed: make(chan struct{}, 1)}
}

func (r *recorder) run(events <-chan ami.Event) {
	for ev := range events {
		r.mu.Lock()
		switch ev.Kind {
		case ami.KindAMI:
			r.msgs = append(r.msgs, ev.Msg)
		case ami.KindConnected:
			r.nConnect++
		case ami.KindDisconnected:
			// 재접속은 Connected 횟수로 확인한다
		}
		r.mu.Unlock()
		select {
		case r.changed <- struct{}{}:
		default:
		}
	}
}

// mark는 지금까지 받은 이벤트 수다. 단계마다 '이 시점 이후'만 세기 위해 쓴다.
func (r *recorder) mark() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.msgs)
}

func (r *recorder) connects() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.nConnect
}

// since는 mark 이후에 받은 name 이벤트 중 match를 만족하는 것들이다.
func (r *recorder) since(mark int, name string, match func(ami.Message) bool) []ami.Message {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []ami.Message
	for _, m := range r.msgs[mark:] {
		if m.Event() == name && match(m) {
			out = append(out, m)
		}
	}
	return out
}

func (r *recorder) counts() map[string]int {
	r.mu.Lock()
	defer r.mu.Unlock()
	keep := map[string]bool{
		"QueueCallerJoin": true, "AgentConnect": true, "AgentComplete": true, "QueueCallerAbandon": true,
		"QueueMemberAdded": true, "QueueMemberRemoved": true, "OriginateResponse": true,
	}
	c := map[string]int{}
	for _, m := range r.msgs {
		if keep[m.Event()] {
			c[m.Event()]++
		}
	}
	return c
}

// waitFor는 cond가 참이 될 때까지 기다린다.
func (r *recorder) waitFor(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	for !cond() {
		select {
		case <-r.changed:
		case <-time.After(200 * time.Millisecond): // 신호를 놓쳐도 주기적으로 다시 확인
		case <-deadline.C:
			t.Fatalf("%s: %s 안에 기대한 상태가 되지 않음", what, timeout)
		}
	}
}

// waitCount는 mark 이후 name 이벤트가 n개 모일 때까지 기다리고 그 이벤트들을 돌려준다.
// 시간 안에 모이지 않으면 그동안 받은 이벤트 이름을 보여 주고 실패한다.
func (r *recorder) waitCount(t *testing.T, mark, n int, name string, match func(ami.Message) bool) []ami.Message {
	t.Helper()
	deadline := time.NewTimer(stepWait)
	defer deadline.Stop()
	for {
		got := r.since(mark, name, match)
		if len(got) >= n {
			return got
		}
		select {
		case <-r.changed:
		case <-time.After(200 * time.Millisecond):
		case <-deadline.C:
			t.Fatalf("%s %d건을 기다렸지만 %s 안에 %d건만 옴. 그동안 받은 이벤트: %s",
				name, n, stepWait, len(got), r.histogram(mark))
		}
	}
}

func (r *recorder) histogram(mark int) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	c := map[string]int{}
	for _, m := range r.msgs[mark:] {
		c[m.Event()]++
	}
	names := make([]string, 0, len(c))
	for n := range c {
		names = append(names, n)
	}
	sort.Strings(names)
	parts := make([]string, len(names))
	for i, n := range names {
		parts[i] = fmt.Sprintf("%s×%d", n, c[n])
	}
	return strings.Join(parts, " ")
}

// ── 이벤트 조건 ──────────────────────────────────────────────────────────

func isSimMember(m ami.Message) bool { return strings.HasPrefix(m.Get("MemberName"), "SIM-") }

// memberIn은 MemberName이 callOf의 키 중 하나인 이벤트를 고른다.
func memberIn(callOf map[string]ami.Message) func(ami.Message) bool {
	return func(m ami.Message) bool {
		_, ok := callOf[m.Get("MemberName")]
		return ok
	}
}

func callerIn(nums ...string) func(ami.Message) bool {
	set := map[string]bool{}
	for _, n := range nums {
		set[n] = true
	}
	return func(m ami.Message) bool { return set[m.Get("CallerIDNum")] }
}

func field(key, value string) func(ami.Message) bool {
	return func(m ami.Message) bool { return m.Get(key) == value }
}

func channelPrefix(prefix string) func(ami.Message) bool {
	return func(m ami.Message) bool { return strings.HasPrefix(m.Get("Channel"), prefix) }
}

func all(conds ...func(ami.Message) bool) func(ami.Message) bool {
	return func(m ami.Message) bool {
		for _, c := range conds {
			if !c(m) {
				return false
			}
		}
		return true
	}
}

func linkedidOf(msgs []ami.Message, num string) string {
	for _, m := range msgs {
		if m.Get("CallerIDNum") == num {
			return m.Get("Linkedid")
		}
	}
	return ""
}

// ── 환경 ─────────────────────────────────────────────────────────────────

// repoRoot는 docker-compose.yml이 있는 저장소 루트다 (테스트는 engine/e2e에서 실행된다).
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "docker-compose.yml")); err != nil {
		t.Fatalf("저장소 루트를 찾지 못함 (%s): %v", dir, err)
	}
	return dir
}

// compose는 저장소 루트에서 docker compose를 실행하고 합친 출력을 돌려준다.
func compose(root string, args ...string) (string, error) {
	cmd := exec.Command("docker", append([]string{"compose"}, args...)...)
	cmd.Dir = root
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	return out.String(), err
}

// waitFullyBooted는 Asterisk가 모듈을 다 올릴 때까지 기다린다 (컨테이너가 막 떴을 때 필요).
func waitFullyBooted(t *testing.T, root string) {
	t.Helper()
	deadline := time.Now().Add(90 * time.Second)
	for {
		out, err := compose(root, "exec", "-T", "asterisk", "asterisk", "-rx", "core waitfullybooted")
		if err == nil && strings.Contains(out, "fully booted") {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("Asterisk가 90초 안에 준비되지 않음 — docker compose ps / make logs 확인\n%s", lastLines(out, 10))
		}
		time.Sleep(time.Second)
	}
}

// replay는 녹화 파일을 끝까지 읽어 메시지 수를 돌려준다. 중간에 깨진 곳이 있으면 실패한다.
func replay(t *testing.T, path string) int {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	r := ami.NewReader(f)
	if _, found, err := r.SkipBanner(); err != nil || !found {
		t.Fatalf("%s: 배너로 시작하지 않음 (found=%v, err=%v)", path, found, err)
	}
	n := 0
	for {
		_, err := r.ReadMessage()
		if errors.Is(err, io.EOF) {
			return n
		}
		if err != nil {
			t.Fatalf("%s: %d번째 메시지 뒤에서 읽기 실패: %v", path, n, err)
		}
		n++
	}
}

func simIface(ext string) string { return "Local/" + ext + "@sim-agent" }

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
