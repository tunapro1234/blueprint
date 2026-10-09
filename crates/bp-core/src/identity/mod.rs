//! Port of internal/identity/identity.go.
//!
//! Identity answers one question for every outbound message: who is sending
//! it. The answer becomes a visible label — the "[sender]" envelope on
//! agent-to-agent messages and the "[agent]" prefix on WhatsApp — so a wrong
//! answer is not a cosmetic bug: agents act on the name they read.
//!
//! `tmux display-message -p '#S'` with no target is only meaningful INSIDE a
//! pane. Called from cron, a systemd unit or any other process with no TMUX in
//! its environment, tmux answers for whichever client happens to be attached
//! — a spectator, not the sender. Everything below exists so that mistake has
//! exactly one place to live, and so an unattributable message says "unknown"
//! instead of borrowing the orchestrator's name.
//!
//! Test seams: every process-global input (environment, `/proc`, `ps`, the
//! tmux client) is injectable through [`Options`], [`Sessioner`] or the
//! `*_at` functions taking a proc root.

mod origin;

#[cfg(test)]
mod tests;

use std::path::Path;

pub use origin::{
    Origin, calling_pane, calling_pane_at, codex_ancestry_ps, codex_origin, codex_origin_at,
    codex_origin_chain, inspect_ps_process,
};

use crate::gopath;

/// The sliver of the tmux client this module needs. It is a trait so a test
/// can prove `display_session` is never consulted with TMUX empty.
pub trait Sessioner {
    /// `tmux display-message -p '#S'`; the error text is diagnostic only.
    fn display_session(&self) -> Result<String, String>;
}

/// The label for a sender we cannot name. It is deliberately a word no agent
/// answers to: readers must see the gap rather than trust a guess.
pub const UNKNOWN: &str = "unknown";

/// Separates a guessed label from a stated one. It cannot appear in a valid
/// agent name (see [`valid_name`]), so "cron?:inbox_watcher.py" is
/// structurally impossible to mistake for an agent.
pub const INFER_MARK: &str = "?";

/// A label plus how much the label is worth.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct Identity {
    pub label: String,
    pub thread_id: String,
    pub parent: String,
    /// Attribution confidence, not execution authority. [`Identity::authoritative`]
    /// separately excludes declarations and CLI subagents.
    pub certain: bool,
    /// Names the signal that won, for diagnostics and warnings.
    pub source: String,
    pub reason: String,
}

impl Identity {
    /// Whether the label confesses a guess.
    pub fn inferred(&self) -> bool {
        self.label.contains(INFER_MARK)
    }

    /// Permits existing hierarchy/force gates only for verified main agents.
    /// A subagent can be attributed without inheriting its parent's powers.
    pub fn authoritative(&self) -> bool {
        self.certain
            && self.parent.is_empty()
            && (self.source == "tmux" || self.source == "codex-thread")
    }
}

type StrFn<'a, R> = Box<dyn Fn(&str) -> R + 'a>;

/// Tunes the resolution for a given call site. Every `None` uses the real
/// process-global implementation.
#[derive(Default)]
pub struct Options<'a> {
    /// Execution-context probe; `None` uses [`codex_origin`].
    pub origin: Option<Box<dyn Fn() -> Origin + 'a>>,
    /// Supplies a kernel-ancestry-verified pane context, never thread authority.
    pub pane: Option<Box<dyn Fn() -> Result<String, String> + 'a>>,
    /// Looks up the identity pinned to a Codex thread id.
    pub thread: Option<StrFn<'a, Identity>>,
    /// A sender stated by the caller (`bp wa send --from`). Honored only
    /// outside tmux. An invalid value is ignored here; call sites reject it
    /// loudly with [`valid_from`] first.
    pub from: String,
    /// Agentbook membership. A derived name that collides with a real agent is
    /// downgraded to a guess. `None` means "nothing is known".
    pub known: Option<StrFn<'a, bool>>,
    /// Verifies the caller -> parent -> bp daemon -> PID 1 chain for a pid.
    /// `None` uses [`daemon_child`].
    pub daemon_child: Option<Box<dyn Fn(i32) -> bool + 'a>>,
    /// Allows explicitly uncertain process-tree attribution for scripts.
    pub infer: bool,
    /// The label of last resort; empty means [`UNKNOWN`].
    pub fallback: String,
    /// The process chain, nearest first, each entry a split command line.
    /// `None` reads `/proc`.
    pub ancestors: Option<Box<dyn Fn() -> Vec<Vec<String>> + 'a>>,
    /// Environment lookup (`os.Getenv`); `None` reads the real environment.
    pub env: Option<StrFn<'a, String>>,
}

/// The real environment lookup (`os.Getenv`: unset and non-UTF-8 read as "").
pub fn real_env(key: &str) -> String {
    std::env::var(key).unwrap_or_default()
}

/// Whether `name` has agent-name shape (`^[A-Za-z0-9][A-Za-z0-9._-]*$`).
pub fn valid_name(name: &str) -> bool {
    let mut bytes = name.bytes();
    match bytes.next() {
        Some(b) if b.is_ascii_alphanumeric() => {}
        _ => return false,
    }
    bytes.all(|b| b.is_ascii_alphanumeric() || b == b'.' || b == b'_' || b == b'-')
}

/// Keeps an envelope readable and bounded; nothing legitimate is close.
pub const MAX_LABEL: usize = 120;

/// Why [`valid_from`] refused a stated sender.
#[derive(Debug, Clone, PartialEq, Eq, thiserror::Error)]
pub enum FromError {
    #[error("--from requires a label")]
    Empty,
    #[error("--from must not start or end with whitespace")]
    Whitespace,
    #[error("--from is too long (max {MAX_LABEL} bytes)")]
    TooLong,
    #[error("--from must be valid UTF-8")]
    InvalidUtf8,
    #[error("--from must not contain [ or ]: they are the envelope's own markers")]
    Brackets,
    #[error("--from must not contain control characters")]
    Control,
}

/// Accepts the values a caller may state as its own name. A path or a
/// "cron:<path>" origin must stay expressible, so this is a rejection list:
/// anything that could forge envelope structure, smuggle a second line, or
/// hide as a control byte is refused.
pub fn valid_from(value: &str) -> Result<(), FromError> {
    valid_from_bytes(value.as_bytes())
}

/// [`valid_from`] for raw bytes (Go strings may hold invalid UTF-8).
pub fn valid_from_bytes(value: &[u8]) -> Result<(), FromError> {
    if value.is_empty() {
        return Err(FromError::Empty);
    }
    let lossy = String::from_utf8_lossy(value);
    if crate::text::go_trim_space(&lossy) != lossy {
        return Err(FromError::Whitespace);
    }
    if value.len() > MAX_LABEL {
        return Err(FromError::TooLong);
    }
    let Ok(text) = std::str::from_utf8(value) else {
        return Err(FromError::InvalidUtf8);
    };
    if text.contains(['[', ']']) {
        return Err(FromError::Brackets);
    }
    if text.chars().any(|r| (r as u32) < 0x20 || r as u32 == 0x7f) {
        return Err(FromError::Control);
    }
    Ok(())
}

/// Separates a verified pane/thread from a self-declared label. Codex never
/// falls back to inherited tmux, AGENT or login names. Other unverified agent
/// claims are visibly marked and cannot carry hierarchy authority.
pub fn resolve(client: Option<&dyn Sessioner>, opts: &Options<'_>) -> Identity {
    let getenv = |key: &str| -> String {
        match &opts.env {
            Some(env) => env(key),
            None => real_env(key),
        }
    };
    let origin = match &opts.origin {
        Some(probe) => probe(),
        None => match &opts.env {
            Some(env) => codex_origin_with_env(env.as_ref()),
            None => codex_origin(),
        },
    };
    if !origin.thread_id.is_empty() {
        if let Some(thread) = &opts.thread {
            let mut who = thread(&origin.thread_id);
            if !who.label.is_empty() {
                if who.source == "codex-local-hint" {
                    if who.inferred() {
                        who.thread_id = origin.thread_id.clone();
                        who.certain = false;
                        return who;
                    }
                    // Local runtime evidence is intentionally only a hint.
                    // Refuse one whose visible label does not confess that.
                } else if origin.verified {
                    return who;
                } else if who.certain && who.source == "codex-thread" {
                    if let Some(pane) = &opts.pane
                        && let Ok(name) = pane()
                        && name == who.label
                    {
                        // The pin and pane agree on a label; codex-pane remains
                        // excluded from authoritative's power checks.
                        return Identity {
                            label: who.label,
                            thread_id: origin.thread_id,
                            certain: true,
                            source: "codex-pane".into(),
                            ..Identity::default()
                        };
                    }
                    if origin.server_thread {
                        return Identity {
                            label: who.label,
                            thread_id: origin.thread_id,
                            certain: true,
                            source: "codex-app-server".into(),
                            ..Identity::default()
                        };
                    }
                    who.label.push('?');
                    who.thread_id = origin.thread_id;
                    who.certain = false;
                    who.source = "codex-unverified".into();
                    return who;
                } else if who.certain && who.source == "codex-subagent" {
                    // A registry match can provide a readable hint without
                    // proving this caller owns the thread.
                    who.label.push('?');
                    who.thread_id = origin.thread_id;
                    who.certain = false;
                    who.source = "codex-unverified".into();
                    return who;
                }
            }
        }
        return Identity {
            label: format!("codex?:{}", sanitize(&origin.thread_id)),
            thread_id: origin.thread_id,
            source: "codex-unverified".into(),
            ..Identity::default()
        };
    }
    if origin.codex_detected {
        return Identity {
            label: UNKNOWN.into(),
            source: "codex-unverified".into(),
            reason: "Codex caller has no thread evidence".into(),
            ..Identity::default()
        };
    }
    if !getenv("TMUX").is_empty()
        && let Some(client) = client
        && let Ok(value) = client.display_session()
    {
        let value = crate::text::go_trim_space(&value);
        if !value.is_empty() {
            return Identity {
                label: value.to_string(),
                certain: true,
                source: "tmux".into(),
                ..Identity::default()
            };
        }
    }
    let known = |name: &str| -> bool { opts.known.as_ref().is_some_and(|known| known(name)) };
    let mut reason = "no usable sender evidence".to_string();
    if let Some(pane) = &opts.pane {
        match pane() {
            Ok(name) if valid_name(&name) && known(&name) => {
                // CLI subagents can share this process. A readable parent
                // context cannot grant that agent's hierarchy or force rights.
                return Identity {
                    label: format!("{name}?"),
                    parent: name,
                    source: "pane-process-context".into(),
                    ..Identity::default()
                };
            }
            Ok(_) => {}
            Err(err) => reason = err,
        }
    }
    if !opts.from.is_empty() && valid_from(&opts.from).is_ok() {
        if valid_name(&opts.from) {
            return Identity {
                label: format!("declared?:{}", opts.from),
                source: "--from".into(),
                ..Identity::default()
            };
        }
        return Identity {
            label: opts.from.clone(),
            certain: true,
            source: "--from".into(),
            ..Identity::default()
        };
    }
    let agent = getenv("AGENT");
    if !agent.is_empty() && valid_name(&agent) {
        let pid = std::process::id() as i32;
        let is_daemon_child = match &opts.daemon_child {
            Some(check) => check(pid),
            None => daemon_child(pid),
        };
        if is_daemon_child {
            return Identity {
                label: agent,
                certain: true,
                source: "bp-daemon-child".into(),
                ..Identity::default()
            };
        }
        return Identity {
            label: format!("agent?:{agent}"),
            source: "AGENT".into(),
            ..Identity::default()
        };
    }
    // Root login is disabled on the box, so people SSH as themselves and
    // reach bp through sudo: these name that human.
    for key in ["SUDO_USER", "USER", "LOGNAME"] {
        let value = getenv(key);
        if value.is_empty() || value == "root" || !valid_name(&value) {
            continue;
        }
        if value == "server-main" || known(&value) {
            // A login name that happens to be an agent's name must not
            // inherit that agent's standing.
            return Identity {
                label: format!("user{INFER_MARK}:{value}"),
                source: format!("{key}-collision"),
                ..Identity::default()
            };
        }
        return Identity {
            label: value,
            certain: true,
            source: key.into(),
            ..Identity::default()
        };
    }
    if opts.infer {
        let chain = match &opts.ancestors {
            Some(ancestors) => ancestors(),
            None => {
                let ppid = rustix::process::getppid().map_or(0, |p| p.as_raw_nonzero().get());
                proc_ancestors_at(Path::new("/proc"), ppid, 8)
            }
        };
        let label = inferred(&chain);
        if !label.is_empty() {
            return Identity {
                label,
                source: "process-tree".into(),
                ..Identity::default()
            };
        }
    }
    if !opts.fallback.is_empty() {
        return Identity {
            label: format!("fallback?:{}", sanitize(&opts.fallback)),
            source: "fallback".into(),
            ..Identity::default()
        };
    }
    Identity {
        label: UNKNOWN.into(),
        source: "none".into(),
        reason,
        ..Identity::default()
    }
}

fn codex_origin_with_env(env: &dyn Fn(&str) -> String) -> Origin {
    let pid = std::process::id() as i32;
    let ppid = rustix::process::getppid().map_or(0, |p| p.as_raw_nonzero().get());
    codex_origin_at(
        &env("CODEX_THREAD_ID"),
        pid,
        ppid,
        Path::new("/proc"),
        &inspect_ps_process,
    )
}

/// Programs that say nothing about who is calling: the script they are
/// running does.
const INTERPRETERS: &[&str] = &[
    "sh", "bash", "zsh", "dash", "ksh", "fish", "env", "sudo", "nohup", "timeout", "xargs",
    "python", "python3", "perl", "ruby", "node", "deno", "bwrap",
];

/// Maps a recognizable ancestor to the origin worth reporting.
fn launcher(name: &str) -> Option<&'static str> {
    Some(match name {
        "cron" | "crond" | "cronie" | "anacron" | "atd" => "cron",
        "systemd" => "systemd",
        "sshd" => "ssh",
        _ => return None,
    })
}

/// Builds a label from the process tree. Every label produced here carries
/// [`INFER_MARK`], and a label that somehow would not is dropped.
pub fn inferred(chain: &[Vec<String>]) -> String {
    if chain.is_empty() || chain[0].is_empty() {
        return String::new();
    }
    let mut kind = String::new();
    for frame in chain {
        let Some(first) = frame.first() else { continue };
        if let Some(mapped) = launcher(&crate::text::go_to_lower(&program(first))) {
            kind = mapped.to_string();
            break;
        }
    }
    if kind.is_empty() {
        kind = crate::text::go_to_lower(&program(&chain[0][0]));
    }
    let mut detail = String::new();
    'frames: for frame in chain {
        for arg in frame.iter().skip(1) {
            if scriptish(arg) {
                detail = program(arg);
                break 'frames;
            }
        }
    }
    let (mut kind, detail) = (sanitize(&kind), sanitize(&detail));
    if kind.is_empty() && detail.is_empty() {
        return String::new();
    }
    if kind.is_empty() {
        kind = "proc".into();
    }
    let mut label = format!("{kind}{INFER_MARK}");
    if !detail.is_empty() && detail != kind {
        label.push(':');
        label.push_str(&detail);
    }
    if !label.contains(INFER_MARK) || valid_name(&label) {
        return String::new(); // never hand back an agent-shaped name
    }
    label
}

/// Strips a path and a login shell's leading dash.
fn program(arg: &str) -> String {
    gopath::base(arg).trim_start_matches('-').to_string()
}

/// Whether an argument names the work being done rather than how it is being
/// run: a path, or a file with an extension.
fn scriptish(arg: &str) -> bool {
    if arg.is_empty() || arg.starts_with('-') {
        return false;
    }
    // A shell's -c command string is not a script: its last path is whatever
    // the command happens to mention (issue #9).
    if arg.contains([' ', '\t', '\n']) {
        return false;
    }
    let base = program(arg);
    if base.is_empty()
        || base == "bp"
        || INTERPRETERS.contains(&crate::text::go_to_lower(&base).as_str())
    {
        return false;
    }
    arg.contains('/') || base.contains('.')
}

/// Keeps a label's pieces to characters that cannot disturb an envelope or a
/// log line, and bounded in length.
pub fn sanitize(value: &str) -> String {
    let mut out = String::new();
    for r in value.chars() {
        if r.is_ascii_alphanumeric() || r == '.' || r == '_' || r == '-' {
            out.push(r);
        }
        if out.len() >= 40 {
            break;
        }
    }
    out.trim_matches(['.', '-', '_']).to_string()
}

/// Reads command lines up the process tree, nearest first.
pub fn proc_ancestors_at(proc_root: &Path, mut pid: i32, limit: usize) -> Vec<Vec<String>> {
    let mut chain = Vec::new();
    let mut hop = 0;
    while hop < limit && pid > 1 {
        let args = proc_cmdline_at(proc_root, pid);
        if !args.is_empty() {
            chain.push(args);
        }
        match proc_parent_at(proc_root, pid) {
            Some(parent) if parent != pid => pid = parent,
            _ => break,
        }
        hop += 1;
    }
    chain
}

/// `/proc/<pid>/cmdline` split on NUL with empty parts dropped.
pub fn proc_cmdline_at(proc_root: &Path, pid: i32) -> Vec<String> {
    let Ok(data) = std::fs::read(proc_root.join(pid.to_string()).join("cmdline")) else {
        return Vec::new();
    };
    String::from_utf8_lossy(&data)
        .split('\0')
        .filter(|part| !part.is_empty())
        .map(str::to_string)
        .collect()
}

/// PPid from `/proc/<pid>/status`. status is used instead of stat because a
/// comm containing spaces or parentheses makes stat's fields ambiguous.
pub fn proc_parent_at(proc_root: &Path, pid: i32) -> Option<i32> {
    let data = std::fs::read(proc_root.join(pid.to_string()).join("status")).ok()?;
    let text = String::from_utf8_lossy(&data);
    for line in text.split('\n') {
        if let Some(rest) = line.strip_prefix("PPid:") {
            return crate::text::go_trim_space(rest).parse().ok();
        }
    }
    None
}

/// Whether pid's exact parent chain is caller -> parent -> bp daemon -> PID 1.
/// The daemon executable is checked by basename because the service and its
/// CLI children may use different bp installations.
pub fn daemon_child(pid: i32) -> bool {
    daemon_child_at(Path::new("/proc"), pid)
}

/// [`daemon_child`] against an arbitrary proc root.
pub fn daemon_child_at(proc_root: &Path, pid: i32) -> bool {
    if pid <= 1 {
        return false;
    }
    let Some(bridge) = proc_parent_at(proc_root, pid).filter(|p| *p > 1) else {
        return false;
    };
    let Some(daemon) = proc_parent_at(proc_root, bridge).filter(|p| *p > 1) else {
        return false;
    };
    if proc_parent_at(proc_root, daemon) != Some(1) {
        return false;
    }
    let base = proc_root.join(daemon.to_string());
    let Ok(target) = std::fs::read_link(base.join("exe")) else {
        return false;
    };
    let target = target.to_string_lossy();
    if gopath::base(target.strip_suffix(" (deleted)").unwrap_or(&target)) != "bp" {
        return false;
    }
    let Ok(command) = std::fs::read(base.join("cmdline")) else {
        return false;
    };
    let command = String::from_utf8_lossy(&command);
    let args: Vec<&str> = command.split('\0').collect();
    args.len() > 1 && args[1] == "daemon"
}
