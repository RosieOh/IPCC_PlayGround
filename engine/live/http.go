package live

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"ipcc/engine/state"
)

// Handler는 화면(web/)용 HTTP API다. 브라우저는 직접 부르지 않고 게이트웨이(Next.js 라우트 핸들러)를 거친다.
//
//	GET  /api/state     지금 상태 전체 (JSON)
//	GET  /api/events    SSE. 처음에 "event: state"(전체 상태), 이후 상태머신 출력 줄과 연결 상태 줄을 하나씩
//	POST /api/commands  상담원 명령 {"kind":"notready","iface":"Local/1001@sim-agent","arg":"식사"}
//
// 인증이 없다. cmd/engine은 기본으로 127.0.0.1에만 연다.
func (r *Runner) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/state", r.handleState)
	mux.HandleFunc("GET /api/events", r.handleEvents)
	mux.HandleFunc("POST /api/commands", r.handleCommand)
	return mux
}

func (r *Runner) handleState(w http.ResponseWriter, req *http.Request) {
	s, err := r.view(req.Context(), nil)
	if err != nil {
		writeJSONError(w, http.StatusServiceUnavailable, err)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_, _ = w.Write([]byte(s))
}

// handleEvents는 SSE 스트림이다. 접속하면 전체 상태, 이후에는 바뀐 줄만 보낸다.
// 화면이 끊겼다 다시 붙으면(EventSource가 알아서 재접속) 다시 전체 상태부터 받으므로 놓친 줄을 걱정하지 않는다.
func (r *Runner) handleEvents(w http.ResponseWriter, req *http.Request) {
	ctx := req.Context()
	sub := make(chan string, 256)
	s, err := r.view(ctx, sub)
	if err != nil {
		writeJSONError(w, http.StatusServiceUnavailable, err)
		return
	}
	defer r.unsubscribe(sub)

	h := w.Header()
	h.Set("Content-Type", "text/event-stream; charset=utf-8")
	h.Set("Cache-Control", "no-cache, no-transform") // no-transform: 중간 압축이 스트림을 모아 두지 않게
	h.Set("X-Accel-Buffering", "no")
	rc := http.NewResponseController(w)
	if _, err := fmt.Fprintf(w, "retry: 3000\nevent: state\ndata: %s\n\n", s); err != nil {
		return
	}
	if err := rc.Flush(); err != nil {
		return
	}

	ping := time.NewTicker(15 * time.Second) // 프록시가 조용한 연결을 끊지 않게
	defer ping.Stop()
	for {
		var chunk string
		select {
		case <-ctx.Done():
			return
		case line, ok := <-sub:
			if !ok {
				return // 밀려서 actor가 끊었다. 화면이 다시 붙는다
			}
			chunk = "data: " + line + "\n\n" // 출력 줄은 한 줄짜리 JSON이라 그대로 data에 넣을 수 있다
		case <-ping.C:
			chunk = ": ping\n\n"
		}
		if _, err := w.Write([]byte(chunk)); err != nil {
			return
		}
		if err := rc.Flush(); err != nil {
			return
		}
	}
}

type commandRequest struct {
	Kind  string `json:"kind"`
	Iface string `json:"iface"`
	Arg   string `json:"arg"`
}

func (r *Runner) handleCommand(w http.ResponseWriter, req *http.Request) {
	var c commandRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, req.Body, 4096)).Decode(&c); err != nil {
		writeJSONError(w, http.StatusBadRequest, fmt.Errorf("JSON을 읽을 수 없음: %w", err))
		return
	}
	// 명령 파일 파서를 그대로 쓴다 (시각 자리는 버린다). 종류·인터페이스 검사도 거기서 한다
	line := strings.Join(strings.Fields("0 "+c.Kind+" "+c.Iface+" "+c.Arg), " ")
	cmds, err := state.ParseCommands(line)
	if err != nil || len(cmds) != 1 {
		if err == nil {
			err = errors.New("명령이 비었음")
		}
		writeJSONError(w, http.StatusBadRequest, err)
		return
	}
	if err := r.Command(req.Context(), cmds[0].Input.Cmd); err != nil {
		writeJSONError(w, http.StatusServiceUnavailable, err)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusAccepted)
	_, _ = w.Write([]byte(`{"ok":true}`))
}

func writeJSONError(w http.ResponseWriter, code int, err error) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
}
