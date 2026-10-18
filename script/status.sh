#!/usr/bin/env bash
# 📊 지금 무엇이 켜져 있는지 한눈에 본다: Docker · Asterisk · 엔진 · 화면 · 큐 · 채널
#
#   script/status.sh
#   script/status.sh --watch    2초마다 새로 그린다 (Ctrl+C로 끝)

set -uo pipefail
# shellcheck source=script/lib.sh
source "$(dirname "$0")/lib.sh"

WATCH=0
for a in "$@"; do
	case "$a" in
	--watch | -w) WATCH=1 ;;
	-h | --help) usage && exit 0 ;;
	*) die "모르는 옵션이에요: $a  (script/status.sh --help)" ;;
	esac
done

show() {
	banner "📊" "IPCC Playground · 지금 상태" "$(date '+%Y-%m-%d %H:%M:%S')"

	row "🐳" "Docker"
	if docker_up; then ok "실행 중"; else fail "꺼져 있어요 (Docker Desktop을 켜세요)"; fi

	row "📡" "Asterisk"
	if asterisk_running; then
		# "11 hours, 35 minutes, 35 seconds" → "11시간 35분" (초는 버린다)
		up=$(asterisk_rx "core show uptime" | sed -n 's/^System uptime: //p' |
			sed -E 's/ years?/년/; s/ weeks?/주/; s/ days?/일/; s/ hours?/시간/; s/ minutes?/분/; s/,? *[0-9]+ seconds?//; s/ seconds?/초/; s/, / /g')
		if asterisk_booted; then ok "실행 중 · ${up:-?}째"; else warn "부팅 중"; fi
	else
		fail "꺼져 있어요 → script/start.sh"
	fi

	row "🧠" "상태머신 엔진"
	if engine_up; then
		state=$(engine_api)
		pids=$(engine_pids | tr '\n' ' ')
		if grep -q '"synced":true' <<<"$state"; then
			ok "실행 중 · AMI 연결·재동기화 끝 ${D}(PID ${pids% })${R}"
		elif grep -q '"connected":true' <<<"$state"; then
			warn "AMI 연결됨 · 재동기화 기다리는 중"
		else
			warn "실행 중이지만 Asterisk(AMI)에 못 붙었어요"
		fi
		summary=$(engine_summary 2>/dev/null) && note "$summary"
	elif [ -n "$(engine_pids)" ]; then
		warn "프로세스는 있는데 화면용 API가 없어요 (-http 꺼짐?)"
	else
		fail "꺼져 있어요 → script/start.sh"
	fi

	row "🌐" "화면 (Next.js)"
	if web_up && [ -n "$(web_pids)" ]; then
		ok "실행 중 · $WEB_URL"
	elif port_busy "$WEB_PORT"; then
		warn "${WEB_PORT}번 포트를 다른 프로그램이 쓰고 있어요"
	else
		fail "꺼져 있어요 → script/start.sh"
	fi

	if asterisk_running; then
		q=$(asterisk_rx "queue show $QUEUE")
		members=$(grep -c "has taken" <<<"$q" || true)
		paused=$(grep -c "paused" <<<"$q" || true)
		waiting=$(grep -cE "^ +[0-9]+\. " <<<"$q" || true)
		row "👥" "큐 $QUEUE"
		if [ "${members:-0}" -gt 0 ]; then
			ok "상담원 ${members}명 · 이석·후처리로 멈춘 ${paused}명 · 대기 고객 ${waiting}명"
		else
			ok "로그인한 상담원 없음 · 대기 고객 ${waiting}명"
		fi
		row "📞" "통화 현황"
		ch=$(asterisk_rx "core show channels count" | tr '\n' ' ')
		ok "$(sed -E 's/ *([0-9]+) active channels? +([0-9]+) active calls? +([0-9]+) calls? processed.*/통화 \2건 · 채널 \1개 · Asterisk 누적 \3건/' <<<"$ch")"
	fi

	printf '\n  %s📁 PID·로그: %s%s\n\n' "$D" "${RUN_DIR#"$ROOT"/}/" "$R"
}

if [ "$WATCH" = 1 ]; then
	trap 'printf "\n  👋 그만 볼게요\n\n"; exit 0' INT
	while true; do
		[ "$TTY" = 1 ] && printf '\033[H\033[2J'
		show
		sleep 2
	done
else
	show
fi
