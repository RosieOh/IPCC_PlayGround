// Package ami는 Asterisk Manager Interface(AMI) 프로토콜을 다룬다.
//
// AMI는 TCP 위의 텍스트 프로토콜이다. 메시지는 "Key: Value" 줄의 묶음이고
// 빈 줄 하나로 끝난다. 메시지는 세 종류가 있다.
//
//	Action    클라이언트 → Asterisk   Action: Originate
//	Response  Asterisk → 클라이언트   Response: Success   (Action에 대한 답)
//	Event     Asterisk → 클라이언트   Event: Newchannel   (언제든 날아옴)
//
// 자세한 설명은 docs/05-ami.md에 있다.
package ami

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// Field는 "Key: Value" 한 줄이다.
type Field struct {
	Key   string
	Value string
}

// Message는 AMI 메시지 하나다.
//
// 같은 키가 여러 번 나올 수 있어서(예: Command 응답의 Output, Originate의 Variable)
// map 대신 순서를 지키는 슬라이스로 보관한다.
type Message struct {
	Fields []Field
}

// ErrInvalidField는 보낼 메시지의 키나 값이 프로토콜을 깨뜨릴 때 돌려준다.
var ErrInvalidField = errors.New("ami: invalid field")

// NewAction은 "Action: name"으로 시작하는 메시지를 만든다.
//
//	act := ami.NewAction("QueuePause").With("Queue", "support").With("Paused", "true")
func NewAction(name string) Message {
	return Message{Fields: []Field{{Key: "Action", Value: name}}}
}

// With는 필드를 하나 덧붙인 '새' 메시지를 돌려준다. 원본은 바뀌지 않는다.
//
// 슬라이스를 새로 만들어 복사한다. append만 하면 여유 용량이 남은 원본 배열을
// 다른 사본과 공유하게 되어, 한쪽 수정이 다른 쪽에 비치는 버그가 생긴다.
func (m Message) With(key, value string) Message {
	fields := make([]Field, len(m.Fields), len(m.Fields)+1)
	copy(fields, m.Fields)
	return Message{Fields: append(fields, Field{Key: key, Value: value})}
}

// Lookup은 key의 첫 번째 값을 찾는다. AMI 키는 대소문자를 구분하지 않는다.
func (m Message) Lookup(key string) (string, bool) {
	for _, f := range m.Fields {
		if strings.EqualFold(f.Key, key) {
			return f.Value, true
		}
	}
	return "", false
}

// Get은 key의 첫 번째 값을 돌려준다. 없으면 빈 문자열이다.
func (m Message) Get(key string) string {
	v, _ := m.Lookup(key)
	return v
}

// GetAll은 key의 모든 값을 순서대로 돌려준다.
func (m Message) GetAll(key string) []string {
	var vs []string
	for _, f := range m.Fields {
		if strings.EqualFold(f.Key, key) {
			vs = append(vs, f.Value)
		}
	}
	return vs
}

// Event는 이벤트 이름이다. 이벤트가 아니면 빈 문자열이다.
func (m Message) Event() string { return m.Get("Event") }

// ActionID는 요청과 응답을 짝짓는 ID다.
func (m Message) ActionID() string { return m.Get("ActionID") }

// IsEvent는 "Event:" 필드가 있는지 본다.
func (m Message) IsEvent() bool {
	_, ok := m.Lookup("Event")
	return ok
}

// IsResponse는 액션에 대한 응답인지 본다.
//
// "Response:" 필드만 보면 안 된다. OriginateResponse 이벤트는
// "Event: OriginateResponse"와 "Response: Success"를 함께 갖고 있어서,
// 이벤트로 먼저 판정하지 않으면 응답으로 잘못 분류되어 사라진다.
func (m Message) IsResponse() bool {
	_, ok := m.Lookup("Response")
	return ok && !m.IsEvent()
}

// withActionID는 ActionID를 id로 바꾼 사본을 돌려준다(기존 ActionID는 버린다).
func (m Message) withActionID(id string) Message {
	fields := make([]Field, 0, len(m.Fields)+1)
	for _, f := range m.Fields {
		if !strings.EqualFold(f.Key, "ActionID") {
			fields = append(fields, f)
		}
	}
	return Message{Fields: append(fields, Field{Key: "ActionID", Value: id})}
}

// Encode는 메시지를 전송 형식(줄마다 CRLF, 끝에 빈 줄)으로 만든다.
//
// 값에 CR/LF가 들어 있으면 거부한다. 그대로 보내면 줄바꿈 뒤의 내용이
// 별도 필드로 해석되어, 외부 입력으로 엉뚱한 필드를 끼워 넣는 공격(헤더 인젝션)이 된다.
func (m Message) Encode() ([]byte, error) {
	var b bytes.Buffer
	for _, f := range m.Fields {
		if f.Key == "" || strings.ContainsAny(f.Key, ":\r\n") {
			return nil, fmt.Errorf("%w: key %q", ErrInvalidField, f.Key)
		}
		if strings.ContainsAny(f.Value, "\r\n") {
			return nil, fmt.Errorf("%w: value of %s contains CR/LF", ErrInvalidField, f.Key)
		}
		b.WriteString(f.Key)
		b.WriteString(": ")
		b.WriteString(f.Value)
		b.WriteString("\r\n")
	}
	b.WriteString("\r\n")
	return b.Bytes(), nil
}

// String은 디버깅용 한 줄 표현이다.
func (m Message) String() string {
	parts := make([]string, len(m.Fields))
	for i, f := range m.Fields {
		parts[i] = f.Key + ": " + f.Value
	}
	return strings.Join(parts, " | ")
}

// MarshalJSON은 필드 순서를 지킨 JSON 객체를 만든다. 같은 키가 여러 번 나오면 배열로 묶는다.
func (m Message) MarshalJSON() ([]byte, error) {
	order := make([]string, 0, len(m.Fields))
	values := make(map[string][]string, len(m.Fields))
	for _, f := range m.Fields {
		if _, seen := values[f.Key]; !seen {
			order = append(order, f.Key)
		}
		values[f.Key] = append(values[f.Key], f.Value)
	}

	var b bytes.Buffer
	b.WriteByte('{')
	for i, k := range order {
		if i > 0 {
			b.WriteByte(',')
		}
		if err := writeJSON(&b, k); err != nil {
			return nil, err
		}
		b.WriteByte(':')
		var v any = values[k]
		if len(values[k]) == 1 {
			v = values[k][0]
		}
		if err := writeJSON(&b, v); err != nil {
			return nil, err
		}
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

// writeJSON은 HTML 이스케이프 없이 JSON을 쓴다.
// 기본 json.Marshal은 "고객" <010...> 의 <, >를 <로 바꿔서 읽기 불편하다.
func writeJSON(b *bytes.Buffer, v any) error {
	var tmp bytes.Buffer
	enc := json.NewEncoder(&tmp)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return err
	}
	b.Write(bytes.TrimSuffix(tmp.Bytes(), []byte("\n")))
	return nil
}
