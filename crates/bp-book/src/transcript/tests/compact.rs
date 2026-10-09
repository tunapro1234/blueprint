//! Port of internal/cache/compact_test.go.

use serde_json::{Value, json};

use super::*;
use crate::transcript::{read_claude_path, read_codex_path};

#[test]
fn test_claude_compact_replaces_old_context_and_ignores_replayed_usage() {
    let _serial = serial();
    let dir = tempfile::tempdir().unwrap();
    let p = dir.path().join("session.jsonl");
    let now = gotime::now();
    let boundary = json!({"type": "system", "subtype": "compact_boundary", "timestamp": rfc(&(now - mins(1))), "compactMetadata": {"postTokens": 12222}});
    let mut rows = vec![
        user(now - hours(1), "human instruction"),
        usage(now - mins(2), 300_000, 61_000),
        boundary,
        json!({"type": "user", "isCompactSummary": true, "timestamp": rfc(&now), "message": {"content": "compacted conversation"}}),
        usage(now - mins(2), 300_000, 61_000), // preserved old record after boundary
        json!({"type": "user", "timestamp": rfc(&now), "message": {"content": [{"type": "tool_result", "content": "output"}]}}),
    ];
    write_jsonl(&p, &rows);
    let s = read_claude_path(&path_str(&p));
    assert!(
        s.known
            && s.ctx_tokens == 12222
            && s.usage_at == now - mins(1)
            && s.last_human_age >= mins(59),
        "pre-compact usage or synthetic user won: {s:?}"
    );
    rows.push(usage(now, 14000, 500));
    write_jsonl(&p, &rows);
    let s = read_claude_path(&path_str(&p));
    assert!(
        s.known && s.ctx_tokens == 14500,
        "new post-compact measurement lost: {s:?}"
    );
}

#[test]
fn test_claude_compact_without_post_tokens_invalidates_old_context() {
    let _serial = serial();
    let dir = tempfile::tempdir().unwrap();
    let p = dir.path().join("session.jsonl");
    let now = gotime::now();
    write_jsonl(
        &p,
        &[
            usage(now - mins(1), 300_000, 61_000),
            json!({"type": "system", "subtype": "compact_boundary", "timestamp": rfc(&now)}),
        ],
    );
    let s = read_claude_path(&path_str(&p));
    assert!(
        !s.known && gotime::is_zero(&s.usage_at),
        "old context retained without post-compact measurement: {s:?}"
    );
}

#[test]
fn test_codex_compaction_waits_for_new_context_measurement() {
    let _serial = serial();
    let dir = tempfile::tempdir().unwrap();
    let p = dir.path().join("session.jsonl");
    let now = gotime::now();
    let token = |n: i64, stamp: Time| -> Value {
        json!({"type": "event_msg", "timestamp": rfc(&stamp), "payload": {"type": "token_count", "info": {"last_token_usage": {"total_tokens": n}}}})
    };
    for marker in [
        json!({"type": "compacted", "timestamp": rfc(&now)}),
        json!({"type": "event_msg", "timestamp": rfc(&now), "payload": {"type": "context_compacted"}}),
    ] {
        let mut rows = vec![token(240_000, now - mins(1)), marker];
        write_jsonl(&p, &rows);
        let s = read_codex_path(&path_str(&p));
        assert!(
            !s.known && gotime::is_zero(&s.usage_at),
            "old context shown after compact: {s:?}"
        );
        rows.push(token(18000, now + TimeDelta::seconds(1)));
        write_jsonl(&p, &rows);
        let s = read_codex_path(&path_str(&p));
        assert!(
            s.known && s.ctx_tokens == 18000,
            "new context lost after compact: {s:?}"
        );
    }
}
