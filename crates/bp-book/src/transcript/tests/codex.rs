//! Port of internal/cache/codex_test.go.

use std::fs;
use std::path::Path;

use serde_json::{Value, json};

use super::*;
use crate::transcript::{
    MAX_TAIL_SIZE, TAIL_SIZE, codex_path, read_codex, read_codex_path, scan_codex_reverse,
};

/// A minimal codex rollout: a session_meta first line carrying the cwd, then
/// a token_count event. The file mtime is set to the event time.
fn write_rollout(path: &Path, cwd: &str, at: Time, tokens: i64, window: i64) {
    let rows = [
        json!({
            "timestamp": rfc(&(at - hours(1)).to_utc().fixed_offset()),
            "type": "session_meta",
            // The real meta line carries ~20KB of base instructions; pad the
            // fixture past any tempting "small" read buffer.
            "payload": {
                "cwd": cwd,
                "originator": "codex-tui",
                "base_instructions": {"text": "You are Codex. ".repeat(2048)},
            },
        }),
        json!({
            "timestamp": rfc(&at.to_utc().fixed_offset()),
            "type": "event_msg",
            "payload": {
                "type": "token_count",
                "info": {"last_token_usage": {"total_tokens": tokens}, "model_context_window": window},
            },
        }),
    ];
    write_jsonl(path, &rows);
    chtimes(path, system_time(&at));
}

#[test]
fn test_read_codex_picks_freshest_rollout_for_folder() {
    let _serial = serial();
    let home = tempfile::tempdir().unwrap();
    let day = home.path().join("sessions/2026/07/29");
    let now = gotime::now();
    write_rollout(
        &day.join("rollout-old.jsonl"),
        "/srv/server-crash",
        now - hours(3),
        50_000,
        258_400,
    );
    write_rollout(
        &day.join("rollout-other.jsonl"),
        "/srv/elsewhere",
        now - mins(1),
        90_000,
        258_400,
    );
    write_rollout(
        &day.join("rollout-fresh.jsonl"),
        "/srv/server-crash",
        now - mins(10),
        177_990,
        258_400,
    );

    let state = read_codex(&path_str(home.path()), "/srv/server-crash");
    assert!(
        state.known && state.ctx_tokens == 177_990 && state.window == 258_400,
        "state={state:?}"
    );
    assert!(
        state.age >= mins(9) && state.age <= mins(11),
        "age={:?}",
        state.age
    );
    assert_eq!(state.last_human_age, gotime::minus_one());
}

#[test]
fn test_read_codex_live_session_in_old_day_dir_wins() {
    let _serial = serial();
    let home = tempfile::tempdir().unwrap();
    let now = gotime::now();
    // A resumed session keeps writing to the rollout in the day directory it
    // was BORN in: the old-dir file with the newest mtime is the live one.
    write_rollout(
        &home.path().join("sessions/2026/07/10/rollout-live.jsonl"),
        "/srv/server-crash",
        now - mins(5),
        123_000,
        258_400,
    );
    write_rollout(
        &home.path().join("sessions/2026/07/29/rollout-stale.jsonl"),
        "/srv/server-crash",
        now - hours(2),
        40_000,
        258_400,
    );
    let state = read_codex(&path_str(home.path()), "/srv/server-crash");
    assert_eq!(state.ctx_tokens, 123_000, "state={state:?}");
}

#[test]
fn test_read_codex_no_matching_folder() {
    let _serial = serial();
    let home = tempfile::tempdir().unwrap();
    write_rollout(
        &home.path().join("sessions/2026/07/29/rollout.jsonl"),
        "/srv/elsewhere",
        gotime::now(),
        1000,
        0,
    );
    let home_s = path_str(home.path());
    assert!(!read_codex(&home_s, "/srv/server-crash").known);
    assert!(!read_codex(&home_s, "").known);
}

/// Codex rollout records reach 7.1 MB in this fleet against a 500 KB window.
#[test]
fn test_read_codex_widens_tail_past_record_larger_than_window() {
    let _serial = serial();
    let home = tempfile::tempdir().unwrap();
    let now = gotime::now();
    let path = home.path().join("sessions/2026/07/29/rollout-big.jsonl");
    write_rollout(&path, "/srv/server-crash", now - mins(8), 177_990, 258_400);
    append_jsonl(
        &path,
        &[json!({
            "timestamp": rfc(&(now - mins(1))),
            "type": "response_item",
            "payload": {"type": "function_call_output", "output": "z".repeat(TAIL_SIZE as usize * 2)},
        })],
    );
    let state = read_codex(&path_str(home.path()), "/srv/server-crash");
    assert!(
        state.known && state.ctx_tokens == 177_990 && state.window == 258_400,
        "state={state:?}"
    );
}

#[test]
fn test_codex_metrics_survive_tool_output_and_partial_writes() {
    let _serial = serial();
    let home = tempfile::tempdir().unwrap();
    let path = home
        .path()
        .join("sessions/2026/09/05/rollout-session.jsonl");
    let now = gotime::now();
    write_rollout(&path, "/work", now - mins(1), 123_456, 258_400);
    let mut rows: Vec<Value> = vec![
        json!({"type": "turn_context", "payload": {"model": "gpt-6-astra", "effort": "high", "service_tier": "priority"}}),
        json!({"type": "response_item", "timestamp": rfc(&now), "payload": {"type": "message", "role": "user", "content": [{"type": "input_text", "text": "please check this"}]}}),
        json!({"type": "event_msg", "timestamp": rfc(&now), "payload": {"type": "task_started"}}),
    ];
    // Many COMPLETE rows used to stop readTail widening, hiding token_count.
    for _ in 0..20 {
        rows.push(json!({"type": "response_item", "payload": {"type": "function_call_output", "output": "x".repeat(64000)}}));
    }
    append_jsonl(&path, &rows);
    let mut file = fs::OpenOptions::new().append(true).open(&path).unwrap();
    std::io::Write::write_all(
        &mut file,
        br#"{"type":"event_msg","payload":{"type":"token_count","info":"#,
    )
    .unwrap();
    drop(file);
    let got = read_codex(&path_str(home.path()), "/work");
    assert!(
        got.known
            && got.ctx_tokens == 123_456
            && got.model == "gpt-6-astra"
            && got.effort == "high"
            && got.busy
            && got.last_human_age >= TimeDelta::zero(),
        "lost metrics: {got:?}"
    );
    assert!(
        got.age >= TimeDelta::seconds(50),
        "tool activity reset usage age: {:?}",
        got.age
    );
}

#[test]
fn test_codex_ignores_exec_and_subagent_sessions() {
    let _serial = serial();
    let home = tempfile::tempdir().unwrap();
    let day = home.path().join("sessions/2026/09/05");
    write_rollout(
        &day.join("rollout-owner.jsonl"),
        "/work",
        gotime::now() - mins(1),
        123,
        0,
    );
    for (name, source) in [
        ("exec", json!("exec")),
        (
            "child",
            json!({"subagent": {"thread_spawn": {"parent_thread_id": "owner"}}}),
        ),
    ] {
        let row = json!({"type": "session_meta", "payload": {"cwd": "/work", "source": source}});
        fs::write(
            day.join(format!("rollout-{name}.jsonl")),
            format!("{row}\n"),
        )
        .unwrap();
    }
    let home_s = path_str(home.path());
    let got = read_codex(&home_s, "/work");
    assert!(
        got.known && got.ctx_tokens == 123,
        "selected helper: {got:?}"
    );
    assert!(
        codex_path(&home_s, "/work", "missing").is_none(),
        "missing pinned thread fell back to another session"
    );
}

#[test]
fn test_reverse_rollout_skips_giant_record_without_losing_earlier_rows() {
    let _serial = serial();
    let dir = tempfile::tempdir().unwrap();
    let path = dir.path().join("rollout.jsonl");
    let data = format!(
        "first\n{}\nlast\n",
        "x".repeat(MAX_TAIL_SIZE as usize + 65536)
    );
    fs::write(&path, data).unwrap();
    let mut rows = Vec::new();
    scan_codex_reverse(&path_str(&path), &mut |row| {
        rows.push(String::from_utf8_lossy(row).into_owned());
        true
    });
    assert_eq!(
        rows.join(","),
        "last,first",
        "wrong rows after giant record: count={}",
        rows.len()
    );
}

/// The scan cache must never freeze what the file or the clock says.
#[test]
fn test_read_codex_path_cache_follows_appends_and_clock() {
    let _serial = serial();
    let dir = tempfile::tempdir().unwrap();
    let now = gotime::now();
    let path = dir.path().join("sessions/2026/09/25/rollout-cache.jsonl");
    write_rollout(&path, "/srv/cache", now - mins(10), 1_000, 200_000);
    let path_s = path_str(&path);
    let first = read_codex_path(&path_s);
    assert!(first.known && first.ctx_tokens == 1_000, "first={first:?}");
    std::thread::sleep(std::time::Duration::from_millis(20));
    let again = read_codex_path(&path_s);
    assert!(
        again.ctx_tokens == 1_000 && again.age > first.age,
        "cached age did not advance: {:?} {:?}",
        first.age,
        again.age
    );
    append_jsonl(
        &path,
        &[json!({
            "timestamp": rfc(&now),
            "type": "event_msg",
            "payload": {"type": "token_count", "info": {"last_token_usage": {"total_tokens": 2_000}, "model_context_window": 200_000}},
        })],
    );
    let after = read_codex_path(&path_s);
    assert_eq!(
        after.ctx_tokens, 2_000,
        "appended token_count not seen: {after:?}"
    );
}
