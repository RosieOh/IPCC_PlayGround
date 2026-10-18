#!/usr/bin/env bash
# 🚀 실시간 현황판을 한 번에 켠다: Docker → Asterisk → 상태머신 엔진 → Next.js 화면 → 브라우저
#
#   script/start.sh             켜기 (이미 켜진 것은 건너뛴다)
#   script/start.sh --demo      켜고 나서 가짜 상담원 3명 로그인 + 가짜 콜 4통
#   script/start.sh --no-open   브라우저를 열지 않는다
#
# 엔진과 화면은 뒤에서 계속 돈다. 끄기: script/stop.sh   상태: script/status.sh   로그: script/logs.sh

set -euo pipefail
# shellcheck source=script/lib.sh
source "$(dirname "$0")/lib.sh"

DEMO=0
OPEN=1
for a in "$@"; do
	case "$a" in
	--demo) DEMO=1 ;;
	--no-open) OPEN=0 ;;
	-h | --help) usage && exit 0 ;;
	*) die "모르는 옵션이에요: $a  (script/start.sh --help)" ;;
	esac
done

banner "🚀" "IPCC Playground · 실시간 현황판 켜기" "Asterisk → 상태머신 엔진(Go) → Next.js 화면"

# ── 1. Docker ─────────────────────────────────────────────────────────────
row "🐳" "Docker"
if docker_up; then
	ok "실행 중"
else
	if command -v open >/dev/null; then open -a Docker 2>/dev/null || true; fi
	spin_until 120 "Docker Desktop 켜는 중" docker_up || die "Docker가 켜지지 않았어요. Docker Desktop을 직접 실행해 주세요."
	ok "켰어요 (${WAITED}초)"
fi

# ── 2. Asterisk ───────────────────────────────────────────────────────────
row "📡" "Asterisk"
if asterisk_running; then
	ok "이미 실행 중"
else
	# 처음이면 이미지를 소스로 빌드하느라 몇 분 걸린다
	spin_run "컨테이너 올리는 중 (처음이면 빌드로 몇 분)" "$RUN_DIR/asterisk-up.log" "${COMPOSE[@]}" up -d --build ||
		die "Asterisk를 올리지 못했어요 → $RUN_DIR/asterisk-up.log"
	ok "올렸어요 (${WAITED}초)"
fi
row "⏳" "Asterisk 부팅"
spin_until 120 "모듈 올리는 중" asterisk_booted || die "Asterisk가 부팅을 끝내지 못했어요 → make logs"
ok "Fully Booted"

# ── 3. 상태머신 엔진 ──────────────────────────────────────────────────────
row "🧠" "상태머신 엔진"
if engine_up; then
	ok "이미 실행 중 ($ENGINE_URL)"
else
	spin_run "Go 빌드 중" "$RUN_DIR/build.log" make -s -C "$ROOT" build || die "빌드 실패 → $RUN_DIR/build.log"
	port_busy 8090 && die "8090번 포트를 다른 프로그램이 쓰고 있어요 (lsof -i tcp:8090)"
	detach "$RUN_DIR/engine.log" "$ROOT/bin/engine" >"$RUN_DIR/engine.pid"
	spin_until 30 "AMI 붙고 재동기화하는 중" engine_synced || die "엔진이 준비되지 않았어요 → script/logs.sh engine"
	ok "켰어요 · AMI 연결·재동기화 끝 (PID $(pid_of engine))"
fi

# ── 4. 화면 (Next.js) ─────────────────────────────────────────────────────
row "🌐" "화면 (Next.js)"
if [ -n "$(web_pids)" ] && web_up; then
	ok "이미 실행 중 ($WEB_URL)"
else
	port_busy "$WEB_PORT" && die "${WEB_PORT}번 포트를 다른 프로그램이 쓰고 있어요 (lsof -i tcp:$WEB_PORT)"
	command -v node >/dev/null || die "Node.js가 없어요. 20.9 이상을 설치해 주세요 (brew install node)"
	if [ ! -d "$ROOT/web/node_modules" ]; then
		spin_run "처음이라 npm ci로 의존성 설치 중" "$RUN_DIR/npm-ci.log" npm --prefix "$ROOT/web" ci --no-audit --no-fund ||
			die "npm ci 실패 → $RUN_DIR/npm-ci.log"
	fi
	(cd "$ROOT/web" && detach "$RUN_DIR/web.log" env NEXT_TELEMETRY_DISABLED=1 \
		./node_modules/.bin/next dev --hostname 127.0.0.1 --port "$WEB_PORT") >"$RUN_DIR/web.pid"
	spin_until 90 "개발 서버 띄우고 첫 화면 컴파일 중" web_up || die "화면 서버가 뜨지 않았어요 → script/logs.sh web"
	ok "켰어요 ($WEB_URL)"
fi

# ── 5. 데모 (선택) ────────────────────────────────────────────────────────
if [ "$DEMO" = 1 ]; then
	echo
	"$ROOT/script/demo.sh" -n 4 --no-banner
fi

# ── 6. 브라우저 ───────────────────────────────────────────────────────────
if [ "$OPEN" = 1 ] && command -v open >/dev/null; then
	open "$WEB_URL" || true
fi

footer_start
printf '  🎉  %s준비됐어요!%s  →  %s%s%s\n\n' "$B" "$R" "$CYN" "$WEB_URL" "$R"
printf '  📞  %s %s\n' "$(pad "가짜 콜 걸기" 14)" "${D}script/demo.sh -n 5   (계속 걸기: --loop)${R}"
printf '  📊  %s %s\n' "$(pad "상태 보기" 14)" "${D}script/status.sh${R}"
printf '  📜  %s %s\n' "$(pad "로그 보기" 14)" "${D}script/logs.sh engine | web | asterisk${R}"
printf '  🛑  %s %s\n' "$(pad "끄기" 14)" "${D}script/stop.sh   (Asterisk까지: --all)${R}"
footer_end
