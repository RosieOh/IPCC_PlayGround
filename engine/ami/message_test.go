package ami

import (
	"bytes"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
	"testing/iotest"
)

func TestReadBannerAndMessages(t *testing.T) {
	stream := "Asterisk Call Manager/11.0.0\r\n" +
		"Response: Success\r\nActionID: a-1\r\nMessage: Authentication accepted\r\n\r\n" +
		"Event: FullyBooted\r\nPrivilege: system,all\r\nStatus: Fully Booted\r\n\r\n"
	r := NewReader(strings.NewReader(stream))

	v, err := r.ReadBanner()
	if err != nil || v != "11.0.0" {
		t.Fatalf("banner = %q, %v", v, err)
	}
	m1, err := r.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	if !m1.IsResponse() || m1.ActionID() != "a-1" || m1.Get("message") != "Authentication accepted" {
		t.Fatalf("m1 = %v", m1)
	}
	m2, err := r.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	if m2.Event() != "FullyBooted" || m2.Get("Status") != "Fully Booted" {
		t.Fatalf("m2 = %v", m2)
	}
	if _, err := r.ReadMessage(); !errors.Is(err, io.EOF) {
		t.Fatalf("end of stream err = %v, want io.EOF", err)
	}
}

// 한 바이트씩 도착해도, 줄 끝이 LF뿐이어도, 메시지 사이에 빈 줄이 더 있어도 같은 결과여야 한다.
func TestReadMessageHandlesSplitReadsAndLF(t *testing.T) {
	stream := "Event: Newchannel\nChannel: PJSIP/1001-00000001\nCallerIDName: 상담원 1001\n\n\n" +
		"Event: Hangup\r\nCause-txt: Normal Clearing\r\n\r\n"
	r := NewReader(iotest.OneByteReader(strings.NewReader(stream)))

	m1, err := r.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	if m1.Event() != "Newchannel" || m1.Get("CallerIDName") != "상담원 1001" {
		t.Fatalf("m1 = %v", m1)
	}
	m2, err := r.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	if m2.Event() != "Hangup" || m2.Get("Cause-txt") != "Normal Clearing" {
		t.Fatalf("m2 = %v", m2)
	}
}

func TestDuplicateKeysAndColonsInValue(t *testing.T) {
	stream := "Response: Success\r\n" +
		"Output: Channel  Location  State\r\n" +
		"Output:   PJSIP/1001 (None) Up\r\n" +
		"AppData: a:b:c\r\n\r\n"
	m, err := NewReader(strings.NewReader(stream)).ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	// 콜론 뒤 공백은 한 칸만 떼므로 CLI 출력의 들여쓰기가 남는다
	want := []string{"Channel  Location  State", "  PJSIP/1001 (None) Up"}
	if got := m.GetAll("Output"); !reflect.DeepEqual(got, want) {
		t.Fatalf("Output = %q, want %q", got, want)
	}
	if m.Get("AppData") != "a:b:c" {
		t.Fatalf("AppData = %q", m.Get("AppData"))
	}
}

// OriginateResponse는 Response 필드를 가진 '이벤트'다. 실제 Asterisk 녹화에서 발견한 경우.
func TestEventWithResponseFieldIsAnEvent(t *testing.T) {
	m := Message{Fields: []Field{
		{Key: "Event", Value: "OriginateResponse"},
		{Key: "ActionID", Value: "other-session-2"},
		{Key: "Response", Value: "Success"},
	}}
	if !m.IsEvent() || m.IsResponse() {
		t.Fatalf("IsEvent=%v IsResponse=%v, want event only", m.IsEvent(), m.IsResponse())
	}
}

func TestReadMessageUnexpectedEOF(t *testing.T) {
	r := NewReader(strings.NewReader("Event: Newchannel\r\nChannel: PJSIP/1001"))
	if _, err := r.ReadMessage(); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("err = %v, want io.ErrUnexpectedEOF", err)
	}
}

func TestReadBannerRejectsNonAMI(t *testing.T) {
	r := NewReader(strings.NewReader("SSH-2.0-OpenSSH_9.6\r\n"))
	if _, err := r.ReadBanner(); !errors.Is(err, ErrNotAMI) {
		t.Fatalf("err = %v, want ErrNotAMI", err)
	}
}

func TestSkipBanner(t *testing.T) {
	r := NewReader(strings.NewReader("Asterisk Call Manager/11.0.0\r\nEvent: Ping\r\n\r\n"))
	if v, found, err := r.SkipBanner(); err != nil || !found || v != "11.0.0" {
		t.Fatalf("with banner: %q %v %v", v, found, err)
	}
	if m, err := r.ReadMessage(); err != nil || m.Event() != "Ping" {
		t.Fatalf("after banner: %v %v", m, err)
	}

	r = NewReader(strings.NewReader("Event: Ping\r\n\r\n"))
	if _, found, err := r.SkipBanner(); err != nil || found {
		t.Fatalf("without banner: found=%v err=%v", found, err)
	}
	if m, err := r.ReadMessage(); err != nil || m.Event() != "Ping" {
		t.Fatalf("first message must be intact: %v %v", m, err)
	}
}

func TestEncodeRoundTrip(t *testing.T) {
	act := NewAction("Originate").
		With("Channel", "Local/customer@sim").
		With("CallerID", `"고객" <01012345678>`).
		With("Variable", "A=1").
		With("Variable", "B=2")
	wire, err := act.Encode()
	if err != nil {
		t.Fatal(err)
	}
	want := "Action: Originate\r\nChannel: Local/customer@sim\r\nCallerID: \"고객\" <01012345678>\r\n" +
		"Variable: A=1\r\nVariable: B=2\r\n\r\n"
	if string(wire) != want {
		t.Fatalf("wire = %q", wire)
	}
	back, err := NewReader(bytes.NewReader(wire)).ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(back, act) {
		t.Fatalf("round trip = %v, want %v", back, act)
	}
}

func TestEncodeRejectsInjection(t *testing.T) {
	// 값에 CRLF가 있으면 "Action: Hangup"이 별도 필드로 끼어들 수 있다
	if _, err := NewAction("Originate").With("CallerID", "x\r\nAction: Hangup").Encode(); !errors.Is(err, ErrInvalidField) {
		t.Fatalf("CRLF in value: err = %v", err)
	}
	if _, err := NewAction("Ping").With("Bad:Key", "v").Encode(); !errors.Is(err, ErrInvalidField) {
		t.Fatalf("colon in key: err = %v", err)
	}
}

// With는 원본과 배열을 공유하면 안 된다. 여유 용량이 있는 원본에서 두 사본을 만들어 확인한다.
func TestWithDoesNotAliasOriginal(t *testing.T) {
	base := Message{Fields: make([]Field, 1, 8)}
	base.Fields[0] = Field{Key: "Action", Value: "QueueAdd"}

	a := base.With("Interface", "PJSIP/1001")
	b := base.With("Interface", "PJSIP/1002")
	if a.Get("Interface") != "PJSIP/1001" || b.Get("Interface") != "PJSIP/1002" || len(base.Fields) != 1 {
		t.Fatalf("aliasing: a=%v b=%v base=%v", a, b, base)
	}
}

func TestWithActionIDReplaces(t *testing.T) {
	m := NewAction("Ping").With("ActionID", "mine").withActionID("client-1")
	if got := m.GetAll("ActionID"); !reflect.DeepEqual(got, []string{"client-1"}) {
		t.Fatalf("ActionID = %q", got)
	}
}

func TestMarshalJSONKeepsOrderAndGroupsDuplicates(t *testing.T) {
	m := Message{Fields: []Field{
		{Key: "Event", Value: "Newchannel"},
		{Key: "CallerIDName", Value: `"고객" <010>`},
		{Key: "Variable", Value: "A=1"},
		{Key: "Variable", Value: "B=2"},
	}}
	b, err := m.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	want := `{"Event":"Newchannel","CallerIDName":"\"고객\" <010>","Variable":["A=1","B=2"]}`
	if string(b) != want {
		t.Fatalf("json = %s\nwant   %s", b, want)
	}
}
