//! Port of internal/cache/ttl_test.go.

use serde_json::json;

use super::*;
use crate::transcript::{State, read_claude_path};

#[test]
fn test_claude_ttl_comes_from_latest_write_not_global_hour() {
    let _serial = serial();
    for (name, hour, five, elapsed, want) in [
        ("probot-egitim-36m", 3743, 0, mins(36), "warm~"),
        ("five-minute-expired", 0, 3743, mins(36), "cold~"),
        ("five-minute-recent", 0, 3743, mins(1), "warm~"),
        ("hour-expired", 3743, 0, mins(61), "cold~"),
        ("mixed-not-known", 3000, 743, mins(1), "age"),
        ("read-hit-without-ttl", 0, 0, mins(1), "age"),
    ] {
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("session.jsonl");
        let mut row = usage(gotime::now() - elapsed, 86350, hour + five);
        row["message"]["usage"]["cache_creation"] =
            json!({"ephemeral_1h_input_tokens": hour, "ephemeral_5m_input_tokens": five});
        write_jsonl(&path, std::slice::from_ref(&row));
        let s = read_claude_path(&path_str(&path));
        assert_eq!(s.cache_hint().0, want, "{name}: state {s:?}");
        // A compact snapshot has token metrics but proves no cache write.
        write_jsonl(
            &path,
            &[
                row,
                json!({"type": "system", "subtype": "compact_boundary", "timestamp": rfc(&gotime::now()), "compactMetadata": {"postTokens": 1000}}),
            ],
        );
        assert_eq!(
            read_claude_path(&path_str(&path)).cache_hint().0,
            "age",
            "{name}: compact inherited cache TTL"
        );
    }
}

#[test]
fn test_streamed_message_does_not_refresh_cache_clock() {
    let _serial = serial();
    let dir = tempfile::tempdir().unwrap();
    let path = dir.path().join("session.jsonl");
    let now = gotime::now();
    let rows: Vec<_> = [mins(6), mins(1)]
        .into_iter()
        .map(|elapsed| {
            let mut row = usage(now - elapsed, 100, 20);
            row["message"]["id"] = json!("same-response");
            row["message"]["usage"]["cache_creation"] = json!({"ephemeral_5m_input_tokens": 20});
            row
        })
        .collect();
    write_jsonl(&path, &rows);
    let s = read_claude_path(&path_str(&path));
    assert!(
        s.cache_hint().0 == "cold~" && s.age <= mins(2),
        "last chunk renewed cache: {s:?}"
    );
}

#[test]
fn test_model_name_and_recent_tokens_cannot_prove_codex_ttl() {
    for model in ["gpt-6-astra", "gpt-5.6-sol", "gpt-5.5", "claude-fable-5-1"] {
        let s = State {
            known: true,
            model: model.to_owned(),
            age: mins(36),
            ..State::default()
        };
        assert_eq!(s.cache_hint().0, "age", "{model}");
    }
}
