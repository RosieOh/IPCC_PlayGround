#!/usr/bin/env bash
# 📜 로그를 따라가며 본다 (Ctrl+C로 끝)
#
#   script/logs.sh            엔진 (상태머신 출력 줄 JSONL + 로그)
#   script/logs.sh web        화면 (Next.js 개발 서버)
#   script/logs.sh asterisk   Asterisk 콘솔
#   script/logs.sh -n 100 …   처음에 보여 줄 줄 수 (기본 40)

set -euo pipefail
# shellcheck source=script/lib.sh
source "$(dirname "$0")/lib.sh"

N=40
WHAT=engine
while [ $# -gt 0 ]; do
	case "$1" in
	-n) N=${2:?-n 다음에 줄 수}; shift ;;
	engine | web | asterisk) WHAT=$1 ;;
	-h | --help) usage && exit 0 ;;
	*) die "모르는 대상이에요: $1  (engine | web | asterisk)" ;;
	esac
	shift
done

case "$WHAT" in
engine) icon="🧠" title="상태머신 엔진" file="$RUN_DIR/engine.log" ;;
web) icon="🌐" title="화면 (Next.js)" file="$RUN_DIR/web.log" ;;
asterisk) icon="📡" title="Asterisk 콘솔" file="" ;;
esac

banner "$icon" "로그 · $title" "$([ -n "$file" ] && echo "${file#"$ROOT"/}" || echo "docker compose logs asterisk")  ·  Ctrl+C로 끝"
trap 'printf "\n  👋 그만 볼게요\n\n"; exit 0' INT

if [ "$WHAT" = asterisk ]; then
	asterisk_running || die "Asterisk가 꺼져 있어요 → script/start.sh"
	"${COMPOSE[@]}" logs -f --tail="$N" asterisk
	exit 0
fi

[ -f "$file" ] || die "아직 로그가 없어요. script/start.sh로 켠 뒤에 생겨요 ($file)"

# 엔진 로그는 종류별로 색을 입힌다: 상담원 파랑 · 콜 회색 · 결과 초록 · 액션 보라 · 경고 노랑
if [ "$WHAT" = engine ]; then
	tail -n "$N" -F "$file" | while IFS= read -r line; do
		case "$line" in
		*'"kind":"agent"'*) printf '%s👤 %s%s\n' "$BLU" "$line" "$R" ;;
		*'"kind":"call_end"'*) printf '%s🏁 %s%s\n' "$GRN" "$line" "$R" ;;
		*'"kind":"call"'*) printf '%s📞 %s%s\n' "$D" "$line" "$R" ;;
		*'"kind":"action"'*) printf '%s⚡ %s%s\n' "$MAG" "$line" "$R" ;;
		*'"kind":"warn"'*) printf '%s⚠️  %s%s\n' "$YEL" "$line" "$R" ;;
		*level=ERROR*) printf '%s💥 %s%s\n' "$RED" "$line" "$R" ;;
		*level=WARN*) printf '%s⚠️  %s%s\n' "$YEL" "$line" "$R" ;;
		*) printf '   %s\n' "$line" ;;
		esac
	done
else
	tail -n "$N" -F "$file"
fi
