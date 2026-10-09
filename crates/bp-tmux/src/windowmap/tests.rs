//! Port of internal/windowmap/windowmap_test.go.

use super::*;
use std::cell::Cell;
use std::path::Path;

fn temp_root(name: &str) -> PathBuf {
    let dir = std::env::temp_dir().join(format!("bp-tmux-proc-{name}-{}", std::process::id()));
    let _ = std::fs::remove_dir_all(&dir);
    std::fs::create_dir_all(&dir).unwrap();
    dir
}

fn write_proc_stat(root: &Path, pid: i64, ppid: i64, command: &str) {
    let dir = root.join(pid.to_string());
    std::fs::create_dir_all(&dir).unwrap();
    std::fs::write(
        dir.join("stat"),
        format!("{pid} ({command}) S {ppid} 1 2 3 4 5 6\n"),
    )
    .unwrap();
}

fn set(pids: &[i64]) -> PidSet {
    pids.iter().copied().collect()
}

fn window(id: &str, pid: i64, workspace: &str) -> Window {
    Window {
        id: id.into(),
        pid,
        workspace: workspace.into(),
        ..Default::default()
    }
}

fn client(pid: i32, session: &str) -> AttachedClient {
    AttachedClient {
        pid,
        session: session.into(),
    }
}

fn agents(names: &[&str]) -> HashSet<String> {
    names.iter().map(|s| s.to_string()).collect()
}

#[test]
fn proc_lister_walks_fake_proc_tree() {
    let root = temp_root("walk");
    write_proc_stat(&root, 100, 1, "terminal (test)");
    write_proc_stat(&root, 101, 100, "tmux: client");
    write_proc_stat(&root, 102, 101, "codex");
    write_proc_stat(&root, 103, 100, "browser");
    let processes = ProcLister { root: root.clone() };
    let descendants = processes.descendants(100).unwrap();
    for pid in [100, 101, 102, 103] {
        assert!(
            descendants.contains(&pid),
            "{pid} missing from {descendants:?}"
        );
    }
    let err = processes.descendants(999).unwrap_err();
    assert_eq!(err.kind(), io::ErrorKind::NotFound, "{err}");
    std::fs::remove_dir_all(&root).unwrap();
}

struct FakeProcesses(HashMap<i64, PidSet>);

impl ProcessLister for FakeProcesses {
    fn descendants(&self, root: i64) -> io::Result<PidSet> {
        self.0
            .get(&root)
            .cloned()
            .ok_or_else(|| io::Error::from(io::ErrorKind::NotFound))
    }
}

#[test]
fn map_matches_registered_sessions_inside_window_process_trees() {
    let processes = FakeProcesses(HashMap::from([
        (100, set(&[100, 101, 102])),
        (200, set(&[200, 201])),
    ]));
    let windows = [window("window-b", 200, "2"), window("window-a", 100, "1")];
    let clients = [
        client(201, "unregistered"),
        client(102, "agent-a"),
        client(101, "agent-a"),
        client(200, "another-unregistered"),
    ];
    let got = map(&windows, Some(&processes), &clients, &agents(&["agent-a"])).unwrap();
    assert_eq!(got.len(), 1, "{got:?}");
    assert_eq!(got[0].agent, "agent-a");
    assert_eq!(got[0].window.id, "window-a");
}

struct BatchFake {
    sets: HashMap<i64, PidSet>,
    calls: Cell<usize>,
}

impl ProcessLister for BatchFake {
    fn descendants(&self, root: i64) -> io::Result<PidSet> {
        Ok(self.sets.get(&root).cloned().unwrap_or_default())
    }
    fn batch(&self) -> Option<&dyn BatchProcessLister> {
        Some(self)
    }
}

impl BatchProcessLister for BatchFake {
    fn descendants_many(&self, roots: &[i64]) -> io::Result<HashMap<i64, PidSet>> {
        self.calls.set(self.calls.get() + 1);
        Ok(roots
            .iter()
            .filter_map(|root| self.sets.get(root).map(|s| (*root, s.clone())))
            .collect())
    }
}

#[test]
fn map_uses_one_batch_process_snapshot_for_multiple_windows() {
    let processes = BatchFake {
        sets: HashMap::from([(100, set(&[100, 101])), (200, set(&[200, 201]))]),
        calls: Cell::new(0),
    };
    let windows = [window("a", 100, ""), window("b", 200, "")];
    let clients = [client(101, "agent-a"), client(201, "agent-b")];
    let got = map(
        &windows,
        Some(&processes),
        &clients,
        &agents(&["agent-a", "agent-b"]),
    )
    .unwrap();
    assert_eq!(processes.calls.get(), 1);
    assert_eq!(got.len(), 2, "{got:?}");
}

#[test]
fn map_requires_process_lister_for_windows() {
    let err = map(&[window("window", 100, "")], None, &[], &HashSet::new()).unwrap_err();
    assert_eq!(err.to_string(), "process lister is unavailable");
}

#[test]
fn proc_lister_batch_matches_single_walks() {
    let root = temp_root("batch");
    write_proc_stat(&root, 10, 1, "a");
    write_proc_stat(&root, 11, 10, "b");
    write_proc_stat(&root, 20, 1, "c");
    let processes = ProcLister { root: root.clone() };
    let many = processes
        .batch()
        .expect("ProcLister is a batch lister")
        .descendants_many(&[10, 20, 99])
        .unwrap();
    assert_eq!(many.get(&10), Some(&set(&[10, 11])));
    assert_eq!(many.get(&20), Some(&set(&[20])));
    assert!(!many.contains_key(&99));
    std::fs::remove_dir_all(&root).unwrap();
}
