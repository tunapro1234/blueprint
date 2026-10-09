//! Port of internal/cache/codex_incremental_test.go.

use std::fs;

use super::*;
use crate::go_struct;
use crate::godecode::{RawJson, unmarshal_ok};
use crate::transcript::codex::{CODEX_SCANS, CodexScan, cached_codex_scan};
use crate::transcript::codex_decode::{CODEX_RECORD_DECODES, DECODE_COUNT};
use crate::transcript::{
    State, automatic, codex_id, content_text, decode_codex_record, scan_codex_reverse,
};

#[derive(Default)]
struct RefLast {
    total: i64,
}
go_struct!(RefLast {
    total: "total_tokens"
});

#[derive(Default)]
struct RefInfo {
    last: Option<RefLast>,
    window: i64,
}
go_struct!(RefInfo {
    last: "last_token_usage",
    window: "model_context_window"
});

#[derive(Default)]
struct RefPayload {
    id: String,
    kind: String,
    role: String,
    model: String,
    effort: String,
    service_tier: String,
    content: RawJson,
    info: Option<RefInfo>,
}
go_struct!(RefPayload {
    id: "id",
    kind: "type",
    role: "role",
    model: "model",
    effort: "effort",
    service_tier: "service_tier",
    content: "content",
    info: "info",
});

struct RefRecord {
    kind: String,
    timestamp: Time,
    payload: RefPayload,
}
impl Default for RefRecord {
    fn default() -> Self {
        RefRecord {
            kind: String::new(),
            timestamp: gotime::zero(),
            payload: RefPayload::default(),
        }
    }
}
go_struct!(RefRecord {
    kind: "type",
    timestamp: "timestamp",
    payload: "payload"
});

/// The whole-file reverse scan the incremental cache must reproduce exactly.
fn reference_codex_scan(path: &str) -> CodexScan {
    let mut result = State {
        path: path.to_owned(),
        thread_id: codex_id(path),
        ..State::unknown()
    };
    let (mut usage_time, mut human_time, mut turn_time) =
        (gotime::zero(), gotime::zero(), gotime::zero());
    let (mut model_seen, mut human_seen, mut turn_seen, mut usage_seen) =
        (false, false, false, false);
    scan_codex_reverse(path, &mut |line| {
        let mut r = RefRecord::default();
        if !unmarshal_ok(line, &mut r) {
            return true;
        }
        if r.kind == "session_meta" {
            result.thread_id = r.payload.id.clone();
        }
        if r.kind == "turn_context" && !model_seen {
            result.model = r.payload.model.clone();
            result.effort = r.payload.effort.clone();
            result.service_tier = r.payload.service_tier.clone();
            model_seen = true;
        }
        if r.kind == "compacted" || (r.kind == "event_msg" && r.payload.kind == "context_compacted")
        {
            usage_seen = true;
        }
        if r.kind == "event_msg" {
            if r.payload.kind == "token_count"
                && !usage_seen
                && let Some(info) = &r.payload.info
                && let Some(last) = &info.last
            {
                result.known = true;
                usage_seen = true;
                result.ctx_tokens = last.total;
                result.window = info.window;
                usage_time = r.timestamp;
            }
            if !turn_seen {
                match r.payload.kind.as_str() {
                    "task_started" => {
                        result.busy = true;
                        turn_seen = true;
                        turn_time = r.timestamp;
                    }
                    "task_complete" | "turn_aborted" => {
                        result.busy = false;
                        turn_seen = true;
                        turn_time = r.timestamp;
                    }
                    _ => {}
                }
            }
        }
        if !human_seen
            && r.kind == "response_item"
            && r.payload.kind == "message"
            && r.payload.role == "user"
            && !automatic(&content_text(r.payload.content.as_bytes()))
        {
            human_time = r.timestamp;
            human_seen = true;
        }
        !(usage_seen && model_seen && human_seen && turn_seen)
    });
    result.usage_at = usage_time;
    result.turn_at = turn_time;
    result.turn_known = turn_seen;
    CodexScan {
        state: result,
        human: human_time,
    }
}

fn q(s: &str) -> String {
    serde_json::to_string(s).unwrap()
}

fn random_codex_row(rng: &mut Rng, n: i64) -> String {
    let base = gotime::parse_rfc3339("2026-09-25T00:00:00Z").unwrap();
    let stamp = q(&rfc(&(base + TimeDelta::seconds(n))));
    match rng.intn(14) {
        0 => format!(
            r#"{{"timestamp":{stamp},"type":"session_meta","payload":{{"id":"thread-{}"}}}}"#,
            rng.intn(4)
        ),
        1 => {
            let (m, e, t) = (rng.intn(3), rng.intn(3), rng.intn(2));
            format!(
                r#"{{"timestamp":{stamp},"type":"turn_context","payload":{{"model":"m{m}","effort":"e{e}","service_tier":"t{t}"}}}}"#
            )
        }
        2 => {
            let (total, window) = (rng.intn(1_000_000), rng.intn(3) * 100_000);
            format!(
                r#"{{"timestamp":{stamp},"type":"event_msg","payload":{{"type":"token_count","info":{{"last_token_usage":{{"total_tokens":{total}}},"model_context_window":{window}}}}}}}"#
            )
        }
        3 => format!(
            r#"{{"timestamp":{stamp},"type":"event_msg","payload":{{"type":"token_count","info":null}}}}"#
        ),
        4 => {
            if rng.intn(2) == 0 {
                format!(r#"{{"timestamp":{stamp},"type":"compacted","payload":{{}}}}"#)
            } else {
                format!(
                    r#"{{"timestamp":{stamp},"type":"event_msg","payload":{{"type":"context_compacted"}}}}"#
                )
            }
        }
        5 => {
            let kind = q(["task_started", "task_complete", "turn_aborted"][rng.intn(3)]);
            format!(r#"{{"timestamp":{stamp},"type":"event_msg","payload":{{"type":{kind}}}}}"#)
        }
        6 => {
            let texts = [
                "please fix it",
                "[ANNOUNCE] x",
                "health-watch: y",
                "[usage-policy] z",
                "",
            ];
            let text = q(texts[rng.intn(texts.len())]);
            format!(
                r#"{{"timestamp":{stamp},"type":"response_item","payload":{{"type":"message","role":"user","content":[{{"type":"input_text","text":{text}}}]}}}}"#
            )
        }
        7 => format!(
            r#"{{"timestamp":{stamp},"type":"response_item","payload":{{"type":"message","role":"assistant","content":"ok"}}}}"#
        ),
        8 => r#"{"type":"event_msg","payload":"#.to_owned(), // torn
        9 => String::new(),
        10 => "not json".to_owned(),
        11 => {
            let output = q(&"x".repeat(rng.intn(3000)));
            format!(
                r#"{{"timestamp":{stamp},"type":"response_item","payload":{{"type":"function_call_output","output":{output}}}}}"#
            )
        }
        _ => format!(
            r#"{{"timestamp":{stamp},"type":"event_msg","payload":{{"type":"agent_message"}}}}"#
        ),
    }
}

#[test]
fn test_cached_codex_scan_matches_whole_file_scan() {
    let _serial = serial();
    let mut rng = Rng::new(7);
    let dir = tempfile::tempdir().unwrap();
    let mut incremental = 0;
    for history in 0..120u64 {
        let path = dir.path().join(format!(
            "rollout-2026-09-25T00-00-00-{history:08}-0000-0000-0000-000000000000.jsonl"
        ));
        let path_s = path_str(&path);
        let mut content = String::new();
        let mut rows = 0i64;
        for step in 0..12u64 {
            if rng.intn(10) == 0 {
                // A rewrite, not an append: the cache must not merge across it.
                content.clear();
                for _ in 0..rng.intn(20) {
                    rows += 1;
                    content.push_str(&random_codex_row(&mut rng, rows));
                    content.push('\n');
                }
            } else {
                for _ in 0..rng.intn(25) {
                    rows += 1;
                    content.push_str(&random_codex_row(&mut rng, rows));
                    content.push('\n');
                }
            }
            let mut written = content.clone();
            // Sometimes the writer is caught mid-row.
            if rng.intn(4) == 0 {
                rows += 1;
                let mut partial = random_codex_row(&mut rng, rows);
                if rng.intn(2) == 0 && !partial.is_empty() {
                    let cut = rng.intn(partial.len());
                    partial.truncate(cut);
                }
                written.push_str(&partial);
                content.push_str(&partial);
                if rng.intn(2) == 0 {
                    content.push('\n');
                }
            }
            fs::write(&path, &written).unwrap();
            // Size and modification time can repeat within the clock's
            // resolution; give every step its own time as the writer would.
            let stamp = SystemTime::UNIX_EPOCH
                + std::time::Duration::from_secs(1_700_000_000 + history * 100 + step);
            chtimes(&path, stamp);
            {
                let scans = CODEX_SCANS.lock().unwrap_or_else(|p| p.into_inner());
                if let Some(entry) = scans.get(&path_s)
                    && entry.rows > 0
                    && entry.rows < written.len() as i64
                {
                    incremental += 1;
                }
            }
            let (got, want) = (cached_codex_scan(&path_s), reference_codex_scan(&path_s));
            assert_eq!(got, want, "history {history} step {step}\nfile:\n{written}");
        }
    }
    assert!(
        incremental >= 500,
        "only {incremental} scans could reuse a cached prefix"
    );
}

fn fixture() -> Vec<u8> {
    fs::read(concat!(
        env!("CARGO_MANIFEST_DIR"),
        "/testdata/status-rollout.jsonl"
    ))
    .unwrap()
}

#[test]
fn test_codex_decoder_matches_legacy_fixtures_and_large_row() {
    let _serial = serial();
    let fixture = fixture();
    let large = format!(
        "{{\"type\":\"file_history_snapshot\",\"payload\":{{\"base_instructions\":{}}}}}\n",
        q(&"x".repeat(128 * 1024))
    );
    let mut with_large = fixture.clone();
    with_large.extend_from_slice(large.as_bytes());
    for (name, content) in [
        ("rollout fixture", fixture),
        ("large unrelated row", with_large),
    ] {
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("rollout-fixture.jsonl");
        fs::write(&path, content).unwrap();
        let path_s = path_str(&path);
        let (got, want) = (cached_codex_scan(&path_s), reference_codex_scan(&path_s));
        assert_eq!(got, want, "{name}: new decoder vs legacy decoder");
    }
}

#[test]
fn test_codex_record_decode_is_shared_across_scanners() {
    let _serial = serial();
    let dir = tempfile::tempdir().unwrap();
    let path = dir.path().join("rollout.jsonl");
    fs::write(&path, fixture()).unwrap();
    let path_s = path_str(&path);
    {
        let mut decodes = CODEX_RECORD_DECODES
            .lock()
            .unwrap_or_else(|p| p.into_inner());
        decodes.entries.clear();
        decodes.bytes = 0;
    }
    let count = || DECODE_COUNT.with(|c| c.get());
    let start = count();
    let _ = cached_codex_scan(&path_s);
    let after_state_scan = count() - start;
    scan_codex_reverse(&path_s, &mut |line| {
        decode_codex_record(line);
        true
    });
    let after_attention_scan = count() - start;
    scan_codex_reverse(&path_s, &mut |line| {
        decode_codex_record(line);
        true
    });
    let after_repeat = count() - start;
    assert!(
        after_repeat == after_attention_scan && after_attention_scan - after_state_scan == 1,
        "decodes: state={after_state_scan} after attention={after_attention_scan} after repeat={after_repeat}; only the unvisited header should decode"
    );
}
