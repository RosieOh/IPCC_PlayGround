package rtpcheck

import (
	"crypto/md5"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
)

// sipMsg는 SIP 메시지 하나다. 이 패키지에 필요한 만큼만 파싱한다.
type sipMsg struct {
	start   string // 시작 줄: "SIP/2.0 200 OK" 또는 "INVITE sip:602@… SIP/2.0"
	headers []sipHeader
	body    string
}

type sipHeader struct{ name, value string }

func parseSIP(b []byte) (sipMsg, error) {
	head, body, _ := strings.Cut(string(b), "\r\n\r\n")
	lines := strings.Split(head, "\r\n")
	if len(lines) == 0 || !strings.Contains(lines[0], "SIP/2.0") {
		return sipMsg{}, errors.New("rtpcheck: not a SIP message")
	}
	m := sipMsg{start: lines[0], body: body}
	for _, l := range lines[1:] {
		name, value, ok := strings.Cut(l, ":")
		if !ok {
			continue
		}
		m.headers = append(m.headers, sipHeader{strings.TrimSpace(name), strings.TrimSpace(value)})
	}
	return m, nil
}

// get은 헤더 값을 돌려준다. SIP 헤더 이름은 대소문자를 구분하지 않는다.
func (m sipMsg) get(name string) string {
	for _, h := range m.headers {
		if strings.EqualFold(h.name, name) {
			return h.value
		}
	}
	return ""
}

func (m sipMsg) isResponse() bool { return strings.HasPrefix(m.start, "SIP/2.0 ") }

// status는 응답 코드다 (요청이면 0).
func (m sipMsg) status() int {
	if !m.isResponse() {
		return 0
	}
	f := strings.Fields(m.start)
	if len(f) < 2 {
		return 0
	}
	n, _ := strconv.Atoi(f[1])
	return n
}

// cseq는 CSeq 헤더의 번호와 메서드다 ("2 INVITE" → 2, "INVITE").
func (m sipMsg) cseq() (int, string) {
	f := strings.Fields(m.get("CSeq"))
	if len(f) != 2 {
		return 0, ""
	}
	n, _ := strconv.Atoi(f[0])
	return n, f[1]
}

// tagOf는 From/To 헤더의 tag 파라미터 값을 꺼낸다.
func tagOf(v string) string {
	for _, p := range strings.Split(v, ";")[1:] {
		if k, val, ok := strings.Cut(strings.TrimSpace(p), "="); ok && strings.EqualFold(k, "tag") {
			return val
		}
	}
	return ""
}

// uriOf는 "<sip:…>" 또는 "sip:…;param" 형태에서 SIP URI만 꺼낸다 (Contact → 이후 요청의 목적지).
func uriOf(v string) string {
	if i := strings.Index(v, "<"); i >= 0 {
		if j := strings.Index(v[i:], ">"); j > 0 {
			return v[i+1 : i+j]
		}
	}
	u, _, _ := strings.Cut(v, ";")
	return strings.TrimSpace(u)
}

// parseChallenge는 WWW-Authenticate 값("Digest realm="…",nonce="…",…")을 파라미터 맵으로 바꾼다.
// 따옴표 안의 쉼표는 구분자가 아니다.
func parseChallenge(v string) map[string]string {
	v = strings.TrimSpace(v)
	if len(v) >= 6 && strings.EqualFold(v[:6], "Digest") {
		v = v[6:]
	}
	params := map[string]string{}
	var parts []string
	var cur strings.Builder
	inQuote := false
	for _, r := range v {
		switch {
		case r == '"':
			inQuote = !inQuote
			cur.WriteRune(r)
		case r == ',' && !inQuote:
			parts = append(parts, cur.String())
			cur.Reset()
		default:
			cur.WriteRune(r)
		}
	}
	parts = append(parts, cur.String())
	for _, p := range parts {
		k, val, ok := strings.Cut(strings.TrimSpace(p), "=")
		if !ok {
			continue
		}
		params[strings.ToLower(strings.TrimSpace(k))] = strings.Trim(strings.TrimSpace(val), `"`)
	}
	return params
}

// digestResponse는 HTTP/SIP Digest 인증의 response 값을 계산한다 (RFC 2617, MD5).
// 비밀번호를 그대로 보내지 않고, 서버가 준 nonce와 섞은 해시만 보낸다.
func digestResponse(user, pass, realm, nonce, method, uri, qop, cnonce, nc string) string {
	ha1 := md5hex(user + ":" + realm + ":" + pass)
	ha2 := md5hex(method + ":" + uri)
	if qop == "" {
		return md5hex(ha1 + ":" + nonce + ":" + ha2)
	}
	return md5hex(ha1 + ":" + nonce + ":" + nc + ":" + cnonce + ":" + qop + ":" + ha2)
}

func md5hex(s string) string {
	sum := md5.Sum([]byte(s))
	return hex.EncodeToString(sum[:])
}

// authorization은 401/407 응답에 답할 Authorization(또는 Proxy-Authorization) 헤더 한 줄을 만든다.
func authorization(chal sipMsg, user, pass, method, uri, cnonce string) (string, error) {
	header, value := "Authorization", chal.get("WWW-Authenticate")
	if value == "" {
		header, value = "Proxy-Authorization", chal.get("Proxy-Authenticate")
	}
	p := parseChallenge(value)
	if p["nonce"] == "" {
		return "", fmt.Errorf("rtpcheck: no digest challenge in %q", chal.start)
	}
	if alg := p["algorithm"]; alg != "" && !strings.EqualFold(alg, "MD5") {
		return "", fmt.Errorf("rtpcheck: unsupported digest algorithm %q", alg)
	}
	qop := ""
	for _, q := range strings.Split(p["qop"], ",") {
		if strings.TrimSpace(q) == "auth" {
			qop = "auth"
		}
	}
	const nc = "00000001"
	resp := digestResponse(user, pass, p["realm"], p["nonce"], method, uri, qop, cnonce, nc)

	var b strings.Builder
	fmt.Fprintf(&b, `%s: Digest username="%s", realm="%s", nonce="%s", uri="%s", response="%s", algorithm=MD5`,
		header, user, p["realm"], p["nonce"], uri, resp)
	if p["opaque"] != "" {
		fmt.Fprintf(&b, `, opaque="%s"`, p["opaque"])
	}
	if qop != "" {
		fmt.Fprintf(&b, `, qop=%s, nc=%s, cnonce="%s"`, qop, nc, cnonce)
	}
	return b.String(), nil
}

// sdpOffer는 우리 RTP 주소와 코덱(PCMU)을 알리는 SDP다.
func sdpOffer(ip string, port int) string {
	return strings.Join([]string{
		"v=0",
		"o=rtpcheck 1 1 IN IP4 " + ip,
		"s=rtpcheck",
		"c=IN IP4 " + ip,
		"t=0 0",
		fmt.Sprintf("m=audio %d RTP/AVP 0", port),
		"a=rtpmap:0 PCMU/8000",
		"a=ptime:20",
		"a=sendrecv",
		"",
	}, "\r\n")
}

// parseSDPAnswer는 상대가 "음성을 여기로 보내라"고 한 주소(c=, m=)와 첫 코덱을 꺼낸다.
// c=는 세션 수준과 미디어 수준에 올 수 있고, 미디어 수준이 우선한다.
func parseSDPAnswer(body string) (*net.UDPAddr, string, error) {
	var ip, pt string
	port := 0
	inAudio := false
	rtpmap := map[string]string{}
	for _, line := range strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n") {
		switch {
		case strings.HasPrefix(line, "m="):
			f := strings.Fields(line[2:])
			inAudio = len(f) >= 4 && f[0] == "audio"
			if inAudio {
				port, _ = strconv.Atoi(f[1])
				pt = f[3]
			}
		case strings.HasPrefix(line, "c="):
			f := strings.Fields(line[2:])
			if len(f) == 3 && (ip == "" || inAudio) {
				ip = f[2]
			}
		case strings.HasPrefix(line, "a=rtpmap:"):
			k, v, _ := strings.Cut(line[len("a=rtpmap:"):], " ")
			rtpmap[k] = v
		}
	}
	addr := net.ParseIP(ip)
	if addr == nil || port == 0 {
		return nil, "", fmt.Errorf("rtpcheck: no audio address in SDP answer")
	}
	codec := rtpmap[pt]
	if codec == "" {
		codec = "PT " + pt
	}
	return &net.UDPAddr{IP: addr, Port: port}, codec, nil
}
