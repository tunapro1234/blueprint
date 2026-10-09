//! Ports of the internal/cache tests, one module per Go test file.

mod cache;
mod claude_tail;
mod codex;
mod codex_incremental;
mod compact;
mod effort;
mod jsonrow;
mod rollout_paths;
mod ttl;

use std::fs::{self, File, FileTimes};
use std::io::Write;
use std::path::Path;
use std::sync::{Mutex, MutexGuard};
use std::time::SystemTime;

use chrono::TimeDelta;
use serde_json::{Value, json};

use crate::gotime::{self, Time};

/// The Go tests run one at a time and some swap package-level caches; Rust
/// runs tests in parallel threads, so the tests here take this lock.
static SERIAL: Mutex<()> = Mutex::new(());

pub(super) fn serial() -> MutexGuard<'static, ()> {
    SERIAL
        .lock()
        .unwrap_or_else(|poisoned| poisoned.into_inner())
}

pub(super) fn rfc(t: &Time) -> String {
    gotime::format_rfc3339_nano(t)
}

pub(super) fn mins(n: i64) -> TimeDelta {
    TimeDelta::minutes(n)
}

pub(super) fn hours(n: i64) -> TimeDelta {
    TimeDelta::hours(n)
}

pub(super) fn user(timestamp: Time, text: &str) -> Value {
    json!({"type": "user", "timestamp": rfc(&timestamp), "message": {"role": "user", "content": text}})
}

pub(super) fn usage(timestamp: Time, read: i64, creation: i64) -> Value {
    json!({
        "type": "assistant",
        "timestamp": rfc(&timestamp),
        "message": {
            "role": "assistant",
            "model": "claude-opus-5",
            "usage": {"cache_read_input_tokens": read, "cache_creation_input_tokens": creation},
        },
    })
}

pub(super) fn synthetic(timestamp: Time) -> Value {
    json!({"type": "assistant", "timestamp": rfc(&timestamp), "message": {"role": "assistant", "model": "<synthetic>"}})
}

/// json.Encoder output: one compact value per line.
pub(super) fn write_jsonl(path: &Path, rows: &[Value]) {
    if let Some(parent) = path.parent() {
        fs::create_dir_all(parent).unwrap();
    }
    let mut file = File::create(path).unwrap();
    for row in rows {
        file.write_all(format!("{row}\n").as_bytes()).unwrap();
    }
}

pub(super) fn append_jsonl(path: &Path, rows: &[Value]) {
    let mut file = fs::OpenOptions::new().append(true).open(path).unwrap();
    for row in rows {
        file.write_all(format!("{row}\n").as_bytes()).unwrap();
    }
}

/// os.Chtimes.
pub(super) fn chtimes(path: &Path, at: SystemTime) {
    let file = File::open(path).unwrap();
    file.set_times(FileTimes::new().set_accessed(at).set_modified(at))
        .unwrap();
}

pub(super) fn system_time(t: &Time) -> SystemTime {
    SystemTime::UNIX_EPOCH
        + std::time::Duration::from_nanos(t.timestamp_nanos_opt().unwrap() as u64)
}

pub(super) fn path_str(path: &Path) -> String {
    path.to_string_lossy().into_owned()
}

/// A test stand-in for `tmux.ResumeSessionPath`: the newest `*.jsonl` in the
/// munged project directory whose last custom-title is `agent`.
pub(super) fn resolve(projects_root: &str, dir: &str, agent: &str) -> Option<String> {
    let munged: String = dir
        .chars()
        .map(|c| if c.is_ascii_alphanumeric() { c } else { '-' })
        .collect();
    let project = Path::new(projects_root).join(munged);
    let mut best: Option<(SystemTime, String)> = None;
    for entry in fs::read_dir(&project).ok()?.filter_map(Result::ok) {
        let name = entry.file_name().to_string_lossy().into_owned();
        if entry.file_type().ok()?.is_dir() || !name.ends_with(".jsonl") {
            continue;
        }
        let data = fs::read(entry.path()).ok()?;
        let title = data
            .split(|&c| c == b'\n')
            .filter_map(|line| serde_json::from_slice::<Value>(line).ok())
            .filter(|v| v["type"] == "custom-title")
            .filter_map(|v| v["customTitle"].as_str().map(str::to_owned))
            .next_back();
        if title.as_deref() != Some(agent) {
            continue;
        }
        let modified = entry.metadata().ok()?.modified().ok()?;
        if best.as_ref().is_none_or(|(at, _)| modified > *at) {
            best = Some((modified, path_str(&entry.path())));
        }
    }
    best.map(|(_, path)| path)
}

/// A small deterministic generator standing in for Go's math/rand in the
/// randomized equivalence tests (the exact sequence does not matter).
pub(super) struct Rng(u64);

impl Rng {
    pub(super) fn new(seed: u64) -> Rng {
        Rng(seed.wrapping_mul(0x9E37_79B9_7F4A_7C15) | 1)
    }

    pub(super) fn intn(&mut self, n: usize) -> usize {
        // splitmix64
        self.0 = self.0.wrapping_add(0x9E37_79B9_7F4A_7C15);
        let mut z = self.0;
        z = (z ^ (z >> 30)).wrapping_mul(0xBF58_476D_1CE4_E5B9);
        z = (z ^ (z >> 27)).wrapping_mul(0x94D0_49BB_1331_11EB);
        z ^= z >> 31;
        (z % n as u64) as usize
    }
}
