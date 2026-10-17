//! ipcc-state replay [--cmds 명령파일] <녹화파일…>
//!
//! 녹화를 상태머신에 넣고 출력 줄(JSONL)을 표준출력에 쓴다. Go의 `bin/state replay`와 같은 출력이 나와야 한다.

use std::io::{BufWriter, Write};
use std::process::ExitCode;

use ipcc_state::engine::Policy;
use ipcc_state::replay::{inputs_from_recordings, merge, parse_commands, run};

const USAGE: &str = "사용법: ipcc-state replay [--cmds 명령파일] <녹화파일…>";

fn main() -> ExitCode {
    let args: Vec<String> = std::env::args().skip(1).collect();
    if args.first().map(String::as_str) != Some("replay") {
        eprintln!("{USAGE}");
        return ExitCode::from(2);
    }
    let mut files = Vec::new();
    let mut cmds = None;
    let mut it = args[1..].iter();
    while let Some(a) = it.next() {
        if a == "--cmds" {
            cmds = it.next().cloned();
        } else {
            files.push(a.clone());
        }
    }
    if files.is_empty() {
        eprintln!("{USAGE}");
        return ExitCode::from(2);
    }

    let mut texts = Vec::new();
    for f in &files {
        match std::fs::read_to_string(f) {
            Ok(t) => texts.push(t),
            Err(e) => {
                eprintln!("{f}: {e}");
                return ExitCode::from(1);
            }
        }
    }
    let mut inputs = inputs_from_recordings(&texts);
    if let Some(path) = cmds {
        let parsed = std::fs::read_to_string(&path).map_err(|e| e.to_string()).and_then(|t| parse_commands(&t));
        match parsed {
            Ok(c) => inputs = merge(inputs, c),
            Err(e) => {
                eprintln!("{path}: {e}");
                return ExitCode::from(1);
            }
        }
    }

    let stdout = std::io::stdout();
    let mut w = BufWriter::new(stdout.lock());
    for line in run(&inputs, Policy::default()) {
        if writeln!(w, "{line}").is_err() {
            return ExitCode::from(1);
        }
    }
    ExitCode::SUCCESS
}
