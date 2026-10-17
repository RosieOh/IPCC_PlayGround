#!/bin/sh
# 상태머신 구현 하나(Rust 레거시 또는 Go)로 모든 녹화를 재생해 출력 디렉터리에 쓴다.
# make golden / make diff-test가 두 구현에 똑같이 쓴다.
#
# 사용법: replay-all.sh <state 실행 파일> <출력 디렉터리> <녹화 디렉터리…>
#   <이름>.ami, <이름>.log → <출력>/<이름>.golden.jsonl
#   <이름>.cmds            → <출력>/<이름>.cmds.golden.jsonl  (같은 디렉터리의 <이름>.ami에 상담원 명령을 끼워 재생)
set -eu
bin=$1 out=$2
shift 2

for dir in "$@"; do
	for f in "$dir"/*.ami "$dir"/*.log; do
		[ -e "$f" ] || continue
		name=$(basename "$f")
		"$bin" replay "$f" >"$out/${name%.*}.golden.jsonl"
	done
	for c in "$dir"/*.cmds; do
		[ -e "$c" ] || continue
		name=$(basename "$c" .cmds)
		"$bin" replay --cmds "$c" "$dir/$name.ami" >"$out/$name.cmds.golden.jsonl"
	done
done
