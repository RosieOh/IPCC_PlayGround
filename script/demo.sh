#!/usr/bin/env bash
# 📞 현황판이 움직이게: 가짜 상담원을 로그인·대기로 만들고 가짜 고객 콜을 건다 (먼저 script/start.sh)
#
#   script/demo.sh              상담원 3명(1001~1003) 대기 + 콜 6통 (3초 간격)
#   script/demo.sh -n 10        콜 10통
#   script/demo.sh -a 2         상담원 2명만 (1001, 1002)
#   script/demo.sh --loop       Ctrl+C를 누를 때까지 몇 통씩 계속 건다
#   script/demo.sh --rooms      룸 큐 현황판용: 스킬이 다른 상담원(1001 support · 1002 sales · 1003 모든 큐) + support·sales 콜
#
# 상담원 명령은 엔진 API로 보낸다 (화면의 버튼과 같은 길). 콜은 bin/amicli sim-call이 Originate로 만든다.

set -euo pipefail
# shellcheck source=script/lib.sh
source "$(dirname "$0")/lib.sh"

CALLS=6
AGENTS=3
LOOP=0
ROOMS=0
BANNER=1
while [ $# -gt 0 ]; do
	case "$1" in
	-n) CALLS=${2:?-n 다음에 콜 수}; shift ;;
	-a) AGENTS=${2:?-a 다음에 상담원 수}; shift ;;
	--loop) LOOP=1 ;;
	--rooms) ROOMS=1 ;;
	--no-banner) BANNER=0 ;;
	-h | --help) usage && exit 0 ;;
	*) die "모르는 옵션이에요: $1  (script/demo.sh --help)" ;;
	esac
	shift
done
case "$CALLS$AGENTS" in *[!0-9]*) die "-n, -a에는 숫자를 주세요" ;; esac
if [ "$AGENTS" -lt 1 ] || [ "$AGENTS" -gt 3 ]; then die "가짜 상담원은 1~3명이에요 (Local/1001~1003@sim-agent)"; fi

if [ "$ROOMS" = 1 ]; then
	[ "$BANNER" = 1 ] && banner "🏠" "IPCC Playground · 룸 큐 데모" "스킬이 다른 가짜 상담원 ${AGENTS}명 + support·sales 콜"
	rq_synced || die "룸 큐 엔진이 꺼져 있거나 아직 준비 중이에요 → script/start.sh"
	SEND=send_rq
	BOARD_URL="$WEB_URL/rooms"
else
	[ "$BANNER" = 1 ] && banner "📞" "IPCC Playground · 데모" "가짜 상담원 ${AGENTS}명 + 가짜 고객 콜"
	engine_synced || die "엔진이 꺼져 있거나 아직 준비 중이에요 → script/start.sh"
	SEND=send_cmd
	BOARD_URL="$WEB_URL"
fi
ensure_bins

# ── 상담원: 로그인(이석) → (룸 큐면 스킬) → 대기 ────────────────────────────
row "👥" "가짜 상담원 대기"
names=""
for i in $(seq 1 "$AGENTS"); do
	iface="Local/100${i}@sim-agent"
	"$SEND" login "$iface" "SIM-100${i}" || die "엔진에 명령을 보내지 못했어요"
	names="$names SIM-100${i}"
done
if [ "$ROOMS" = 1 ]; then
	send_rq skills Local/1001@sim-agent support || true
	[ "$AGENTS" -ge 2 ] && { send_rq skills Local/1002@sim-agent sales || true; }
	[ "$AGENTS" -ge 3 ] && { send_rq skills Local/1003@sim-agent "" || true; }
fi
sleep 0.5
for i in $(seq 1 "$AGENTS"); do "$SEND" ready "Local/100${i}@sim-agent" || true; done
ok "${names# } 대기$([ "$ROOMS" = 1 ] && echo " (스킬: 1001 support · 1002 sales · 1003 모든 큐)")"

# ── 콜 ────────────────────────────────────────────────────────────────────
# sim-call 출력 "인입 1/4  01042724236"을 보기 좋게 바꾼다. call_n 콜수 간격 [내선]
call_n() {
	"$ROOT/bin/amicli" sim-call -n "$1" -gap "${2:-3s}" -exten "${3:-customer}" 2>&1 | while IFS= read -r line; do
		case "$line" in
		인입*) printf '      📲 %s%s\n' "$(sed -E 's/인입 ([0-9]+)\/([0-9]+) +([0-9]+)/\1\/\2  고객 \3/' <<<"$line")" "${4:-}" ;;
		*) printf '      %s%s%s\n' "$D" "$line" "$R" ;;
		esac
	done
}
# calls 콜수 [간격] → 룸 큐면 절반은 sales(5101)로
calls() {
	if [ "$ROOMS" = 1 ]; then
		call_n $(($1 - $1 / 2)) "${2:-3s}" rq-customer "  → support"
		[ $(($1 / 2)) -gt 0 ] && call_n $(($1 / 2)) "${2:-3s}" rq-sales "  → sales"
		return 0
	fi
	call_n "$1" "${2:-3s}"
}
summary_line() { if [ "$ROOMS" = 1 ]; then rq_summary; else engine_summary; fi; }

if [ "$LOOP" = 0 ]; then
	printf '  📞  가짜 콜 %s통 %s(3초 간격)%s\n' "$CALLS" "$D" "$R"
	calls "$CALLS"
	if [ "$BANNER" = 1 ]; then
		echo
		printf '  👀  화면에서 보세요 → %s%s%s  %s(벨 → 통화 → 후처리 10초 → 대기)%s\n\n' "$CYN" "$BOARD_URL" "$R" "$D" "$R"
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
	summary=$(summary_line 2>/dev/null) && note "📊 $summary"
	sleep "$rest"
done
