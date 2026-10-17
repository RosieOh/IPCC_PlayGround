//! AMI 녹화 파싱. Go의 `ami.Reader`와 같은 규칙으로 메시지를 자른다.

/// AMI 메시지 하나. 같은 키가 여러 번 나올 수 있어 순서 있는 목록으로 담는다.
#[derive(Debug, Clone, Default, PartialEq)]
pub struct Message {
    pub fields: Vec<(String, String)>,
}

impl Message {
    /// 키의 첫 값. AMI 키는 대소문자를 구분하지 않는다. 없으면 "".
    pub fn get(&self, key: &str) -> &str {
        self.fields
            .iter()
            .find(|(k, _)| k.eq_ignore_ascii_case(key))
            .map(|(_, v)| v.as_str())
            .unwrap_or("")
    }

    pub fn has(&self, key: &str) -> bool {
        self.fields.iter().any(|(k, _)| k.eq_ignore_ascii_case(key))
    }

    pub fn event(&self) -> &str {
        self.get("Event")
    }

    pub fn is_event(&self) -> bool {
        self.has("Event")
    }

    /// Response 필드가 있어도 Event가 있으면 이벤트다 (OriginateResponse).
    pub fn is_response(&self) -> bool {
        self.has("Response") && !self.is_event()
    }
}

/// 녹화(받은 바이트 그대로)를 메시지 목록으로 자른다.
/// 첫 줄의 배너는 건너뛰고, 빈 줄 없이 끝난 마지막 메시지(잘린 녹화)는 버린다.
///
/// 줄은 '\n'으로 끝나야 줄이다. `split('\n')`을 쓰면 "…Z: cut\r\n"처럼 줄바꿈 하나로 끝난 녹화에서
/// 맨 뒤에 빈 조각("")이 하나 더 나오고, 그걸 메시지 끝(빈 줄)으로 잘못 봐서 잘린 메시지를 살린다.
/// Go 포팅과의 차분 테스트에서 드러난 버그다 (testdata/truncated.ami, docs/07 10절).
pub fn parse(text: &str) -> Vec<Message> {
    let mut out = Vec::new();
    let mut cur = Message::default();
    for (i, piece) in text.split_inclusive('\n').enumerate() {
        let Some(raw) = piece.strip_suffix('\n') else { break }; // 줄바꿈 없이 끝난 줄 = 잘린 녹화
        let line = raw.strip_suffix('\r').unwrap_or(raw);
        if i == 0 && line.starts_with("Asterisk Call Manager/") {
            continue;
        }
        if line.is_empty() {
            if !cur.fields.is_empty() {
                out.push(std::mem::take(&mut cur));
            }
            continue;
        }
        match line.split_once(':') {
            Some((k, v)) => cur
                .fields
                .push((k.to_string(), v.strip_prefix(' ').unwrap_or(v).to_string())),
            None => cur.fields.push((String::new(), line.to_string())),
        }
    }
    out
}

/// "1792123142.227526"(유닉스 초.마이크로초) → 마이크로초. 소수는 6자리로 맞추고, 숫자가 아니면 None.
pub fn parse_ts(s: &str) -> Option<i64> {
    let (sec, frac) = s.split_once('.').unwrap_or((s, ""));
    if sec.is_empty() || !sec.bytes().all(|b| b.is_ascii_digit()) || !frac.bytes().all(|b| b.is_ascii_digit()) {
        return None;
    }
    let mut f: String = frac.chars().take(6).collect();
    while f.len() < 6 {
        f.push('0');
    }
    Some(sec.parse::<i64>().ok()? * 1_000_000 + f.parse::<i64>().ok()?)
}

/// 마이크로초 → "초.마이크로초(6자리)". 출력의 시각 형식이다.
pub fn fmt_ts(us: i64) -> String {
    format!("{}.{:06}", us / 1_000_000, us % 1_000_000)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn parses_banner_crlf_lf_and_drops_truncated_tail() {
        let text = "Asterisk Call Manager/11.0.0\r\nEvent: A\r\nX: 1\r\n\r\n\nEvent: B\nY:  two\n\nEvent: C\r\nZ: cut";
        let msgs = parse(text);
        assert_eq!(msgs.len(), 2);
        assert_eq!(msgs[0].event(), "A");
        assert_eq!(msgs[1].get("y"), " two"); // 콜론 뒤 공백은 한 칸만 뗀다
    }

    #[test]
    fn message_cut_right_after_a_line_break_is_still_truncated() {
        // 줄바꿈 하나로 끝났지만 빈 줄은 없다 → 메시지가 끝나지 않았다
        let msgs = parse("Event: A\r\n\r\nEvent: B\r\nInterface: x\r\n");
        assert_eq!(msgs.len(), 1);
        assert_eq!(msgs[0].event(), "A");
    }

    #[test]
    fn originate_response_is_an_event() {
        let m = Message { fields: vec![("Event".into(), "OriginateResponse".into()), ("Response".into(), "Success".into())] };
        assert!(m.is_event() && !m.is_response());
    }

    #[test]
    fn timestamps() {
        assert_eq!(parse_ts("1792123142.227526"), Some(1_792_123_142_227_526));
        assert_eq!(parse_ts("10.5"), Some(10_500_000));
        assert_eq!(parse_ts("7"), Some(7_000_000));
        assert_eq!(parse_ts("x.1"), None);
        assert_eq!(fmt_ts(1_792_123_142_000_045), "1792123142.000045");
    }
}
