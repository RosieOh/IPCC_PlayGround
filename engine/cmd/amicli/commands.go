package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"math/rand/v2"
	"os"
	"strconv"
	"strings"
	"time"

	"ipcc/engine/ami"
)

// runWatch는 이벤트를 실시간으로 보여 준다. CTI 엔진의 '수신부'를 그대로 축소한 것이다.
func runWatch(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("watch", flag.ExitOnError)
	o := addConnFlags(fs)
	flt := addFilterFlags(fs)
	record := fs.String("record", "recordings", `수신 원본을 녹화할 디렉터리 (""이면 녹화 안 함)`)
	asJSON := fs.Bool("json", false, "AMI 이벤트를 JSON Lines로 출력 (상태 메시지는 stderr로)")
	noSnapshot := fs.Bool("no-snapshot", false, "접속할 때 상태 스냅샷을 받지 않음")
	_ = fs.Parse(args)
	flt.init()

	status := io.Writer(os.Stdout)
	if *asJSON {
		status = os.Stderr
	}

	cfg := o.config("on", false)
	cfg.RecordDir = *record
	c := ami.New(cfg)
	go func() { _ = c.Run(ctx) }()

	for ev := range c.Events() {
		switch ev.Kind {
		case ami.KindConnected:
			fmt.Fprintf(status, "── %s 연결됨 #%d (AMI %s)\n", clock(ev.Received), ev.ConnID, ev.Version)
			if !*noSnapshot {
				printSnapshot(ctx, status, c)
			}
		case ami.KindDisconnected:
			fmt.Fprintf(status, "── %s 연결 끊김 #%d: %v\n   끊긴 동안의 이벤트는 사라졌다. 다시 붙으면 스냅샷으로 상태를 맞춘다.\n",
				clock(ev.Received), ev.ConnID, ev.Err)
		case ami.KindAMI:
			if !flt.show(ev.Msg.Event()) {
				continue
			}
			if *asJSON {
				writeJSONLine(os.Stdout, ev.Msg)
				continue
			}
			printEvent(os.Stdout, ev.Msg, ev.Received, flt.verbose)
		}
	}
	return nil
}

// printSnapshot은 '지금 이 순간'의 상태를 목록형 액션으로 다시 받는다.
//
// 이벤트만으로 상태를 쌓는 엔진은 연결이 끊겼던 동안의 변화를 알 수 없다.
// 그래서 (재)접속할 때마다 이렇게 기준점을 다시 잡는다. Stage 2에서는 이 결과로 상태머신을 보정한다.
func printSnapshot(ctx context.Context, w io.Writer, c *ami.Client) {
	sctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	_, chans, err := c.SendList(sctx, ami.NewAction("CoreShowChannels"))
	if err != nil {
		fmt.Fprintf(w, "   스냅샷 실패 (CoreShowChannels): %v\n", err)
		return
	}
	fmt.Fprintf(w, "── 스냅샷: 채널 %d개\n", len(chans))
	for _, ch := range chans {
		fmt.Fprintf(w, "   %-42s %-8s %s → %s  linkedid=%s\n",
			ch.Get("Channel"), ch.Get("ChannelStateDesc"), ch.Get("CallerIDNum"), ch.Get("Exten"), ch.Get("Linkedid"))
	}

	_, items, err := c.SendList(sctx, ami.NewAction("QueueStatus"))
	if err != nil {
		fmt.Fprintf(w, "   스냅샷 실패 (QueueStatus): %v\n", err)
		return
	}
	for _, m := range items {
		switch m.Event() {
		case "QueueParams":
			fmt.Fprintf(w, "── 큐 %s: 대기 %s명, 응대 %s, 포기 %s, 서비스레벨 %s%%\n",
				m.Get("Queue"), m.Get("Calls"), m.Get("Completed"), m.Get("Abandoned"), m.Get("ServicelevelPerf"))
		case "QueueMember":
			fmt.Fprintf(w, "   상담원 %-12s %-28s 상태=%s paused=%s 통화중=%s\n",
				m.Get("Name"), first(m.Get("Location"), m.Get("Interface")),
				deviceState(m.Get("Status")), m.Get("Paused"), m.Get("InCall"))
		case "QueueEntry":
			fmt.Fprintf(w, "   대기 %s번 %s (%s초째)\n", m.Get("Position"), m.Get("CallerIDNum"), m.Get("Wait"))
		}
	}
}

// runAction은 액션 하나를 보내고 응답을 출력한다.
func runAction(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("action", flag.ExitOnError)
	o := addConnFlags(fs)
	asJSON := fs.Bool("json", false, "JSON Lines로 출력")
	_ = fs.Parse(args)
	if fs.NArg() < 1 {
		return errors.New("액션 이름이 필요함. 예) amicli action CoreShowChannels")
	}

	act := ami.NewAction(fs.Arg(0))
	for _, kv := range fs.Args()[1:] {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			return fmt.Errorf("Key=Value 형식이 아님: %q", kv)
		}
		act = act.With(k, v)
	}

	c, stop, err := connectOnce(ctx, o)
	if err != nil {
		return err
	}
	defer stop()

	resp, items, err := c.SendList(ctx, act)
	if strings.EqualFold(act.Get("Action"), "Command") && !*asJSON && err == nil {
		printCommandOutput(os.Stdout, resp)
		return nil
	}
	for _, m := range append([]ami.Message{resp}, items...) {
		if len(m.Fields) == 0 {
			continue
		}
		if *asJSON {
			writeJSONLine(os.Stdout, m)
		} else {
			printMessage(os.Stdout, m)
		}
	}
	if !*asJSON && len(items) > 0 {
		fmt.Printf("(목록 %d건)\n", len(items))
	}
	return err
}

// runReplay는 녹화 파일을 읽어 watch와 같은 형식으로 보여 준다.
func runReplay(args []string) error {
	fs := flag.NewFlagSet("replay", flag.ExitOnError)
	flt := addFilterFlags(fs)
	asJSON := fs.Bool("json", false, "JSON Lines로 출력")
	_ = fs.Parse(args)
	flt.init()
	if fs.NArg() != 1 {
		return errors.New("녹화 파일 경로가 필요함. 예) amicli replay recordings/ami-20261016-140000-c1.log")
	}

	f, err := os.Open(fs.Arg(0))
	if err != nil {
		return err
	}
	defer f.Close()

	r := ami.NewReader(f)
	if v, found, err := r.SkipBanner(); err != nil {
		return err
	} else if found && !*asJSON {
		fmt.Printf("── 녹화 시작 (AMI %s)\n", v)
	}

	// 우리가 보낸 목록형 액션(스냅샷)의 응답에 딸린 항목은 실시간 이벤트가 아니므로 기본으로 숨긴다
	listIDs := map[string]bool{}
	var total, shown int
	for {
		m, err := r.ReadMessage()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "── %d번째 메시지 뒤에서 녹화가 잘림: %v\n", total, err)
			break
		}
		total++

		id := m.ActionID()
		if m.IsResponse() {
			if strings.EqualFold(m.Get("EventList"), "start") {
				listIDs[id] = true
			}
			if !flt.all {
				continue
			}
		} else if listIDs[id] {
			if strings.EqualFold(m.Get("EventList"), "Complete") {
				delete(listIDs, id)
			}
			if !flt.all {
				continue
			}
		}
		if !flt.show(m.Event()) {
			continue
		}
		shown++
		if *asJSON {
			writeJSONLine(os.Stdout, m)
		} else {
			printEvent(os.Stdout, m, time.Time{}, flt.verbose)
		}
	}
	if !*asJSON {
		fmt.Printf("── 끝: 메시지 %d개 중 %d개 표시\n", total, shown)
	}
	return nil
}

// runAgent는 상담원의 큐 로그인/로그아웃/이석/대기를 AMI 액션으로 바꾼다.
// Stage 2에서는 이 판단을 CTI 엔진이 하고, 여기서는 사람이 직접 한다.
func runAgent(ctx context.Context, args []string) error {
	if len(args) < 1 {
		return errors.New("login | logout | pause | unpause 중 하나가 필요함")
	}
	verb := args[0]
	fs := flag.NewFlagSet("agent "+verb, flag.ExitOnError)
	o := addConnFlags(fs)
	sim := fs.Bool("sim", false, "가짜 상담원 (Local/<내선>@sim-agent)")
	queue := fs.String("queue", "support", "큐 이름")
	reason := fs.String("reason", "", "이석 사유 (pause일 때). 예) 휴식, 식사, 교육")
	_ = fs.Parse(args[1:])

	exts := fs.Args()
	if len(exts) == 0 {
		exts = []string{"1001", "1002", "1003"}
	}

	c, stop, err := connectOnce(ctx, o)
	if err != nil {
		return err
	}
	defer stop()

	failed := 0
	for _, ext := range exts {
		iface, name := "PJSIP/"+ext, "상담원"+ext
		if *sim {
			iface, name = "Local/"+ext+"@sim-agent", "SIM-"+ext
		}

		var act ami.Message
		switch verb {
		case "login":
			act = ami.NewAction("QueueAdd").With("Queue", *queue).With("Interface", iface).
				With("MemberName", name).With("Paused", "false")
		case "logout":
			act = ami.NewAction("QueueRemove").With("Queue", *queue).With("Interface", iface)
		case "pause", "unpause":
			act = ami.NewAction("QueuePause").With("Queue", *queue).With("Interface", iface).
				With("Paused", strconv.FormatBool(verb == "pause"))
			if *reason != "" {
				act = act.With("Reason", *reason)
			}
		default:
			return fmt.Errorf("알 수 없는 동작 %q (login | logout | pause | unpause)", verb)
		}

		resp, err := c.Send(ctx, act)
		if err != nil {
			failed++
			fmt.Printf("✗ %-8s %-26s %v\n", verb, iface, err)
			continue
		}
		fmt.Printf("✓ %-8s %-26s %s\n", verb, iface, resp.Get("Message"))
	}
	if failed > 0 {
		return fmt.Errorf("%d건 실패", failed)
	}
	return nil
}

// runSimCall은 가짜 고객 콜을 만든다.
//
// Originate로 Local/customer@sim 채널을 만들면 ;2 쪽은 다이얼플랜 [sim] → [inbound] → 큐로 가고,
// ;1 쪽은 '고객 전화기'가 되어 Wait(patience)로 수화기를 들고 있다가 끊는다.
func runSimCall(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("sim-call", flag.ExitOnError)
	o := addConnFlags(fs)
	n := fs.Int("n", 1, "인입시킬 콜 수")
	gap := fs.Duration("gap", 2*time.Second, "콜 사이 간격")
	patience := fs.Int("patience", 300, "고객이 전화를 들고 있는 최대 시간(초). 짧게 주면 대기 중 포기가 된다")
	talk := fs.Int("talk", 0, "상담원 통화 시간(초). 0이면 5~15초 랜덤")
	_ = fs.Parse(args)

	c, stop, err := connectOnce(ctx, o)
	if err != nil {
		return err
	}
	defer stop()

	for i := range *n {
		num := fmt.Sprintf("010%08d", rand.IntN(100_000_000))
		act := ami.NewAction("Originate").
			With("Channel", "Local/customer@sim").
			With("Application", "Wait").
			With("Data", strconv.Itoa(*patience)).
			With("CallerID", fmt.Sprintf(`"고객" <%s>`, num)).
			With("Async", "true") // 응답(연결)을 기다리지 않고 바로 돌아온다
		if *talk > 0 {
			// __ 접두사: 이 채널에서 파생되는 채널(큐가 부르는 상담원 쪽)까지 변수가 상속된다
			act = act.With("Variable", "__SIM_TALK="+strconv.Itoa(*talk))
		}
		if _, err := c.Send(ctx, act); err != nil {
			return err
		}
		fmt.Printf("인입 %d/%d  %s\n", i+1, *n, num)
		if i < *n-1 {
			select {
			case <-time.After(*gap):
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}
	return nil
}
