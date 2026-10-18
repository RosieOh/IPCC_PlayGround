#!/usr/bin/env bash
# 📞 현황판이 움직이게: 가짜 상담원을 로그인·대기로 만들고 가짜 고객 콜을 건다 (먼저 script/start.sh)
#
#   script/demo.sh              상담원 3명(1001~1003) 대기 + 콜 6통 (3초 간격)
#   script/demo.sh -n 10        콜 10통
#   script/demo.sh -a 2         상담원 2명만 (1001, 1002)
#   script/demo.sh --loop       Ctrl+C를 누를 때까지 몇 통씩 계속 건다
#
# 상담원 명령은 엔진 API로 보낸다 (화면의 버튼과 같은 길). 콜은 bin/amicli sim-call이 Originate로 만든다.

set -euo pipefail
# shellcheck source=script/lib.sh
source "$(dirname "$0")/lib.sh"

CALLS=6
AGENTS=3
LOOP=0
BANNER=1
while [ $# -gt 0 ]; do
	case "$1" in
	-n) CALLS=${2:?-n 다음에 콜 수}; shift ;;
	-a) AGENTS=${2:?-a 다음에 상담원 수}; shift ;;
	--loop) LOOP=1 ;;
	--no-banner) BANNER=0 ;;
	-h | --help) usage && exit 0 ;;
	*) die "모르는 옵션이에요: $1  (script/demo.sh --help)" ;;
	esac
	shift
done
case "$CALLS$AGENTS" in *[!0-9]*) die "-n, -a에는 숫자를 주세요" ;; esac
if [ "$AGENTS" -lt 1 ] || [ "$AGENTS" -gt 3 ]; then die "가짜 상담원은 1~3명이에요 (Local/1001~1003@sim-agent)"; fi

[ "$BANNER" = 1 ] && banner "📞" "IPCC Playground · 데모" "가짜 상담원 ${AGENTS}명 + 가짜 고객 콜"

engine_synced || die "엔진이 꺼져 있거나 아직 준비 중이에요 → script/start.sh"
ensure_bins

# ── 상담원: 로그인(이석 상태로 큐에 들어간다) → 대기 ─────────────────────────
row "👥" "가짜 상담원 대기"
names=""
for i in $(seq 1 "$AGENTS"); do
	iface="Local/100${i}@sim-agent"
	send_cmd login "$iface" "SIM-100${i}" || die "엔진에 명령을 보내지 못했어요"
	names="$names SIM-100${i}"
done
sleep 0.5
for i in $(seq 1 "$AGENTS"); do send_cmd ready "Local/100${i}@sim-agent" || true; done
ok "${names# } 대기"

# ── 콜 ────────────────────────────────────────────────────────────────────
# sim-call 출력 "인입 1/4  01042724236"을 보기 좋게 바꾼다
calls() {
	"$ROOT/bin/amicli" sim-call -n "$1" -gap "${2:-3s}" 2>&1 | while IFS= read -r line; do
		case "$line" in
		인입*) printf '      📲 %s\n' "$(sed -E 's/인입 ([0-9]+)\/([0-9]+) +([0-9]+)/\1\/\2  고객 \3/' <<<"$line")" ;;
		*) printf '      %s%s%s\n' "$D" "$line" "$R" ;;
		esac
	done
}

if [ "$LOOP" = 0 ]; then
	printf '  📞  가짜 콜 %s통 %s(3초 간격)%s\n' "$CALLS" "$D" "$R"
	calls "$CALLS"
	if [ "$BANNER" = 1 ]; then
		echo
		printf '  👀  화면에서 보세요 → %s%s%s  %s(벨 → 통화 → 후처리 10초 → 대기)%s\n\n' "$CYN" "$WEB_URL" "$R" "$D" "$R"
	fi
	exit 0
fi

round=0
trap 'printf "\n  👋 데모를 멈췄어요. 상담원은 그대로예요 (끄기: script/stop.sh)\n\n"; exit 0' INT
printf '  🔁  계속 걸어요 %s(Ctrl+C로 멈춤)%s\n' "$D" "$R"
while true; do
	round=$((round + 1))
	n=$((RANDOM % 3 + 2))   # 2~4통
	rest=$((RANDOM % 8 + 6)) # 6~13초 쉬기
	printf '\n  %s#%d%s  📞 %d통\n' "$MAG" "$round" "$R" "$n"
	calls "$n" 2s
	summary=$(engine_summary 2>/dev/null) && note "📊 $summary"
	sleep "$rest"
done
