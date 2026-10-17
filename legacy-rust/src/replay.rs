//! 녹화 재생: AMI 녹화(와 명령 파일)를 시각이 붙은 입력 목록으로 바꾸고, 엔진에 넣어 출력 줄을 모은다.

use std::collections::BTreeMap;

use crate::ami::{parse, parse_ts, Message};
use crate::engine::{Command, CommandKind, Engine, Input, Policy};

pub struct Timed {
    pub at: i64,
    pub input: Input,
}

/// 녹화들을 입력 목록으로 바꾼다.
/// 응답은 버리고, 우리가 보낸 목록형 액션의 결과(CoreShowChannels, QueueStatus)는 스냅샷 입력으로 묶는다.
/// Timestamp가 없는 메시지는 바로 앞 메시지의 시각을 쓴다.
pub fn inputs_from_recordings(texts: &[String]) -> Vec<Timed> {
    let mut out = Vec::new();
    let mut last = 0i64;
    for text in texts {
        let mut lists: BTreeMap<String, Vec<Message>> = BTreeMap::new();
        for m in parse(text) {
            if m.is_response() {
                if m.get("EventList").eq_ignore_ascii_case("start") {
                    lists.insert(m.get("ActionID").to_string(), Vec::new());
                }
                continue;
            }
            if !m.is_event() {
                continue;
            }
            let at = parse_ts(m.get("Timestamp")).unwrap_or(last);
            last = at;
            let aid = m.get("ActionID").to_string();
            if !aid.is_empty() && lists.contains_key(&aid) {
                if m.get("EventList").eq_ignore_ascii_case("Complete") {
                    let items = lists.remove(&aid).unwrap_or_default();
                    match m.event() {
                        "CoreShowChannelsComplete" => out.push(Timed { at, input: Input::ChannelSnapshot(items) }),
                        "QueueStatusComplete" => out.push(Timed { at, input: Input::QueueSnapshot(items) }),
                        _ => {}
                    }
                } else if let Some(items) = lists.get_mut(&aid) {
                    items.push(m);
                }
                continue;
            }
            out.push(Timed { at, input: Input::Event(m) });
        }
    }
    out
}

/// 명령 파일: 한 줄에 "<시각> <명령> <인터페이스> [인자…]". 빈 줄과 #으로 시작하는 줄은 건너뛴다.
/// 명령은 login(인자 = 이름), logout, ready, notready(인자 = 사유), wrapup.
pub fn parse_commands(text: &str) -> Result<Vec<Timed>, String> {
    let mut out = Vec::new();
    for (n, raw) in text.lines().enumerate() {
        let line = raw.trim();
        if line.is_empty() || line.starts_with('#') {
            continue;
        }
        let f: Vec<&str> = line.split_whitespace().collect();
        if f.len() < 3 {
            return Err(format!("{}번째 줄: <시각> <명령> <인터페이스>가 필요함", n + 1));
        }
        let at = parse_ts(f[0]).ok_or_else(|| format!("{}번째 줄: 시각이 아님: {}", n + 1, f[0]))?;
        let kind = match f[1] {
            "login" => CommandKind::Login,
            "logout" => CommandKind::Logout,
            "ready" => CommandKind::Ready,
            "notready" => CommandKind::NotReady,
            "wrapup" => CommandKind::WrapUp,
            other => return Err(format!("{}번째 줄: 모르는 명령: {}", n + 1, other)),
        };
        out.push(Timed {
            at,
            input: Input::Command(Command { kind, iface: f[2].to_string(), arg: f[3..].join(" ") }),
        });
    }
    Ok(out)
}

/// 명령을 이벤트 사이에 끼운다. 명령은 자기 시각보다 '뒤'인 첫 이벤트 바로 앞에 들어간다 (같은 시각이면 이벤트가 먼저).
pub fn merge(events: Vec<Timed>, commands: Vec<Timed>) -> Vec<Timed> {
    let mut out = Vec::with_capacity(events.len() + commands.len());
    let mut cmds = commands.into_iter().peekable();
    for e in events {
        while cmds.peek().map_or(false, |c| c.at < e.at) {
            out.push(cmds.next().unwrap());
        }
        out.push(e);
    }
    out.extend(cmds);
    out
}

/// 입력을 순서대로 넣고, 끝에서 남은 타이머를 흘린 뒤 스냅샷 한 줄을 붙인다.
pub fn run(inputs: &[Timed], policy: Policy) -> Vec<String> {
    let mut e = Engine::new(policy);
    let mut lines = Vec::new();
    for ti in inputs {
        lines.extend(e.apply(ti.at, &ti.input).iter().map(|o| o.line()));
    }
    lines.extend(e.flush().iter().map(|o| o.line()));
    lines.push(e.snapshot().line());
    lines
}
