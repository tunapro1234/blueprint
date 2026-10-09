//! Port of internal/cache/rollout_paths_test.go.

use std::fs;
use std::path::{Path, PathBuf};

use super::*;
use crate::transcript::glob;
use crate::transcript::rollout_paths_by_id;

fn set_dir_time(dir: &Path, at: SystemTime) {
    chtimes(dir, at);
}

#[test]
fn test_codex_rollout_paths_follows_moved_rollout() {
    let _serial = serial();
    let home = tempfile::tempdir().unwrap();
    let home_s = path_str(home.path());
    let id = "019a0000-0000-7000-8000-00000000cafe";
    let write = |day: &str| -> String {
        let path = home
            .path()
            .join("sessions/2026/09")
            .join(day)
            .join(format!("rollout-x-{id}.jsonl"));
        fs::create_dir_all(path.parent().unwrap()).unwrap();
        fs::write(&path, "{}\n").unwrap();
        path_str(&path)
    };
    let first = write("24");
    assert_eq!(
        rollout_paths_by_id(&home_s, id),
        std::slice::from_ref(&first),
        "first lookup"
    );
    assert_eq!(
        rollout_paths_by_id(&home_s, id),
        std::slice::from_ref(&first),
        "cached lookup"
    );
    let second = write("25");
    fs::remove_file(&first).unwrap();
    assert_eq!(
        rollout_paths_by_id(&home_s, id),
        std::slice::from_ref(&second),
        "lookup after move"
    );
    assert_eq!(
        rollout_paths_by_id(&home_s, id),
        std::slice::from_ref(&second),
        "cached lookup after move"
    );
    write("26");
    assert_eq!(
        rollout_paths_by_id(&home_s, id).len(),
        2,
        "second copy hidden by the cache"
    );
}

#[test]
fn test_rollout_paths_by_id_sees_new_files_at_once() {
    let _serial = serial();
    let home = tempfile::tempdir().unwrap();
    let home_s = path_str(home.path());
    let id = "019a0000-0000-7000-8000-00000000beef";
    let day = home.path().join("sessions/2026/09/25");
    fs::create_dir_all(&day).unwrap();
    assert!(rollout_paths_by_id(&home_s, id).is_empty(), "miss");
    let path = day.join(format!("rollout-y-{id}.jsonl"));
    fs::write(&path, "{}\n").unwrap();
    assert_eq!(
        rollout_paths_by_id(&home_s, id),
        [path_str(&path)],
        "new rollout"
    );
    fs::write(day.join(format!("copy-{id}.jsonl")), "{}\n").unwrap();
    assert_eq!(
        rollout_paths_by_id(&home_s, id).len(),
        2,
        "duplicate hidden by the cache"
    );
}

/// Settled directories are served from the index; a directory modified within
/// the racy window of the lookup is globbed again.
#[test]
fn test_rollout_paths_by_id_reuses_only_settled_directories() {
    let _serial = serial();
    let home = tempfile::tempdir().unwrap();
    let home_s = path_str(home.path());
    let id = "019a0000-0000-7000-8000-00000000f00d";
    let day = home.path().join("sessions/2026/09/25");
    fs::create_dir_all(&day).unwrap();
    fs::write(day.join(format!("rollout-z-{id}.jsonl")), "{}\n").unwrap();
    let old = SystemTime::now() - std::time::Duration::from_secs(3600);
    let settle = || {
        let dirs: [PathBuf; 4] = [
            day.clone(),
            day.parent().unwrap().to_path_buf(),
            day.parent().unwrap().parent().unwrap().to_path_buf(),
            home.path().join("sessions"),
        ];
        for dir in dirs {
            set_dir_time(&dir, old);
        }
    };
    settle();
    assert_eq!(rollout_paths_by_id(&home_s, id).len(), 1, "settled lookup");
    // A copy whose directory mtime is forced back to the cached value is
    // invisible exactly when the answer is being reused.
    let copy_path = day.join(format!("copy-{id}.jsonl"));
    fs::write(&copy_path, "{}\n").unwrap();
    settle();
    assert_eq!(
        rollout_paths_by_id(&home_s, id).len(),
        1,
        "settled answer was not reused"
    );
    // Same trick with a fresh mtime that is still equal between the two
    // calls: the racy window forces a new glob.
    let fresh = SystemTime::now();
    set_dir_time(&day, fresh);
    assert_eq!(rollout_paths_by_id(&home_s, id).len(), 2, "fresh lookup");
    fs::remove_file(&copy_path).unwrap();
    set_dir_time(&day, fresh);
    assert_eq!(
        rollout_paths_by_id(&home_s, id).len(),
        1,
        "racy answer was reused"
    );
}

#[test]
fn test_rollout_paths_by_id_matches_glob_across_generated_tree() {
    let _serial = serial();
    let home = tempfile::tempdir().unwrap();
    let home_s = path_str(home.path());
    let root = home.path().join("sessions");
    let ids = [
        "019a0000-0000-7000-8000-00000000cafe",
        "thread-with-hyphens",
        "missing",
        "wild*card",
    ];
    let entries: [(&str, String, bool); 7] = [
        ("2024/12/31", format!("rollout-a-{}.jsonl", ids[0]), false),
        ("2025/01/01", format!("copy-{}.jsonl", ids[0]), false), // Duplicate id in another day.
        ("2025/01/02", format!("rollout-{}.jsonl", ids[1]), false),
        ("2025/01/02", format!("rollout-{}.txt", ids[0]), false), // Non-jsonl suffix.
        ("2025/01/02", format!("directory-{}.jsonl", ids[0]), true), // Glob includes matching directories.
        ("2025/01/02", "rollout-wild-X-card.jsonl".to_owned(), false),
        ("2026/02/03", "unrelated.jsonl".to_owned(), false),
    ];
    for (day, name, dir) in entries {
        let path = root.join(day).join(name);
        if dir {
            fs::create_dir_all(&path).unwrap();
            continue;
        }
        fs::create_dir_all(path.parent().unwrap()).unwrap();
        fs::write(&path, "not opened by the index\n").unwrap();
    }
    let root_s = path_str(&root);
    for id in ids {
        let name = format!("*-{id}.jsonl");
        let want = glob::glob(&glob::join(&[&root_s, "*", "*", "*", &name])).unwrap();
        assert_eq!(rollout_paths_by_id(&home_s, id), want, "id {id:?}");
    }
    // The glob port itself must find what Go's Glob does here.
    let name = format!("*-{}.jsonl", ids[0]);
    assert_eq!(
        glob::glob(&glob::join(&[&root_s, "*", "*", "*", &name]))
            .unwrap()
            .len(),
        3
    );
}
