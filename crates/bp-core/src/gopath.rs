//! Go `path/filepath` (Unix) lexical helpers on `&str` paths, so ported code
//! keeps Go's exact results (`Clean("")` is `"."`, `Join` skips empty
//! elements, `Rel` is lexical, ...).

/// Go `filepath.Clean`.
pub fn clean(path: &str) -> String {
    if path.is_empty() {
        return ".".to_string();
    }
    let rooted = path.starts_with('/');
    let mut parts: Vec<&str> = Vec::new();
    for part in path.split('/') {
        match part {
            "" | "." => {}
            ".." => {
                if parts.last().is_some_and(|p| *p != "..") {
                    parts.pop();
                } else if !rooted {
                    parts.push("..");
                }
            }
            p => parts.push(p),
        }
    }
    let joined = parts.join("/");
    match (rooted, joined.is_empty()) {
        (true, _) => format!("/{joined}"),
        (false, true) => ".".to_string(),
        (false, false) => joined,
    }
}

/// Go `filepath.Join`: non-empty elements joined by `/`, then cleaned;
/// `""` when every element is empty.
pub fn join<S: AsRef<str>>(elems: &[S]) -> String {
    let parts: Vec<&str> = elems
        .iter()
        .map(AsRef::as_ref)
        .filter(|e| !e.is_empty())
        .collect();
    if parts.is_empty() {
        return String::new();
    }
    clean(&parts.join("/"))
}

/// Two-element [`join`].
pub fn join2(a: &str, b: &str) -> String {
    join(&[a, b])
}

/// Go `filepath.IsAbs`.
pub fn is_abs(path: &str) -> bool {
    path.starts_with('/')
}

/// Go `filepath.Base`.
pub fn base(path: &str) -> String {
    if path.is_empty() {
        return ".".to_string();
    }
    let trimmed = path.trim_end_matches('/');
    if trimmed.is_empty() {
        return "/".to_string();
    }
    match trimmed.rfind('/') {
        Some(i) => trimmed[i + 1..].to_string(),
        None => trimmed.to_string(),
    }
}

/// Go `filepath.Dir`.
pub fn dir(path: &str) -> String {
    let cut = path.rfind('/').map(|i| i + 1).unwrap_or(0);
    clean(&path[..cut])
}

/// Go `filepath.Ext`.
pub fn ext(path: &str) -> &str {
    for (i, b) in path.bytes().enumerate().rev() {
        if b == b'/' {
            break;
        }
        if b == b'.' {
            return &path[i..];
        }
    }
    ""
}

/// Go `filepath.Rel` (lexical). Errors exactly when Go does: one path is
/// absolute and the other is not, or `targ` cannot be reached without
/// knowing the current directory.
pub fn rel(basepath: &str, targpath: &str) -> Result<String, String> {
    let base = clean(basepath);
    let targ = clean(targpath);
    if targ == base {
        return Ok(".".to_string());
    }
    let base_s = if base == "." { "" } else { base.as_str() };
    let targ_s = if targ == "." { "" } else { targ.as_str() };
    let base_slashed = base_s.starts_with('/');
    let targ_slashed = targ_s.starts_with('/');
    if base_slashed != targ_slashed {
        return Err(format!("Rel: can't make {targpath} relative to {basepath}"));
    }
    let bl = base_s.len();
    let tl = targ_s.len();
    let (bb, tb) = (base_s.as_bytes(), targ_s.as_bytes());
    let (mut b0, mut bi, mut t0, mut ti) = (0, 0, 0, 0);
    loop {
        while bi < bl && bb[bi] != b'/' {
            bi += 1;
        }
        while ti < tl && tb[ti] != b'/' {
            ti += 1;
        }
        if targ_s[t0..ti] != base_s[b0..bi] {
            break;
        }
        if bi < bl {
            bi += 1;
        }
        if ti < tl {
            ti += 1;
        }
        b0 = bi;
        t0 = ti;
        if b0 >= bl && t0 >= tl {
            break;
        }
    }
    if &base_s[b0..bi] == ".." {
        return Err(format!("Rel: can't make {targpath} relative to {basepath}"));
    }
    if b0 != bl {
        let seps = base_s[b0..bl].matches('/').count();
        let mut out = String::from("..");
        for _ in 0..seps {
            out.push_str("/..");
        }
        if t0 != tl {
            out.push('/');
            out.push_str(&targ_s[t0..]);
        }
        return Ok(out);
    }
    Ok(targ_s[t0..].to_string())
}

/// Go `filepath.Abs`: joins a relative path onto the current directory.
pub fn abs(path: &str) -> std::io::Result<String> {
    if is_abs(path) {
        return Ok(clean(path));
    }
    let cwd = std::env::current_dir()?;
    Ok(join2(&cwd.to_string_lossy(), path))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn clean_cases() {
        for (input, want) in [
            ("", "."),
            ("abc", "abc"),
            ("abc/def", "abc/def"),
            ("a/b/c", "a/b/c"),
            (".", "."),
            ("..", ".."),
            ("../..", "../.."),
            ("../../abc", "../../abc"),
            ("/abc", "/abc"),
            ("/", "/"),
            ("abc/", "abc"),
            ("abc/def/", "abc/def"),
            ("/abc/", "/abc"),
            ("//abc", "/abc"),
            ("abc//def//ghi", "abc/def/ghi"),
            ("abc/./def", "abc/def"),
            ("/./abc/def", "/abc/def"),
            ("abc/def/ghi/../jkl", "abc/def/jkl"),
            ("abc/def/../ghi/../jkl", "abc/jkl"),
            ("abc/def/..", "abc"),
            ("abc/def/../..", "."),
            ("/abc/def/../..", "/"),
            ("abc/def/../../..", ".."),
            ("/abc/def/../../..", "/"),
            ("abc/def/../../../ghi/jkl/../../../mno", "../../mno"),
            ("/../abc", "/abc"),
        ] {
            assert_eq!(clean(input), want, "{input}");
        }
    }

    #[test]
    fn rel_cases() {
        for (b, t, want) in [
            ("a/b", "a/b", Ok(".")),
            ("a/b/.", "a/b", Ok(".")),
            ("a/b", "a/b/c", Ok("c")),
            ("a/b/c", "a/b", Ok("..")),
            ("a/b/c", "a/c/d", Ok("../../c/d")),
            ("a/b", "c/d", Ok("../../c/d")),
            ("a/b", "../c", Ok("../../../c")),
            ("../a", "b", Err(())),
            ("../a", "../b", Ok("../b")),
            ("/a/b", "/a/b/c", Ok("c")),
            ("/a", "/b/c", Ok("../b/c")),
            ("/", "/a", Ok("a")),
            ("/a", "b", Err(())),
            ("/root/x", "/root/x-old", Ok("../x-old")),
        ] {
            assert_eq!(rel(b, t).map_err(|_| ()), want.map(String::from), "{b} {t}");
        }
        assert_eq!(base("/a/b/"), "b");
        assert_eq!(dir("/a/b"), "/a");
        assert_eq!(dir("b"), ".");
        assert_eq!(ext("a/b.json"), ".json");
        assert_eq!(ext("a.b/c"), "");
        assert_eq!(join(&["", ""]), "");
        assert_eq!(join(&["/home/x", "~/.ssh"[2..].as_ref()]), "/home/x/.ssh");
    }
}
