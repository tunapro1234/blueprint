//! Port of internal/cache/cache_test.go.

use serde_json::json;

use super::*;
use crate::transcript::{TAIL_SIZE, folder_path, read};

#[test]
fn test_cache_age_and_last_human() {
    let _serial = serial();
    let root = tempfile::tempdir().unwrap();
    let (folder, agent) = ("/srv/project", "ada");
    let path = root.path().join("-srv-project").join("session.jsonl");
    let now = gotime::now();
    let mut lines = vec![
        json!({"type": "custom-title", "customTitle": agent}),
        user(now - hours(30), "real old message"),
        user(now - hours(4), "[ANNOUNCE ada] automated"),
        // Compatibility fixture for announcement text written by older bp versions.
        user(now - hours(3), "[ ada DUYURU (27 Tem)]\nautomated"),
        user(now - hours(2), "health-watch: automated"),
        user(now - mins(110), "[ ada ANNOUNCEMENT (27 Jul)]\nautomated"),
        user(now - mins(90), "[usage-policy] automated"),
        user(
            now - mins(80),
            "[3 accumulated announcements — 26-27 Jul]\nautomated",
        ),
        // Compatibility fixture for persisted digests written before the English-only pass.
        user(now - mins(70), "[3 birikmis duyuru — 26-27 Tem]\nautomated"),
        usage(now - mins(59), 150_000, 70_000),
        synthetic(now - mins(58)),
    ];
    write_jsonl(&path, &lines);
    let root_s = path_str(root.path());
    let state = read(&resolve, &root_s, folder, agent);
    assert!(
        state.known && state.age >= mins(58) && state.age < mins(60),
        "warm state={state:?}"
    );
    assert_eq!(
        state.model, "claude-opus-5",
        "synthetic placeholder must not win"
    );
    assert_eq!(state.ctx_tokens, 220_000);
    assert!(
        state.last_human_age >= hours(29) && state.last_human_age <= hours(31),
        "{:?}",
        state.last_human_age
    );

    lines.push(user(now - mins(20), "actual user message"));
    lines.push(usage(now - mins(61), 200_000, 50_000));
    write_jsonl(&path, &lines);
    let state = read(&resolve, &root_s, folder, agent);
    assert!(state.known && state.age >= mins(60), "cold state={state:?}");
    assert!(
        state.last_human_age >= mins(19) && state.last_human_age <= mins(21),
        "{:?}",
        state.last_human_age
    );
}

/// A single transcript record can be larger than the tail window; the tail
/// must widen instead of reporting a silent partial read.
#[test]
fn test_read_widens_tail_past_record_larger_than_window() {
    let _serial = serial();
    let root = tempfile::tempdir().unwrap();
    let (folder, agent) = ("/srv/project", "ada");
    let now = gotime::now();
    let lines = vec![
        json!({"type": "custom-title", "customTitle": agent}),
        user(now - hours(2), "real user message"),
        usage(now - mins(30), 150_000, 70_000),
        // One record twice the tail window, as a big tool result or paste makes.
        user(
            now - mins(10),
            &format!("[tool_result] {}", "z".repeat(TAIL_SIZE as usize * 2)),
        ),
    ];
    write_jsonl(
        &root.path().join("-srv-project").join("session.jsonl"),
        &lines,
    );
    let state = read(&resolve, &path_str(root.path()), folder, agent);
    assert!(
        state.known,
        "state={state:?}, want a known state: the tail window sat inside one record"
    );
    assert_eq!(state.ctx_tokens, 220_000);
    assert_eq!(state.model, "claude-opus-5");
    assert!(
        state.last_human_age >= mins(9) && state.last_human_age <= mins(11),
        "{:?}",
        state.last_human_age
    );
}

/// An agentbook folder may carry an annotation.
#[test]
fn test_read_accepts_annotated_agentbook_folder() {
    let _serial = serial();
    let root = tempfile::tempdir().unwrap();
    let agent = "server-main";
    let now = gotime::now();
    write_jsonl(
        &root.path().join("-srv").join("session.jsonl"),
        &[
            json!({"type": "custom-title", "customTitle": agent}),
            user(now - mins(10), "hello"),
            usage(now - mins(5), 100_000, 20_000),
        ],
    );
    let root_s = path_str(root.path());
    let plain = read(&resolve, &root_s, "/srv", agent);
    let annotated = read(&resolve, &root_s, "/srv (home: /srv/server-main)", agent);
    assert!(plain.known, "plain folder unreadable: {plain:?}");
    assert!(
        annotated.known && annotated.ctx_tokens == plain.ctx_tokens,
        "annotated={annotated:?} plain={plain:?}"
    );
}

/// A folder with a space in it is a path, not an annotation.
#[test]
fn test_folder_path_leaves_real_paths_alone() {
    for folder in ["/srv/probot", "relative/dir", ""] {
        assert_eq!(folder_path(folder), folder);
    }
    assert_eq!(folder_path("/srv (home: /srv/server-main)"), "/srv");
}
