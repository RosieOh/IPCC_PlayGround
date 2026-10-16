package ami

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strings"
)

// bannerPrefix는 접속 직후 Asterisk가 보내는 첫 줄의 앞부분이다.
//
//	Asterisk Call Manager/11.0.0
const bannerPrefix = "Asterisk Call Manager/"

// ErrNotAMI는 첫 줄이 AMI 배너가 아닐 때 돌려준다. 포트를 잘못 짚은 경우가 대부분이다.
var ErrNotAMI = errors.New("ami: not an AMI banner")

// Reader는 AMI 바이트 스트림을 Message 단위로 자른다.
// 네트워크 연결과 녹화 파일에 똑같이 쓸 수 있다(둘 다 io.Reader).
type Reader struct {
	br *bufio.Reader
}

// NewReader는 r에서 메시지를 읽는 Reader를 만든다.
func NewReader(r io.Reader) *Reader {
	return &Reader{br: bufio.NewReaderSize(r, 64*1024)}
}

// ReadBanner는 배너 줄을 읽고 AMI 프로토콜 버전을 돌려준다.
// 배너는 빈 줄로 끝나지 않는 유일한 줄이라서 메시지와 따로 읽는다.
func (r *Reader) ReadBanner() (version string, err error) {
	line, err := r.readLine()
	if err != nil {
		return "", err
	}
	if !strings.HasPrefix(line, bannerPrefix) {
		return "", fmt.Errorf("%w: %q", ErrNotAMI, line)
	}
	return strings.TrimPrefix(line, bannerPrefix), nil
}

// SkipBanner는 스트림이 배너로 시작할 때만 그 줄을 건너뛴다. 녹화 파일을 재생할 때 쓴다.
func (r *Reader) SkipBanner() (version string, found bool, err error) {
	head, err := r.br.Peek(len(bannerPrefix))
	if err != nil {
		if errors.Is(err, io.EOF) {
			return "", false, nil // 배너보다 짧은 파일
		}
		return "", false, err
	}
	if string(head) != bannerPrefix {
		return "", false, nil
	}
	version, err = r.ReadBanner()
	return version, err == nil, err
}

// ReadMessage는 빈 줄이 나올 때까지 읽어서 메시지 하나를 만든다.
//
// 메시지 사이의 빈 줄이 여러 개여도 건너뛴다. 메시지 중간에 스트림이 끝나면
// io.ErrUnexpectedEOF를, 메시지 경계에서 깔끔하게 끝나면 io.EOF를 돌려준다.
func (r *Reader) ReadMessage() (Message, error) {
	var m Message
	for {
		line, err := r.readLine()
		if err != nil {
			if errors.Is(err, io.EOF) && len(m.Fields) > 0 {
				return m, io.ErrUnexpectedEOF
			}
			return m, err
		}
		if line == "" {
			if len(m.Fields) == 0 {
				continue
			}
			return m, nil
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			// 콜론이 없는 줄(옛 버전 Command 출력 등)은 키 없이 값으로만 보관한다.
			m.Fields = append(m.Fields, Field{Value: line})
			continue
		}
		// "Key: Value"에서 콜론 뒤 공백 '한 칸'만 뗀다.
		// CLI 출력처럼 값 자체가 공백으로 시작하는 경우 들여쓰기를 지키기 위해서다.
		m.Fields = append(m.Fields, Field{Key: key, Value: strings.TrimPrefix(value, " ")})
	}
}

// readLine은 한 줄을 읽고 끝의 CRLF(또는 LF)를 뗀다.
func (r *Reader) readLine() (string, error) {
	line, err := r.br.ReadString('\n')
	if err != nil {
		if errors.Is(err, io.EOF) && line != "" {
			return "", io.ErrUnexpectedEOF // 개행 없이 끊긴 줄
		}
		return "", err
	}
	line = strings.TrimSuffix(line, "\n")
	return strings.TrimSuffix(line, "\r"), nil
}
