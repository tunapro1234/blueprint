//! Port of internal/cache/effort_test.go.

use serde_json::{Value, json};

use super::*;
use crate::transcript::read_claude_path;

#[test]
fn test_claude_effort_follows_latest_main_assistant() {
    let _serial = serial();
    let row = |model: &str, effort: Value, sidechain: bool| -> Value {
        json!({
            "type": "assistant", "effort": effort, "isSidechain": sidechain,
            "timestamp": rfc(&gotime::now()),
            "message": {"role": "assistant", "model": model,
                "usage": {"input_tokens": 100, "cache_read_input_tokens": 200}},
        })
    };
    let cases: Vec<(&str, &str, &str, Vec<Value>)> = vec![
        ("observed-fable", "claude-fable-5-1", "medium", vec![]),
        (
            "effort-change",
            "claude-fable-5-1",
            "high",
            vec![row("claude-fable-5-1", json!("high"), false)],
        ),
        (
            "sidechain",
            "claude-fable-5-1",
            "medium",
            vec![row("claude-opus-5", json!("max"), true)],
        ),
        (
            "synthetic",
            "claude-fable-5-1",
            "medium",
            vec![row("<synthetic>", json!("high"), false)],
        ),
        (
            "missing-after-model-switch",
            "claude-opus-5",
            "",
            vec![row("claude-opus-5", Value::Null, false)],
        ),
        (
            "unknown-schema-retains-usage",
            "claude-opus-5",
            "",
            vec![row("claude-opus-5", json!({"value": 3}), false)],
        ),
    ];
    for (name, model, effort, tail) in cases {
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("session.jsonl");
        let mut rows = vec![row("claude-fable-5-1", json!("medium"), false)];
        rows.extend(tail);
        write_jsonl(&path, &rows);
        let s = read_claude_path(&path_str(&path));
        assert!(
            s.model == model && s.effort == effort && s.known && s.ctx_tokens == 300,
            "{name}: model={:?} effort={:?} known={} tokens={}",
            s.model,
            s.effort,
            s.known,
            s.ctx_tokens
        );
    }
}
