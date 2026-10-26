# shellcheck shell=bash
# shellcheck disable=SC2034  # 여기서 정한 변수(색, 주소, WAITED)는 source한 스크립트가 쓴다
# script/lib.sh — script/*.sh가 같이 쓰는 도구. 직접 실행하지 않고 source한다.
#
# macOS 기본 bash(3.2)에서도 돌아가게 bash 4 문법(연관 배열, ${x,,}, mapfile 등)은 쓰지 않는다.
# 켠 프로세스의 PID와 로그는 저장소의 .run/에 둔다 (git 제외).

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
RUN_DIR="$ROOT/.run"
ENGINE_URL="http://127.0.0.1:8090"
RQ_URL="http://127.0.0.1:8091" # 룸 큐 엔진 (bin/rqengine -http, docs/12)
WEB_PORT="${WEB_PORT:-3000}" # 3000번을 다른 프로그램이 쓰면: WEB_PORT=3001 script/start.sh (status·stop에도 같이 준다)
WEB_URL="http://localhost:$WEB_PORT"
QUEUE="support"
COMPOSE=(docker compose -f "$ROOT/docker-compose.yml")
mkdir -p "$RUN_DIR"

# ── 색 (터미널일 때만. NO_COLOR가 있으면 끈다) ────────────────────────────
if [ -t 1 ] && [ -z "${NO_COLOR:-}" ]; then
	B=$'\033[1m' D=$'\033[2m' R=$'\033[0m'
	RED=$'\033[31m' GRN=$'\033[32m' YEL=$'\033[33m' BLU=$'\033[34m' MAG=$'\033[35m' CYN=$'\033[36m'
	TTY=1
else
	B='' D='' R='' RED='' GRN='' YEL='' BLU='' MAG='' CYN=''
	TTY=0
fi

LINE="━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"

# banner "🚀" "제목" "부제"
banner() {
	printf '\n%s%s%s\n' "$BLU" "$LINE" "$R"
	printf '  %s  %s%s%s\n' "$1" "$B" "$2" "$R"
	[ -n "${3:-}" ] && printf '      %s%s%s\n' "$D" "$3" "$R"
	printf '%s%s%s\n\n' "$BLU" "$LINE" "$R"
}

footer_start() { printf '\n%s%s%s\n' "$BLU" "$LINE" "$R"; }
footer_end() { printf '%s%s%s\n\n' "$BLU" "$LINE" "$R"; }

# 화면에서 차지하는 칸 수: 한글·한자·전각·이모지는 2칸, 나머지(가운뎃점 · 등)는 1칸
dwidth() {
	printf '%s' "$1" | perl -CS -e 'local $/; my $s = <STDIN> // ""; my $w = 0;
		for my $o (map { ord } split //, $s) {
			$w += (($o >= 0x1100 && $o <= 0x115F) || ($o >= 0x2E80 && $o <= 0xA4CF) || ($o >= 0xAC00 && $o <= 0xD7A3) ||
				($o >= 0xF900 && $o <= 0xFAFF) || ($o >= 0xFF00 && $o <= 0xFF60) || ($o >= 0x1F300 && $o <= 0x1FAFF)) ? 2 : 1;
		}
		print $w;'
}

# pad "글자" 칸수 → 오른쪽을 공백으로 채운다
pad() {
	local n=$(($2 - $(dwidth "$1")))
	[ "$n" -lt 0 ] && n=0
	printf '%s%*s' "$1" "$n" ''
}

# row "🐳" "Docker" → "  🐳  Docker ················ " (줄바꿈 없음. 뒤에 ok/warn/fail이 붙는다)
row() {
	local n=$((26 - $(dwidth "$2")))
	[ "$n" -lt 2 ] && n=2
	printf '  %s  %s %s%s%s ' "$1" "$2" "$D" "$(printf '·%.0s' $(seq 1 "$n"))" "$R"
}

ok() { printf '%s✔%s %s\n' "$GRN" "$R" "$*"; }
warn() { printf '%s▲%s %s\n' "$YEL" "$R" "$*"; }
fail() { printf '%s✖%s %s\n' "$RED" "$R" "$*"; }
note() { printf '      %s%s%s\n' "$D" "$*" "$R"; }
die() {
	printf '\n  %s💥 %s%s\n' "$RED" "$*" "$R" >&2
	exit 1
}

# 도움말: 스크립트 맨 위 주석(두 번째 줄부터 첫 빈 줄 전까지)을 그대로 보여 준다
usage() { sed -n '2,/^$/p' "$0" | sed 's/^# \{0,1\}//'; }

# ── 기다리기 ──────────────────────────────────────────────────────────────

SPIN=(⠋ ⠙ ⠹ ⠸ ⠼ ⠴ ⠦ ⠧ ⠇ ⠏)
WAITED=0 # 마지막 spin_* 가 기다린 초

# spin_until 제한초 "메시지" 명령… → 명령이 성공할 때까지 스피너를 돌린다. 시간이 넘으면 1
spin_until() {
	local limit=$1 msg=$2 i=0 start=$SECONDS
	shift 2
	[ "$TTY" = 1 ] && printf '\0337'
	until "$@" >/dev/null 2>&1; do
		if [ $((SECONDS - start)) -ge "$limit" ]; then
			[ "$TTY" = 1 ] && printf '\0338\033[K'
			WAITED=$((SECONDS - start))
			return 1
		fi
		[ "$TTY" = 1 ] && printf '\0338\033[K%s%s%s %s %s%d초%s' "$CYN" "${SPIN[$((i % 10))]}" "$R" "$msg" "$D" $((SECONDS - start)) "$R"
		i=$((i + 1))
		sleep 0.2
	done
	[ "$TTY" = 1 ] && printf '\0338\033[K'
	WAITED=$((SECONDS - start))
	return 0
}

# spin_run "메시지" 로그파일 명령… → 명령을 뒤에서 돌리며 스피너를 보여 주고, 명령의 종료 코드를 돌려준다
spin_run() {
	local msg=$1 log=$2 i=0 start=$SECONDS pid rc
	shift 2
	"$@" >"$log" 2>&1 &
	pid=$!
	[ "$TTY" = 1 ] && printf '\0337'
	while kill -0 "$pid" 2>/dev/null; do
		[ "$TTY" = 1 ] && printf '\0338\033[K%s%s%s %s %s%d초%s' "$CYN" "${SPIN[$((i % 10))]}" "$R" "$msg" "$D" $((SECONDS - start)) "$R"
		i=$((i + 1))
		sleep 0.2
	done
	[ "$TTY" = 1 ] && printf '\0338\033[K'
	rc=0
	wait "$pid" || rc=$?
	WAITED=$((SECONDS - start))
	return "$rc"
}

# ── 상태 확인 ─────────────────────────────────────────────────────────────

pid_alive() { [ -n "${1:-}" ] && kill -0 "$1" 2>/dev/null; }
pid_of() { [ -f "$RUN_DIR/$1.pid" ] && cat "$RUN_DIR/$1.pid"; }

docker_up() { docker info >/dev/null 2>&1; }
asterisk_running() { [ -n "$("${COMPOSE[@]}" ps --status running -q asterisk 2>/dev/null)" ]; }
asterisk_booted() { "${COMPOSE[@]}" exec -T asterisk asterisk -rx "core waitfullybooted" 2>/dev/null | grep -q "fully booted"; }
asterisk_rx() { "${COMPOSE[@]}" exec -T asterisk asterisk -rx "$1" 2>/dev/null; }

engine_api() { curl -sf --max-time 2 "$ENGINE_URL/api/state"; }
engine_up() { engine_api >/dev/null; }
engine_synced() { engine_api | grep -q '"synced":true'; }
# 저장소의 bin/engine 프로세스 (make engine으로 켠 것도 찾는다)
engine_pids() { pgrep -f "(^|/)bin/engine( |$)" 2>/dev/null || true; }

rq_api() { curl -sf --max-time 2 "$RQ_URL/api/state"; }
rq_up() { rq_api >/dev/null; }
rq_synced() { rq_api | grep -q '"synced":true'; }
rq_pids() { pgrep -f "(^|/)bin/rqengine( |$)" 2>/dev/null || true; }

web_up() { [ "$(curl -s --max-time 3 -o /dev/null -w '%{http_code}' "http://127.0.0.1:$WEB_PORT/" 2>/dev/null)" = 200 ]; }
# 3000번 포트에서 듣고 있는 프로세스 중 이 저장소의 web/에서 실행된 것만
web_pids() {
	local p cwd
	for p in $(lsof -ti "tcp:$WEB_PORT" -sTCP:LISTEN 2>/dev/null); do
		cwd=$(lsof -a -p "$p" -d cwd -Fn 2>/dev/null | sed -n 's/^n//p')
		case "$cwd" in "$ROOT/web"*) echo "$p" ;; esac
	done
}
port_busy() { [ -n "$(lsof -ti "tcp:$1" -sTCP:LISTEN 2>/dev/null)" ]; }

# 엔진 명령 보내기: send_cmd 종류 인터페이스 [인자]  (룸 큐 엔진은 send_rq)
send_cmd() {
	curl -sf --max-time 3 -X POST "$ENGINE_URL/api/commands" -H 'Content-Type: application/json' \
		-d "{\"kind\":\"$1\",\"iface\":\"$2\",\"arg\":\"${3:-}\"}" >/dev/null
}
send_rq() {
	curl -sf --max-time 3 -X POST "$RQ_URL/api/commands" -H 'Content-Type: application/json' \
		-d "{\"kind\":\"$1\",\"iface\":\"$2\",\"arg\":\"${3:-}\"}" >/dev/null
}

# 엔진 상태 JSON에서 요약 한 줄을 뽑는다 (node가 있을 때)
engine_summary() {
	command -v node >/dev/null || return 1
	# shellcheck disable=SC2016  # 작은따옴표 안은 node의 JavaScript다
	engine_api | node -e '
		let s = ""; process.stdin.on("data", d => (s += d)).on("end", () => {
			const v = JSON.parse(s), a = v.snapshot.agents.filter(x => x.state !== "LoggedOut");
			const n = (...st) => a.filter(x => st.includes(x.state)).length;
			const t = v.totals;
			console.log(`상담원 ${a.length}명 (대기 ${n("Ready")} · 벨 ${n("Ringing")} · 통화 ${n("Talking", "Hold")} · 후처리 ${n("ACW")} · 이석 ${n("NotReady")})` +
				` · 진행 중 콜 ${v.snapshot.calls.length}건 · 처리 ${t.handled}건 · 포기 ${t.abandoned}건`);
		});'
}

# 룸 큐 엔진 상태 JSON에서 요약 한 줄 (상담원은 두 축: 업무 모드 × 통화 단계)
rq_summary() {
	command -v node >/dev/null || return 1
	# shellcheck disable=SC2016  # 작은따옴표 안은 node의 JavaScript다
	rq_api | node -e '
		let s = ""; process.stdin.on("data", d => (s += d)).on("end", () => {
			const v = JSON.parse(s), a = v.agents.filter(x => x.mode !== "Logout");
			const n = (k, val) => a.filter(x => x[k] === val).length;
			const ready = a.filter(x => x.mode === "Inbound" && x.phase === "Idle").length;
			console.log(`상담원 ${a.length}명 (받을 수 있음 ${ready} · 벨 ${n("phase", "Ring")} · 통화 ${n("phase", "Talk")} · 후처리 ${n("mode", "ACW")} · 이석 ${n("mode", "NotReady")})` +
				` · 진행 중인 방 ${v.calls.length}개 · 처리 ${v.totals.handled}건 · 포기 ${v.totals.abandoned}건`);
		});'
}

# detach 로그파일 명령… → 터미널과 떨어진 새 세션에서 뒤로 돌리고 PID를 출력한다.
# nohup만으로는 부족하다: 터미널 창을 닫으면 Next.js 개발 서버가 같이 꺼졌다 (macOS에는 setsid 명령이 없어 perl을 쓴다)
detach() {
	local log=$1
	shift
	perl -MPOSIX -e 'POSIX::setsid() or die "setsid: $!\n"; exec @ARGV or die "exec: $!\n"' "$@" </dev/null >>"$log" 2>&1 &
	echo $!
}

ensure_bins() {
	if [ ! -x "$ROOT/bin/engine" ] || [ ! -x "$ROOT/bin/rqengine" ] || [ ! -x "$ROOT/bin/amicli" ]; then
		(cd "$ROOT" && make -s build >/dev/null)
	fi
}
