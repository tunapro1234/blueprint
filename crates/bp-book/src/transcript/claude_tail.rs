//! Port of internal/cache/claude_tail.go.
//!
//! Caches decoded transcript windows by path. Status and the bar renderer read
//! every Claude agent's transcript on each refresh. Reading the window is
//! cheap; decoding its records is not, and a working transcript only appends,
//! so records are decoded once and reused by the hash of their bytes.

use std::collections::HashMap;
use std::fs::{File, Metadata};
use std::hash::{BuildHasher, RandomState};
use std::sync::{LazyLock, Mutex};
use std::time::SystemTime;

use super::{ClaudeFold, ClaudeMetrics, ClaudeRow, decode_claude_row, is_blank, read_tail};

pub(crate) struct ClaudeTailEntry {
    pub(crate) size: u64,
    modified: Option<SystemTime>,
    metrics: ClaudeMetrics,
    pub(crate) decoded: HashMap<u64, ClaudeRow>,
}

pub(crate) static CLAUDE_TAILS: LazyLock<Mutex<HashMap<String, ClaudeTailEntry>>> =
    LazyLock::new(|| Mutex::new(HashMap::new()));

static CLAUDE_ROW_SEED: LazyLock<RandomState> = LazyLock::new(RandomState::new);

fn lock() -> std::sync::MutexGuard<'static, HashMap<String, ClaudeTailEntry>> {
    CLAUDE_TAILS
        .lock()
        .unwrap_or_else(|poisoned| poisoned.into_inner())
}

/// Folds exactly the records the window read does. Only the decode of a record
/// already seen at this path is skipped; a record is a pure function of its
/// bytes, so any rewrite of the file is still read correctly.
pub(crate) fn cached_claude_tail(
    path: &str,
    file: &File,
    info: &Metadata,
    tail_size: u64,
) -> Option<ClaudeMetrics> {
    let size = info.len();
    let modified = info.modified().ok();
    let previous = {
        let mut entries = lock();
        if let Some(entry) = entries.get(path)
            && entry.size == size
            && entry.modified == modified
        {
            return Some(entry.metrics.clone());
        }
        // The previous decodes are only reused to build the new entry, which
        // replaces this one below.
        entries.remove(path)
    };
    let Some(data) = read_tail(file, size, tail_size) else {
        forget_claude_tail(path);
        return None;
    };
    let previous = previous.map(|entry| entry.decoded).unwrap_or_default();
    let mut decoded: HashMap<u64, ClaudeRow> = HashMap::with_capacity(previous.len() + 16);
    let mut fold = ClaudeFold::new();
    for line in data.split(|&c| c == b'\n') {
        if is_blank(line) {
            continue;
        }
        let key = CLAUDE_ROW_SEED.hash_one(line);
        let row = decoded.entry(key).or_insert_with(|| {
            previous
                .get(&key)
                .cloned()
                .unwrap_or_else(|| decode_claude_row(line))
        });
        fold.add(row);
    }
    let metrics = fold.metrics();
    let mut entries = lock();
    if entries.len() > 256 {
        entries.clear();
    }
    entries.insert(
        path.to_owned(),
        ClaudeTailEntry {
            size,
            modified,
            metrics: metrics.clone(),
            decoded,
        },
    );
    Some(metrics)
}

pub(crate) fn forget_claude_tail(path: &str) {
    lock().remove(path);
}
