//! 출력 한 줄(JSON 객체)을 만든다. Go와 바이트 단위로 같아야 하므로 직렬화 라이브러리 없이 직접 쓴다.
//! 규칙: 키 순서는 넣은 순서 그대로, 문자열은 " \ 제어문자만 이스케이프하고 한글 등은 그대로 쓴다.

#[derive(Debug, Clone, PartialEq)]
pub enum Val {
    Str(String),
    Int(i64),
    Bool(bool),
    /// 이미 만든 JSON 조각 (스냅샷의 배열 등)
    Raw(String),
}

#[derive(Debug, Clone, PartialEq, Default)]
pub struct Out {
    pub fields: Vec<(&'static str, Val)>,
}

impl Out {
    pub fn new() -> Self {
        Out::default()
    }

    pub fn s(mut self, k: &'static str, v: &str) -> Self {
        self.fields.push((k, Val::Str(v.to_string())));
        self
    }

    pub fn i(mut self, k: &'static str, v: i64) -> Self {
        self.fields.push((k, Val::Int(v)));
        self
    }

    pub fn b(mut self, k: &'static str, v: bool) -> Self {
        self.fields.push((k, Val::Bool(v)));
        self
    }

    pub fn raw(mut self, k: &'static str, v: String) -> Self {
        self.fields.push((k, Val::Raw(v)));
        self
    }

    /// 필드 값 (테스트용). 문자열 필드만 본다.
    pub fn get(&self, k: &str) -> &str {
        for (key, v) in &self.fields {
            if *key == k {
                if let Val::Str(s) = v {
                    return s;
                }
            }
        }
        ""
    }

    pub fn line(&self) -> String {
        let mut o = String::from("{");
        for (i, (k, v)) in self.fields.iter().enumerate() {
            if i > 0 {
                o.push(',');
            }
            o.push_str(&quote(k));
            o.push(':');
            match v {
                Val::Str(s) => o.push_str(&quote(s)),
                Val::Int(n) => o.push_str(&n.to_string()),
                Val::Bool(b) => o.push_str(if *b { "true" } else { "false" }),
                Val::Raw(r) => o.push_str(r),
            }
        }
        o.push('}');
        o
    }
}

pub fn quote(s: &str) -> String {
    let mut o = String::with_capacity(s.len() + 2);
    o.push('"');
    for c in s.chars() {
        match c {
            '"' => o.push_str("\\\""),
            '\\' => o.push_str("\\\\"),
            '\n' => o.push_str("\\n"),
            '\r' => o.push_str("\\r"),
            '\t' => o.push_str("\\t"),
            c if (c as u32) < 0x20 => o.push_str(&format!("\\u{:04x}", c as u32)),
            c => o.push(c),
        }
    }
    o.push('"');
    o
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn renders_in_insertion_order_with_minimal_escaping() {
        let o = Out::new().s("name", "상담원 \"1\"\n").i("n", -3).b("ok", true).raw("a", "[]".into());
        assert_eq!(o.line(), r#"{"name":"상담원 \"1\"\n","n":-3,"ok":true,"a":[]}"#);
        assert_eq!(quote("\u{1}"), "\"\\u0001\"");
    }
}
