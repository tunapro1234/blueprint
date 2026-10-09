//! Port of Go's `path/filepath` `Match`, `Glob`, `Clean` and `Join` (Unix
//! rules), which `internal/cache` relies on for rollout lookups.

use std::ffi::OsStr;
use std::fs;
use std::os::unix::ffi::OsStrExt;

/// `filepath.ErrBadPattern`.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub struct BadPattern;

/// `filepath.Clean`.
pub fn clean(path: &str) -> String {
    let p = path.as_bytes();
    if p.is_empty() {
        return ".".to_owned();
    }
    let rooted = p[0] == b'/';
    let n = p.len();
    let mut out: Vec<u8> = Vec::with_capacity(n);
    let (mut r, mut dotdot) = (0, 0);
    if rooted {
        out.push(b'/');
        r = 1;
        dotdot = 1;
    }
    while r < n {
        if p[r] == b'/' || (p[r] == b'.' && (r + 1 == n || p[r + 1] == b'/')) {
            r += 1;
        } else if p[r] == b'.' && p.get(r + 1) == Some(&b'.') && (r + 2 == n || p[r + 2] == b'/') {
            r += 2;
            if out.len() > dotdot {
                out.pop();
                while out.len() > dotdot && *out.last().unwrap_or(&b'/') != b'/' {
                    out.pop();
                }
                // Go's w points at the separator; drop it as well.
                if out.len() > dotdot && out.last() == Some(&b'/') {
                    out.pop();
                }
            } else if !rooted {
                if !out.is_empty() {
                    out.push(b'/');
                }
                out.extend_from_slice(b"..");
                dotdot = out.len();
            }
        } else {
            if (rooted && out.len() != 1) || (!rooted && !out.is_empty()) {
                out.push(b'/');
            }
            while r < n && p[r] != b'/' {
                out.push(p[r]);
                r += 1;
            }
        }
    }
    if out.is_empty() {
        out.push(b'.');
    }
    String::from_utf8_lossy(&out).into_owned()
}

/// `filepath.Join`.
pub fn join(elems: &[&str]) -> String {
    match elems.iter().position(|e| !e.is_empty()) {
        Some(i) => clean(&elems[i..].join("/")),
        None => String::new(),
    }
}

/// `filepath.Match`.
pub fn match_pattern(pattern: &str, name: &str) -> Result<bool, BadPattern> {
    match_bytes(pattern.as_bytes(), name.as_bytes())
}

fn match_bytes(mut pattern: &[u8], mut name: &[u8]) -> Result<bool, BadPattern> {
    'pattern: while !pattern.is_empty() {
        let (star, chunk, rest) = scan_chunk(pattern);
        pattern = rest;
        if star && chunk.is_empty() {
            return Ok(!name.contains(&b'/'));
        }
        let (t, ok, err) = match_chunk(chunk, name);
        if ok && (t.is_empty() || !pattern.is_empty()) {
            name = t;
            continue;
        }
        if let Some(err) = err {
            return Err(err);
        }
        if star {
            let mut i = 0;
            while i < name.len() && name[i] != b'/' {
                let (t, ok, err) = match_chunk(chunk, &name[i + 1..]);
                if ok {
                    if pattern.is_empty() && !t.is_empty() {
                        i += 1;
                        continue;
                    }
                    name = t;
                    continue 'pattern;
                }
                if let Some(err) = err {
                    return Err(err);
                }
                i += 1;
            }
        }
        return Ok(false);
    }
    Ok(name.is_empty())
}

fn scan_chunk(mut pattern: &[u8]) -> (bool, &[u8], &[u8]) {
    let mut star = false;
    while let Some(b'*') = pattern.first() {
        pattern = &pattern[1..];
        star = true;
    }
    let mut inrange = false;
    let mut i = 0;
    while i < pattern.len() {
        match pattern[i] {
            b'\\' => {
                if i + 1 < pattern.len() {
                    i += 1;
                }
            }
            b'[' => inrange = true,
            b']' => inrange = false,
            b'*' if !inrange => break,
            _ => {}
        }
        i += 1;
    }
    (star, &pattern[..i], &pattern[i..])
}

/// Go's `utf8.DecodeRuneInString`: (rune, size), RuneError/1 when invalid.
fn decode_rune(s: &[u8]) -> (u32, usize) {
    if s.is_empty() {
        return (0xFFFD, 0);
    }
    let len = match s[0] {
        0x00..=0x7f => return (u32::from(s[0]), 1),
        0xc2..=0xdf => 2,
        0xe0..=0xef => 3,
        0xf0..=0xf4 => 4,
        _ => return (0xFFFD, 1),
    };
    match s.get(..len).and_then(|b| std::str::from_utf8(b).ok()) {
        Some(text) => (text.chars().next().map_or(0xFFFD, u32::from), len),
        None => (0xFFFD, 1),
    }
}

fn match_chunk<'a>(mut chunk: &[u8], mut s: &'a [u8]) -> (&'a [u8], bool, Option<BadPattern>) {
    let mut failed = false;
    while !chunk.is_empty() {
        if !failed && s.is_empty() {
            failed = true;
        }
        match chunk[0] {
            b'[' => {
                let mut r = 0u32;
                if !failed {
                    let (rr, n) = decode_rune(s);
                    r = rr;
                    s = &s[n..];
                }
                chunk = &chunk[1..];
                let mut negated = false;
                if chunk.first() == Some(&b'^') {
                    negated = true;
                    chunk = &chunk[1..];
                }
                let mut matched = false;
                let mut nrange = 0;
                loop {
                    if chunk.first() == Some(&b']') && nrange > 0 {
                        chunk = &chunk[1..];
                        break;
                    }
                    let (lo, rest) = match get_esc(chunk) {
                        Ok(v) => v,
                        Err(err) => return (b"", false, Some(err)),
                    };
                    chunk = rest;
                    let mut hi = lo;
                    if chunk[0] == b'-' {
                        match get_esc(&chunk[1..]) {
                            Ok((h, rest)) => {
                                hi = h;
                                chunk = rest;
                            }
                            Err(err) => return (b"", false, Some(err)),
                        }
                    }
                    if lo <= r && r <= hi {
                        matched = true;
                    }
                    nrange += 1;
                }
                if matched == negated {
                    failed = true;
                }
            }
            b'?' => {
                if !failed {
                    if s[0] == b'/' {
                        failed = true;
                    }
                    let (_, n) = decode_rune(s);
                    s = &s[n..];
                }
                chunk = &chunk[1..];
            }
            c => {
                let c = if c == b'\\' {
                    chunk = &chunk[1..];
                    match chunk.first() {
                        Some(&c) => c,
                        None => return (b"", false, Some(BadPattern)),
                    }
                } else {
                    c
                };
                if !failed {
                    if c != s[0] {
                        failed = true;
                    }
                    s = &s[1..];
                }
                chunk = &chunk[1..];
            }
        }
    }
    if failed {
        (b"", false, None)
    } else {
        (s, true, None)
    }
}

fn get_esc(mut chunk: &[u8]) -> Result<(u32, &[u8]), BadPattern> {
    if chunk.is_empty() || chunk[0] == b'-' || chunk[0] == b']' {
        return Err(BadPattern);
    }
    if chunk[0] == b'\\' {
        chunk = &chunk[1..];
        if chunk.is_empty() {
            return Err(BadPattern);
        }
    }
    let (r, n) = decode_rune(chunk);
    let rest = &chunk[n..];
    if (r == 0xFFFD && n == 1) || rest.is_empty() {
        return Err(BadPattern);
    }
    Ok((r, rest))
}

fn has_meta(path: &str) -> bool {
    path.bytes()
        .any(|c| matches!(c, b'*' | b'?' | b'[' | b'\\'))
}

/// `filepath.Glob`.
pub fn glob(pattern: &str) -> Result<Vec<String>, BadPattern> {
    glob_with_limit(pattern, 0)
}

fn glob_with_limit(pattern: &str, depth: usize) -> Result<Vec<String>, BadPattern> {
    if depth == 10_000 {
        return Err(BadPattern);
    }
    match_pattern(pattern, "")?;
    if !has_meta(pattern) {
        if fs::symlink_metadata(pattern).is_err() {
            return Ok(Vec::new());
        }
        return Ok(vec![pattern.to_owned()]);
    }
    let split = pattern.rfind('/').map_or(0, |i| i + 1);
    let (dir, file) = pattern.split_at(split);
    let dir = match dir {
        "" => ".",
        "/" => "/",
        _ => &dir[..dir.len() - 1],
    };
    if !has_meta(dir) {
        let mut matches = Vec::new();
        glob_dir(dir, file, &mut matches)?;
        return Ok(matches);
    }
    if dir == pattern {
        return Err(BadPattern);
    }
    let mut matches = Vec::new();
    for d in glob_with_limit(dir, depth + 1)? {
        glob_dir(&d, file, &mut matches)?;
    }
    Ok(matches)
}

fn glob_dir(dir: &str, pattern: &str, matches: &mut Vec<String>) -> Result<(), BadPattern> {
    match fs::metadata(dir) {
        Ok(info) if info.is_dir() => {}
        _ => return Ok(()),
    }
    let Ok(entries) = fs::read_dir(dir) else {
        return Ok(());
    };
    let mut names: Vec<Vec<u8>> = entries
        .filter_map(Result::ok)
        .map(|e| e.file_name().as_bytes().to_vec())
        .collect();
    names.sort();
    for name in names {
        if match_bytes(pattern.as_bytes(), &name)? {
            let name = OsStr::from_bytes(&name).to_string_lossy().into_owned();
            matches.push(join(&[dir, &name]));
        }
    }
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn clean_matches_go() {
        for (input, want) in [
            ("", "."),
            ("/", "/"),
            ("a/b/../c", "a/c"),
            ("/../a", "/a"),
            ("../../a", "../../a"),
            ("a/../..", ".."),
            ("/a/b/./c//", "/a/b/c"),
            ("abc/def/../../..", ".."),
            ("/abc/def/../../..", "/"),
            ("a/b/c/../../d", "a/d"),
        ] {
            assert_eq!(clean(input), want, "{input}");
        }
    }

    #[test]
    fn match_matches_go() {
        for (pattern, name, want) in [
            ("abc", "abc", Ok(true)),
            ("*", "abc", Ok(true)),
            ("*c", "abc", Ok(true)),
            ("a*", "ab/c", Ok(false)),
            ("a*/b", "abc/b", Ok(true)),
            ("a*b*c*d*e*/f", "axbxcxdxe/f", Ok(true)),
            ("ab[c]", "abc", Ok(true)),
            ("ab[b-d]", "abc", Ok(true)),
            ("ab[^c]", "abc", Ok(false)),
            ("a\\*b", "a*b", Ok(true)),
            ("a?b", "a/b", Ok(false)),
            ("[", "a", Err(BadPattern)),
            ("[^", "a", Err(BadPattern)),
            ("a[", "a", Err(BadPattern)),
            ("*x", "xxx", Ok(true)),
            ("*-wild*card.jsonl", "rollout-wild-X-card.jsonl", Ok(true)),
        ] {
            assert_eq!(match_pattern(pattern, name), want, "{pattern} {name}");
        }
    }
}
