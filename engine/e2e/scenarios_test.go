//go:build e2e

package e2e

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ipcc/engine/ami"
)

// scenario는 상태머신(Stage 2) 테스트 재료가 될 녹화 하나다.
type scenario struct {
	name string // 녹화 파일 이름 (<name>.ami)
	desc string
	run  func(t *testing.T, h *harness)
}

// TestRecordScenarios는 시나리오마다 AMI 연결을 새로 열어 녹화하고, 호스트명을 가려 RECORD_SCENARIOS 디렉터리에 저장한다.
//
//	make scenarios                 (engine/state/testdata/*.ami 전부 다시 만들기)
//	make scenarios only=three-way  (SCENARIOS=이름,이름 — 고른 것만. 나머지 녹화와 기대 출력은 그대로 둔다)
//
// 녹화는 사실의 원천이다. 상태머신이 기대하는 이벤트 순서는 이 녹화에서 가져온다 (docs/06).
func TestRecordScenarios(t *testing.T) {
	out := os.Getenv("RECORD_SCENARIOS")
	if out == "" {
		t.Skip("RECORD_SCENARIOS가 없어서 건너뜀 (make scenarios로 실행)")
	}
	root := repoRoot(t)
	waitFullyBooted(t, root)
	host, _ := os.Hostname()

	only := map[string]bool{}
	for _, n := range strings.Split(os.Getenv("SCENARIOS"), ",") {
		if n = strings.TrimSpace(n); n != "" {
			only[n] = true
		}
	}

	var index []string
	for _, sc := range scenarios {
		index = append(index, fmt.Sprintf("| `%s.ami` | %s |", sc.name, sc.desc))
		if len(only) > 0 && !only[sc.name] {
			continue
		}
		t.Run(sc.name, func(t *testing.T) {
			h := newHarness(t, root)
			h.requireNoRealActivity(t)
			h.removeTestAgents()
			h.waitIdle(t)
			t.Cleanup(h.removeTestAgents)

			sc.run(t, h)
			h.waitIdle(t)
			h.removeTestAgents()
			time.Sleep(300 * time.Millisecond) // 로그아웃 이벤트까지 녹화에 담기게
			h.stop()

			files, _ := filepath.Glob(filepath.Join(h.recordDir, "ami-*.log"))
			if len(files) != 1 {
				t.Fatalf("녹화 파일이 %d개 (1개여야 함): %v", len(files), files)
			}
			raw, err := os.ReadFile(files[0])
			if err != nil {
				t.Fatal(err)
			}
			text := string(raw)
			if host != "" {
				text = strings.ReplaceAll(text, host, "amicli-host")
			}
			dst := filepath.Join(out, sc.name+".ami")
			if err := os.WriteFile(dst, []byte(text), 0o644); err != nil {
				t.Fatal(err)
			}
			t.Logf("%s: %d바이트", dst, len(text))
		})
	}

	readme := "# 상태머신 테스트용 AMI 녹화\n\n" +
		"`make scenarios`로 다시 만든다 (engine/e2e/scenarios_test.go). 호스트명은 `amicli-host`로 가렸다.\n" +
		"각 `<이름>.ami`의 기대 출력은 `<이름>.golden.jsonl`이고, Rust 레거시로 만든다 (`make golden`).\n" +
		"`make diff-test`는 이 녹화들과 Stage 1 녹화(`engine/testdata/*.log`)를 Rust와 Go로 재생해 출력을 비교한다.\n\n" +
		"| 녹화 | 시나리오 |\n|---|---|\n" + strings.Join(index, "\n") + "\n\n" +
		"그 밖의 파일 (손으로 만든 것, `make scenarios`가 건드리지 않는다)\n\n" +
		"| 파일 | 내용 |\n|---|---|\n" +
		"| `normal.cmds` | `normal.ami`에 끼워 넣는 상담원 명령 (통화 중 이석 예약, 후처리 끝 버튼) → `normal.cmds.golden.jsonl` |\n" +
		"| `truncated.ami` | `normal.ami`를 메시지 중간(줄바꿈 바로 뒤)에서 자른 것. Rust 레거시의 파서 버그를 차분 테스트가 잡은 사례 (docs/07 10절) |\n"
	if err := os.WriteFile(filepath.Join(out, "README.md"), []byte(readme), 0o644); err != nil {
		t.Fatal(err)
	}
}

var scenarios = []scenario{
	{"normal", "가짜 상담원 2명, 고객 2명 — 큐 → 연결 → 2초 통화 → 상담원이 끊음", func(t *testing.T, h *harness) {
		h.login(t, simIface("1001"), "SIM-1001")
		h.login(t, simIface("1002"), "SIM-1002")
		mark := h.rec.mark()
		h.originate(t, "customer", h.number(), 60, 2)
		h.originate(t, "customer", h.number(), 60, 2)
		h.rec.waitCount(t, mark, 2, "AgentComplete", isSimMember)
	}},
	{"abandon", "상담원이 이석인 동안 고객이 4초 기다리다 끊음 — 포기호", func(t *testing.T, h *harness) {
		h.login(t, simIface("1001"), "SIM-1001")
		h.send(t, ami.NewAction("QueuePause").With("Queue", queueName).With("Interface", simIface("1001")).
			With("Paused", "true").With("Reason", "휴식"))
		mark := h.rec.mark()
		h.originate(t, "customer", h.number(), 4, 2)
		h.rec.waitCount(t, mark, 1, "QueueCallerAbandon", field("Queue", queueName))
	}},
	{"noanswer", "받지 않는 상담원에게 15초 벨 → 무응답 → 다음 상담원이 받음", func(t *testing.T, h *harness) {
		h.login(t, noAnswerIface, "SIM-NA-1004")
		mark := h.rec.mark()
		h.originate(t, "customer", h.number(), 60, 2)
		h.rec.waitCount(t, mark, 1, "AgentCalled", field("MemberName", "SIM-NA-1004"))
		h.login(t, simIface("1001"), "SIM-1001") // 첫 벨이 울린 뒤에 들어와서, 무응답 다음 차례가 된다
		h.rec.waitCount(t, mark, 1, "AgentRingNoAnswer", field("MemberName", "SIM-NA-1004"))
		h.rec.waitCount(t, mark, 1, "AgentComplete", field("MemberName", "SIM-1001"))
	}},
	{"ring-abandon", "상담원 벨이 울리는 동안(6초) 고객이 3초 만에 끊음", func(t *testing.T, h *harness) {
		h.login(t, simIface("1001"), "SIM-1001")
		mark := h.rec.mark()
		h.originateRing(t, "customer", h.number(), 3, 6, 2)
		h.rec.waitCount(t, mark, 1, "AgentCalled", field("MemberName", "SIM-1001"))
		h.rec.waitCount(t, mark, 1, "QueueCallerAbandon", func(ami.Message) bool { return true })
	}},
	{"caller-hangup", "통화 중에 고객이 먼저 끊음 (AgentComplete Reason=caller)", func(t *testing.T, h *harness) {
		h.login(t, simIface("1001"), "SIM-1001")
		mark := h.rec.mark()
		h.originate(t, "customer", h.number(), 6, 20) // 고객은 6초 뒤 끊고, 상담원은 20초 통화할 생각
		h.rec.waitCount(t, mark, 1, "AgentComplete", field("MemberName", "SIM-1001"))
	}},
	{"queue-timeout", "로그인한 상담원이 없고 고객은 큐에서 8초 뒤 타임아웃", func(t *testing.T, h *harness) {
		mark := h.rec.mark()
		h.originate(t, "customer-short", h.number(), 60, 2)
		h.rec.waitCount(t, mark, 1, "QueueCallerLeave", field("Queue", queueName))
	}},
	{"hold", "실제 SIP 상담원(9002)이 통화 중 보류했다가 해제 (re-INVITE sendonly → sendrecv)", func(t *testing.T, h *harness) {
		uas := make(chan error, 1)
		go func() {
			out, err := h.sippRun(agentSIPp, "-sf", "/sipp/uas-agent-hold.xml")
			if err != nil {
				err = fmt.Errorf("%w\n%s", err, lastLines(out, 20))
			}
			uas <- err
		}()
		time.Sleep(time.Second)
		h.login(t, sipAgentIface, sipAgentName)
		mark := h.rec.mark()
		h.sipp(t, "-sf", "/sipp/uac-call-wait-bye.xml", "-s", "5000")
		if err := <-uas; err != nil {
			t.Fatalf("상담원 SIPp 실패: %v", err)
		}
		h.rec.waitCount(t, mark, 1, "AgentComplete", field("MemberName", sipAgentName))
	}},
	{"blind-transfer", "상담원 1001이 받고 1초 뒤 고객을 1002에게 블라인드 전환 (AMI BlindTransfer)", func(t *testing.T, h *harness) {
		h.login(t, simIface("1001"), "SIM-1001")
		mark := h.rec.mark()
		h.originate(t, "customer", h.number(), 60, 4)
		conn := h.rec.waitCount(t, mark, 1, "AgentConnect", field("MemberName", "SIM-1001"))[0]
		time.Sleep(time.Second)
		h.send(t, ami.NewAction("BlindTransfer").With("Channel", conn.Get("DestChannel")).
			With("Context", "transfer").With("Exten", "1002"))
		h.rec.waitCount(t, mark, 1, "BlindTransfer", func(ami.Message) bool { return true })
	}},
	{"attended-transfer", "상담원 1001이 받고 1002와 먼저 통화한 뒤 넘김 (AMI Atxfer)", func(t *testing.T, h *harness) {
		h.login(t, simIface("1001"), "SIM-1001")
		mark := h.rec.mark()
		h.originate(t, "customer", h.number(), 60, 5)
		conn := h.rec.waitCount(t, mark, 1, "AgentConnect", field("MemberName", "SIM-1001"))[0]
		time.Sleep(time.Second)
		h.send(t, ami.NewAction("Atxfer").With("Channel", conn.Get("DestChannel")).
			With("Context", "transfer").With("Exten", "1002"))
		h.rec.waitCount(t, mark, 1, "AttendedTransfer", func(ami.Message) bool { return true })
	}},
	{"three-way", "상담원 1001이 1002를 어텐디드 전환으로 부른 뒤 *3(atxferthreeway)으로 3자통화 → 1001이 먼저 빠짐", func(t *testing.T, h *harness) {
		h.login(t, simIface("1001"), "SIM-1001")
		mark := h.rec.mark()
		h.originate(t, "customer", h.number(), 60, 8)
		agentCh := h.rec.waitCount(t, mark, 1, "AgentConnect", field("MemberName", "SIM-1001"))[0].Get("DestChannel")
		time.Sleep(time.Second)
		h.send(t, ami.NewAction("Atxfer").With("Channel", agentCh).With("Context", "transfer").With("Exten", "1002"))
		h.rec.waitCount(t, mark, 1, "DialEnd", all(field("DialStatus", "ANSWER"),
			func(m ami.Message) bool { return strings.HasPrefix(m.Get("DestChannel"), "Local/1002@sim-agent") }))
		// 전환하는 쪽(상담원 채널)이 *3을 누른 것처럼: PlayDTMF Receive=true는 DTMF를 '받은' 것으로 흉내 낸다
		for _, d := range []string{"*", "3"} {
			h.send(t, ami.NewAction("PlayDTMF").With("Channel", agentCh).With("Digit", d).With("Receive", "true"))
		}
		h.rec.waitCount(t, mark, 1, "AgentComplete", field("MemberName", "SIM-1001"))
	}},
	{"sip-agent", "실제 SIP 고객(9001) → 대표번호 → 실제 SIP 상담원(9002)이 받고 먼저 끊음", func(t *testing.T, h *harness) {
		h.stepSIPAgent(t)
	}},
}

// login은 상담원 하나를 큐에 넣고 QueueMemberAdded를 기다린다.
func (h *harness) login(t *testing.T, iface, name string) {
	t.Helper()
	mark := h.rec.mark()
	h.send(t, ami.NewAction("QueueAdd").With("Queue", queueName).With("Interface", iface).
		With("MemberName", name).With("Paused", "false"))
	h.rec.waitCount(t, mark, 1, "QueueMemberAdded", field("MemberName", name))
}
