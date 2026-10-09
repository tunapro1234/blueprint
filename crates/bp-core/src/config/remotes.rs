//! Port of internal/config/remotes.go: `UpdateRemote`.
//!
//! Go round-trips YAML through a `yaml.Node` tree and re-encodes the whole
//! document with yaml.v3 (2-space indent). This port edits the text instead:
//! only the `remotes` block changes, so for canonical files (2-space block
//! style, no blank lines) the bytes match Go. Files Go would re-format
//! (blank lines, other indentation, flow style inside unrelated keys) keep
//! their original formatting here.

use std::collections::BTreeMap;
use std::path::Path;

use serde_json::value::RawValue;

use super::decode::{parse_yaml, read_remotes};
use super::tree::{Ctx, Format, Val};
use super::{Error, RemoteConfig, go_io_error, valid_federation_name, validate_remotes};
use crate::fs::{AtomicOptions, FileLock, LockKind};
use crate::gojson::time::go_quote;
use crate::projectschema::{analyze, base60_float, double_quoted, is_old_bool, resolves_non_string};

/// Go `UpdateRemote`: atomically adds, replaces (`Some`) or removes (`None`)
/// one remote in an existing config file. A sidecar `<path>.lock` flock
/// serializes bp writers; other keys and YAML comments are kept.
pub fn update_remote(path: &str, name: &str, value: Option<&RemoteConfig>) -> Result<(), Error> {
    if path.is_empty() {
        return Err(Error("config path is required".to_string()));
    }
    let candidate = match value {
        Some(value) => {
            let mut candidate = value.clone();
            if candidate.transport.is_empty() {
                candidate.transport = "ssh".to_string();
            }
            validate_remotes(&BTreeMap::from([(name.to_string(), candidate.clone())])).map_err(Error)?;
            Some(candidate)
        }
        None => {
            if !valid_federation_name(name) {
                return Err(Error(format!("invalid server name {}", go_quote(name))));
            }
            None
        }
    };

    let lock_path = format!("{path}.lock");
    let _lock = FileLock::lock_path(Path::new(&lock_path), LockKind::Exclusive)
        .map_err(|e| Error(format!("open {lock_path}: {}", go_io_error(&e))))?;

    let data = std::fs::read(path).map_err(|e| Error(format!("open {path}: {}", go_io_error(&e))))?;
    let encoded = if crate::gopath::ext(path) == ".json" {
        update_remote_json(&data, name, candidate.as_ref())
    } else {
        update_remote_yaml(&data, name, candidate.as_ref())
    }
    .map_err(Error)?;
    let options = AtomicOptions {
        temp_pattern: ".config-*.tmp".to_string(),
        mode: None,
        preserve_mode: true,
        sync_dir: false,
    };
    crate::fs::atomic_write(Path::new(path), &encoded, &options)
        .map_err(|e| Error(go_io_error(&e)))?;
    Ok(())
}

pub(crate) fn update_remote_json(data: &[u8], name: &str, value: Option<&RemoteConfig>) -> Result<Vec<u8>, String> {
    let clean = crate::gojson::sanitize(data);
    let mut root: BTreeMap<String, Box<RawValue>> = BTreeMap::new();
    if !crate::text::go_trim_space(&String::from_utf8_lossy(&clean)).is_empty() {
        root = serde_json::from_slice::<Option<BTreeMap<String, Box<RawValue>>>>(&clean)
            .map_err(|e| crate::gojson::go_syntax_message(&e))?
            .unwrap_or_default();
    }
    let mut remotes = BTreeMap::new();
    if let Some(raw) = root.get("remotes") {
        let node = crate::gojson::parse_node(raw.get().as_bytes()).map_err(|e| format!("remotes: {e}"))?;
        let ctx = Ctx {
            format: Format::Json,
            type_name: "config.RemoteConfig",
            path: String::new(),
        };
        read_remotes(&mut remotes, &Val::from_node(node), &ctx).map_err(|e| format!("remotes: {e}"))?;
    }
    match value {
        None => {
            if remotes.remove(name).is_none() {
                return Err(format!("unknown remote: {name}"));
            }
        }
        Some(value) => {
            remotes.insert(name.to_string(), value.clone());
        }
    }
    if remotes.is_empty() {
        root.remove("remotes");
    } else {
        let raw = crate::gojson::to_string(&remotes).map_err(|e| e.to_string())?;
        root.insert(
            "remotes".to_string(),
            RawValue::from_string(raw).map_err(|e| e.to_string())?,
        );
    }
    let mut encoded = crate::gojson::to_vec_indent(&root, "", "  ").map_err(|e| e.to_string())?;
    encoded.push(b'\n');
    Ok(encoded)
}

// ---- YAML ----

fn indent_of(line: &str) -> usize {
    line.len() - line.trim_start_matches([' ', '\t']).len()
}

fn is_blank(line: &str) -> bool {
    line.trim().is_empty()
}

fn is_comment(line: &str) -> bool {
    line.trim_start().starts_with('#')
}

fn is_marker(line: &str) -> bool {
    let t = line.trim_end();
    t == "---" || t == "..." || t.starts_with("--- ") || t.starts_with("... ")
}

fn is_content(line: &str) -> bool {
    !is_blank(line) && !is_comment(line) && !is_marker(line)
}

/// The key of a block mapping line (`key: value`, `'key':`, `"key":`) and
/// the text after the colon.
fn line_key(line: &str) -> Option<(String, &str, &str)> {
    let body = line.trim_start_matches([' ', '\t']);
    let (key, token_len) = if let Some(rest) = body.strip_prefix('\'') {
        let mut key = String::new();
        let mut chars = rest.char_indices().peekable();
        let mut end = None;
        while let Some((i, c)) = chars.next() {
            if c == '\'' {
                if chars.peek().map(|(_, n)| *n) == Some('\'') {
                    key.push('\'');
                    chars.next();
                    continue;
                }
                end = Some(i + 1);
                break;
            }
            key.push(c);
        }
        (key, 1 + end?)
    } else if body.starts_with('"') {
        let mut end = None;
        let mut escaped = false;
        for (i, c) in body.char_indices().skip(1) {
            if escaped {
                escaped = false;
            } else if c == '\\' {
                escaped = true;
            } else if c == '"' {
                end = Some(i + 1);
                break;
            }
        }
        let token = &body[..end?];
        (serde_json::from_str::<String>(token).ok()?, token.len())
    } else {
        let bytes = body.as_bytes();
        let mut i = 0;
        loop {
            if i >= bytes.len() {
                return None;
            }
            if bytes[i] == b':' && (i + 1 == bytes.len() || matches!(bytes[i + 1], b' ' | b'\t' | b'\n' | b'\r')) {
                break;
            }
            i += 1;
        }
        (body[..i].trim_end().to_string(), i)
    };
    let after = body[token_len..].trim_start_matches([' ', '\t']);
    let after = after.strip_prefix(':')?;
    Some((key, &body[..token_len], after))
}

/// Whether the text after `key:` holds an inline value (not just a comment).
fn has_inline_value(after: &str) -> bool {
    let t = after.trim();
    !t.is_empty() && !t.starts_with('#')
}

/// A mapping key as yaml.v3 emits a `!!str` scalar node.
fn yaml_key(name: &str) -> String {
    if resolves_non_string(name) {
        return double_quoted(name);
    }
    match analyze(name) {
        (true, _) => name.to_string(),
        (false, true) => format!("'{}'", name.replace('\'', "''")),
        _ => double_quoted(name),
    }
}

/// A string as yaml.v3's `stringv` emits it (block context).
fn yaml_string(value: &str) -> String {
    crate::projectschema::yaml_scalar(value)
}

/// A string in flow context (`{a: b}`): flow indicators also forbid plain.
fn yaml_flow_string(value: &str) -> String {
    if resolves_non_string(value) || base60_float(value) || is_old_bool(value) {
        return double_quoted(value);
    }
    let (plain, single) = analyze(value);
    let flow_indicator = value.contains([',', '?', '[', ']', '{', '}', ':']);
    if plain && !flow_indicator {
        value.to_string()
    } else if single {
        format!("'{}'", value.replace('\'', "''"))
    } else {
        double_quoted(value)
    }
}

fn remote_fields(remote: &RemoteConfig) -> Vec<(&'static str, String, bool)> {
    let mut out = vec![("host", remote.host.clone(), true)];
    if remote.port != 0 {
        out.push(("port", remote.port.to_string(), false));
    }
    for (key, value) in [
        ("user", &remote.user),
        ("identity", &remote.identity),
        ("transport", &remote.transport),
        ("moshPorts", &remote.mosh_ports),
        ("elevate", &remote.elevate),
    ] {
        if !value.is_empty() {
            out.push((key, value.clone(), true));
        }
    }
    out
}

fn render_block_entry(indent: usize, step: usize, key: &str, remote: &RemoteConfig) -> String {
    let pad = " ".repeat(indent);
    let inner = " ".repeat(indent + step);
    let mut out = format!("{pad}{key}:\n");
    for (field, value, is_string) in remote_fields(remote) {
        let rendered = if is_string { yaml_string(&value) } else { value };
        out.push_str(&format!("{inner}{field}: {rendered}\n"));
    }
    out
}

fn render_flow_remote(remote: &RemoteConfig) -> String {
    let parts: Vec<String> = remote_fields(remote)
        .into_iter()
        .map(|(field, value, is_string)| {
            let rendered = if is_string { yaml_flow_string(&value) } else { value };
            format!("{field}: {rendered}")
        })
        .collect();
    format!("{{{}}}", parts.join(", "))
}

fn render_flow_val(value: &Val) -> String {
    match value {
        Val::Null => "null".to_string(),
        Val::Bool(b) => b.to_string(),
        Val::Int(i) => i.to_string(),
        Val::Float(f) => {
            if f.is_nan() {
                ".nan".to_string()
            } else if f.is_infinite() {
                if *f > 0.0 { ".inf" } else { "-.inf" }.to_string()
            } else {
                let s = f.to_string();
                if s.contains(['.', 'e']) { s } else { format!("{s}.0") }
            }
        }
        Val::Str(s) => yaml_flow_string(s),
        Val::Seq(items) => format!("[{}]", items.iter().map(render_flow_val).collect::<Vec<_>>().join(", ")),
        Val::Map(entries) => render_flow_map(entries.iter().map(|(k, v)| (yaml_key(k), render_flow_val(v)))),
    }
}

fn render_flow_map(entries: impl Iterator<Item = (String, String)>) -> String {
    let parts: Vec<String> = entries.map(|(k, v)| format!("{k}: {v}")).collect();
    format!("{{{}}}", parts.join(", "))
}

/// Remotes in flow style after the edit, from the parsed value.
fn flow_remotes(existing: &[(String, Val)], name: &str, value: Option<&RemoteConfig>) -> Result<Option<String>, String> {
    let mut entries: Vec<(String, String)> = Vec::new();
    let mut found = false;
    for (key, val) in existing {
        if key == name {
            found = true;
            if let Some(remote) = value {
                entries.push((yaml_key(key), render_flow_remote(remote)));
            }
        } else {
            entries.push((yaml_key(key), render_flow_val(val)));
        }
    }
    match value {
        None if !found => return Err(format!("unknown remote: {name}")),
        Some(remote) if !found => entries.push((yaml_key(name), render_flow_remote(remote))),
        _ => {}
    }
    if entries.is_empty() {
        return Ok(None);
    }
    Ok(Some(render_flow_map(entries.into_iter())))
}

pub(crate) fn update_remote_yaml(data: &[u8], name: &str, value: Option<&RemoteConfig>) -> Result<Vec<u8>, String> {
    let text = String::from_utf8_lossy(data).into_owned();
    let lines: Vec<&str> = text.split_inclusive('\n').collect();

    if !lines.iter().any(|l| is_content(l)) {
        // yaml.v3 finds no node: a fresh mapping (comments are dropped).
        let Some(remote) = value else {
            return Err(format!("unknown remote: {name}"));
        };
        return Ok(format!("remotes:\n{}", render_block_entry(2, 2, &yaml_key(name), remote)).into_bytes());
    }
    let parsed = parse_yaml(&text)?;
    let Val::Map(root) = &parsed else {
        return Err("config must be a YAML mapping".to_string());
    };
    let remotes_val = root.iter().rev().find(|(k, _)| k == "remotes").map(|(_, v)| v);
    let remotes_entries = match remotes_val {
        None => None,
        Some(Val::Map(entries)) => Some(entries.as_slice()),
        Some(_) => return Err("remotes must be a mapping".to_string()),
    };

    let first_content = lines.iter().position(|l| is_content(l)).unwrap_or(0);
    if lines[first_content].trim_start().starts_with('{') {
        // Flow-style root: yaml.v3 re-emits it in flow style.
        let mut entries: Vec<(String, String)> = Vec::new();
        let mut seen = false;
        for (key, val) in root {
            if key == "remotes" {
                seen = true;
                if let Some(rendered) = flow_remotes(remotes_entries.unwrap_or(&[]), name, value)? {
                    entries.push((yaml_key(key), rendered));
                }
            } else {
                entries.push((yaml_key(key), render_flow_val(val)));
            }
        }
        if !seen {
            let Some(remote) = value else {
                return Err(format!("unknown remote: {name}"));
            };
            let inner = render_flow_map(std::iter::once((yaml_key(name), render_flow_remote(remote))));
            entries.push(("remotes".to_string(), inner));
        }
        return Ok(format!("{}\n", render_flow_map(entries.into_iter())).into_bytes());
    }

    let remotes_line = lines.iter().position(|l| {
        indent_of(l) == 0 && is_content(l) && line_key(l).is_some_and(|(k, _, _)| k == "remotes")
    });
    let mut out: Vec<String> = lines.iter().map(|l| l.to_string()).collect();
    if let Some(last) = out.last_mut()
        && !last.ends_with('\n')
    {
        last.push('\n');
    }

    let Some(r) = remotes_line else {
        let Some(remote) = value else {
            return Err(format!("unknown remote: {name}"));
        };
        let block = format!("remotes:\n{}", render_block_entry(2, 2, &yaml_key(name), remote));
        let at = match out.iter().rposition(|l| is_content(l)) {
            Some(last) => out[last + 1..]
                .iter()
                .position(|l| l.trim_end() == "...")
                .map_or(out.len(), |p| last + 1 + p),
            None => out.len(),
        };
        out.insert(at, block);
        return Ok(out.concat().into_bytes());
    };

    let (_, _, after) = line_key(lines[r]).expect("remotes line has a key");
    if has_inline_value(after) {
        // Flow-style remotes value on the key line.
        let rendered = flow_remotes(remotes_entries.unwrap_or(&[]), name, value)?;
        match rendered {
            Some(flow) => out[r] = format!("remotes: {flow}\n"),
            None => {
                out.remove(r);
            }
        }
        return Ok(finish(out));
    }

    // Block-style remotes: the indented lines after the key.
    let mut block_end = r + 1;
    for (j, line) in lines.iter().enumerate().skip(r + 1) {
        if is_blank(line) {
            continue;
        }
        if indent_of(line) == 0 {
            if is_comment(line) {
                continue;
            }
            break;
        }
        block_end = j + 1;
    }
    let child_indent = lines[r + 1..block_end]
        .iter()
        .find(|l| is_content(l))
        .map(|l| indent_of(l))
        .unwrap_or(2);
    let step = child_indent.max(1);
    // (key, head start, key line)
    let mut entries: Vec<(String, String, usize, usize)> = Vec::new();
    for j in r + 1..block_end {
        let line = lines[j];
        if !is_content(line) || indent_of(line) != child_indent || line.trim_start().starts_with("- ") {
            continue;
        }
        let Some((key, token, _)) = line_key(line) else {
            continue;
        };
        let floor = entries.last().map_or(r + 1, |e| e.3 + 1);
        let mut head = j;
        while head > floor && is_comment(lines[head - 1]) && indent_of(lines[head - 1]) == child_indent {
            head -= 1;
        }
        entries.push((key, token.to_string(), head, j));
    }
    let span_end = |index: usize| entries.get(index + 1).map_or(block_end, |e| e.2);
    let position = entries.iter().position(|e| e.0 == name);
    match (value, position) {
        (None, None) => return Err(format!("unknown remote: {name}")),
        (None, Some(i)) => {
            let (start, end) = (entries[i].2, span_end(i));
            if entries.len() == 1 {
                out.drain(r..block_end);
            } else {
                out.drain(start..end);
            }
        }
        (Some(remote), Some(i)) => {
            let (key_line, end) = (entries[i].3, span_end(i));
            let rendered = render_block_entry(child_indent, step, &entries[i].1, remote);
            out.splice(key_line..end, std::iter::once(rendered));
        }
        (Some(remote), None) => {
            let rendered = render_block_entry(child_indent, step, &yaml_key(name), remote);
            out.insert(block_end, rendered);
        }
    }
    Ok(finish(out))
}

fn finish(out: Vec<String>) -> Vec<u8> {
    if !out.iter().any(|l| is_content(l)) {
        return b"{}\n".to_vec();
    }
    out.concat().into_bytes()
}
