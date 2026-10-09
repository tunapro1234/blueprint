//! Port of internal/windowmap: maps registered agents to the compositor
//! windows whose process trees contain their attached tmux clients.

use std::collections::{HashMap, HashSet, VecDeque};
use std::fs;
use std::io;
use std::path::PathBuf;
use std::process::Command;

use crate::compositor::Window;
use crate::types::AttachedClient;

/// A set of process ids.
pub type PidSet = HashSet<i64>;

/// `ProcessLister`: the descendants (root included) of one process.
/// A root that does not exist is an `io::ErrorKind::NotFound` error.
pub trait ProcessLister {
    fn descendants(&self, root: i64) -> io::Result<PidSet>;

    /// `BatchProcessLister`, when this lister can answer many roots from one
    /// snapshot.
    fn batch(&self) -> Option<&dyn BatchProcessLister> {
        None
    }
}

/// `BatchProcessLister`: descendants of many roots from one snapshot; roots
/// that do not exist are absent from the result.
pub trait BatchProcessLister {
    fn descendants_many(&self, roots: &[i64]) -> io::Result<HashMap<i64, PidSet>>;
}

/// `Mapping`: one agent shown in one window.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Mapping {
    pub agent: String,
    pub window: Window,
}

/// Errors of [`map`], with Go's message text.
#[derive(Debug, thiserror::Error)]
pub enum MapError {
    #[error("process lister is unavailable")]
    NoProcessLister,
    #[error("list window process descendants: {0}")]
    Batch(#[source] io::Error),
    #[error("list descendants of window {id}: {source}")]
    Window {
        id: String,
        #[source]
        source: io::Error,
    },
}

/// `Map` returns, sorted by agent then window id, every registered agent
/// (`agents`) whose attached client runs inside a window's process tree.
pub fn map(
    windows: &[Window],
    processes: Option<&dyn ProcessLister>,
    clients: &[AttachedClient],
    agents: &HashSet<String>,
) -> Result<Vec<Mapping>, MapError> {
    let mut by_pid: HashMap<i64, Vec<&str>> = HashMap::new();
    for client in clients {
        if client.pid > 0 && agents.contains(&client.session) {
            by_pid
                .entry(i64::from(client.pid))
                .or_default()
                .push(&client.session);
        }
    }
    let mut roots = Vec::with_capacity(windows.len());
    let mut root_seen = HashSet::new();
    for window in windows {
        if root_seen.insert(window.pid) {
            roots.push(window.pid);
        }
    }
    if roots.is_empty() {
        return Ok(Vec::new());
    }
    let processes = processes.ok_or(MapError::NoProcessLister)?;
    let mut descendant_sets: HashMap<i64, PidSet> = HashMap::new();
    if let Some(batch) = processes.batch() {
        descendant_sets = batch.descendants_many(&roots).map_err(MapError::Batch)?;
    } else {
        let mut attempted = HashSet::new();
        for window in windows {
            if !attempted.insert(window.pid) {
                continue;
            }
            match processes.descendants(window.pid) {
                Ok(set) => {
                    descendant_sets.insert(window.pid, set);
                }
                Err(err) if err.kind() == io::ErrorKind::NotFound => {}
                Err(source) => {
                    return Err(MapError::Window {
                        id: window.id.clone(),
                        source,
                    });
                }
            }
        }
    }
    let mut mappings = Vec::new();
    for window in windows {
        let Some(descendants) = descendant_sets.get(&window.pid) else {
            continue;
        };
        let mut seen = HashSet::new();
        for pid in descendants {
            for session in by_pid.get(pid).into_iter().flatten() {
                if seen.insert(*session) {
                    mappings.push(Mapping {
                        agent: session.to_string(),
                        window: window.clone(),
                    });
                }
            }
        }
    }
    mappings.sort_by(|a, b| {
        a.agent
            .cmp(&b.agent)
            .then_with(|| a.window.id.cmp(&b.window.id))
    });
    Ok(mappings)
}

/// `DefaultProcessLister`: /proc on Linux, `ps` elsewhere.
pub fn default_process_lister() -> Box<dyn ProcessLister> {
    if cfg!(target_os = "linux") {
        Box::new(ProcLister::default())
    } else {
        Box::new(PsLister::default())
    }
}

fn check_roots(roots: &[i64]) -> io::Result<()> {
    for pid in roots {
        if *pid <= 0 {
            return Err(io::Error::other(format!("invalid process id {pid}")));
        }
    }
    Ok(())
}

fn single(lister: &dyn BatchProcessLister, root: i64) -> io::Result<PidSet> {
    let mut sets = lister.descendants_many(&[root])?;
    sets.remove(&root)
        .ok_or_else(|| io::Error::from(io::ErrorKind::NotFound))
}

/// `ProcLister` reads a /proc tree; `root` defaults to "/proc" and is
/// injectable for tests.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct ProcLister {
    pub root: PathBuf,
}

impl ProcessLister for ProcLister {
    fn descendants(&self, root: i64) -> io::Result<PidSet> {
        single(self, root)
    }

    fn batch(&self) -> Option<&dyn BatchProcessLister> {
        Some(self)
    }
}

impl BatchProcessLister for ProcLister {
    fn descendants_many(&self, roots: &[i64]) -> io::Result<HashMap<i64, PidSet>> {
        check_roots(roots)?;
        let root = if self.root.as_os_str().is_empty() {
            PathBuf::from("/proc")
        } else {
            self.root.clone()
        };
        let mut children: HashMap<i64, Vec<i64>> = HashMap::new();
        let mut found = HashSet::new();
        for entry in fs::read_dir(&root)? {
            let entry = entry?;
            let name = entry.file_name();
            let Some(pid) = name.to_str().and_then(|n| n.parse::<i64>().ok()) else {
                continue;
            };
            if !entry.file_type().is_ok_and(|t| t.is_dir()) {
                continue;
            }
            let Ok(data) = fs::read(entry.path().join("stat")) else {
                continue;
            };
            let Some(close) = data.iter().rposition(|b| *b == b')') else {
                continue;
            };
            let rest = String::from_utf8_lossy(&data[close + 1..]);
            let fields: Vec<&str> = rest.split_whitespace().collect();
            if fields.len() < 2 {
                continue;
            }
            let Ok(ppid) = fields[1].parse::<i64>() else {
                continue;
            };
            children.entry(ppid).or_default().push(pid);
            found.insert(pid);
        }
        Ok(descendants_for_roots(roots, &children, &found))
    }
}

/// `PSLister` runs `ps -axo pid=,ppid=` (`bin` overrides the executable).
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct PsLister {
    pub bin: String,
}

impl ProcessLister for PsLister {
    fn descendants(&self, root: i64) -> io::Result<PidSet> {
        single(self, root)
    }

    fn batch(&self) -> Option<&dyn BatchProcessLister> {
        Some(self)
    }
}

impl BatchProcessLister for PsLister {
    fn descendants_many(&self, roots: &[i64]) -> io::Result<HashMap<i64, PidSet>> {
        check_roots(roots)?;
        let bin = if self.bin.is_empty() { "ps" } else { &self.bin };
        let output = Command::new(bin)
            .args(["-axo", "pid=,ppid="])
            .output()
            .map_err(|err| io::Error::other(format!("list processes: {err}")))?;
        if !output.status.success() {
            return Err(io::Error::other(format!(
                "list processes: {}",
                crate::compositor::go_exit_status(&output.status)
            )));
        }
        let mut children: HashMap<i64, Vec<i64>> = HashMap::new();
        let mut found = HashSet::new();
        for line in String::from_utf8_lossy(&output.stdout).split('\n') {
            let fields: Vec<&str> = line.split_whitespace().collect();
            if fields.len() != 2 {
                continue;
            }
            let (Ok(pid), Ok(ppid)) = (fields[0].parse::<i64>(), fields[1].parse::<i64>()) else {
                continue;
            };
            children.entry(ppid).or_default().push(pid);
            found.insert(pid);
        }
        Ok(descendants_for_roots(roots, &children, &found))
    }
}

/// `descendantsForRoots`: breadth-first walk of the child table.
fn descendants_for_roots(
    roots: &[i64],
    children: &HashMap<i64, Vec<i64>>,
    found: &HashSet<i64>,
) -> HashMap<i64, PidSet> {
    let mut result = HashMap::with_capacity(roots.len());
    for &root in roots {
        if !found.contains(&root) {
            continue;
        }
        let mut set = PidSet::from([root]);
        let mut queue = VecDeque::from([root]);
        while let Some(pid) = queue.pop_front() {
            for &child in children.get(&pid).into_iter().flatten() {
                if set.insert(child) {
                    queue.push_back(child);
                }
            }
        }
        result.insert(root, set);
    }
    result
}

#[cfg(test)]
mod tests;
