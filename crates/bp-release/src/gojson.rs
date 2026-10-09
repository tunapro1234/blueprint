//! Minimal Go `encoding/json` compatibility helpers used by buildinfo/release.
//!
//! TODO(dedupe): replace with `bp-core::gojson` once that module lands.

use serde_json::{Map, Value};
use std::fmt::Write as _;
use std::time::Duration;

/// Appends `s` as a JSON string literal exactly like Go's `json.Marshal`
/// (HTML-safe escaping of `<`, `>`, `&`, plus U+2028/U+2029).
pub fn write_string(out: &mut String, s: &str) {
    out.push('"');
    for c in s.chars() {
        match c {
            '"' => out.push_str("\\\""),
            '\\' => out.push_str("\\\\"),
            '\n' => out.push_str("\\n"),
            '\r' => out.push_str("\\r"),
            '\t' => out.push_str("\\t"),
            '\u{8}' => out.push_str("\\b"),
            '\u{c}' => out.push_str("\\f"),
            '<' | '>' | '&' | '\u{2028}' | '\u{2029}' => {
                let _ = write!(out, "\\u{:04x}", c as u32);
            }
            c if (c as u32) < 0x20 => {
                let _ = write!(out, "\\u{:04x}", c as u32);
            }
            c => out.push(c),
        }
    }
    out.push('"');
}

/// Parses a JSON document leniently like Go: invalid UTF-8 becomes U+FFFD.
pub(crate) fn parse(data: &[u8]) -> Option<Value> {
    let text = String::from_utf8_lossy(data);
    serde_json::from_str(&text).ok()
}

/// Looks up a struct field the way Go's decoder does: an exact key match wins,
/// otherwise a case-insensitive match. (Go assigns duplicates in document
/// order; serde_json's sorted map cannot, so an exact key always wins here.)
pub(crate) fn field<'a>(map: &'a Map<String, Value>, name: &str) -> Option<&'a Value> {
    if let Some(v) = map.get(name) {
        return Some(v);
    }
    map.iter()
        .filter(|(k, _)| k.to_lowercase() == name.to_lowercase())
        .map(|(_, v)| v)
        .next_back()
}

/// Result of decoding one field into a Go-typed destination: `Ok(None)` for a
/// missing key or JSON null (Go leaves the zero value), `Err` for a type mismatch.
pub(crate) type Decoded<T> = Result<Option<T>, ()>;

pub(crate) fn string_field(map: &Map<String, Value>, name: &str) -> Decoded<String> {
    match field(map, name) {
        None | Some(Value::Null) => Ok(None),
        Some(Value::String(s)) => Ok(Some(s.clone())),
        Some(_) => Err(()),
    }
}

pub(crate) fn bool_field(map: &Map<String, Value>, name: &str) -> Decoded<bool> {
    match field(map, name) {
        None | Some(Value::Null) => Ok(None),
        Some(Value::Bool(b)) => Ok(Some(*b)),
        Some(_) => Err(()),
    }
}

pub(crate) fn i64_field(map: &Map<String, Value>, name: &str) -> Decoded<i64> {
    match field(map, name) {
        None | Some(Value::Null) => Ok(None),
        Some(Value::Number(n)) => n.as_i64().map(Some).ok_or(()),
        Some(_) => Err(()),
    }
}

pub(crate) fn u64_field(map: &Map<String, Value>, name: &str) -> Decoded<u64> {
    match field(map, name) {
        None | Some(Value::Null) => Ok(None),
        Some(Value::Number(n)) => n.as_u64().map(Some).ok_or(()),
        Some(_) => Err(()),
    }
}

/// Formats a duration like Go's `time.Duration.String`.
pub fn duration_string(d: Duration) -> String {
    let nanos = d.as_nanos();
    if nanos == 0 {
        return "0s".to_string();
    }
    if nanos < 1_000_000_000 {
        let (unit, scale) = if nanos < 1_000 {
            ("ns", 1u128)
        } else if nanos < 1_000_000 {
            ("µs", 1_000)
        } else {
            ("ms", 1_000_000)
        };
        return format!("{}{unit}", fraction(nanos, scale));
    }
    let total_secs = nanos / 1_000_000_000;
    let frac = nanos % 1_000_000_000;
    let hours = total_secs / 3600;
    let minutes = (total_secs / 60) % 60;
    let secs = total_secs % 60;
    let mut out = String::new();
    if hours > 0 {
        let _ = write!(out, "{hours}h");
    }
    if hours > 0 || minutes > 0 {
        let _ = write!(out, "{minutes}m");
    }
    let _ = write!(
        out,
        "{}s",
        fraction(secs * 1_000_000_000 + frac, 1_000_000_000)
    );
    out
}

fn fraction(value: u128, scale: u128) -> String {
    let whole = value / scale;
    let rest = value % scale;
    if rest == 0 {
        return whole.to_string();
    }
    let width = scale.ilog10() as usize;
    let digits = format!("{rest:0width$}");
    format!("{whole}.{}", digits.trim_end_matches('0'))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn duration_matches_go() {
        for (d, want) in [
            (Duration::ZERO, "0s"),
            (Duration::from_nanos(1), "1ns"),
            (Duration::from_micros(1), "1µs"),
            (Duration::from_millis(15), "15ms"),
            (Duration::from_millis(500), "500ms"),
            (Duration::from_micros(1500), "1.5ms"),
            (Duration::from_secs(20), "20s"),
            (Duration::from_millis(1500), "1.5s"),
            (Duration::from_secs(90), "1m30s"),
            (Duration::from_secs(3600), "1h0m0s"),
            (Duration::from_secs(3661), "1h1m1s"),
        ] {
            assert_eq!(duration_string(d), want);
        }
    }

    #[test]
    fn string_escaping_matches_go() {
        let mut out = String::new();
        write_string(&mut out, "a<b>&\"\\\n\u{1}\u{8}\u{2028}é");
        assert_eq!(out, r#""a\u003cb\u003e\u0026\"\\\n\u0001\b\u2028é""#);
    }

    #[test]
    fn field_lookup_is_case_insensitive_with_exact_preference() {
        let Value::Object(map) = serde_json::json!({"PID": 1, "pid": 2}) else {
            unreachable!()
        };
        assert_eq!(i64_field(&map, "pid"), Ok(Some(2)));
        let Value::Object(map) = serde_json::json!({"Pid": 3}) else {
            unreachable!()
        };
        assert_eq!(i64_field(&map, "pid"), Ok(Some(3)));
        let Value::Object(map) = serde_json::json!({"pid": 1.0}) else {
            unreachable!()
        };
        assert_eq!(i64_field(&map, "pid"), Err(()));
    }
}
