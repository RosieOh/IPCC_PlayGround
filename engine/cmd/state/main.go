// state replay [--cmds 명령파일] <녹화파일…>
//
// 녹화를 상태머신에 넣고 출력 줄(JSONL)을 표준출력에 쓴다.
// Rust 레거시의 `ipcc-state replay`와 바이트 단위로 같은 출력이 나와야 한다 (make diff-test).
package main

import (
	"bufio"
	"fmt"
	"os"
	"unicode/utf8"

	"ipcc/engine/ami"
	"ipcc/engine/state"
)

const usage = "사용법: state replay [--cmds 명령파일] <녹화파일…>"

func main() { os.Exit(run(os.Args[1:])) }

func run(args []string) int {
	if len(args) == 0 || args[0] != "replay" {
		fmt.Fprintln(os.Stderr, usage)
		return 2
	}
	var files []string
	cmds := ""
	for i := 1; i < len(args); i++ {
		if args[i] == "--cmds" {
			if i+1 < len(args) {
				cmds = args[i+1]
			}
			i++
			continue
		}
		files = append(files, args[i])
	}
	if len(files) == 0 {
		fmt.Fprintln(os.Stderr, usage)
		return 2
	}

	var recs [][]ami.Message
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", f, err)
			return 1
		}
		msgs, err := state.ParseRecording(data)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", f, err)
			return 1
		}
		recs = append(recs, msgs)
	}
	inputs := state.InputsFromRecordings(recs)
	if cmds != "" {
		c, err := readCommands(cmds)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", cmds, err)
			return 1
		}
		inputs = state.Merge(inputs, c)
	}

	w := bufio.NewWriter(os.Stdout)
	for _, line := range state.Run(inputs, state.DefaultPolicy()) {
		if _, err := fmt.Fprintln(w, line); err != nil {
			return 1
		}
	}
	if err := w.Flush(); err != nil {
		return 1
	}
	return 0
}

func readCommands(path string) ([]state.Timed, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if !utf8.Valid(data) {
		return nil, state.ErrNotUTF8
	}
	return state.ParseCommands(string(data))
}
