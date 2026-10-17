// rtpcheck는 SIP로 에코 번호(602)에 전화를 걸어 음성(RTP)이 양방향으로 오가는지 확인한다.
//
// Mac에서 실행하면 소프트폰과 같은 길(127.0.0.1:5060 → Docker Desktop 포트포워딩 → 컨테이너)을 지나므로,
// NAT 설정(EXTERNAL_IP, RTP 포트 공개 범위)이 맞는지 사람 없이 확인할 수 있다.
//
//	make rtp-check
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"ipcc/engine/rtpcheck"
)

func main() {
	server := flag.String("server", envOr("SIP_ADDR", "127.0.0.1:5060"), "SIP 서버 (LAN 모드 확인은 Mac의 LAN IP:5060)")
	user := flag.String("user", "9001", "인증 사용자 (자동 테스트 전용 내선)")
	pass := flag.String("pass", "sipp9001", "비밀번호")
	exten := flag.String("exten", "602", "걸 번호 (602 = 안내멘트 없는 에코)")
	packets := flag.Int("n", 100, "보낼 RTP 패킷 수 (20ms 간격)")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	res, err := rtpcheck.Run(ctx, rtpcheck.Config{
		Server: *server, User: *user, Password: *pass, Exten: *exten, Packets: *packets, Timeout: 5 * time.Second,
	})
	fmt.Printf("SIP  우리 %s → 서버 %s (%s → %s)\n", res.LocalSIP, *server, *user, *exten)
	if res.RemoteRTP != "" {
		fmt.Printf("SDP  Asterisk가 알려 준 음성 주소 %s (%s), 우리 음성 주소 %s\n", res.RemoteRTP, res.Codec, res.LocalRTP)
	}
	if res.Sent > 0 {
		fmt.Printf("RTP  보냄 %d / 받음 %d", res.Sent, res.Received)
		if res.Received > 0 {
			fmt.Printf(", 첫 에코까지 %s", res.FirstEcho.Round(time.Millisecond))
		}
		fmt.Println()
	}
	switch {
	case err != nil:
		fmt.Println("✗", err)
		os.Exit(1)
	case !res.OK():
		fmt.Println("✗ 보낸 음성이 돌아오지 않았다 — 'SDP' 줄의 주소가 이 컴퓨터에서 갈 수 있는 주소인지 본다 (docs/04-sip-sdp-rtp.md 6절)")
		os.Exit(1)
	default:
		fmt.Println("✓ 양방향 음성 경로 정상")
	}
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
