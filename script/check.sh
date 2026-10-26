#!/usr/bin/env bash
# ✅ CI와 같은 검사를 한 번에 돌리고 결과를 표로 보여 준다
#
#   script/check.sh            단위 테스트 · lint · Rust · 차분 테스트 · 화면 · e2e(재시작 포함)
#   script/check.sh --quick    e2e를 뺀다 (Asterisk 없이 1~2분)
#   script/check.sh --no-restart   e2e에서 Asterisk 재시작 단계를 뺀다
#
# 검사마다 로그는 .run/check-<이름>.log 에 남는다. 하나가 실패해도 끝까지 돌고, 실패가 있으면 종료 코드 1.
# 엔진(룸 큐 엔진 포함)·화면이 떠 있으면 e2e·화면 검사는 건너뛴다 (먼저 script/stop.sh).

set -uo pipefail
# shellcheck source=script/lib.sh
source "$(dirname "$0")/lib.sh"

QUICK=0
RESTART=1
for a in "$@"; do
	case "$a" in
	--quick) QUICK=1 ;;
	--no-restart) RESTART=0 ;;
	-h | --help) usage && exit 0 ;;
	*) die "모르는 옵션이에요: $a  (script/check.sh --help)" ;;
	esac
done

banner "✅" "IPCC Playground · 검사" "$([ "$QUICK" = 1 ] && echo "e2e 빼고 (--quick)" || echo "CI와 같은 전부")"

results=""
failed=0
start_all=$SECONDS

# check 이모지 "이름" 로그이름 명령…
check() {
	local icon=$1 name=$2 log="$RUN_DIR/check-$3.log"
	shift 3
	row "$icon" "$name"
	if spin_run "도는 중" "$log" "$@"; then
		ok "통과 ${D}(${WAITED}초)${R}"
		results="$results|$icon|$name|✔ 통과|${WAITED}초"
	else
		fail "실패 ${D}(${WAITED}초) → ${log#"$ROOT"/}${R}"
		tail -n 12 "$log" | sed "s/^/      ${D}│${R} /"
		results="$results|$icon|$name|✖ 실패|${WAITED}초"
		failed=$((failed + 1))
	fi
}

cd "$ROOT" || exit 1
check "🧪" "Go 단위 테스트" test make test
check "🎨" "Go lint" lint make lint
check "🦀" "Rust 레거시 테스트" rust make rust-test
check "🔁" "차분 테스트 (Rust = Go)" diff make diff-test
if [ -n "$(web_pids)" ]; then
	# web-check의 npm ci는 node_modules를 지우고 다시 깐다 → 떠 있는 개발 서버가 깨진다
	row "🌐" "화면 (lint·타입·빌드)"
	warn "건너뜀: 화면 서버가 떠 있어요 (npm ci가 node_modules를 지워요) → script/stop.sh 후 다시"
	results="$results|🌐|화면 (lint·타입·빌드)|▲ 건너뜀|-"
	failed=$((failed + 1))
else
	check "🌐" "화면 (lint·타입·빌드)" web make web-check
fi

if [ "$QUICK" = 0 ]; then
	if [ -n "$(engine_pids)$(rq_pids)" ]; then
		row "📡" "e2e (실제 Asterisk)"
		warn "건너뜀: 엔진이 떠 있어요. e2e의 엔진과 같은 상담원·콜을 움직여요 → script/stop.sh 후 다시"
		results="$results|📡|e2e (실제 Asterisk)|▲ 건너뜀|-"
		failed=$((failed + 1))
	else
		if [ "$RESTART" = 1 ]; then
			check "📡" "e2e (재시작 포함)" e2e env E2E_RESTART=1 make e2e
		else
			check "📡" "e2e (실제 Asterisk)" e2e make e2e
		fi
	fi
fi

# ── 결과 표 ───────────────────────────────────────────────────────────────
footer_start
printf '  %s결과%s %s(모두 %d초)%s\n\n' "$B" "$R" "$D" $((SECONDS - start_all)) "$R"
IFS='|' read -r -a cells <<<"${results#|}"
i=0
while [ $i -lt ${#cells[@]} ]; do
	status=${cells[$((i + 2))]}
	case "$status" in ✔*) color=$GRN ;; ✖*) color=$RED ;; *) color=$YEL ;; esac
	printf '  %s  %s %s%s%s  %s%s%s\n' "${cells[$i]}" "$(pad "${cells[$((i + 1))]}" 26)" "$color" "$(pad "$status" 10)" "$R" "$D" "${cells[$((i + 3))]}" "$R"
	i=$((i + 4))
done
echo
if [ "$failed" = 0 ]; then
	printf '  🎉  %s모두 통과했어요!%s PR을 올려도 돼요.\n' "$B" "$R"
else
	printf '  😵  %s%d개가 통과하지 못했어요.%s 위의 로그를 확인해 주세요.\n' "$RED" "$failed" "$R"
fi
footer_end
exit $((failed > 0))
