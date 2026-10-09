//! Port of the byte-level pieces of Go `encoding/json` (encode.go `appendString`,
//! `floatEncoder`, indent.go `appendCompact`/`appendIndent`).

use super::Error;

const HEX: &[u8; 16] = b"0123456789abcdef";

/// Appends `s` as a JSON string literal exactly like Go's `appendString`.
///
/// `escape_html` mirrors `Encoder.SetEscapeHTML`: when true, `<`, `>` and `&`
/// become `<`, `>`, `&`. U+2028 and U+2029 are always escaped.
/// Invalid UTF-8 bytes (only possible through [`write_string_bytes`]) become
/// `�`, one per invalid byte, like Go.
pub fn write_string(out: &mut Vec<u8>, s: &str, escape_html: bool) {
    write_string_bytes(out, s.as_bytes(), escape_html);
}

/// Like [`write_string`] for a Go string that may hold invalid UTF-8.
pub fn write_string_bytes(out: &mut Vec<u8>, s: &[u8], escape_html: bool) {
    out.push(b'"');
    let mut start = 0;
    let mut i = 0;
    while i < s.len() {
        let b = s[i];
        if b < 0x80 {
            if is_safe(b, escape_html) {
                i += 1;
                continue;
            }
            out.extend_from_slice(&s[start..i]);
            match b {
                b'\\' | b'"' => out.extend_from_slice(&[b'\\', b]),
                0x08 => out.extend_from_slice(b"\\b"),
                0x0c => out.extend_from_slice(b"\\f"),
                b'\n' => out.extend_from_slice(b"\\n"),
                b'\r' => out.extend_from_slice(b"\\r"),
                b'\t' => out.extend_from_slice(b"\\t"),
                _ => {
                    out.extend_from_slice(b"\\u00");
                    out.push(HEX[(b >> 4) as usize]);
                    out.push(HEX[(b & 0xf) as usize]);
                }
            }
            i += 1;
            start = i;
            continue;
        }
        match decode_rune(&s[i..]) {
            None => {
                out.extend_from_slice(&s[start..i]);
                out.extend_from_slice(b"\\ufffd");
                i += 1;
                start = i;
            }
            Some((c, size)) => {
                if c == '\u{2028}' || c == '\u{2029}' {
                    out.extend_from_slice(&s[start..i]);
                    out.extend_from_slice(b"\\u202");
                    out.push(HEX[(c as u32 & 0xf) as usize]);
                    i += size;
                    start = i;
                } else {
                    i += size;
                }
            }
        }
    }
    out.extend_from_slice(&s[start..]);
    out.push(b'"');
}

fn is_safe(b: u8, escape_html: bool) -> bool {
    if b < 0x20 || b == b'"' || b == b'\\' {
        return false;
    }
    !(escape_html && (b == b'<' || b == b'>' || b == b'&'))
}

/// Decodes one UTF-8 sequence like Go's `utf8.DecodeRune`; `None` means Go
/// would return `(RuneError, 1)`.
pub(crate) fn decode_rune(s: &[u8]) -> Option<(char, usize)> {
    let len = match s.first()? {
        0x00..=0x7f => 1,
        0xc2..=0xdf => 2,
        0xe0..=0xef => 3,
        0xf0..=0xf4 => 4,
        _ => return None,
    };
    if s.len() < len {
        return None;
    }
    std::str::from_utf8(&s[..len])
        .ok()
        .and_then(|v| v.chars().next())
        .map(|c| (c, len))
}

/// Formats a float64 exactly like Go's JSON `floatEncoder` (bits = 64).
///
/// NaN and infinities are rejected with Go's `json: unsupported value` error.
pub fn format_float(f: f64) -> Result<String, Error> {
    if !f.is_finite() {
        return Err(Error::UnsupportedValue(go_float_name(f)));
    }
    let abs = f.abs();
    if abs != 0.0 && !(1e-6..1e21).contains(&abs) {
        Ok(exp_format(format!("{f:e}")))
    } else {
        Ok(format!("{f}"))
    }
}

/// Formats a float32 like Go's JSON `floatEncoder` with bits = 32.
pub fn format_float32(f: f32) -> Result<String, Error> {
    if !f.is_finite() {
        return Err(Error::UnsupportedValue(go_float_name(f as f64)));
    }
    let abs = f.abs();
    if abs != 0.0 && !(1e-6..1e21).contains(&abs) {
        Ok(exp_format(format!("{f:e}")))
    } else {
        Ok(format!("{f}"))
    }
}

fn go_float_name(f: f64) -> String {
    if f.is_nan() {
        "NaN".into()
    } else if f > 0.0 {
        "+Inf".into()
    } else {
        "-Inf".into()
    }
}

/// Converts Rust's `{:e}` (`1.5e-7`, `1e21`) to Go's cleaned JSON form
/// (`1.5e-7`, `1e+21`, `1e-10`): Go prints at least two exponent digits and
/// then strips the leading zero of a negative single-digit exponent.
fn exp_format(rust: String) -> String {
    let (mantissa, exp) = rust.split_once('e').expect("LowerExp has an exponent");
    let (sign, digits) = match exp.strip_prefix('-') {
        Some(d) => ('-', d),
        None => ('+', exp),
    };
    let digits = if sign == '+' && digits.len() < 2 {
        format!("0{digits}")
    } else {
        digits.to_string()
    };
    format!("{mantissa}e{sign}{digits}")
}

/// Go `json.Compact` (with the HTML escaping `json.Marshal` applies to
/// `RawMessage` values when `escape_html` is true). The input must be valid
/// JSON; it is validated first.
pub fn compact(src: &[u8], escape_html: bool) -> Result<Vec<u8>, Error> {
    validate(src)?;
    let mut out = Vec::with_capacity(src.len());
    append_compact(&mut out, src, escape_html);
    Ok(out)
}

pub(crate) fn validate(src: &[u8]) -> Result<(), Error> {
    serde_json::from_slice::<serde::de::IgnoredAny>(src)
        .map(|_| ())
        .map_err(|e| Error::Syntax(super::go_syntax_message(&e)))
}

pub(crate) fn append_compact(out: &mut Vec<u8>, src: &[u8], escape_html: bool) {
    let mut in_string = false;
    let mut escaped = false;
    let mut i = 0;
    while i < src.len() {
        let c = src[i];
        if in_string {
            if escaped {
                escaped = false;
            } else if c == b'\\' {
                escaped = true;
            } else if c == b'"' {
                in_string = false;
            }
            if escape_html && (c == b'<' || c == b'>' || c == b'&') {
                out.extend_from_slice(b"\\u00");
                out.push(HEX[(c >> 4) as usize]);
                out.push(HEX[(c & 0xf) as usize]);
                i += 1;
                continue;
            }
            if c == 0xe2 && i + 2 < src.len() && src[i + 1] == 0x80 && src[i + 2] & !1 == 0xa8 {
                out.extend_from_slice(b"\\u202");
                out.push(HEX[(src[i + 2] & 0xf) as usize]);
                i += 3;
                continue;
            }
            out.push(c);
            i += 1;
            continue;
        }
        match c {
            b' ' | b'\t' | b'\n' | b'\r' => {}
            b'"' => {
                in_string = true;
                out.push(c);
            }
            _ => out.push(c),
        }
        i += 1;
    }
}

/// Go `json.Indent` applied to compact (or any valid) JSON: `prefix` starts
/// every line except the first, `indent` is repeated per nesting level, and
/// empty objects/arrays stay `{}` / `[]`. Trailing whitespace after the value
/// (such as the encoder's newline) is preserved.
pub fn indent(src: &[u8], prefix: &str, indent: &str) -> Result<Vec<u8>, Error> {
    let end = src.len()
        - src
            .iter()
            .rev()
            .take_while(|b| matches!(b, b' ' | b'\t' | b'\n' | b'\r'))
            .count();
    validate(&src[..end])?;
    let mut out = Vec::with_capacity(src.len() * 2);
    append_indent(&mut out, &src[..end], prefix, indent);
    out.extend_from_slice(&src[end..]);
    Ok(out)
}

pub(crate) fn append_indent(out: &mut Vec<u8>, src: &[u8], prefix: &str, indent: &str) {
    let mut need_indent = false;
    let mut depth = 0usize;
    let mut in_string = false;
    let mut escaped = false;
    let newline = |out: &mut Vec<u8>, depth: usize| {
        out.push(b'\n');
        out.extend_from_slice(prefix.as_bytes());
        for _ in 0..depth {
            out.extend_from_slice(indent.as_bytes());
        }
    };
    for &c in src {
        if in_string {
            if escaped {
                escaped = false;
            } else if c == b'\\' {
                escaped = true;
            } else if c == b'"' {
                in_string = false;
            }
            out.push(c);
            continue;
        }
        if matches!(c, b' ' | b'\t' | b'\n' | b'\r') {
            continue;
        }
        if need_indent && c != b'}' && c != b']' {
            need_indent = false;
            depth += 1;
            newline(out, depth);
        }
        match c {
            b'"' => {
                in_string = true;
                out.push(c);
            }
            b'{' | b'[' => {
                need_indent = true;
                out.push(c);
            }
            b',' => {
                out.push(c);
                newline(out, depth);
            }
            b':' => out.extend_from_slice(b": "),
            b'}' | b']' => {
                if need_indent {
                    need_indent = false;
                } else {
                    depth = depth.saturating_sub(1);
                    newline(out, depth);
                }
                out.push(c);
            }
            _ => out.push(c),
        }
    }
}

/// Standard padded base64, which Go uses for `[]byte` values.
pub(crate) fn base64_std(data: &[u8]) -> String {
    const TABLE: &[u8; 64] = b"ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/";
    let mut out = String::with_capacity(data.len().div_ceil(3) * 4);
    for chunk in data.chunks(3) {
        let b = [
            chunk[0],
            *chunk.get(1).unwrap_or(&0),
            *chunk.get(2).unwrap_or(&0),
        ];
        let n = (u32::from(b[0]) << 16) | (u32::from(b[1]) << 8) | u32::from(b[2]);
        out.push(TABLE[(n >> 18) as usize & 63] as char);
        out.push(TABLE[(n >> 12) as usize & 63] as char);
        out.push(if chunk.len() > 1 {
            TABLE[(n >> 6) as usize & 63] as char
        } else {
            '='
        });
        out.push(if chunk.len() > 2 {
            TABLE[n as usize & 63] as char
        } else {
            '='
        });
    }
    out
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn floats() {
        for (f, want) in [
            (0.0, "0"),
            (-0.0, "-0"),
            (1.0, "1"),
            (1.5, "1.5"),
            (1e20, "100000000000000000000"),
            (1e21, "1e+21"),
            (1.5e-7, "1.5e-7"),
            (1e-10, "1e-10"),
            (0.000001, "0.000001"),
            (123456789.125, "123456789.125"),
            (1e100, "1e+100"),
            (5e-324, "5e-324"),
        ] {
            assert_eq!(format_float(f).unwrap(), want, "{f}");
        }
        assert!(format_float(f64::NAN).is_err());
    }

    #[test]
    fn base64() {
        assert_eq!(base64_std(b""), "");
        assert_eq!(base64_std(b"f"), "Zg==");
        assert_eq!(base64_std(b"fo"), "Zm8=");
        assert_eq!(base64_std(b"foo"), "Zm9v");
        assert_eq!(base64_std(b"foob"), "Zm9vYg==");
    }
}
