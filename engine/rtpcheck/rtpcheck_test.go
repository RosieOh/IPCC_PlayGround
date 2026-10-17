package rtpcheck

import (
	"context"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"
)

// RFC 2617 3.5절의 예제 값으로 계산이 맞는지 확인한다.
func TestDigestResponseRFC2617(t *testing.T) {
	got := digestResponse("Mufasa", "Circle Of Life", "testrealm@host.com",
		"dcd98b7102dd2f0e8b11d0f600bfb0c093", "GET", "/dir/index.html", "auth", "0a4f113b", "00000001")
	if want := "6629fae49393a05397450978507c4ef1"; got != want {
		t.Fatalf("response = %s, want %s", got, want)
	}
}

// Asterisk가 실제로 보낸 401의 헤더 (docs/04-sip-sdp-rtp.md 3절)
func TestParseChallengeFromAsterisk(t *testing.T) {
	p := parseChallenge(`Digest realm="asterisk",nonce="1792123590/c3bc2643b17339c6558c4bc65007b1d2",opaque="323d1b2c6629862f",algorithm=MD5,qop="auth"`)
	want := map[string]string{
		"realm": "asterisk", "nonce": "1792123590/c3bc2643b17339c6558c4bc65007b1d2",
		"opaque": "323d1b2c6629862f", "algorithm": "MD5", "qop": "auth",
	}
	for k, v := range want {
		if p[k] != v {
			t.Errorf("%s = %q, want %q", k, p[k], v)
		}
	}
}

// Asterisk가 실제로 보낸 200 OK의 SDP answer
func TestParseSDPAnswerFromAsterisk(t *testing.T) {
	body := "v=0\r\no=- 1 4 IN IP4 127.0.0.1\r\ns=Asterisk\r\nc=IN IP4 127.0.0.1\r\nt=0 0\r\n" +
		"m=audio 10052 RTP/AVP 0 101\r\na=rtpmap:0 PCMU/8000\r\na=rtpmap:101 telephone-event/8000\r\n" +
		"a=fmtp:101 0-16\r\na=ptime:20\r\na=maxptime:140\r\na=sendrecv\r\n"
	addr, codec, err := parseSDPAnswer(body)
	if err != nil {
		t.Fatal(err)
	}
	if addr.String() != "127.0.0.1:10052" || codec != "PCMU/8000" {
		t.Fatalf("addr=%s codec=%s", addr, codec)
	}
}

func TestTagAndURIHelpers(t *testing.T) {
	if got := tagOf(`<sip:600@127.0.0.1>;tag=z9hG4bK-75-1-0`); got != "z9hG4bK-75-1-0" {
		t.Errorf("tagOf = %q", got)
	}
	if got := uriOf(`<sip:127.0.0.1:5060>`); got != "sip:127.0.0.1:5060" {
		t.Errorf("uriOf = %q", got)
	}
	if got := uriOf(`sip:asterisk@127.0.0.1:5060;transport=udp`); got != "sip:asterisk@127.0.0.1:5060" {
		t.Errorf("uriOf (no brackets) = %q", got)
	}
}

func TestRTPPacketHeader(t *testing.T) {
	p := rtpPacket(0x0102, 0x03040506, 0x0708090a, true, []byte{0xFF, 0xFF})
	want := []byte{0x80, 0x80, 0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x09, 0x0a, 0xFF, 0xFF}
	if string(p) != string(want) {
		t.Fatalf("packet = % x", p)
	}
}

func TestRunAgainstFakeServer(t *testing.T) {
	srv := newFakeUAS(t, "9001", "secret")
	res, err := Run(context.Background(), Config{
		Server: srv.addr, User: "9001", Password: "secret", Exten: "602", Packets: 25, Timeout: 2 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.OK() || res.Received < 20 {
		t.Fatalf("sent=%d received=%d, want almost all echoed", res.Sent, res.Received)
	}
	if res.RemoteRTP != srv.rtpAddr || res.Codec != "PCMU/8000" {
		t.Fatalf("remote=%s codec=%s, want %s PCMU/8000", res.RemoteRTP, res.Codec, srv.rtpAddr)
	}
	if got := srv.methods(); got != "INVITE ACK INVITE ACK BYE" {
		t.Fatalf("requests = %q", got)
	}
}

func TestRunWrongPassword(t *testing.T) {
	srv := newFakeUAS(t, "9001", "secret")
	_, err := Run(context.Background(), Config{Server: srv.addr, Password: "wrong", Timeout: 2 * time.Second})
	if err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("err = %v, want 403", err)
	}
}

func TestRunNoServer(t *testing.T) {
	// 아무도 듣지 않는 포트: 재전송하다가 타임아웃이나 ICMP 거부로 실패해야 한다
	ln, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.LocalAddr().String()
	ln.Close()
	if _, err := Run(context.Background(), Config{Server: addr, Timeout: 700 * time.Millisecond}); err == nil {
		t.Fatal("want error when nobody answers")
	}
}

// fakeUAS는 Asterisk 흉내를 낸다: 인증 요구(401) → digest 확인 → 200(SDP) → RTP 에코 → BYE에 200.
type fakeUAS struct {
	t       *testing.T
	addr    string
	rtpAddr string
	seen    chan string
}

func newFakeUAS(t *testing.T, user, pass string) *fakeUAS {
	t.Helper()
	sip, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	rtp, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sip.Close(); rtp.Close() })
	f := &fakeUAS{t: t, addr: sip.LocalAddr().String(), rtpAddr: rtp.LocalAddr().String(), seen: make(chan string, 32)}

	go func() { // RTP 에코 (Asterisk의 Echo())
		buf := make([]byte, 2048)
		for {
			n, from, err := rtp.ReadFromUDP(buf)
			if err != nil {
				return
			}
			_, _ = rtp.WriteToUDP(buf[:n], from)
		}
	}()

	go func() {
		buf := make([]byte, 65535)
		for {
			n, from, err := sip.ReadFromUDP(buf)
			if err != nil {
				return
			}
			req, err := parseSIP(buf[:n])
			if err != nil || req.isResponse() {
				continue
			}
			method := strings.Fields(req.start)[0]
			f.seen <- method
			reply := func(status, extra, body string) {
				msg := "SIP/2.0 " + status + "\r\n" +
					"Via: " + req.get("Via") + "\r\n" +
					"From: " + req.get("From") + "\r\n" +
					"To: " + req.get("To") + ";tag=srvtag\r\n" +
					"Call-ID: " + req.get("Call-ID") + "\r\n" +
					"CSeq: " + req.get("CSeq") + "\r\n" + extra
				if body != "" {
					msg += "Content-Type: application/sdp\r\n"
				}
				msg += fmt.Sprintf("Content-Length: %d\r\n\r\n%s", len(body), body)
				_, _ = sip.WriteToUDP([]byte(msg), from)
			}
			switch method {
			case "INVITE":
				auth := req.get("Authorization")
				if auth == "" {
					reply("401 Unauthorized", `WWW-Authenticate: Digest realm="test",nonce="n0nce",opaque="op",algorithm=MD5,qop="auth"`+"\r\n", "")
					continue
				}
				p := parseChallenge(auth)
				want := digestResponse(user, pass, "test", "n0nce", "INVITE", p["uri"], p["qop"], p["cnonce"], p["nc"])
				if p["username"] != user || p["response"] != want || p["opaque"] != "op" {
					reply("403 Forbidden", "", "")
					continue
				}
				reply("100 Trying", "", "")
				rtpPort := rtp.LocalAddr().(*net.UDPAddr).Port
				reply("200 OK", "Contact: <sip:fake@"+f.addr+">\r\n",
					fmt.Sprintf("v=0\r\no=- 1 1 IN IP4 127.0.0.1\r\ns=-\r\nc=IN IP4 127.0.0.1\r\nt=0 0\r\nm=audio %d RTP/AVP 0\r\na=rtpmap:0 PCMU/8000\r\n", rtpPort))
			case "BYE":
				reply("200 OK", "", "")
			}
		}
	}()
	return f
}

// methods는 지금까지 받은 요청 메서드를 순서대로 돌려준다.
func (f *fakeUAS) methods() string {
	var got []string
	for {
		select {
		case m := <-f.seen:
			got = append(got, m)
		case <-time.After(100 * time.Millisecond):
			return strings.Join(got, " ")
		}
	}
}
