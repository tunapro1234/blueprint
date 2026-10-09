//! Port of internal/projectschema: loads and validates portable Blueprint
//! project schemas (`.blueprint/schema.yaml`).

use serde::{Deserialize, Deserializer};
use std::collections::{BTreeMap, HashMap, HashSet};

use crate::gojson::time::go_quote;
use crate::gopath;

pub const DIRECTORY: &str = ".blueprint";
pub const FILENAME: &str = "schema.yaml";

/// Schema failures (messages match Go's `fmt.Errorf` texts; YAML syntax
/// errors come from serde_norway and differ from yaml.v3's wording).
#[derive(Debug, Clone, PartialEq, Eq, thiserror::Error)]
#[error("{0}")]
pub struct Error(pub String);

pub type Result<T> = std::result::Result<T, Error>;

fn err<T>(message: impl Into<String>) -> Result<T> {
    Err(Error(message.into()))
}

#[derive(Debug, Clone, PartialEq, Eq, Default, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct Schema {
    #[serde(default, deserialize_with = "go_int")]
    pub version: i64,
    #[serde(default, deserialize_with = "go_string")]
    pub history: String,
    #[serde(default, deserialize_with = "go_string")]
    pub remote: String,
    #[serde(default, deserialize_with = "go_list")]
    pub agents: Vec<Agent>,
}

#[derive(Debug, Clone, PartialEq, Eq, Default, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct Agent {
    #[serde(default, deserialize_with = "go_string")]
    pub name: String,
    #[serde(default, deserialize_with = "go_string")]
    pub folder: String,
    #[serde(default, deserialize_with = "go_string")]
    pub parent: String,
    #[serde(default, deserialize_with = "go_string")]
    pub runtime: String,
    #[serde(default, deserialize_with = "go_string")]
    pub role: String,
    #[serde(default, deserialize_with = "go_string")]
    pub color: String,
    #[serde(default, deserialize_with = "go_string")]
    pub model: String,
    #[serde(default, deserialize_with = "go_string")]
    pub effort: String,
    #[serde(default, deserialize_with = "go_string")]
    pub launch: String,
}

/// yaml.v3 leaves a field unset for `null`; any other scalar's raw text is
/// the string value.
fn go_string<'de, D: Deserializer<'de>>(d: D) -> std::result::Result<String, D::Error> {
    Ok(Option::<String>::deserialize(d)?.unwrap_or_default())
}

fn go_int<'de, D: Deserializer<'de>>(d: D) -> std::result::Result<i64, D::Error> {
    Ok(Option::<i64>::deserialize(d)?.unwrap_or_default())
}

fn go_list<'de, D: Deserializer<'de>, T: Deserialize<'de>>(
    d: D,
) -> std::result::Result<Vec<T>, D::Error> {
    Ok(Option::<Vec<T>>::deserialize(d)?.unwrap_or_default())
}

/// A schema loaded from disk (Go `projectschema.Loaded`).
#[derive(Debug, Clone, PartialEq, Eq, Default)]
pub struct Loaded {
    pub schema: Schema,
    pub project_dir: String,
    pub path: String,
    pub content: Vec<u8>,
    pub folders: BTreeMap<String, String>,
}

/// `^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`.
fn valid_agent_name(name: &str) -> bool {
    let bytes = name.as_bytes();
    !bytes.is_empty()
        && bytes.len() <= 64
        && bytes[0].is_ascii_alphanumeric()
        && bytes[1..]
            .iter()
            .all(|b| b.is_ascii_alphanumeric() || matches!(b, b'_' | b'.' | b'-'))
}

/// `<project>/.blueprint/schema.yaml`.
pub fn path(project_dir: &str) -> String {
    gopath::join(&[project_dir, DIRECTORY, FILENAME])
}

fn eval_symlinks(path: &str) -> std::io::Result<String> {
    Ok(std::fs::canonicalize(path)?.to_string_lossy().into_owned())
}

/// Go `projectschema.Load`: the directory containing `.blueprint` is the
/// portable project root. Every agent folder must already exist so symlink
/// resolution can prove it stays inside that root.
pub fn load(project_dir: &str) -> Result<Loaded> {
    let absolute = gopath::abs(project_dir).map_err(|e| Error(e.to_string()))?;
    let project_dir =
        eval_symlinks(&absolute).map_err(|e| Error(format!("resolve project directory: {e}")))?;
    let path = path(&project_dir);
    let data = std::fs::read(&path).map_err(|e| Error(format!("read {path}: {e}")))?;
    let schema = decode(&data).map_err(|e| Error(format!("parse {path}: {e}")))?;
    let folders = resolve_folders(&project_dir, &schema)
        .map_err(|e| Error(format!("validate {path}: {e}")))?;
    Ok(Loaded {
        schema,
        project_dir,
        path,
        content: data,
        folders,
    })
}

/// Go `projectschema.Decode`: strict YAML (unknown keys, duplicate keys and
/// extra documents rejected) followed by [`validate`].
pub fn decode(data: &[u8]) -> Result<Schema> {
    let text = std::str::from_utf8(data).map_err(|e| Error(format!("yaml: invalid UTF-8: {e}")))?;
    let schema: Schema = serde_norway::from_str(text).map_err(|e| {
        let message = e.to_string();
        if message.contains("more than one document") {
            Error("expected a single YAML document".into())
        } else {
            Error(format!("yaml: {message}"))
        }
    })?;
    validate(&schema)?;
    Ok(schema)
}

fn has_control(value: &str) -> bool {
    value.chars().any(char::is_control)
}


/// Go `projectschema.Validate`.
pub fn validate(schema: &Schema) -> Result<()> {
    if schema.version != 1 {
        return err("version must be 1");
    }
    match schema.history.as_str() {
        "none" | "file" => {
            if !schema.remote.is_empty() {
                return err("remote is only valid when history is remote");
            }
        }
        "remote" => {
            if crate::text::go_trim_space(&schema.remote).is_empty() {
                return err("remote is required when history is remote");
            }
            if has_control(&schema.remote) {
                return err("remote contains control characters");
            }
        }
        _ => return err("history must be none, file, or remote"),
    }
    if schema.agents.is_empty() {
        return err("agents must not be empty");
    }
    let mut by_name: HashMap<&str, &Agent> = HashMap::with_capacity(schema.agents.len());
    for (index, agent) in schema.agents.iter().enumerate() {
        let label = format!("agents[{index}]");
        if !valid_agent_name(&agent.name) {
            return err(format!("{label}.name is invalid"));
        }
        if by_name.contains_key(agent.name.as_str()) {
            return err(format!("duplicate agent name {}", go_quote(&agent.name)));
        }
        if agent.folder.is_empty() || gopath::is_abs(&agent.folder) || has_control(&agent.folder) {
            return err(format!("{label}.folder must be a relative path"));
        }
        let clean = gopath::clean(&agent.folder);
        if clean == ".." || clean.starts_with("../") {
            return err(format!("{label}.folder escapes the project root"));
        }
        match agent.runtime.as_str() {
            "claude" | "codex" | "hermes" | "opencode" => {}
            _ => {
                return err(format!(
                    "{label}.runtime must be claude, codex, hermes, or opencode"
                ));
            }
        }
        if schema.history != "none" && agent.runtime != "claude" && agent.runtime != "codex" {
            return err(format!(
                "{label} runtime {} is not supported with history {}; use history none",
                agent.runtime, schema.history
            ));
        }
        if crate::text::go_trim_space(&agent.role).is_empty() || has_control(&agent.role) {
            return err(format!(
                "{label}.role must be non-empty text without control characters"
            ));
        }
        if has_control(&agent.model) || has_control(&agent.effort) || has_control(&agent.launch) {
            return err(format!(
                "{label} launch settings contain control characters"
            ));
        }
        if agent.color.is_empty() {
            return err(format!("{label}.color is required"));
        }
        if agent.color != "auto"
            && let Err(e) = crate::config::color_index(&agent.color)
        {
            return err(format!("{label}.color: {e}"));
        }
        if !agent.launch.is_empty() && agent.launch != "default" && agent.launch != "no-sandbox" {
            return err(format!("{label}.launch must be default or no-sandbox"));
        }
        if agent.launch == "no-sandbox" && agent.runtime != "codex" {
            return err(format!(
                "{label}.launch no-sandbox requires the codex runtime"
            ));
        }
        if !agent.effort.is_empty() && agent.runtime != "claude" && agent.runtime != "codex" {
            return err(format!(
                "{label}.effort is not supported by the {} runtime",
                agent.runtime
            ));
        }
        by_name.insert(&agent.name, agent);
    }
    for agent in &schema.agents {
        if !agent.parent.is_empty() {
            if agent.parent == agent.name {
                return err(format!(
                    "agent {} cannot be its own parent",
                    go_quote(&agent.name)
                ));
            }
            if !by_name.contains_key(agent.parent.as_str()) {
                return err(format!(
                    "agent {} has unknown parent {}",
                    go_quote(&agent.name),
                    go_quote(&agent.parent)
                ));
            }
        }
        let mut seen: HashSet<&str> = HashSet::from([agent.name.as_str()]);
        let mut parent = agent.parent.as_str();
        while !parent.is_empty() {
            if !seen.insert(parent) {
                return err(format!("parent cycle includes {}", go_quote(parent)));
            }
            parent = by_name.get(parent).map(|a| a.parent.as_str()).unwrap_or("");
        }
    }
    Ok(())
}

/// Go `projectschema.ResolveFolders`: agent name → resolved folder, each
/// proven to stay inside the (symlink-resolved) project root.
pub fn resolve_folders(project_dir: &str, schema: &Schema) -> Result<BTreeMap<String, String>> {
    let root = eval_symlinks(project_dir).map_err(|e| Error(e.to_string()))?;
    let root = gopath::clean(&root);
    let mut result = BTreeMap::new();
    for agent in &schema.agents {
        let candidate = gopath::join2(&root, &gopath::clean(&agent.folder));
        let resolved = eval_symlinks(&candidate)
            .map_err(|e| Error(format!("agent {} folder: {e}", go_quote(&agent.name))))?;
        if !std::fs::metadata(&resolved).is_ok_and(|m| m.is_dir()) {
            return err(format!(
                "agent {} folder is not a directory",
                go_quote(&agent.name)
            ));
        }
        let escapes = match gopath::rel(&root, &resolved) {
            Ok(rel) => rel == ".." || rel.starts_with("../") || gopath::is_abs(&rel),
            Err(_) => true,
        };
        if escapes {
            return err(format!(
                "agent {} folder escapes the project root through a symlink",
                go_quote(&agent.name)
            ));
        }
        result.insert(agent.name.clone(), resolved);
    }
    Ok(result)
}

/// Go `projectschema.OrderedAgents`: parents before children; agents that
/// become ready in the same round are sorted by name.
pub fn ordered_agents(schema: &Schema) -> Vec<Agent> {
    let mut by_name: BTreeMap<&str, &Agent> = BTreeMap::new();
    for agent in &schema.agents {
        by_name.insert(&agent.name, agent);
    }
    let mut result = Vec::new();
    let mut done: HashSet<&str> = HashSet::new();
    while result.len() < schema.agents.len() {
        let ready: Vec<&str> = by_name
            .iter()
            .filter(|(name, agent)| {
                !done.contains(*name)
                    && (agent.parent.is_empty() || done.contains(agent.parent.as_str()))
            })
            .map(|(name, _)| *name)
            .collect();
        if ready.is_empty() {
            // Go loops forever on cycles or duplicate names; stop instead.
            break;
        }
        for name in ready {
            result.push(by_name[name].clone());
            done.insert(name);
        }
    }
    result
}

/// Go `projectschema.Encode`: yaml.v3 output with indent 2.
pub fn encode(schema: &Schema) -> Vec<u8> {
    let mut out = String::new();
    out.push_str(&format!("version: {}\n", schema.version));
    out.push_str(&format!("history: {}\n", yaml_scalar(&schema.history)));
    if !schema.remote.is_empty() {
        out.push_str(&format!("remote: {}\n", yaml_scalar(&schema.remote)));
    }
    if schema.agents.is_empty() {
        out.push_str("agents: []\n");
        return out.into_bytes();
    }
    out.push_str("agents:\n");
    for agent in &schema.agents {
        let fields: [(&str, &str, bool); 9] = [
            ("name", &agent.name, false),
            ("folder", &agent.folder, false),
            ("parent", &agent.parent, true),
            ("runtime", &agent.runtime, false),
            ("role", &agent.role, false),
            ("color", &agent.color, false),
            ("model", &agent.model, true),
            ("effort", &agent.effort, true),
            ("launch", &agent.launch, true),
        ];
        let mut first = true;
        for (key, value, omitempty) in fields {
            if omitempty && value.is_empty() {
                continue;
            }
            out.push_str(if first { "  - " } else { "    " });
            first = false;
            out.push_str(key);
            out.push_str(": ");
            out.push_str(&yaml_scalar(value));
            out.push('\n');
        }
    }
    out.into_bytes()
}

// ---- yaml.v3 string scalar style (encode.go stringv + emitterc.go) ----

fn take_while(s: &str, f: impl Fn(u8) -> bool) -> usize {
    s.bytes().take_while(|b| f(*b)).count()
}

/// `^[-+]?(\.[0-9]+|[0-9]+(\.[0-9]*)?)([eE][-+]?[0-9]+)?$`.
fn yaml_style_float(s: &str) -> bool {
    let mut rest = s.strip_prefix(['+', '-']).unwrap_or(s);
    if let Some(frac) = rest.strip_prefix('.') {
        let n = take_while(frac, |b| b.is_ascii_digit());
        if n == 0 {
            return false;
        }
        rest = &frac[n..];
    } else {
        let n = take_while(rest, |b| b.is_ascii_digit());
        if n == 0 {
            return false;
        }
        rest = &rest[n..];
        if let Some(frac) = rest.strip_prefix('.') {
            rest = &frac[take_while(frac, |b| b.is_ascii_digit())..];
        }
    }
    if let Some(exp) = rest.strip_prefix(['e', 'E']) {
        let exp = exp.strip_prefix(['+', '-']).unwrap_or(exp);
        let n = take_while(exp, |b| b.is_ascii_digit());
        return n > 0 && n == exp.len();
    }
    rest.is_empty()
}

/// `^[-+]?[0-9][0-9_]*(?::[0-5]?[0-9])+(?:\.[0-9_]*)?$`.
pub(crate) fn base60_float(s: &str) -> bool {
    let rest = s.strip_prefix(['+', '-']).unwrap_or(s);
    if !rest.starts_with(|c: char| c.is_ascii_digit()) {
        return false;
    }
    let mut rest = &rest[take_while(rest, |b| b.is_ascii_digit() || b == b'_')..];
    let mut groups = 0;
    while let Some(after) = rest.strip_prefix(':') {
        let n = take_while(after, |b| b.is_ascii_digit());
        let ok = match n {
            1 => true,
            2 => after.as_bytes()[0] <= b'5',
            _ => false,
        };
        if !ok {
            return false;
        }
        rest = &after[n..];
        groups += 1;
    }
    if groups == 0 {
        return false;
    }
    if let Some(frac) = rest.strip_prefix('.') {
        rest = &frac[take_while(frac, |b| b.is_ascii_digit() || b == b'_')..];
    }
    rest.is_empty()
}

/// Go `strconv.ParseInt(s, 0, 64)`/`ParseUint` acceptance (base prefixes,
/// `_` already stripped by the caller).
fn go_parses_int(s: &str) -> bool {
    let unsigned = s.strip_prefix(['+', '-']).unwrap_or(s);
    let (digits, radix) = if let Some(r) = unsigned
        .strip_prefix("0x")
        .or_else(|| unsigned.strip_prefix("0X"))
    {
        (r, 16)
    } else if let Some(r) = unsigned
        .strip_prefix("0b")
        .or_else(|| unsigned.strip_prefix("0B"))
    {
        (r, 2)
    } else if let Some(r) = unsigned
        .strip_prefix("0o")
        .or_else(|| unsigned.strip_prefix("0O"))
    {
        (r, 8)
    } else if unsigned.len() > 1 && unsigned.starts_with('0') {
        (&unsigned[1..], 8)
    } else {
        (unsigned, 10)
    };
    if digits.is_empty() {
        return false;
    }
    let Ok(magnitude) = u64::from_str_radix(digits, radix) else {
        return false;
    };
    match s.as_bytes()[0] {
        b'-' => magnitude <= 1 << 63,
        _ => true,
    }
}

fn is_timestamp(s: &str) -> bool {
    let year = s.bytes().take_while(u8::is_ascii_digit).count();
    if year != 4 || s.as_bytes().get(4) != Some(&b'-') {
        return false;
    }
    // Date part "2006-1-2" (one- or two-digit month/day), optionally
    // followed by a time; any such prefix resolves to !!timestamp.
    let rest = &s[5..];
    let mut parts = rest.splitn(2, '-');
    let month = parts.next().unwrap_or("");
    let Some(tail) = parts.next() else {
        return false;
    };
    let day_len = tail.bytes().take_while(u8::is_ascii_digit).count();
    let valid_num = |t: &str| (1..=2).contains(&t.len()) && t.bytes().all(|b| b.is_ascii_digit());
    if !valid_num(month) || !(1..=2).contains(&day_len) {
        return false;
    }
    let after = &tail[day_len..];
    // Only the date-only form is checked exactly; time forms are rare in
    // schema strings and also quoted by yaml.v3.
    after.is_empty() || after.starts_with(['T', 't', ' '])
}

/// Whether yaml.v3's `resolve("", s)` yields a tag other than `!!str`.
pub(crate) fn resolves_non_string(s: &str) -> bool {
    if matches!(
        s,
        "true"
            | "True"
            | "TRUE"
            | "false"
            | "False"
            | "FALSE"
            | ""
            | "~"
            | "null"
            | "Null"
            | "NULL"
            | ".nan"
            | ".NaN"
            | ".NAN"
            | ".inf"
            | ".Inf"
            | ".INF"
            | "+.inf"
            | "+.Inf"
            | "+.INF"
            | "-.inf"
            | "-.Inf"
            | "-.INF"
            | "<<"
    ) {
        return true;
    }
    match s.as_bytes()[0] {
        b'.' => s.parse::<f64>().is_ok() && !s.contains(['i', 'I', 'n', 'N']),
        b'+' | b'-' | b'0'..=b'9' => {
            if is_timestamp(s) {
                return true;
            }
            let plain = s.replace('_', "");
            go_parses_int(&plain) || (yaml_style_float(&plain) && plain.parse::<f64>().is_ok())
        }
        _ => false,
    }
}

pub(crate) fn is_old_bool(s: &str) -> bool {
    matches!(
        s,
        "y" | "Y"
            | "yes"
            | "Yes"
            | "YES"
            | "on"
            | "On"
            | "ON"
            | "n"
            | "N"
            | "no"
            | "No"
            | "NO"
            | "off"
            | "Off"
            | "OFF"
    )
}

fn yaml_printable(c: char) -> bool {
    matches!(c, '\n' | '\u{20}'..='\u{7e}' | '\u{85}' | '\u{a0}'..='\u{d7ff}' | '\u{e000}'..='\u{fffd}' | '\u{10000}'..)
        && c != '\u{feff}'
}

fn yaml_break(c: char) -> bool {
    matches!(c, '\r' | '\n' | '\u{85}' | '\u{2028}' | '\u{2029}')
}

/// libyaml `analyze_scalar` for block context: (block plain allowed,
/// single quoted allowed).
pub(crate) fn analyze(value: &str) -> (bool, bool) {
    let chars: Vec<char> = value.chars().collect();
    let mut block_indicators = value.starts_with("---") || value.starts_with("...");
    let (mut line_breaks, mut special, mut tabs) = (false, false, false);
    let (mut leading_space, mut leading_break, mut trailing_space, mut trailing_break) =
        (false, false, false, false);
    let (mut break_space, mut space_break) = (false, false);
    let (mut previous_space, mut previous_break) = (false, false);
    let mut preceded_by_whitespace = true;
    for (i, &c) in chars.iter().enumerate() {
        let next = chars.get(i + 1).copied();
        let followed_by_whitespace = matches!(next, None | Some(' ') | Some('\t'));
        if i == 0 {
            match c {
                '#' | ',' | '[' | ']' | '{' | '}' | '&' | '*' | '!' | '|' | '>' | '\'' | '"'
                | '%' | '@' | '`' => block_indicators = true,
                '?' | ':' | '-' if followed_by_whitespace => block_indicators = true,
                _ => {}
            }
        } else {
            match c {
                ':' if followed_by_whitespace => block_indicators = true,
                '#' if preceded_by_whitespace => block_indicators = true,
                _ => {}
            }
        }
        if c == '\t' {
            tabs = true;
        } else if !yaml_printable(c) {
            special = true;
        }
        let last = i + 1 == chars.len();
        if c == ' ' {
            leading_space |= i == 0;
            trailing_space |= last;
            break_space |= previous_break;
            previous_space = true;
            previous_break = false;
        } else if yaml_break(c) {
            line_breaks = true;
            leading_break |= i == 0;
            trailing_break |= last;
            space_break |= previous_space;
            previous_space = false;
            previous_break = true;
        } else {
            previous_space = false;
            previous_break = false;
        }
        preceded_by_whitespace = matches!(c, ' ' | '\t' | '\0') || yaml_break(c);
    }
    let mut plain = true;
    let mut single = true;
    if leading_space || leading_break || trailing_space || trailing_break {
        plain = false;
    }
    if break_space || space_break || tabs || special {
        plain = false;
        single = false;
    }
    if line_breaks || block_indicators {
        plain = false;
    }
    (plain, single)
}

pub(crate) fn double_quoted(value: &str) -> String {
    let mut out = String::from("\"");
    for c in value.chars() {
        if yaml_printable(c) && !yaml_break(c) && c != '"' && c != '\\' && c != '\u{feff}' {
            out.push(c);
            continue;
        }
        match c {
            '\0' => out.push_str("\\0"),
            '\u{7}' => out.push_str("\\a"),
            '\u{8}' => out.push_str("\\b"),
            '\t' => out.push_str("\\t"),
            '\n' => out.push_str("\\n"),
            '\u{b}' => out.push_str("\\v"),
            '\u{c}' => out.push_str("\\f"),
            '\r' => out.push_str("\\r"),
            '\u{1b}' => out.push_str("\\e"),
            '"' => out.push_str("\\\""),
            '\\' => out.push_str("\\\\"),
            '\u{85}' => out.push_str("\\N"),
            '\u{a0}' => out.push_str("\\_"),
            '\u{2028}' => out.push_str("\\L"),
            '\u{2029}' => out.push_str("\\P"),
            c if (c as u32) <= 0xff => out.push_str(&format!("\\x{:02X}", c as u32)),
            c if (c as u32) <= 0xffff => out.push_str(&format!("\\u{:04X}", c as u32)),
            c => out.push_str(&format!("\\U{:08X}", c as u32)),
        }
    }
    out.push('"');
    out
}

/// A string as yaml.v3 emits it in block context: plain when it is
/// unambiguous, else single-quoted, else double-quoted. Multi-line strings
/// are double-quoted here (yaml.v3 would use a literal block).
pub(crate) fn yaml_scalar(value: &str) -> String {
    let can_use_plain = !resolves_non_string(value) && !base60_float(value) && !is_old_bool(value);
    if !can_use_plain || value.contains('\n') {
        return double_quoted(value);
    }
    let (plain, single) = analyze(value);
    if plain {
        value.to_string()
    } else if single {
        format!("'{}'", value.replace('\'', "''"))
    } else {
        double_quoted(value)
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    const VALID_SCHEMA_YAML: &str = "version: 1
history: none
agents:
  - name: lead
    folder: .
    runtime: claude
    role: lead
    color: blue
  - name: worker
    folder: sub
    parent: lead
    runtime: codex
    role: worker
    color: purple
    model: gpt-example
    effort: high
    launch: no-sandbox
";

    #[test]
    fn decode_requires_known_fields_and_supported_values() {
        decode(VALID_SCHEMA_YAML.as_bytes()).expect("valid schema");
        for input in [
            VALID_SCHEMA_YAML.replacen("history: none", "history: someday", 1),
            VALID_SCHEMA_YAML.replacen("folder: .", "folder: ../outside", 1),
            VALID_SCHEMA_YAML.replacen("    role: lead", "    role: lead\n    unknown: value", 1),
            VALID_SCHEMA_YAML.replacen("history: none", "history: none\nunknown: value", 1),
            VALID_SCHEMA_YAML.replacen("version: 1", "version: 2", 1),
            VALID_SCHEMA_YAML.replacen("    role: lead", "    role: lead\n    role: again", 1),
            format!("{VALID_SCHEMA_YAML}---\nversion: 1\n"),
        ] {
            assert!(
                decode(input.as_bytes()).is_err(),
                "invalid schema accepted:\n{input}"
            );
        }
        let e = decode(format!("{VALID_SCHEMA_YAML}---\nversion: 1\n").as_bytes()).unwrap_err();
        assert_eq!(e.0, "expected a single YAML document");
    }

    #[test]
    fn decode_scalar_strings_like_yaml_v3() {
        let input = VALID_SCHEMA_YAML
            .replacen("color: blue", "color: 33", 1)
            .replacen("model: gpt-example", "model: ~", 1);
        let schema = decode(input.as_bytes()).unwrap();
        assert_eq!(schema.agents[0].color, "33");
        assert_eq!(schema.agents[1].model, "");
    }

    #[test]
    fn decode_rejects_history_for_unsupported_runtimes() {
        let input = VALID_SCHEMA_YAML
            .replacen("history: none", "history: file", 1)
            .replacen("runtime: codex", "runtime: opencode", 1);
        let e = decode(input.as_bytes()).unwrap_err();
        assert!(e.0.contains("not supported with history file"), "{e}");
    }

    #[test]
    fn validate_rejects_cycles_and_control_characters() {
        let mut schema = decode(VALID_SCHEMA_YAML.as_bytes()).unwrap();
        schema.agents[0].parent = "worker".into();
        let e = validate(&schema).unwrap_err();
        assert!(e.0.contains("cycle"), "{e}");
        let mut schema = decode(VALID_SCHEMA_YAML.as_bytes()).unwrap();
        schema.agents[0].role = "lead\nforged plan".into();
        assert!(
            validate(&schema).is_err(),
            "control character in role was accepted"
        );
        let mut schema = decode(VALID_SCHEMA_YAML.as_bytes()).unwrap();
        schema.agents[0].color = "999".into();
        assert_eq!(
            validate(&schema).unwrap_err().0,
            "agents[0].color: invalid color \"999\": use a named color or 0–255"
        );
    }

    #[test]
    fn load_rejects_folder_symlink_outside_project() {
        let parent = tempfile::tempdir().unwrap();
        let project = parent.path().join("project");
        let outside = parent.path().join("outside");
        std::fs::create_dir_all(project.join(DIRECTORY)).unwrap();
        std::fs::create_dir_all(&outside).unwrap();
        std::os::unix::fs::symlink(&outside, project.join("linked")).unwrap();
        let data = VALID_SCHEMA_YAML.replacen("folder: sub", "folder: linked", 1);
        let project = project.to_string_lossy().into_owned();
        std::fs::write(path(&project), data).unwrap();
        let e = load(&project).unwrap_err();
        assert!(e.0.contains("escapes the project root"), "{e}");
    }

    #[test]
    fn load_resolves_folders() {
        let parent = tempfile::tempdir().unwrap();
        let project = parent.path().join("project");
        std::fs::create_dir_all(project.join(DIRECTORY)).unwrap();
        std::fs::create_dir_all(project.join("sub")).unwrap();
        let project = project.to_string_lossy().into_owned();
        std::fs::write(path(&project), VALID_SCHEMA_YAML).unwrap();
        let loaded = load(&project).unwrap();
        assert_eq!(
            loaded.folders["worker"],
            gopath::join2(&loaded.project_dir, "sub")
        );
        assert_eq!(loaded.folders["lead"], loaded.project_dir);
        assert_eq!(loaded.content, VALID_SCHEMA_YAML.as_bytes());
    }

    #[test]
    fn ordered_agents_places_parents_first_deterministically() {
        let schema = decode(
            b"version: 1
history: none
agents:
  - name: z-child
    folder: .
    parent: lead
    runtime: claude
    role: child
    color: blue
  - name: lead
    folder: .
    runtime: claude
    role: lead
    color: blue
  - name: alpha
    folder: .
    runtime: claude
    role: helper
    color: blue
",
        )
        .unwrap();
        let names: Vec<_> = ordered_agents(&schema)
            .into_iter()
            .map(|a| a.name)
            .collect();
        assert_eq!(names, ["alpha", "lead", "z-child"]);
    }

    #[test]
    fn encode_round_trips_like_yaml_v3() {
        let schema = decode(VALID_SCHEMA_YAML.as_bytes()).unwrap();
        assert_eq!(
            String::from_utf8(encode(&schema)).unwrap(),
            VALID_SCHEMA_YAML
        );
        assert_eq!(
            String::from_utf8(encode(&Schema::default())).unwrap(),
            "version: 0\nhistory: \"\"\nagents: []\n"
        );
    }

    #[test]
    fn yaml_scalar_quoting() {
        for (input, want) in [
            ("plain", "plain"),
            ("", "\"\""),
            ("true", "\"true\""),
            ("yes", "\"yes\""),
            ("60001", "\"60001\""),
            ("1_000", "\"1_000\""),
            ("0x1F", "\"0x1F\""),
            ("1.5", "\"1.5\""),
            ("1e3", "\"1e3\""),
            (".5", "\".5\""),
            ("2001-01-02", "\"2001-01-02\""),
            ("1:20", "\"1:20\""),
            ("60000:61000", "60000:61000"),
            ("~", "\"~\""),
            ("null", "\"null\""),
            ("/x # y", "'/x # y'"),
            ("/keys/it's: here", "'/keys/it''s: here'"),
            ("~/.ssh/id", "~/.ssh/id"),
            ("sudo -u null", "sudo -u null"),
            ("- a", "'- a'"),
            ("-a", "-a"),
            (" lead", "' lead'"),
            ("a\tb", "\"a\\tb\""),
            ("a\nb", "\"a\\nb\""),
            ("ünï", "ünï"),
            ("x\u{1b}", "\"x\\e\""),
        ] {
            assert_eq!(yaml_scalar(input), want, "{input:?}");
        }
    }
}
