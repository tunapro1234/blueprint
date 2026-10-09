//! Port of internal/cache/cache.go: Claude transcript metrics and the shared
//! [`State`] the status, bar and delivery code read.
//!
//! Ported from Go revision b877eb4.

mod activity;
mod claude_tail;
mod codex;
mod codex_decode;
pub mod glob;
mod jsonrow;
mod local;

#[cfg(test)]
mod tests;

pub use activity::Activity;
pub use codex::{
    codex_id, codex_path, read_codex, read_codex_path, read_codex_path_at, read_codex_session,
    rollout_path, rollout_paths_by_id, scan_codex_reverse,
};
pub use codex_decode::{
    CodexInfo, CodexPayload, CodexRecord, CodexTokenUsage, decode_codex_record,
};
pub use jsonrow::{
    definitely_other_compact_record_type, has_compact_record_type_prefix, may_have_record_type,
};
pub(crate) use local::go_path_error;
pub use local::{LocalBinding, LocalObservation, read_local_observation};

use std::collections::{BTreeMap, HashMap};
use std::fs::File;
use std::io;
use std::os::unix::fs::FileExt;
use std::path::Path;

use chrono::TimeDelta;

use crate::go_struct;
use crate::godecode::{RawJson, unmarshal_ok};
use crate::gotime::{self, Duration, Time};

/// The window bp normally reads from the end of a session file: enough for the
/// recent records, cheap enough to run for every agent on every status refresh.
pub(crate) const TAIL_SIZE: u64 = 500 * 1024;

/// Bounds the retry for the case [`TAIL_SIZE`] cannot cover: ONE record longer
/// than the window. Beyond 16 MiB a single record is genuinely unreadable here
/// and the state stays unknown.
pub(crate) const MAX_TAIL_SIZE: u64 = 16 * 1024 * 1024;

/// `ReadAt` into `buf`: returns the bytes read and whether an error (including
/// a short read at EOF) occurred, like Go's `(n, err)`.
pub(crate) fn read_at(file: &File, buf: &mut [u8], offset: u64) -> (usize, bool) {
    let mut done = 0;
    while done < buf.len() {
        match file.read_at(&mut buf[done..], offset + done as u64) {
            Ok(0) => return (done, true),
            Ok(n) => done += n,
            Err(err) if err.kind() == io::ErrorKind::Interrupted => {}
            Err(_) => return (done, true),
        }
    }
    (done, false)
}

/// `len(bytes.TrimSpace(b)) == 0`.
pub(crate) fn is_blank(b: &[u8]) -> bool {
    match std::str::from_utf8(b) {
        Ok(s) => s.trim().is_empty(),
        // An invalid byte is never space, so the slice is not blank.
        Err(_) => false,
    }
}

/// Returns the complete records at the end of a session file: the tail window
/// with its leading partial record removed, widened once to [`MAX_TAIL_SIZE`]
/// when that window held no complete record. `None` means the file could not
/// be read at all.
pub(crate) fn read_tail(file: &File, size: u64, tail_size: u64) -> Option<Vec<u8>> {
    for window in [tail_size, MAX_TAIL_SIZE] {
        let start = size.saturating_sub(window);
        let mut data = vec![0u8; (size - start) as usize];
        let (n, failed) = read_at(file, &mut data, start);
        if failed && n == 0 {
            return None;
        }
        data.truncate(n);
        if start == 0 {
            return Some(data);
        }
        match data.iter().position(|&c| c == b'\n') {
            Some(newline) => {
                data.drain(..=newline);
            }
            None => data.clear(),
        }
        if !is_blank(&data) {
            return Some(data);
        }
    }
    None
}

/// `^\[\d+ (?:accumulated announcements|birikmis duyuru)`: persisted
/// transcripts from older bp versions use the Turkish digest prefix.
fn digest_prefix(text: &str) -> bool {
    let Some(rest) = text.strip_prefix('[') else {
        return false;
    };
    let digits = rest.bytes().take_while(u8::is_ascii_digit).count();
    if digits == 0 {
        return false;
    }
    let rest = &rest[digits..];
    rest.starts_with(" accumulated announcements") || rest.starts_with(" birikmis duyuru")
}

/// What bp knows about one agent's session.
#[derive(Debug, Clone, PartialEq)]
pub struct State {
    pub activity: Option<Box<Activity>>,
    pub usage_at: Time,
    pub turn_at: Time,
    pub turn_known: bool,
    pub age: Duration,
    /// Known only from the latest request's explicit write breakdown.
    pub cache_ttl: Duration,
    /// Starts at the first observed message chunk, not its final chunk.
    pub cache_age: Duration,
    pub ctx_tokens: i64,
    pub last_human_age: Duration,
    pub known: bool,
    /// What the session actually ran last, read from the assistant records.
    pub model: String,
    pub effort: String,
    pub service_tier: String,
    pub thread_id: String,
    pub path: String,
    pub busy: bool,
    pub runtime: String,
    pub runtime_error: String,
    /// The model's context window when the session reports one (codex does,
    /// claude does not). Zero means unknown.
    pub window: i64,
}

impl Default for State {
    /// Go's zero `State{}` (note: `last_human_age` is 0, not -1).
    fn default() -> Self {
        State {
            activity: None,
            usage_at: gotime::zero(),
            turn_at: gotime::zero(),
            turn_known: false,
            age: TimeDelta::zero(),
            cache_ttl: TimeDelta::zero(),
            cache_age: TimeDelta::zero(),
            ctx_tokens: 0,
            last_human_age: TimeDelta::zero(),
            known: false,
            model: String::new(),
            effort: String::new(),
            service_tier: String::new(),
            thread_id: String::new(),
            path: String::new(),
            busy: false,
            runtime: String::new(),
            runtime_error: String::new(),
            window: 0,
        }
    }
}

impl State {
    /// `State{LastHumanAge: -1}`, the "nothing known" answer.
    pub fn unknown() -> State {
        State {
            last_human_age: gotime::minus_one(),
            ..State::default()
        }
    }

    /// Labels estimates explicitly. A usage timestamp alone is not a TTL.
    pub fn cache_hint(&self) -> (&'static str, Duration) {
        if !self.known || self.cache_ttl <= TimeDelta::zero() {
            return ("age", self.age);
        }
        if self.cache_age < self.cache_ttl {
            return ("warm~", self.cache_age);
        }
        ("cold~", self.cache_age)
    }
}

/// The one place a caller's folder string becomes a path. An agentbook folder
/// may carry an annotation (`/srv (home: /srv/server-main)`); only a SECOND
/// field makes the first one a prefix.
pub fn folder_path(folder: &str) -> &str {
    let mut fields = folder
        .split(|c: char| c.is_whitespace())
        .filter(|f| !f.is_empty());
    if let (Some(first), Some(_)) = (fields.next(), fields.next())
        && first.starts_with('/')
    {
        return first;
    }
    folder
}

/// Resolves the Claude session file for (projects root, folder, agent):
/// `tmux.ResumeSessionPath` in Go. Injected because bp-tmux owns it.
pub type SessionResolver<'a> = &'a dyn Fn(&str, &str, &str) -> Option<String>;

/// `cache.Read`: the newest session titled `agent` in `folder`.
pub fn read(resolve: SessionResolver<'_>, projects_root: &str, folder: &str, agent: &str) -> State {
    match resolve(projects_root, folder_path(folder), agent) {
        Some(path) => read_claude_path(&path),
        None => State::unknown(),
    }
}

/// `cache.Fleet`.
pub fn fleet(
    resolve: SessionResolver<'_>,
    projects_root: &str,
    folders: &BTreeMap<String, String>,
) -> HashMap<String, State> {
    folders
        .iter()
        .map(|(agent, folder)| (agent.clone(), read(resolve, projects_root, folder, agent)))
        .collect()
}

/// Reads metrics only from the caller's resolved session.
pub fn read_claude_path(path: &str) -> State {
    read_claude_path_at(path, &gotime::now())
}

/// [`read_claude_path`] with an injected clock.
pub fn read_claude_path_at(path: &str, now: &Time) -> State {
    let Ok(file) = File::open(path) else {
        return State::unknown();
    };
    let Ok(info) = file.metadata() else {
        return State::unknown();
    };
    match claude_tail::cached_claude_tail(path, &file, &info, TAIL_SIZE) {
        Some(metrics) => metrics.state(path, now),
        None => State::unknown(),
    }
}

/// One transcript record reduced to what the metrics use.
#[derive(Debug, Clone)]
pub(crate) struct ClaudeRow {
    /// Unparsable or sidechain rows still count as content.
    ignored: bool,
    boundary: bool,
    /// The compact boundary's reported size, -1 when absent.
    post_tokens: i64,
    stamp: Time,
    stamped: bool,
    id: String,
    model: String,
    effort: String,
    usage: bool,
    ctx: i64,
    ttl: Duration,
    human: bool,
}

#[derive(Default)]
struct ClaudeRecord {
    kind: String,
    subtype: String,
    is_compact_summary: bool,
    is_sidechain: bool,
    compact_metadata: Option<CompactMetadata>,
    timestamp: String,
    message: RawJson,
    role: String,
    content: RawJson,
    effort: RawJson,
}
go_struct!(ClaudeRecord {
    kind: "type",
    subtype: "subtype",
    is_compact_summary: "isCompactSummary",
    is_sidechain: "isSidechain",
    compact_metadata: "compactMetadata",
    timestamp: "timestamp",
    message: "message",
    role: "role",
    content: "content",
    effort: "effort",
});

#[derive(Default)]
struct CompactMetadata {
    post_tokens: Option<i64>,
}
go_struct!(CompactMetadata {
    post_tokens: "postTokens"
});

#[derive(Default)]
struct ClaudeMessage {
    id: String,
    role: String,
    model: String,
    content: RawJson,
    usage: Option<ClaudeUsage>,
}
go_struct!(ClaudeMessage {
    id: "id",
    role: "role",
    model: "model",
    content: "content",
    usage: "usage"
});

#[derive(Default)]
struct ClaudeUsage {
    input: i64,
    cache_read: i64,
    cache_creation: i64,
    creation: ClaudeCreation,
}
go_struct!(ClaudeUsage {
    input: "input_tokens",
    cache_read: "cache_read_input_tokens",
    cache_creation: "cache_creation_input_tokens",
    creation: "cache_creation",
});

#[derive(Default)]
struct ClaudeCreation {
    hour: i64,
    five: i64,
}
go_struct!(ClaudeCreation {
    hour: "ephemeral_1h_input_tokens",
    five: "ephemeral_5m_input_tokens"
});

pub(crate) fn decode_claude_row(line: &[u8]) -> ClaudeRow {
    let mut row = ClaudeRow {
        ignored: false,
        boundary: false,
        post_tokens: -1,
        stamp: gotime::zero(),
        stamped: false,
        id: String::new(),
        model: String::new(),
        effort: String::new(),
        usage: false,
        ctx: 0,
        ttl: TimeDelta::zero(),
        human: false,
    };
    let mut record = ClaudeRecord::default();
    if !unmarshal_ok(line, &mut record) || record.is_sidechain {
        row.ignored = true;
        return row;
    }
    if let Some(stamp) = gotime::parse_rfc3339(&record.timestamp) {
        row.stamp = stamp;
        row.stamped = true;
    }
    if record.kind == "system" && record.subtype == "compact_boundary" {
        row.boundary = true;
        if let Some(post) = record.compact_metadata.as_ref().and_then(|m| m.post_tokens)
            && post >= 0
        {
            row.post_tokens = post;
        }
        return row;
    }
    let mut message = ClaudeMessage::default();
    let _ = unmarshal_ok(record.message.as_bytes(), &mut message);
    row.id = message.id;
    // "<synthetic>" marks interrupt/error placeholders, not a real turn.
    if !message.model.is_empty() && message.model != "<synthetic>" {
        row.model = message.model;
        // Claude stores effort on the outer assistant record. Unknown shapes
        // don't discard otherwise valid token usage in the record.
        if record.kind == "assistant" || message.role == "assistant" {
            let _ = unmarshal_ok(record.effort.as_bytes(), &mut row.effort);
        }
    }
    if let Some(usage) = &message.usage {
        row.usage = true;
        row.ctx = usage
            .input
            .wrapping_add(usage.cache_read)
            .wrapping_add(usage.cache_creation);
        // Mixed/absent TTLs and read-only hits do not establish the lifetime
        // of the current prefix.
        let c = &usage.creation;
        if c.hour > 0 && c.five == 0 && c.hour == usage.cache_creation {
            row.ttl = TimeDelta::hours(1);
        } else if c.five > 0 && c.hour == 0 && c.five == usage.cache_creation {
            row.ttl = TimeDelta::minutes(5);
        }
    }
    if record.kind != "user" && record.role != "user" && message.role != "user" {
        return row;
    }
    let content = if message.content.is_empty() {
        &record.content
    } else {
        &message.content
    };
    let text = content_text(content.as_bytes());
    row.human =
        !record.is_compact_summary && !text.trim().is_empty() && !automatic(&text) && row.stamped;
    row
}

/// A folded window. Ages are left to the caller, so a cached fold stays valid
/// as time passes.
#[derive(Debug, Clone)]
pub(crate) struct ClaudeMetrics {
    usage_time: Time,
    human_time: Time,
    cache_time: Time,
    cache_ttl: Duration,
    ctx_tokens: i64,
    model: String,
    effort: String,
}

pub(crate) struct ClaudeFold {
    metrics: ClaudeMetrics,
    compact_time: Time,
    message_starts: HashMap<String, Time>,
}

impl ClaudeFold {
    pub(crate) fn new() -> ClaudeFold {
        ClaudeFold {
            metrics: ClaudeMetrics {
                usage_time: gotime::zero(),
                human_time: gotime::zero(),
                cache_time: gotime::zero(),
                cache_ttl: TimeDelta::zero(),
                ctx_tokens: 0,
                model: String::new(),
                effort: String::new(),
            },
            compact_time: gotime::zero(),
            message_starts: HashMap::new(),
        }
    }

    pub(crate) fn add(&mut self, row: &ClaudeRow) {
        if row.ignored {
            return;
        }
        let f = &mut self.metrics;
        if row.boundary {
            self.compact_time = row.stamp;
            f.usage_time = gotime::zero();
            f.ctx_tokens = 0;
            f.cache_time = gotime::zero();
            f.cache_ttl = TimeDelta::zero();
            if row.post_tokens >= 0 {
                f.usage_time = self.compact_time;
                f.ctx_tokens = row.post_tokens;
            }
            return;
        }
        if row.stamped && !row.id.is_empty() {
            match self.message_starts.get(&row.id) {
                Some(previous) if row.stamp >= *previous => {}
                _ => {
                    self.message_starts.insert(row.id.clone(), row.stamp);
                }
            }
        }
        if !row.model.is_empty() {
            f.model.clone_from(&row.model);
            f.effort.clone_from(&row.effort);
        }
        if row.usage
            && row.stamped
            && (gotime::is_zero(&self.compact_time) || row.stamp > self.compact_time)
        {
            f.usage_time = row.stamp;
            f.ctx_tokens = row.ctx;
            f.cache_ttl = row.ttl;
            f.cache_time = row.stamp;
            if let Some(start) = self.message_starts.get(&row.id) {
                f.cache_time = *start;
            }
        }
        if row.human {
            f.human_time = row.stamp;
        }
    }

    pub(crate) fn metrics(&self) -> ClaudeMetrics {
        self.metrics.clone()
    }
}

impl ClaudeMetrics {
    pub(crate) fn state(&self, path: &str, now: &Time) -> State {
        let base = Path::new(path)
            .file_name()
            .map(|n| n.to_string_lossy().into_owned())
            .unwrap_or_default();
        let mut result = State {
            ctx_tokens: self.ctx_tokens,
            model: self.model.clone(),
            effort: self.effort.clone(),
            path: path.to_owned(),
            thread_id: base.strip_suffix(".jsonl").unwrap_or(&base).to_owned(),
            usage_at: self.usage_time,
            ..State::unknown()
        };
        if self.cache_ttl > TimeDelta::zero() {
            result.cache_ttl = self.cache_ttl;
            result.cache_age = age(now, &self.cache_time);
        }
        if !gotime::is_zero(&self.usage_time) {
            result.known = true;
            result.age = age(now, &self.usage_time);
        }
        if !gotime::is_zero(&self.human_time) {
            result.last_human_age = age(now, &self.human_time);
        }
        result
    }
}

#[derive(Default)]
struct TextPart {
    text: String,
}
go_struct!(TextPart { text: "text" });

/// The text of a message `content`: a string, or the `text` of each part.
pub(crate) fn content_text(raw: &[u8]) -> String {
    let mut text = String::new();
    if unmarshal_ok(raw, &mut text) {
        return text;
    }
    let mut parts: Vec<TextPart> = Vec::new();
    if !unmarshal_ok(raw, &mut parts) {
        return String::new();
    }
    parts
        .iter()
        .map(|p| p.text.as_str())
        .collect::<Vec<_>>()
        .join(" ")
}

/// Whether a user message was produced by bp or a monitor, not a human.
pub(crate) fn automatic(text: &str) -> bool {
    let text = text.trim();
    if text.starts_with("[ANNOUNCE")
        || text.starts_with("health-watch:")
        || text.starts_with("[usage-policy]")
        || digest_prefix(text)
    {
        return true;
    }
    let first = text.split('\n').next().unwrap_or_default();
    // Older announcement producers used DUYURU; keep reading it alongside the English marker.
    text.starts_with("[ ") && (first.contains(" ANNOUNCEMENT (") || first.contains(" DUYURU ("))
}

/// `now.Sub(then)`, never negative.
pub(crate) fn age(now: &Time, then: &Time) -> Duration {
    let value = gotime::sub(now, then);
    if value < TimeDelta::zero() {
        TimeDelta::zero()
    } else {
        value
    }
}
