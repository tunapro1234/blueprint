//! Port of internal/buildinfo/buildinfo_test.go.

use bp_release::buildinfo::{
    self, ExecutableStat, Identity, cached_hash_with, executable_stat, hash_file, record,
    recorded_with, version_json,
};
use std::cell::Cell;
use std::io;
use std::path::Path;
use std::time::{Duration, SystemTime};

/// Go `countedHash`: a hash function that counts its computations.
struct Counted(Cell<usize>);

impl Counted {
    fn new() -> Self {
        Self(Cell::new(0))
    }
    fn hash(&self, path: &Path) -> io::Result<String> {
        self.0.set(self.0.get() + 1);
        hash_file(path)
    }
    fn calls(&self) -> usize {
        self.0.get()
    }
}

#[test]
fn daemon_record_rejects_reused_pid_and_changed_executable() {
    let dir = tempfile::tempdir().unwrap();
    let path = dir.path().join("daemon-runtime.json");
    record(&path).unwrap();
    let counted = Counted::new();
    let (current, verified) = recorded_with(&path, &|p| counted.hash(p));
    let current = current.expect("identity");
    if current.start_ticks.is_empty() {
        eprintln!("process identity unavailable on this OS");
        return;
    }
    assert!(
        current.executable_stat.is_some() && counted.calls() == 0,
        "startup stat={:?} hash computations={}; matching stat must skip hashing",
        current.executable_stat,
        counted.calls()
    );
    assert_eq!(verified, "verified executable");
    for change in ["pid-start", "sha"] {
        let mut bad = current.clone();
        if change == "pid-start" {
            bad.start_ticks.push('0');
        } else {
            bad.sha256 = "different".into();
        }
        std::fs::write(&path, bad.to_json()).unwrap();
        let (_, status) = recorded_with(&path, &|p| counted.hash(p));
        assert!(
            status.starts_with("unverified:"),
            "stale daemon identified as current: {status}"
        );
    }
}

#[test]
fn current_hash_cache_uses_unchanged_executable_stat() {
    let dir = tempfile::tempdir().unwrap();
    let executable = dir.path().join("bp");
    let cache = dir.path().join("status-executable-hash.json");
    std::fs::write(&executable, "first executable image").unwrap();
    let stat = executable_stat(&executable).expect("executable stat unavailable");
    let counted = Counted::new();
    let hash = |p: &Path| counted.hash(p);
    let want = cached_hash_with(&executable, Some(&stat), Some(&cache), &hash).unwrap();
    let got = cached_hash_with(&executable, Some(&stat), Some(&cache), &hash).unwrap();
    assert!(
        got == want && counted.calls() == 1,
        "hash={got} want {want}, computations={}",
        counted.calls()
    );
    // Byte shape of the cache file matches Go's json.Marshal(hashCache).
    let data = std::fs::read_to_string(&cache).unwrap();
    assert_eq!(
        data,
        format!(
            r#"{{"stat":{{"device":{},"inode":{},"size":{},"mtime_ns":{},"ctime_ns":{}}},"sha256":"{want}"}}"#,
            stat.device, stat.inode, stat.size, stat.mtime_ns, stat.ctime_ns
        )
    );
}

#[test]
fn current_hash_cache_rehashes_replacement_and_in_place_rewrite() {
    let dir = tempfile::tempdir().unwrap();
    let executable = dir.path().join("bp");
    let cache = dir.path().join("status-executable-hash.json");
    std::fs::write(&executable, "first image").unwrap();
    let first = executable_stat(&executable).expect("executable stat unavailable");
    let counted = Counted::new();
    let hash = |p: &Path| counted.hash(p);
    cached_hash_with(&executable, Some(&first), Some(&cache), &hash).unwrap();
    let replacement = dir.path().join("replacement");
    std::fs::write(&replacement, "first image").unwrap();
    std::fs::rename(&replacement, &executable).unwrap();
    let replaced = executable_stat(&executable).unwrap();
    assert_ne!(
        replaced.inode, first.inode,
        "replacement inode did not change"
    );
    cached_hash_with(&executable, Some(&replaced), Some(&cache), &hash).unwrap();
    std::fs::write(&executable, "other image").unwrap();
    std::thread::sleep(Duration::from_millis(2));
    let file = std::fs::File::options()
        .write(true)
        .open(&executable)
        .unwrap();
    file.set_times(
        std::fs::FileTimes::new()
            .set_accessed(SystemTime::now())
            .set_modified(SystemTime::now()),
    )
    .unwrap();
    drop(file);
    let rewritten = executable_stat(&executable).unwrap();
    assert!(
        rewritten.inode == replaced.inode && rewritten != replaced,
        "in-place rewrite did not change tracked identity: before={replaced:?} after={rewritten:?}"
    );
    cached_hash_with(&executable, Some(&rewritten), Some(&cache), &hash).unwrap();
    assert_eq!(counted.calls(), 3);
}

#[test]
fn current_hash_cache_ignores_corrupt_cache() {
    let dir = tempfile::tempdir().unwrap();
    let executable = dir.path().join("bp");
    let cache = dir.path().join("status-executable-hash.json");
    std::fs::write(&executable, "executable").unwrap();
    let stat = executable_stat(&executable).unwrap();
    std::fs::write(&cache, "{broken").unwrap();
    let counted = Counted::new();
    let hash = |p: &Path| counted.hash(p);
    let first = cached_hash_with(&executable, Some(&stat), Some(&cache), &hash).unwrap();
    let second = cached_hash_with(&executable, Some(&stat), Some(&cache), &hash).unwrap();
    assert!(
        first == second && counted.calls() == 1,
        "computations={}",
        counted.calls()
    );
}

#[test]
fn recorded_old_identity_falls_back_to_hash() {
    let dir = tempfile::tempdir().unwrap();
    let path = dir.path().join("daemon-runtime.json");
    record(&path).unwrap();
    let mut identity = Identity::from_json(&std::fs::read(&path).unwrap()).unwrap();
    identity.executable_stat = None; // Runtime records written before stat identities.
    std::fs::write(&path, identity.to_json()).unwrap();
    let counted = Counted::new();
    let hash = |p: &Path| counted.hash(p);
    let (_, status) = recorded_with(&path, &hash);
    assert!(
        status == "verified executable" && counted.calls() == 1,
        "status={status} computations={}",
        counted.calls()
    );
    let (_, status) = recorded_with(&path, &hash);
    assert!(
        status == "verified executable" && counted.calls() == 1,
        "cached status={status} computations={}",
        counted.calls()
    );
    let mut proof = path.as_os_str().to_owned();
    proof.push(".verified-hash.json");
    std::fs::write(&proof, "{broken").unwrap();
    let (_, status) = recorded_with(&path, &hash);
    assert!(
        status == "verified executable" && counted.calls() == 2,
        "corrupt proof status={status} computations={}",
        counted.calls()
    );
}

#[test]
fn executable_stat_fields_are_nanosecond_values() {
    let dir = tempfile::tempdir().unwrap();
    let path = dir.path().join("executable");
    std::fs::write(&path, "x").unwrap();
    let stat = executable_stat(&path).unwrap();
    assert!(
        stat.size == 1
            && stat.device != 0
            && stat.inode != 0
            && stat.mtime_ns != 0
            && stat.ctime_ns != 0,
        "unexpected stat identity: {stat:?}"
    );
}

// Rust-only: byte shape and decoding rules shared with Go readers/writers.

#[test]
fn version_json_matches_go_encoder_shape() {
    let identity = Identity {
        start_ticks: "123".into(),
        pid: 42,
        executable: "/usr/local/bin/bp<&>".into(),
        sha256: "ab".into(),
        executable_stat: Some(ExecutableStat {
            device: 1,
            inode: 2,
            size: 3,
            mtime_ns: 4,
            ctime_ns: 5,
        }),
        revision: "deadbeef".into(),
        modified: true,
    };
    assert_eq!(
        version_json("1.9.30", &identity),
        concat!(
            r#"{"version":"1.9.30","process_start_ticks":"123","pid":42,"#,
            r#""executable":"/usr/local/bin/bp\u003c\u0026\u003e","sha256":"ab","#,
            r#""executable_stat":{"device":1,"inode":2,"size":3,"mtime_ns":4,"ctime_ns":5},"#,
            r#""revision":"deadbeef","source_modified":true}"#,
            "\n"
        )
    );
    let empty = Identity {
        pid: 7,
        ..Identity::default()
    };
    assert_eq!(
        version_json("1.0.0", &empty),
        "{\"version\":\"1.0.0\",\"pid\":7,\"source_modified\":false}\n"
    );
    assert_eq!(
        Identity::from_json(identity.to_json().as_bytes()).unwrap(),
        identity
    );
}

#[test]
fn identity_decoding_follows_go_unmarshal() {
    let parsed =
        Identity::from_json(br#"{"PID":5,"Sha256":"x","extra":[1],"executable_stat":null}"#)
            .unwrap();
    assert_eq!(
        (parsed.pid, parsed.sha256.as_str(), parsed.executable_stat),
        (5, "x", None)
    );
    for bad in [
        &br#"{"pid":"5"}"#[..],
        br#"{"pid":5.5}"#,
        br#"{"executable_stat":{"size":"1"}}"#,
        b"{broken",
        b"[]",
    ] {
        assert!(
            Identity::from_json(bad).is_none(),
            "{}",
            String::from_utf8_lossy(bad)
        );
    }
}

#[test]
fn recorded_status_strings() {
    let dir = tempfile::tempdir().unwrap();
    let path = dir.path().join("daemon-runtime.json");
    let (identity, status) = buildinfo::recorded(&path);
    assert!(identity.is_none());
    assert_eq!(status, "unknown: no readable daemon identity");
    std::fs::write(&path, r#"{"pid":0,"sha256":"x"}"#).unwrap();
    assert_eq!(
        buildinfo::recorded(&path).1,
        "unknown: invalid daemon identity"
    );
    std::fs::write(&path, r#"{"pid":1,"sha256":"x"}"#).unwrap();
    assert_eq!(
        buildinfo::recorded(&path).1,
        "unverified: running executable differs"
    );
    let sha = "a".repeat(64);
    std::fs::write(&path, format!(r#"{{"pid":1,"sha256":"{sha}"}}"#)).unwrap();
    assert_eq!(
        buildinfo::recorded(&path).1,
        "unverified: daemon process identity not readable or changed"
    );
}

#[test]
fn current_identity_describes_this_process() {
    let identity = buildinfo::current();
    assert_eq!(identity.pid, i64::from(std::process::id()));
    assert_eq!(identity.revision, buildinfo::REVISION);
    assert_eq!(identity.modified, buildinfo::SOURCE_MODIFIED);
    let exe = std::env::current_exe().unwrap();
    assert_eq!(identity.sha256, hash_file(&exe).unwrap());
    if cfg!(target_os = "linux") {
        assert!(!identity.start_ticks.is_empty());
        assert_eq!(identity.executable, exe.to_string_lossy());
        assert_eq!(identity.executable_stat, executable_stat(&exe));
    }
    let dir = tempfile::tempdir().unwrap();
    let cache = dir.path().join("hash.json");
    let cached = buildinfo::current_cached(&cache);
    assert_eq!(cached.sha256, identity.sha256);
    assert!(cache.exists());
}

#[test]
fn process_start_reads_field_22_after_the_command_name() {
    let dir = tempfile::tempdir().unwrap();
    let path = dir.path().join("stat");
    let fields: Vec<String> = (3..=25).map(|n| n.to_string()).collect();
    std::fs::write(&path, format!("1 (we ird) name) {}", fields.join(" "))).unwrap();
    assert_eq!(buildinfo::process_start(&path), "22");
    std::fs::write(&path, "1 (short) 3 4").unwrap();
    assert_eq!(buildinfo::process_start(&path), "");
}
