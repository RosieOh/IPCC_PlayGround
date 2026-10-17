package state

import (
	"fmt"
	"strconv"
	"strings"
)

// raw는 이미 만든 JSON 조각이다 (스냅샷의 배열).
type raw string

type field struct {
	key string
	val any // string, int64, bool, raw
}

// Out은 출력 한 줄(JSON 객체)이다. Rust의 json::Out을 옮긴 것으로, 키는 넣은 순서대로 나간다.
// encoding/json을 쓰지 않는 이유: 맵 순서·HTML 이스케이프·숫자 형식이 Rust 쪽과 달라져 차분 테스트가 깨진다.
type Out struct {
	fields []field
}

func newOut() *Out { return &Out{} }

func (o *Out) S(k, v string) *Out       { o.fields = append(o.fields, field{k, v}); return o }
func (o *Out) I(k string, v int64) *Out { o.fields = append(o.fields, field{k, v}); return o }
func (o *Out) B(k string, v bool) *Out  { o.fields = append(o.fields, field{k, v}); return o }
func (o *Out) Raw(k, v string) *Out     { o.fields = append(o.fields, field{k, raw(v)}); return o }

// Get은 문자열 필드 값이다 (테스트용).
func (o *Out) Get(k string) string {
	for _, f := range o.fields {
		if s, ok := f.val.(string); ok && f.key == k {
			return s
		}
	}
	return ""
}

// Line은 한 줄짜리 JSON을 만든다.
func (o *Out) Line() string {
	var b strings.Builder
	b.WriteByte('{')
	for i, f := range o.fields {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(quote(f.key))
		b.WriteByte(':')
		switch v := f.val.(type) {
		case string:
			b.WriteString(quote(v))
		case int64:
			b.WriteString(strconv.FormatInt(v, 10))
		case bool:
			b.WriteString(strconv.FormatBool(v))
		case raw:
			b.WriteString(string(v))
		}
	}
	b.WriteByte('}')
	return b.String()
}

// quote는 " \ 제어문자만 이스케이프하고 한글 등은 그대로 쓴다 (Rust json::quote와 같은 규칙).
func quote(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch {
		case r == '"':
			b.WriteString(`\"`)
		case r == '\\':
			b.WriteString(`\\`)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\r':
			b.WriteString(`\r`)
		case r == '\t':
			b.WriteString(`\t`)
		case r < 0x20:
			fmt.Fprintf(&b, `\u%04x`, r)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// parseTS는 "1792123142.227526"(유닉스 초.마이크로초)을 마이크로초로 바꾼다. 소수는 6자리로 맞춘다.
func parseTS(s string) (int64, bool) {
	sec, frac, _ := strings.Cut(s, ".")
	if sec == "" || !allDigits(sec) || !allDigits(frac) {
		return 0, false
	}
	if len(frac) > 6 {
		frac = frac[:6]
	}
	frac += strings.Repeat("0", 6-len(frac))
	secN, err := strconv.ParseInt(sec, 10, 64)
	if err != nil {
		return 0, false
	}
	fracN, err := strconv.ParseInt(frac, 10, 64)
	if err != nil {
		return 0, false
	}
	return secN*1_000_000 + fracN, true
}

// fmtTS는 마이크로초를 출력 형식 "초.마이크로초(6자리)"로 바꾼다.
func fmtTS(us int64) string {
	return fmt.Sprintf("%d.%06d", us/1_000_000, us%1_000_000)
}

func allDigits(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// Bool은 bool 필드 값이다 (액션의 paused). 없으면 false.
func (o *Out) Bool(k string) bool {
	for _, f := range o.fields {
		if b, ok := f.val.(bool); ok && f.key == k {
			return b
		}
	}
	return false
}
