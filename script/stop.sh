#!/usr/bin/env bash
# 🛑 실시간 현황판을 끈다: 화면(Next.js) → 가짜 상담원 정리 → 상태머신 엔진 → 룸 큐 엔진
#
#   script/stop.sh          화면과 두 엔진을 끄고, 가짜 상담원(Local/100x@sim-agent)을 큐에서 뺀다
#   script/stop.sh --all    Asterisk 컨테이너까지 내린다 (녹음·로그·CDR이 든 data/는 남는다)
#   script/stop.sh --keep-agents   가짜 상담원은 큐에 그대로 둔다
#
# 이 저장소에서 켠 것만 끈다 (다른 프로그램이 쓰는 3000번 포트의 Next.js 등은 건드리지 않는다).

set -euo pipefail
# shellcheck source=script/lib.sh
source "$(dirname "$0")/lib.sh"

ALL=0
AGENTS=1
for a in "$@"; do
	case "$a" in
	--all) ALL=1 ;;
	--keep-agents) AGENTS=0 ;;
	-h | --help) usage && exit 0 ;;
	*) die "모르는 옵션이에요: $a  (script/stop.sh --help)" ;;
	esac
done

banner "🛑" "IPCC Playground · 끄기" "$([ "$ALL" = 1 ] && echo "화면 → 엔진 → Asterisk" || echo "화면 → 엔진 (Asterisk는 그대로)")"

# stop_pids "메시지" PID… → TERM을 보내고 끝날 때까지 기다린다 (5초 넘으면 KILL)
stop_pids() {
	local msg=$1
	shift
	[ $# -eq 0 ] && return 0
	kill "$@" 2>/dev/null || true
	if ! spin_until 5 "$msg" pids_gone "$@"; then
		kill -9 "$@" 2>/dev/null || true
	fi
}
# alive PID… → 살아 있는 것만 한 줄로 (죽은 PID가 남은 .pid 파일이 있어도 멈추지 않게 if로 거른다)
alive() {
	local p
	for p in "$@"; do
		if pid_alive "$p"; then echo "$p"; fi
	done | sort -u | tr '\n' ' '
}
pids_gone() {
	local p
	for p in "$@"; do pid_alive "$p" && return 1; done
	return 0
}

# ── 1. 화면 ───────────────────────────────────────────────────────────────
row "🌐" "화면 (Next.js)"
# shellcheck disable=SC2046
pids=$(alive $(pid_of web || true) $(web_pids))
if [ -n "${pids// /}" ]; then
	# shellcheck disable=SC2086
	stop_pids "끄는 중" $pids
	ok "껐어요"
else
	ok "꺼져 있었어요"
fi
rm -f "$RUN_DIR/web.pid"

# ── 2. 가짜 상담원 ────────────────────────────────────────────────────────
row "👥" "가짜 상담원 정리"
if [ "$AGENTS" = 0 ]; then
	ok "그대로 둬요 (--keep-agents)"
elif asterisk_running; then
	n=$(asterisk_rx "queue show $QUEUE" | grep -c "@sim-agent" || true)
	if [ "${n:-0}" -gt 0 ]; then
		ensure_bins
		"$ROOT/bin/amicli" agent logout -sim >/dev/null 2>&1 || true
		ok "큐에서 ${n}명 뺐어요"
	else
		ok "큐에 없어요"
	fi
else
	ok "Asterisk가 꺼져 있어서 할 일이 없어요"
fi

# ── 3. 엔진 ───────────────────────────────────────────────────────────────
row "🧠" "상태머신 엔진"
# shellcheck disable=SC2046
pids=$(alive $(pid_of engine || true) $(engine_pids))
if [ -n "${pids// /}" ]; then
	# shellcheck disable=SC2086
	stop_pids "끄는 중" $pids
	ok "껐어요"
else
	ok "꺼져 있었어요"
fi
rm -f "$RUN_DIR/engine.pid"

# ── 4. 룸 큐 엔진 ─────────────────────────────────────────────────────────
# 룸 큐는 Asterisk 큐 멤버를 쓰지 않아서 정리할 것이 없다. 상담원 업무 상태는 data/rqengine/agents.json에 남는다
row "🏠" "룸 큐 엔진"
# shellcheck disable=SC2046
pids=$(alive $(pid_of rqengine || true) $(rq_pids))
if [ -n "${pids// /}" ]; then
	# shellcheck disable=SC2086
	stop_pids "끄는 중" $pids
	ok "껐어요"
else
	ok "꺼져 있었어요"
fi
rm -f "$RUN_DIR/rqengine.pid"

# ── 5. Asterisk (--all) ───────────────────────────────────────────────────
if [ "$ALL" = 1 ]; then
	row "📡" "Asterisk"
	if asterisk_running; then
		spin_run "컨테이너 내리는 중" "$RUN_DIR/asterisk-down.log" "${COMPOSE[@]}" down || die "내리지 못했어요 → $RUN_DIR/asterisk-down.log"
		ok "내렸어요 (${WAITED}초)"
	else
		ok "꺼져 있었어요"
	fi
fi

footer_start
printf '  😴  %s모두 껐어요.%s  다시 켜기 → %sscript/start.sh%s\n' "$B" "$R" "$CYN" "$R"
[ "$ALL" = 0 ] && printf '      %sAsterisk는 켜 둔 채예요. 내리려면 script/stop.sh --all%s\n' "$D" "$R"
footer_end
