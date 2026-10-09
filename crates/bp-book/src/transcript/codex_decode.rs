//! Port of internal/cache/codex_decode.go.

use std::collections::HashMap;
use std::hash::{BuildHasher, RandomState};
use std::sync::{LazyLock, Mutex};

use crate::go_struct;
use crate::godecode::{RawJson, unmarshal_ok};
use crate::gotime::{self, Time};

/// The shared decoded shape used by rollout state and attention readers.
#[derive(Debug, Clone, PartialEq)]
pub struct CodexRecord {
    pub kind: String,
    pub timestamp: Time,
    pub payload: CodexPayload,
}

impl Default for CodexRecord {
    fn default() -> Self {
        CodexRecord {
            kind: String::new(),
            timestamp: gotime::zero(),
            payload: CodexPayload::default(),
        }
    }
}
go_struct!(CodexRecord {
    kind: "type",
    timestamp: "timestamp",
    payload: "payload"
});

#[derive(Debug, Clone, Default, PartialEq)]
pub struct CodexPayload {
    pub id: String,
    pub kind: String,
    pub role: String,
    pub model: String,
    pub effort: String,
    pub service_tier: String,
    pub content: RawJson,
    pub info: Option<CodexInfo>,
}
go_struct!(CodexPayload {
    id: "id",
    kind: "type",
    role: "role",
    model: "model",
    effort: "effort",
    service_tier: "service_tier",
    content: "content",
    info: "info",
});

#[derive(Debug, Clone, Default, PartialEq)]
pub struct CodexInfo {
    pub last: Option<CodexTokenUsage>,
    pub window: i64,
}
go_struct!(CodexInfo {
    last: "last_token_usage",
    window: "model_context_window"
});

#[derive(Debug, Clone, Default, PartialEq)]
pub struct CodexTokenUsage {
    pub total: i64,
}
go_struct!(CodexTokenUsage {
    total: "total_tokens"
});

pub(crate) struct DecodeEntry {
    line: Vec<u8>,
    record: CodexRecord,
    valid: bool,
}

pub(crate) struct DecodeCache {
    seed: RandomState,
    pub(crate) entries: HashMap<u64, Vec<DecodeEntry>>,
    pub(crate) bytes: usize,
}

pub(crate) static CODEX_RECORD_DECODES: LazyLock<Mutex<DecodeCache>> = LazyLock::new(|| {
    Mutex::new(DecodeCache {
        seed: RandomState::new(),
        entries: HashMap::new(),
        bytes: 0,
    })
});

fn lock() -> std::sync::MutexGuard<'static, DecodeCache> {
    CODEX_RECORD_DECODES
        .lock()
        .unwrap_or_else(|poisoned| poisoned.into_inner())
}

#[cfg(test)]
thread_local! {
    /// Counts real decodes on this thread (Go's `unmarshalCodexRecord` hook).
    pub(crate) static DECODE_COUNT: std::cell::Cell<usize> = const { std::cell::Cell::new(0) };
}

fn unmarshal_codex_record(line: &[u8], record: &mut CodexRecord) -> bool {
    #[cfg(test)]
    DECODE_COUNT.with(|count| count.set(count.get() + 1));
    unmarshal_ok(line, record)
}

/// Reuses only a parsed row value. It does not cache rollout activity or make
/// a cross-process status claim.
pub fn decode_codex_record(line: &[u8]) -> (CodexRecord, bool) {
    let key;
    {
        let cache = lock();
        key = cache.seed.hash_one(line);
        if let Some(found) = cache
            .entries
            .get(&key)
            .and_then(|list| list.iter().find(|e| e.line == line))
        {
            return (found.record.clone(), found.valid);
        }
    }
    let mut record = CodexRecord::default();
    let valid = unmarshal_codex_record(line, &mut record);
    if line.len() > 256 * 1024 {
        return (record, valid);
    }
    let mut cache = lock();
    if cache.bytes + line.len() > 4 * 1024 * 1024 {
        cache.entries.clear();
        cache.bytes = 0;
    }
    if cache.bytes + line.len() <= 4 * 1024 * 1024 {
        cache.bytes += line.len();
        cache.entries.entry(key).or_default().push(DecodeEntry {
            line: line.to_vec(),
            record: record.clone(),
            valid,
        });
    }
    (record, valid)
}
