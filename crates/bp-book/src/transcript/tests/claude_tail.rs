//! Port of internal/cache/claude_tail_test.go.

use std::collections::HashMap;
use std::fs::{self, File};
use std::path::Path;

use serde_json::{Map, Value, json};

use super::*;
use crate::go_struct;
use crate::godecode::{RawJson, unmarshal, unmarshal_ok};
use crate::transcript::claude_tail::{CLAUDE_TAILS, cached_claude_tail};
use crate::transcript::{State, age, automatic, content_text, is_blank, read_tail};

fn reference_now() -> Time {
    gotime::parse_rfc3339("2026-09-25T12:00:00Z").unwrap()
}

fn parse_time(value: &str) -> Option<Time> {
    gotime::parse_rfc3339(value)
}

#[derive(Default)]
struct RefMetadata {
    post_tokens: Option<i64>,
}
go_struct!(RefMetadata {
    post_tokens: "postTokens"
});

#[derive(Default)]
struct RefRecord {
    kind: String,
    subtype: String,
    is_compact_summary: bool,
    is_sidechain: bool,
    compact_metadata: Option<RefMetadata>,
    timestamp: String,
    message: RawJson,
    role: String,
    content: RawJson,
    effort: RawJson,
}
go_struct!(RefRecord {
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
struct RefCreation {
    hour: i64,
    five: i64,
}
go_struct!(RefCreation {
    hour: "ephemeral_1h_input_tokens",
    five: "ephemeral_5m_input_tokens"
});

#[derive(Default)]
struct RefUsage {
    input: i64,
    cache_read: i64,
    cache_creation: i64,
    creation: RefCreation,
}
go_struct!(RefUsage {
    input: "input_tokens",
    cache_read: "cache_read_input_tokens",
    cache_creation: "cache_creation_input_tokens",
    creation: "cache_creation",
});

#[derive(Default)]
struct RefMessage {
    id: String,
    role: String,
    model: String,
    content: RawJson,
    usage: Option<RefUsage>,
}
go_struct!(RefMessage {
    id: "id",
    role: "role",
    model: "model",
    content: "content",
    usage: "usage"
});

/// The whole-window read the incremental cache must reproduce exactly.
fn reference_read_claude_path(path: &str, tail_size: u64) -> State {
    let Ok(file) = File::open(path) else {
        return State::unknown();
    };
    let Ok(info) = file.metadata() else {
        return State::unknown();
    };
    let Some(data) = read_tail(&file, info.len(), tail_size) else {
        return State::unknown();
    };

    let (mut usage_time, mut human_time, mut compact_time) =
        (gotime::zero(), gotime::zero(), gotime::zero());
    let mut cache_time = gotime::zero();
    let mut cache_ttl = TimeDelta::zero();
    let mut message_starts: HashMap<String, Time> = HashMap::new();
    let mut ctx_tokens = 0i64;
    let (mut model, mut effort) = (String::new(), String::new());
    for line in data.split(|&c| c == b'\n') {
        if is_blank(line) {
            continue;
        }
        let mut record = RefRecord::default();
        if !unmarshal_ok(line, &mut record) {
            continue;
        }
        if record.is_sidechain {
            continue;
        }
        if record.kind == "system" && record.subtype == "compact_boundary" {
            compact_time = parse_time(&record.timestamp).unwrap_or_else(gotime::zero);
            usage_time = gotime::zero();
            ctx_tokens = 0;
            cache_time = gotime::zero();
            cache_ttl = TimeDelta::zero();
            if let Some(post) = record.compact_metadata.as_ref().and_then(|m| m.post_tokens)
                && post >= 0
            {
                usage_time = compact_time;
                ctx_tokens = post;
            }
            continue;
        }
        let mut message = RefMessage::default();
        let _ = unmarshal(record.message.as_bytes(), &mut message);
        if let Some(stamp) = parse_time(&record.timestamp)
            && !message.id.is_empty()
        {
            match message_starts.get(&message.id) {
                Some(previous) if stamp >= *previous => {}
                _ => {
                    message_starts.insert(message.id.clone(), stamp);
                }
            }
        }
        if !message.model.is_empty() && message.model != "<synthetic>" {
            model = message.model.clone();
            effort = String::new();
            if record.kind == "assistant" || message.role == "assistant" {
                let _ = unmarshal(record.effort.as_bytes(), &mut effort);
            }
        }
        if let Some(usage) = &message.usage
            && let Some(timestamp) = parse_time(&record.timestamp)
            && (gotime::is_zero(&compact_time) || timestamp > compact_time)
        {
            usage_time = timestamp;
            ctx_tokens = usage.input + usage.cache_read + usage.cache_creation;
            cache_ttl = TimeDelta::zero();
            cache_time = timestamp;
            if let Some(start) = message_starts.get(&message.id) {
                cache_time = *start;
            }
            let c = &usage.creation;
            if c.hour > 0 && c.five == 0 && c.hour == usage.cache_creation {
                cache_ttl = hours(1);
            } else if c.five > 0 && c.hour == 0 && c.five == usage.cache_creation {
                cache_ttl = mins(5);
            }
        }
        if record.kind != "user" && record.role != "user" && message.role != "user" {
            continue;
        }
        let content = if message.content.is_empty() {
            &record.content
        } else {
            &message.content
        };
        let text = content_text(content.as_bytes());
        if record.is_compact_summary || text.trim().is_empty() || automatic(&text) {
            continue;
        }
        if let Some(timestamp) = parse_time(&record.timestamp) {
            human_time = timestamp;
        }
    }

    let base = Path::new(path)
        .file_name()
        .map(|n| n.to_string_lossy().into_owned())
        .unwrap_or_default();
    let mut result = State {
        ctx_tokens,
        model,
        effort,
        path: path.to_owned(),
        thread_id: base.strip_suffix(".jsonl").unwrap_or(&base).to_owned(),
        usage_at: usage_time,
        ..State::unknown()
    };
    let now = reference_now();
    if cache_ttl > TimeDelta::zero() {
        result.cache_ttl = cache_ttl;
        result.cache_age = age(&now, &cache_time);
    }
    if !gotime::is_zero(&usage_time) {
        result.known = true;
        result.age = age(&now, &usage_time);
    }
    if !gotime::is_zero(&human_time) {
        result.last_human_age = age(&now, &human_time);
    }
    result
}

fn obj(value: Value) -> Map<String, Value> {
    match value {
        Value::Object(map) => map,
        _ => unreachable!(),
    }
}

fn random_claude_row(rng: &mut Rng, n: i64, big: usize) -> String {
    let now = reference_now();
    let mut stamp = rfc(&(now - hours(1) + TimeDelta::seconds(n)));
    if rng.intn(9) == 0 {
        // Records written out of order and without a timestamp.
        stamp = [
            String::new(),
            "not a time".to_owned(),
            rfc(&(now - hours(2))),
        ][rng.intn(3)]
        .clone();
    }
    let id = format!("msg_{}", rng.intn(6));
    let mut record = obj(json!({"timestamp": stamp}));
    match rng.intn(13) {
        0 => {
            record.insert("type".into(), json!("system"));
            record.insert("subtype".into(), json!("compact_boundary"));
            match rng.intn(3) {
                0 => {
                    record.insert(
                        "compactMetadata".into(),
                        json!({"postTokens": rng.intn(50000)}),
                    );
                }
                1 => {
                    record.insert("compactMetadata".into(), json!({"postTokens": -1}));
                }
                _ => {}
            }
        }
        1..=3 => {
            let mut creation = Map::new();
            let cc = rng.intn(3) * 1000;
            match rng.intn(4) {
                0 => {
                    creation.insert("ephemeral_1h_input_tokens".into(), json!(cc));
                }
                1 => {
                    creation.insert("ephemeral_5m_input_tokens".into(), json!(cc));
                }
                2 => {
                    creation.insert("ephemeral_1h_input_tokens".into(), json!(cc / 2));
                    creation.insert("ephemeral_5m_input_tokens".into(), json!(cc / 2));
                }
                _ => {}
            }
            let model = ["claude-a", "claude-b", "<synthetic>", ""][rng.intn(4)];
            let mut message = obj(json!({"id": id, "role": "assistant", "model": model}));
            if rng.intn(3) > 0 {
                message.insert(
                    "usage".into(),
                    json!({
                        "input_tokens": rng.intn(100),
                        "cache_read_input_tokens": rng.intn(90000),
                        "cache_creation_input_tokens": cc,
                        "cache_creation": creation,
                    }),
                );
            }
            record.insert("type".into(), json!("assistant"));
            record.insert("message".into(), Value::Object(message));
            match rng.intn(4) {
                0 => {
                    record.insert("effort".into(), json!("high"));
                }
                1 => {
                    record.insert("effort".into(), json!(3));
                }
                2 => {
                    record.insert("effort".into(), Value::Null);
                }
                _ => {}
            }
        }
        4 | 5 => {
            let text = [
                "please fix it",
                "[ANNOUNCE] x",
                "health-watch: y",
                "  ",
                "[ 3 ANNOUNCEMENT (x)",
            ][rng.intn(5)];
            record.insert("type".into(), json!("user"));
            if rng.intn(2) == 0 {
                record.insert("message".into(), json!({"role": "user", "content": text}));
            } else {
                record.insert(
                    "message".into(),
                    json!({"role": "user", "content": [{"type": "text", "text": text}]}),
                );
            }
            if rng.intn(6) == 0 {
                record.insert("isCompactSummary".into(), json!(true));
            }
        }
        6 => {
            record.insert("type".into(), json!("queue"));
            record.insert("role".into(), json!("user"));
            record.insert("content".into(), json!("typed ahead"));
        }
        7 => {
            record.insert("type".into(), json!("assistant"));
            record.insert("isSidechain".into(), json!(true));
            record.insert(
                "message".into(),
                json!({"id": id, "model": "side", "usage": {"input_tokens": 1}}),
            );
        }
        8 => return r#"{"type":"assistant","message":"#.to_owned(), // torn
        9 => return ["", "   ", "\t", "garbage"][rng.intn(4)].to_owned(),
        10 => {
            record.insert("type".into(), json!("user"));
            let text = "r".repeat(rng.intn(big));
            record.insert(
                "message".into(),
                json!({"role": "user", "content": [{"type": "tool_result", "text": text}]}),
            );
        }
        11 => {
            record.insert("type".into(), json!("assistant"));
            record.insert("message".into(), json!("not an object"));
        }
        _ => {
            record.insert("type".into(), json!("assistant"));
            record.insert(
                "message".into(),
                json!({"id": 5, "model": "claude-c", "usage": {"input_tokens": "x"}}),
            );
        }
    }
    Value::Object(record).to_string()
}

#[test]
fn test_cached_claude_tail_matches_window_read() {
    let _serial = serial();
    let tail_size: u64 = 4096;
    let mut rng = Rng::new(11);
    let dir = tempfile::tempdir().unwrap();
    let mut reused = 0;
    for history in 0..150 {
        let path = dir
            .path()
            .join(format!("{history:08}-0000-0000-0000-000000000000.jsonl"));
        let path_s = path_str(&path);
        let mut content: Vec<u8> = Vec::new();
        let mut rows = 0i64;
        // Some histories hold records longer than the window, forcing the
        // widened read.
        let big = if history % 5 == 0 {
            3 * tail_size as usize
        } else {
            200
        };
        for step in 0..14 {
            match rng.intn(12) {
                0 => content.clear(),
                // A rewrite that keeps the size but changes an early byte.
                1 if !content.is_empty() => {
                    let i = rng.intn(content.len());
                    content[i] = b'Z';
                }
                _ => {}
            }
            let limit = [3, 30, 120][rng.intn(3)];
            let count = rng.intn(limit);
            for _ in 0..count {
                rows += 1;
                content.extend_from_slice(random_claude_row(&mut rng, rows, big).as_bytes());
                content.push(b'\n');
            }
            let mut written = content.clone();
            if rng.intn(3) == 0 {
                rows += 1;
                let mut partial = random_claude_row(&mut rng, rows, big).into_bytes();
                if rng.intn(2) == 0 && !partial.is_empty() {
                    let cut = rng.intn(partial.len());
                    partial.truncate(cut);
                }
                written.extend_from_slice(&partial);
                if rng.intn(2) == 0 {
                    content.extend_from_slice(&partial);
                    content.push(b'\n');
                }
            }
            fs::write(&path, &written).unwrap();
            let stamp = SystemTime::UNIX_EPOCH
                + std::time::Duration::from_secs(1_700_000_000 + history * 100 + step);
            chtimes(&path, stamp);
            {
                let tails = CLAUDE_TAILS.lock().unwrap_or_else(|p| p.into_inner());
                if let Some(entry) = tails.get(&path_s)
                    && entry.size != written.len() as u64
                    && !entry.decoded.is_empty()
                {
                    reused += 1;
                }
            }
            let file = File::open(&path).unwrap();
            let info = file.metadata().unwrap();
            let got = match cached_claude_tail(&path_s, &file, &info, tail_size) {
                Some(metrics) => metrics.state(&path_s, &reference_now()),
                None => State::unknown(),
            };
            drop(file);
            let want = reference_read_claude_path(&path_s, tail_size);
            assert_eq!(got, want, "history {history} step {step}");
        }
    }
    assert!(
        reused >= 500,
        "only {reused} reads could reuse decoded records"
    );
}
