// Package rtpcheck는 소프트폰처럼 SIP로 에코 번호에 전화를 걸어, 음성(RTP) 패킷이 실제로 오가는지 센다.
//
// Mac Docker Desktop의 포트포워딩과 NAT 설정(EXTERNAL_IP)을 거친 음성 경로를 사람 없이 검증하려고 만들었다.
// SIP를 직접 말해 보는 최소 구현이기도 하다 (docs/04-sip-sdp-rtp.md):
//
//	INVITE(SDP offer) → 401 → ACK → INVITE + Authorization → 100 → 200(SDP answer) → ACK
//	→ RTP 주고받기 → BYE → 200
package rtpcheck

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"sync/atomic"
	"time"
)

// Config는 검사 설정이다. 0 값은 기본값으로 채운다.
type Config struct {
	Server   string        // SIP 서버. 기본 "127.0.0.1:5060"
	User     string        // From 사용자이자 인증 사용자. 기본 "9001" (자동 테스트 전용 내선)
	Password string        // 기본 "sipp9001"
	Exten    string        // 걸 번호. 기본 "602" (안내멘트 없이 바로 에코)
	Packets  int           // 보낼 RTP 패킷 수. 20ms 간격이라 기본 100개 = 2초
	Timeout  time.Duration // SIP 최종 응답을 기다리는 시간. 기본 5초
}

func (c Config) withDefaults() Config {
	if c.Server == "" {
		c.Server = "127.0.0.1:5060"
	}
	if c.User == "" {
		c.User = "9001"
	}
	if c.Password == "" {
		c.Password = "sipp9001"
	}
	if c.Exten == "" {
		c.Exten = "602"
	}
	if c.Packets == 0 {
		c.Packets = 100
	}
	if c.Timeout == 0 {
		c.Timeout = 5 * time.Second
	}
	return c
}

// Result는 검사 결과다.
type Result struct {
	LocalSIP  string        // 우리 SIP 주소
	LocalRTP  string        // 우리 RTP 주소 (SDP offer에 적은 곳)
	RemoteRTP string        // Asterisk가 SDP answer의 c=, m=로 알려 준 RTP 주소
	Codec     string        // answer의 첫 코덱 (예: PCMU/8000)
	Sent      int           // 보낸 RTP 패킷
	Received  int           // 받은 RTP 패킷 (에코)
	FirstEcho time.Duration // 첫 패킷을 보낸 뒤 첫 에코가 오기까지
}

// OK는 보낸 패킷의 절반 이상이 돌아왔는지다. 처음 몇 개는 Asterisk가 상대 주소를 배우는 동안
// (strictrtp) 버려질 수 있어서 전부를 요구하지 않는다.
func (r Result) OK() bool { return r.Sent > 0 && r.Received*2 >= r.Sent }

// Run은 전화를 걸어 RTP를 주고받고 끊는다. SIP 단계에서 실패하면 그때까지의 결과와 함께 에러를 돌려준다.
// RTP가 돌아오지 않은 것은 에러가 아니라 Result로 판단한다 (Result.OK).
func Run(ctx context.Context, cfg Config) (Result, error) {
	cfg = cfg.withDefaults()
	var res Result

	server, err := net.ResolveUDPAddr("udp4", cfg.Server)
	if err != nil {
		return res, err
	}
	sipConn, err := net.DialUDP("udp4", nil, server)
	if err != nil {
		return res, err
	}
	defer sipConn.Close()
	local := sipConn.LocalAddr().(*net.UDPAddr)

	rtpConn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: local.IP})
	if err != nil {
		return res, err
	}
	defer rtpConn.Close()
	rtpLocal := rtpConn.LocalAddr().(*net.UDPAddr)
	res.LocalSIP, res.LocalRTP = local.String(), rtpLocal.String()

	d := &dialog{
		conn:       sipConn,
		cfg:        cfg,
		localHost:  local.IP.String(),
		localPort:  local.Port,
		serverHost: cfg.Server,
		callID:     randHex(8) + "@ipcc-rtpcheck",
		fromTag:    randHex(4),
	}
	answer, err := d.invite(ctx, sdpOffer(local.IP.String(), rtpLocal.Port))
	if err != nil {
		return res, err
	}
	remote, codec, err := parseSDPAnswer(answer.body)
	if err != nil {
		_ = d.bye(ctx)
		return res, err
	}
	res.RemoteRTP, res.Codec = remote.String(), codec

	res.Sent, res.Received, res.FirstEcho = exchangeRTP(ctx, rtpConn, remote, cfg.Packets)

	if err := d.bye(ctx); err != nil {
		return res, err
	}
	return res, nil
}

// dialog는 통화 하나의 SIP 상태다.
type dialog struct {
	conn       *net.UDPConn
	cfg        Config
	localHost  string
	localPort  int
	serverHost string // From/To의 호스트 부분 (예: 127.0.0.1:5060)
	callID     string
	fromTag    string
	toTag      string // 상대가 붙인 tag. 다이얼로그가 맺어진 뒤의 요청에 쓴다
	target     string // 이후 요청의 목적지 (200 OK의 Contact)
	cseq       int
}

func (d *dialog) uri() string { return "sip:" + d.cfg.Exten + "@" + d.serverHost }

// build는 요청 하나를 만든다. 요청마다 Via의 branch로 트랜잭션을 구분한다.
func (d *dialog) build(method, uri string, cseq int, branch string, extra []string, body string) []byte {
	to := "<sip:" + d.cfg.Exten + "@" + d.serverHost + ">"
	if d.toTag != "" {
		to += ";tag=" + d.toTag
	}
	msg := fmt.Sprintf("%s %s SIP/2.0\r\n", method, uri) +
		fmt.Sprintf("Via: SIP/2.0/UDP %s:%d;branch=%s;rport\r\n", d.localHost, d.localPort, branch) +
		"Max-Forwards: 70\r\n" +
		fmt.Sprintf("From: <sip:%s@%s>;tag=%s\r\n", d.cfg.User, d.serverHost, d.fromTag) +
		"To: " + to + "\r\n" +
		"Call-ID: " + d.callID + "\r\n" +
		fmt.Sprintf("CSeq: %d %s\r\n", cseq, method) +
		fmt.Sprintf("Contact: <sip:%s@%s:%d>\r\n", d.cfg.User, d.localHost, d.localPort) +
		"User-Agent: ipcc-rtpcheck\r\n"
	for _, h := range extra {
		msg += h + "\r\n"
	}
	if body != "" {
		msg += "Content-Type: application/sdp\r\n"
	}
	msg += fmt.Sprintf("Content-Length: %d\r\n\r\n%s", len(body), body)
	return []byte(msg)
}

// invite는 전화를 걸고 200 OK를 돌려준다. 401/407이면 인증 정보를 붙여 한 번 다시 건다.
func (d *dialog) invite(ctx context.Context, sdp string) (sipMsg, error) {
	d.cseq = 1
	branch := newBranch()
	resp, err := d.transact(ctx, d.build("INVITE", d.uri(), d.cseq, branch, nil, sdp), d.cseq, "INVITE")
	if err != nil {
		return resp, err
	}
	if s := resp.status(); s == 401 || s == 407 {
		// 4xx에 대한 ACK는 INVITE와 같은 트랜잭션이다: 같은 branch, 같은 CSeq 번호, 응답의 To tag
		d.toTag = tagOf(resp.get("To"))
		_, _ = d.conn.Write(d.build("ACK", d.uri(), d.cseq, branch, nil, ""))
		d.toTag = ""

		auth, err := authorization(resp, d.cfg.User, d.cfg.Password, "INVITE", d.uri(), randHex(8))
		if err != nil {
			return resp, err
		}
		d.cseq++
		resp, err = d.transact(ctx, d.build("INVITE", d.uri(), d.cseq, newBranch(), []string{auth}, sdp), d.cseq, "INVITE")
		if err != nil {
			return resp, err
		}
	}
	if resp.status() != 200 {
		return resp, fmt.Errorf("rtpcheck: INVITE %s: %s", d.uri(), resp.start)
	}

	d.toTag = tagOf(resp.get("To"))
	d.target = d.uri()
	if c := uriOf(resp.get("Contact")); c != "" {
		d.target = c
	}
	// 2xx에 대한 ACK는 새 트랜잭션이다: 새 branch, Contact로 보낸다
	_, _ = d.conn.Write(d.build("ACK", d.target, d.cseq, newBranch(), nil, ""))
	return resp, nil
}

func (d *dialog) bye(ctx context.Context) error {
	d.cseq++
	resp, err := d.transact(ctx, d.build("BYE", d.target, d.cseq, newBranch(), nil, ""), d.cseq, "BYE")
	if err != nil {
		return err
	}
	if resp.status() != 200 {
		return fmt.Errorf("rtpcheck: BYE: %s", resp.start)
	}
	return nil
}

// transact는 요청을 보내고 그 요청의 최종 응답(2xx 이상)을 기다린다.
// UDP라서 응답이 없으면 0.5초, 1초, 2초… 간격으로 다시 보낸다 (RFC 3261의 재전송 규칙을 줄인 것).
func (d *dialog) transact(ctx context.Context, req []byte, cseq int, method string) (sipMsg, error) {
	if _, err := d.conn.Write(req); err != nil {
		return sipMsg{}, err
	}
	deadline := time.Now().Add(d.cfg.Timeout)
	interval := 500 * time.Millisecond
	resend := time.Now().Add(interval)
	buf := make([]byte, 65535)
	for {
		if err := ctx.Err(); err != nil {
			return sipMsg{}, err
		}
		now := time.Now()
		if now.After(deadline) {
			return sipMsg{}, fmt.Errorf("rtpcheck: %s: %s 안에 최종 응답이 없음 (Asterisk가 떠 있는지, 포트가 맞는지 확인)", method, d.cfg.Timeout)
		}
		_ = d.conn.SetReadDeadline(earliest(resend, deadline))
		n, err := d.conn.Read(buf)
		if err != nil {
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				if time.Now().After(resend) {
					_, _ = d.conn.Write(req)
					interval = min(interval*2, 2*time.Second)
					resend = time.Now().Add(interval)
				}
				continue
			}
			return sipMsg{}, fmt.Errorf("rtpcheck: %s: %w", method, err)
		}
		m, err := parseSIP(buf[:n])
		if err != nil || !m.isResponse() {
			continue // 상대가 보낸 요청이나 깨진 패킷은 무시한다
		}
		if n, meth := m.cseq(); n != cseq || meth != method || m.status() < 200 {
			continue // 다른 트랜잭션의 응답(재전송된 200 등)이나 1xx
		}
		return m, nil
	}
}

// exchangeRTP는 20ms마다 PCMU 무음 패킷을 보내고, 같은 시간 동안 돌아오는 패킷을 센다.
func exchangeRTP(ctx context.Context, conn *net.UDPConn, remote *net.UDPAddr, packets int) (sent, received int, firstEcho time.Duration) {
	start := time.Now()
	_ = conn.SetReadDeadline(start.Add(time.Duration(packets)*20*time.Millisecond + time.Second))

	var got atomic.Int64
	var first atomic.Int64
	done := make(chan struct{})
	go func() {
		defer close(done)
		buf := make([]byte, 2048)
		for {
			n, _, err := conn.ReadFromUDP(buf)
			if err != nil {
				return // 읽기 마감 시각이 지났거나 소켓이 닫힘
			}
			if n >= 12 && buf[0]>>6 == 2 { // RTP 버전 2
				if got.Add(1) == 1 {
					first.Store(int64(time.Since(start)))
				}
			}
		}
	}()

	var ssrc [4]byte
	_, _ = rand.Read(ssrc[:])
	payload := make([]byte, 160) // 20ms × 8kHz × 1바이트
	for i := range payload {
		payload[i] = 0xFF // μ-law 무음
	}
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
send:
	for i := 0; i < packets; i++ {
		pkt := rtpPacket(uint16(i), uint32(i*160), binary.BigEndian.Uint32(ssrc[:]), i == 0, payload)
		if _, err := conn.WriteToUDP(pkt, remote); err == nil {
			sent++
		}
		select {
		case <-ticker.C:
		case <-ctx.Done():
			break send
		}
	}
	<-done
	return sent, int(got.Load()), time.Duration(first.Load())
}

// rtpPacket은 RTP 헤더(12바이트, RFC 3550) + 페이로드를 만든다. PT 0 = PCMU.
func rtpPacket(seq uint16, ts, ssrc uint32, marker bool, payload []byte) []byte {
	p := make([]byte, 12+len(payload))
	p[0] = 0x80 // 버전 2, 패딩·확장·CSRC 없음
	if marker {
		p[1] = 0x80
	}
	binary.BigEndian.PutUint16(p[2:], seq)
	binary.BigEndian.PutUint32(p[4:], ts)
	binary.BigEndian.PutUint32(p[8:], ssrc)
	copy(p[12:], payload)
	return p
}

func newBranch() string { return "z9hG4bK" + randHex(6) } // z9hG4bK = RFC 3261 방식이라는 표시

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func earliest(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}
