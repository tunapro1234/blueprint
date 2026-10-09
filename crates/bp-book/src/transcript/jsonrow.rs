//! Port of internal/cache/jsonrow.go: cheap prefilters that let readers skip
//! JSONL rows of other types without decoding them.

use crate::godecode::equal_fold_ascii;

fn fold_eq(value: &[u8], candidate: &str) -> bool {
    // strings.EqualFold on Go strings; invalid UTF-8 never folds to ASCII.
    match std::str::from_utf8(value) {
        Ok(s) => equal_fold_ascii(s, candidate),
        Err(_) => false,
    }
}

/// Cheaply recognizes a common compact JSONL prefix. It is only a positive
/// hint; callers still decode the row as usual.
pub fn has_compact_record_type_prefix(line: &[u8], types: &[&str]) -> bool {
    types.iter().any(|t| {
        let prefix = format!("{{\"type\":\"{t}\"");
        line.starts_with(prefix.as_bytes())
    })
}

/// Skips a compact row only when it starts with an unescaped top-level type,
/// has no possible duplicate type key, and names a type outside `wanted`.
/// Escaped or unusual JSON takes the full decoder.
pub fn definitely_other_compact_record_type(line: &[u8], wanted: &[&str]) -> bool {
    const PREFIX: &[u8] = b"{\"type\":\"";
    if !line.starts_with(PREFIX) {
        return false;
    }
    let value_start = PREFIX.len();
    let Some(value_len) = line[value_start..].iter().position(|&c| c == b'"') else {
        return false;
    };
    let value_end = value_start + value_len;
    if line[value_start..value_end].contains(&b'\\') {
        return false;
    }
    let type_name = &line[value_start..value_end];
    if wanted.iter().any(|candidate| fold_eq(type_name, candidate)) {
        return false;
    }
    let mut i = value_end + 1;
    while i < line.len() {
        if line[i] == b'\\' {
            return false;
        }
        if line[i] == b'"'
            && i + 5 < line.len()
            && line[i + 5] == b'"'
            && equal_fold_type(&line[i + 1..i + 5])
        {
            return false;
        }
        i += 1;
    }
    true
}

fn equal_fold_type(value: &[u8]) -> bool {
    value.len() == 4 && value.eq_ignore_ascii_case(b"type")
}

/// Returns false only when it can safely prove that a valid top-level JSON
/// object has a different type. Ambiguous JSON is left to the regular decoder.
pub fn may_have_record_type(line: &[u8], wanted: &[&str]) -> bool {
    let Some(type_name) = top_level_record_type(line) else {
        return true;
    };
    wanted.iter().any(|candidate| fold_eq(type_name, candidate))
}

fn top_level_record_type(data: &[u8]) -> Option<&[u8]> {
    let mut i = skip_json_space(data, 0);
    if i >= data.len() || data[i] != b'{' {
        return None;
    }
    i += 1;
    let mut type_name: &[u8] = b"";
    let mut found = false;
    loop {
        i = skip_json_space(data, i);
        if i >= data.len() {
            return None;
        }
        if data[i] == b'}' {
            i = skip_json_space(data, i + 1);
            return (i == data.len()).then_some(type_name);
        }
        let key_start = i;
        let (key_end, escaped) = scan_json_string(data, i)?;
        if escaped {
            return None;
        }
        let key = &data[key_start + 1..key_end - 1];
        i = skip_json_space(data, key_end);
        if i >= data.len() || data[i] != b':' {
            return None;
        }
        i = skip_json_space(data, i + 1);
        if fold_eq(key, "type") {
            if found {
                return None;
            }
            let value_start = i;
            let (value_end, value_escaped) = scan_json_string(data, i)?;
            if value_escaped {
                return None;
            }
            type_name = &data[value_start + 1..value_end - 1];
            found = true;
            i = value_end;
        } else {
            i = skip_json_value(data, i)?;
        }
        i = skip_json_space(data, i);
        if i >= data.len() {
            return None;
        }
        match data[i] {
            b',' => i += 1,
            b'}' => {
                i = skip_json_space(data, i + 1);
                return (i == data.len()).then_some(type_name);
            }
            _ => return None,
        }
    }
}

fn skip_json_space(data: &[u8], mut i: usize) -> usize {
    while i < data.len() && matches!(data[i], b' ' | b'\t' | b'\n' | b'\r') {
        i += 1;
    }
    i
}

/// Returns the offset after the closing quote and whether the string had escapes.
fn scan_json_string(data: &[u8], start: usize) -> Option<(usize, bool)> {
    if start >= data.len() || data[start] != b'"' {
        return None;
    }
    let mut escaped = false;
    let mut i = start + 1;
    while i < data.len() {
        match data[i] {
            b'"' => return Some((i + 1, escaped)),
            b'\\' => {
                escaped = true;
                i += 1;
                if i >= data.len() {
                    return None;
                }
            }
            b'\n' | b'\r' => return None,
            _ => {}
        }
        i += 1;
    }
    None
}

fn skip_json_value(data: &[u8], start: usize) -> Option<usize> {
    if start >= data.len() {
        return None;
    }
    match data[start] {
        b'"' => scan_json_string(data, start).map(|(end, _)| end),
        open @ (b'{' | b'[') => {
            let mut stack = vec![if open == b'{' { b'}' } else { b']' }];
            let mut i = start + 1;
            while i < data.len() {
                match data[i] {
                    b'"' => {
                        let (end, _) = scan_json_string(data, i)?;
                        i = end - 1;
                    }
                    b'{' => stack.push(b'}'),
                    b'[' => stack.push(b']'),
                    c @ (b'}' | b']') => {
                        if stack.last() != Some(&c) {
                            return None;
                        }
                        stack.pop();
                        if stack.is_empty() {
                            return Some(i + 1);
                        }
                    }
                    _ => {}
                }
                i += 1;
            }
            None
        }
        _ => {
            let mut end = start;
            while end < data.len() {
                match data[end] {
                    b',' | b'}' | b']' | b' ' | b'\t' | b'\n' | b'\r' => {
                        return (end != start).then_some(end);
                    }
                    _ => end += 1,
                }
            }
            (end > start).then_some(end)
        }
    }
}
