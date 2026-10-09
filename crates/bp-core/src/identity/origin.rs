//! Port of internal/identity/origin.go.

use std::collections::HashSet;
use std::path::Path;
use std::process::Command;

use crate::gopath;

use super::{proc_cmdline_at, proc_parent_at};

/// Execution context, separate from a caller's proposed display name. It
/// prevents inherited app-server tmux state from identifying another thread.
/// It is not a boundary against processes with the same OS credentials.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct Origin {
    pub thread_id: String,
    pub verified: bool,
    pub codex_detected: bool,
    pub server_thread: bool,
}

fn parent_pid() -> i32 {
    rustix::process::getppid().map_or(0, |p| p.as_raw_nonzero().get())
}

/// Verifies the per-execution Codex sandbox helper and recognizes a
/// thread-bound child of a shared app-server. An env assignment on bp alone
/// cannot establish a different thread. Inaccessible evidence fails closed.
pub fn codex_origin() -> Origin {
    codex_origin_at(
        &super::real_env("CODEX_THREAD_ID"),
        std::process::id() as i32,
        parent_pid(),
        Path::new("/proc"),
        &inspect_ps_process,
    )
}

/// [`codex_origin`] with every input injected: the thread hint, this
/// process, its parent, the proc root and the `ps` fallback.
pub fn codex_origin_at(
    hint: &str,
    self_pid: i32,
    parent: i32,
    proc_root: &Path,
    inspect: &dyn Fn(i32) -> Option<(i32, String)>,
) -> Origin {
    let mut result = codex_origin_chain(hint, self_pid, parent, proc_root);
    if !result.codex_detected {
        result.codex_detected = codex_ancestry_ps(parent, inspect);
    }
    result
}

/// Walks the `/proc` chain from `pid` (whose child is `self_pid`, 0 when
/// unknown) looking for Codex evidence.
pub fn codex_origin_chain(hint: &str, self_pid: i32, mut pid: i32, proc_root: &Path) -> Origin {
    let mut result = Origin {
        thread_id: hint.to_string(),
        ..Origin::default()
    };
    let mut previous = self_pid;
    let mut seen = HashSet::new();
    let mut hops = 0;
    while pid > 0 && hops < 64 && seen.insert(pid) {
        hops += 1;
        let base = proc_root.join(pid.to_string());
        let Ok(command) = std::fs::read(base.join("cmdline")) else {
            return result;
        };
        let command = String::from_utf8_lossy(&command);
        let args: Vec<&str> = command.split('\0').collect();
        if args[0].is_empty() {
            return result;
        }
        let is_codex = std::fs::read_link(base.join("exe")).is_ok_and(|target| {
            let target = target.to_string_lossy();
            gopath::base(target.strip_suffix(" (deleted)").unwrap_or(&target)) == "codex"
        });
        // The per-execution sandbox helper receives the thread's context.
        // Shared app-server/exec-server processes do not: their startup env
        // may belong to another thread, so never authenticate from those.
        if is_codex {
            result.codex_detected = true;
            if gopath::base(args[0]) == "codex-linux-sandbox" {
                if hint.is_empty() {
                    return result;
                }
                result.verified = process_has_thread_hint(proc_root, pid, hint);
                return result;
            }
            let app_server = args[1..].contains(&"app-server");
            let exec_server = args[1..].contains(&"exec-server");
            if exec_server {
                return result;
            }
            if app_server {
                if previous > 0 && process_has_thread_hint(proc_root, previous, hint) {
                    result.server_thread = true;
                }
                return result;
            }
            // A naked Codex CLI is shared by its root thread and CLI
            // subagents; detecting it must never turn a hint into authority.
            return result;
        }
        let Ok(status) = std::fs::read(base.join("status")) else {
            return result;
        };
        let status = String::from_utf8_lossy(&status);
        let mut parent = 0;
        for line in status.split('\n') {
            if let Some(value) = line.strip_prefix("PPid:") {
                parent = crate::text::go_trim_space(value).parse().unwrap_or(0);
                break;
            }
        }
        previous = pid;
        pid = parent;
    }
    result
}

fn process_has_thread_hint(proc_root: &Path, pid: i32, hint: &str) -> bool {
    if hint.is_empty() || pid <= 0 {
        return false;
    }
    let Ok(env) = std::fs::read(proc_root.join(pid.to_string()).join("environ")) else {
        return false;
    };
    let env = String::from_utf8_lossy(&env);
    for entry in env.split('\0') {
        if let Some(value) = entry.strip_prefix("CODEX_THREAD_ID=") {
            return value == hint;
        }
    }
    false
}

/// The fail-safe path for hosts without readable `/proc` (most notably
/// macOS). `ps` can establish only that a Codex process is an ancestor; it
/// can never authenticate a thread or upgrade authority. `inspect` returns
/// `(parent, command)` for a pid.
pub fn codex_ancestry_ps(mut pid: i32, inspect: &dyn Fn(i32) -> Option<(i32, String)>) -> bool {
    let mut seen = HashSet::new();
    let mut hops = 0;
    while pid > 0 && hops < 64 && seen.insert(pid) {
        hops += 1;
        let Some((parent, command)) = inspect(pid) else {
            return false;
        };
        let name = gopath::base(crate::text::go_trim_space(&command));
        if name == "codex" || name == "codex-linux-sandbox" {
            return true;
        }
        pid = parent;
    }
    false
}

/// `ps -p <pid> -o ppid= -o comm=` as `(parent, command)`.
pub fn inspect_ps_process(pid: i32) -> Option<(i32, String)> {
    let output = Command::new("ps")
        .args(["-p", &pid.to_string(), "-o", "ppid=", "-o", "comm="])
        .output()
        .ok()?;
    if !output.status.success() {
        return None;
    }
    let text = String::from_utf8_lossy(&output.stdout);
    let value = crate::text::go_trim_space(&text);
    let cut = value.find([' ', '\t'])?;
    if cut < 1 {
        return None;
    }
    let parent = value[..cut].parse().ok()?;
    let command = crate::text::go_trim_space(&value[cut..]);
    (!command.is_empty()).then(|| (parent, command.to_string()))
}

/// Proves that the reported tmux pane process is in this caller's process
/// chain. A standalone app-server is never a pane identity, even when its
/// launcher was once attached to that pane. `ps` supplies ancestry on macOS.
pub fn calling_pane(pane_pid: i32) -> bool {
    calling_pane_at(
        Path::new("/proc"),
        std::process::id() as i32,
        pane_pid,
        &|pid| {
            let output = Command::new("ps")
                .args(["-p", &pid.to_string(), "-o", "ppid=", "-o", "args="])
                .output()
                .ok()
                .filter(|o| o.status.success())?;
            let text = String::from_utf8_lossy(&output.stdout).into_owned();
            let fields: Vec<String> = text.split_ascii_whitespace().map(str::to_string).collect();
            if fields.len() < 2 {
                return None;
            }
            let parent = fields[0].parse().ok()?;
            Some((parent, fields[1..].to_vec()))
        },
    )
}

/// [`calling_pane`] with the proc root, start pid and `ps` fallback injected;
/// `ps_args` returns `(parent, argv)` for a pid.
pub fn calling_pane_at(
    proc_root: &Path,
    self_pid: i32,
    pane_pid: i32,
    ps_args: &dyn Fn(i32) -> Option<(i32, Vec<String>)>,
) -> bool {
    if pane_pid <= 0 {
        return false;
    }
    let mut seen = HashSet::new();
    let mut pid = self_pid;
    let mut hops = 0;
    while pid > 0 && hops < 64 && seen.insert(pid) {
        hops += 1;
        let mut args = proc_cmdline_at(proc_root, pid);
        let parent = match proc_parent_at(proc_root, pid) {
            Some(parent) => parent,
            None => match ps_args(pid) {
                Some((parent, ps_args)) => {
                    args = ps_args;
                    parent
                }
                None => return false,
            },
        };
        if args.len() > 1
            && args[1..]
                .iter()
                .any(|arg| arg == "app-server" || arg == "exec-server")
        {
            return false;
        }
        if pid == pane_pid {
            return true;
        }
        pid = parent;
    }
    false
}
