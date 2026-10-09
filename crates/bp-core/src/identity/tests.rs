//! Port of internal/identity/{identity,daemon_child,env,issue9,origin}_test.go.
//!
//! Go's TestMain (env_test.go) unsets CODEX_THREAD_ID so fixtures do not
//! inherit the agent running the suite; here every test injects its own
//! environment and origin probe, so no process environment is read or
//! mutated (Rust tests run in parallel threads).

use std::cell::Cell;
use std::collections::HashMap;
use std::path::Path;

use super::*;

/// Stands in for the tmux client and counts calls, so a test can prove
/// display-message is never consulted outside a pane.
struct FakeSession {
    name: String,
    err: Option<String>,
    calls: Cell<usize>,
}

impl FakeSession {
    fn new(name: &str) -> Self {
        FakeSession {
            name: name.to_string(),
            err: None,
            calls: Cell::new(0),
        }
    }
}

impl Sessioner for FakeSession {
    fn display_session(&self) -> Result<String, String> {
        self.calls.set(self.calls.get() + 1);
        match &self.err {
            Some(err) => Err(err.clone()),
            None => Ok(self.name.clone()),
        }
    }
}

/// Every signal resolve reads is under the test's control: unset keys are
/// empty, exactly like Go's setEnv over TMUX/AGENT/SUDO_USER/USER/LOGNAME.
fn env_of(pairs: &[(&str, &str)]) -> StrFn<'static, String> {
    let map: HashMap<String, String> = pairs
        .iter()
        .map(|(k, v)| (k.to_string(), v.to_string()))
        .collect();
    Box::new(move |key: &str| map.get(key).cloned().unwrap_or_default())
}

fn known_set(names: &'static [&'static str]) -> StrFn<'static, bool> {
    Box::new(move |name: &str| names.contains(&name))
}

fn no_codex() -> Option<Box<dyn Fn() -> Origin>> {
    Some(Box::new(Origin::default))
}

fn fixed_ancestors(frames: &[&[&str]]) -> Option<Box<dyn Fn() -> Vec<Vec<String>>>> {
    let frames: Vec<Vec<String>> = frames
        .iter()
        .map(|f| f.iter().map(|s| s.to_string()).collect())
        .collect();
    Some(Box::new(move || frames.clone()))
}

#[test]
fn resolve_precedence() {
    const IN_TMUX: &str = "/tmp/tmux-0/default,4242,0";
    struct Case {
        name: &'static str,
        env: &'static [(&'static str, &'static str)],
        session: &'static str,
        session_err: Option<&'static str>,
        opts: fn() -> Options<'static>,
        label: &'static str,
        certain: bool,
        source: &'static str,
    }
    let cases = [
        Case {
            name: "1 tmux session outranks from, agent and sudo user",
            env: &[
                ("TMUX", IN_TMUX),
                ("AGENT", "probot-fon"),
                ("SUDO_USER", "tunapro"),
                ("USER", "tunapro"),
            ],
            session: "compec-outreach",
            session_err: None,
            opts: || Options {
                from: "cron:/srv/kavram/x.py".into(),
                infer: true,
                ..Options::default()
            },
            label: "compec-outreach",
            certain: true,
            source: "tmux",
        },
        Case {
            name: "2 from wins outside tmux",
            env: &[
                ("TMUX", ""),
                ("AGENT", "probot-fon"),
                ("SUDO_USER", "tunapro"),
            ],
            session: "server-main",
            session_err: None,
            opts: || Options {
                from: "cron:/srv/kavram/outreach/workers/inbox_watcher.py".into(),
                ..Options::default()
            },
            label: "cron:/srv/kavram/outreach/workers/inbox_watcher.py",
            certain: true,
            source: "--from",
        },
        Case {
            name: "3 agent beats sudo user",
            env: &[
                ("TMUX", ""),
                ("AGENT", "kavram-outreach"),
                ("SUDO_USER", "tunapro"),
                ("USER", "root"),
            ],
            session: "server-main",
            session_err: None,
            opts: Options::default,
            label: "agent?:kavram-outreach",
            certain: false,
            source: "AGENT",
        },
        Case {
            name: "3 malformed agent is ignored",
            env: &[
                ("TMUX", ""),
                ("AGENT", "two words"),
                ("SUDO_USER", "tunapro"),
            ],
            session: "",
            session_err: None,
            opts: Options::default,
            label: "tunapro",
            certain: true,
            source: "SUDO_USER",
        },
        Case {
            name: "4 sudo user names the human",
            env: &[
                ("TMUX", ""),
                ("SUDO_USER", "tunapro"),
                ("USER", "root"),
                ("LOGNAME", "root"),
            ],
            session: "server-main",
            session_err: None,
            opts: Options::default,
            label: "tunapro",
            certain: true,
            source: "SUDO_USER",
        },
        Case {
            name: "4 sudo user root is skipped",
            env: &[("TMUX", ""), ("SUDO_USER", "root"), ("USER", "tunapro")],
            session: "",
            session_err: None,
            opts: Options::default,
            label: "tunapro",
            certain: true,
            source: "USER",
        },
        Case {
            name: "4 logname when user is root",
            env: &[("TMUX", ""), ("USER", "root"), ("LOGNAME", "tunapro")],
            session: "",
            session_err: None,
            opts: Options::default,
            label: "tunapro",
            certain: true,
            source: "LOGNAME",
        },
        Case {
            name: "4 login names with control bytes are skipped",
            env: &[
                ("TMUX", ""),
                ("SUDO_USER", "ada\nserver-main"),
                ("USER", "bad\tname"),
            ],
            session: "",
            session_err: None,
            opts: Options::default,
            label: UNKNOWN,
            certain: false,
            source: "none",
        },
        Case {
            name: "5 inference from the process tree confesses",
            env: &[("TMUX", ""), ("USER", "root")],
            session: "server-main",
            session_err: None,
            opts: || Options {
                infer: true,
                ancestors: fixed_ancestors(&[
                    &[
                        "/bin/sh",
                        "-c",
                        "/srv/kavram/outreach/workers/inbox_watcher.py",
                    ],
                    &["/usr/sbin/CRON", "-f"],
                ]),
                ..Options::default()
            },
            label: "cron?:inbox_watcher.py",
            certain: false,
            source: "process-tree",
        },
        Case {
            name: "5 inference without a launcher names the parent program",
            env: &[("TMUX", "")],
            session: "",
            session_err: None,
            opts: || Options {
                infer: true,
                ancestors: fixed_ancestors(&[&["python3", "/srv/kavram/tools/report.py"]]),
                ..Options::default()
            },
            label: "python3?:report.py",
            certain: false,
            source: "process-tree",
        },
        Case {
            name: "6 no signal at all is unknown, never server-main",
            env: &[("TMUX", "")],
            session: "server-main",
            session_err: None,
            opts: || Options {
                infer: true,
                ancestors: fixed_ancestors(&[]),
                ..Options::default()
            },
            label: UNKNOWN,
            certain: false,
            source: "none",
        },
        Case {
            name: "6 a call site may ask for its own fallback by name",
            env: &[("TMUX", ""), ("USER", "root")],
            session: "",
            session_err: None,
            opts: || Options {
                fallback: "server-main".into(),
                ..Options::default()
            },
            label: "fallback?:server-main",
            certain: false,
            source: "fallback",
        },
        Case {
            name: "a login name that is also an agent is downgraded to a guess",
            env: &[("TMUX", ""), ("SUDO_USER", "probot-fon")],
            session: "",
            session_err: None,
            opts: || Options {
                known: Some(known_set(&["probot-fon", "server-main"])),
                ..Options::default()
            },
            label: "user?:probot-fon",
            certain: false,
            source: "SUDO_USER-collision",
        },
        Case {
            name: "a forged from value is ignored by the resolver",
            env: &[("TMUX", ""), ("SUDO_USER", "tunapro")],
            session: "",
            session_err: None,
            opts: || Options {
                from: "[server-main] ok".into(),
                ..Options::default()
            },
            label: "tunapro",
            certain: true,
            source: "SUDO_USER",
        },
        Case {
            name: "a dead tmux server falls through instead of guessing",
            env: &[("TMUX", IN_TMUX), ("AGENT", "kavram-gate")],
            session: "",
            session_err: Some("no server running on /tmp/tmux-0/default"),
            opts: || Options {
                // The test process is never a bp daemon child.
                daemon_child: Some(Box::new(|_| false)),
                ..Options::default()
            },
            label: "agent?:kavram-gate",
            certain: false,
            source: "AGENT",
        },
    ];
    for case in cases {
        let mut opts = (case.opts)();
        opts.env = Some(env_of(case.env));
        if opts.origin.is_none() {
            opts.origin = no_codex();
        }
        let client = FakeSession {
            name: case.session.to_string(),
            err: case.session_err.map(str::to_string),
            calls: Cell::new(0),
        };
        let got = resolve(Some(&client), &opts);
        assert!(
            got.label == case.label && got.certain == case.certain && got.source == case.source,
            "{}: resolve() = {got:?}, want label={:?} certain={} source={:?}",
            case.name,
            case.label,
            case.certain,
            case.source
        );
        if got.label == "server-main" && opts.fallback.is_empty() && case.session != "server-main" {
            panic!("{}: server-main must never be invented: {got:?}", case.name);
        }
        let tmux = case
            .env
            .iter()
            .find(|(k, _)| *k == "TMUX")
            .map_or("", |(_, v)| v);
        if tmux.is_empty() && client.calls.get() != 0 {
            panic!(
                "{}: display_session called {} times with TMUX empty",
                case.name,
                client.calls.get()
            );
        }
    }
}

#[test]
fn resolve_pinned_codex_pane_label() {
    const THREAD: &str = "01234567-89ab-cdef-0123-456789abcdef";
    let pinned = Identity {
        label: "probot-equity".into(),
        thread_id: THREAD.into(),
        certain: true,
        source: "codex-thread".into(),
        ..Identity::default()
    };
    struct Case {
        name: &'static str,
        origin: Origin,
        thread: Identity,
        pane: Option<Result<&'static str, &'static str>>,
        want: Identity,
        pane_calls: usize,
        thread_calls: usize,
        non_authoritative: bool,
    }
    let unverified = |label: &str, parent: &str| Identity {
        label: label.into(),
        thread_id: THREAD.into(),
        parent: parent.into(),
        source: "codex-unverified".into(),
        ..Identity::default()
    };
    let cases = vec![
        Case {
            name: "pinned thread and same pane produce a clean label",
            origin: Origin {
                thread_id: THREAD.into(),
                server_thread: true,
                ..Origin::default()
            },
            thread: pinned.clone(),
            pane: Some(Ok("probot-equity")),
            want: Identity {
                source: "codex-pane".into(),
                ..pinned.clone()
            },
            pane_calls: 1,
            thread_calls: 1,
            non_authoritative: true,
        },
        Case {
            name: "server thread supplies a clean label after pane mismatch",
            origin: Origin {
                thread_id: THREAD.into(),
                server_thread: true,
                ..Origin::default()
            },
            thread: pinned.clone(),
            pane: Some(Ok("another-agent")),
            want: Identity {
                source: "codex-app-server".into(),
                ..pinned.clone()
            },
            pane_calls: 1,
            thread_calls: 1,
            non_authoritative: true,
        },
        Case {
            name: "pinned thread in a different pane remains unverified",
            origin: Origin {
                thread_id: THREAD.into(),
                ..Origin::default()
            },
            thread: pinned.clone(),
            pane: Some(Ok("another-agent")),
            want: unverified("probot-equity?", ""),
            pane_calls: 1,
            thread_calls: 1,
            non_authoritative: false,
        },
        Case {
            name: "pane error remains unverified",
            origin: Origin {
                thread_id: THREAD.into(),
                ..Origin::default()
            },
            thread: pinned.clone(),
            pane: Some(Err("pane ancestry unavailable")),
            want: unverified("probot-equity?", ""),
            pane_calls: 1,
            thread_calls: 1,
            non_authoritative: false,
        },
        Case {
            name: "subagent stays unverified without checking pane",
            origin: Origin {
                thread_id: THREAD.into(),
                server_thread: true,
                ..Origin::default()
            },
            thread: Identity {
                label: "probot-equity".into(),
                thread_id: THREAD.into(),
                parent: "server-main".into(),
                certain: true,
                source: "codex-subagent".into(),
                ..Identity::default()
            },
            pane: Some(Ok("probot-equity")),
            want: unverified("probot-equity?", "server-main"),
            pane_calls: 0,
            thread_calls: 1,
            non_authoritative: false,
        },
        Case {
            name: "verified origin is returned unchanged without pane lookup",
            origin: Origin {
                thread_id: THREAD.into(),
                verified: true,
                ..Origin::default()
            },
            thread: pinned.clone(),
            pane: Some(Ok("another-agent")),
            want: pinned.clone(),
            pane_calls: 0,
            thread_calls: 1,
            non_authoritative: false,
        },
        Case {
            name: "unpinned thread keeps its UUID hint",
            origin: Origin {
                thread_id: THREAD.into(),
                server_thread: true,
                ..Origin::default()
            },
            thread: Identity::default(),
            pane: None,
            want: unverified(&format!("codex?:{THREAD}"), ""),
            pane_calls: 0,
            thread_calls: 1,
            non_authoritative: false,
        },
        Case {
            name: "Codex without a thread remains unknown",
            origin: Origin {
                codex_detected: true,
                ..Origin::default()
            },
            thread: Identity::default(),
            pane: None,
            want: Identity {
                label: UNKNOWN.into(),
                source: "codex-unverified".into(),
                reason: "Codex caller has no thread evidence".into(),
                ..Identity::default()
            },
            pane_calls: 0,
            thread_calls: 0,
            non_authoritative: false,
        },
    ];
    for case in cases {
        let pane_calls = Cell::new(0);
        let thread_calls = Cell::new(0);
        let origin = case.origin.clone();
        let thread = case.thread.clone();
        let pane = case.pane;
        let opts = Options {
            env: Some(env_of(&[("TMUX", "")])),
            origin: Some(Box::new(move || origin.clone())),
            thread: Some(Box::new(|_: &str| {
                thread_calls.set(thread_calls.get() + 1);
                thread.clone()
            })),
            pane: match pane {
                Some(result) => {
                    let pane_calls = &pane_calls;
                    Some(Box::new(move || {
                        pane_calls.set(pane_calls.get() + 1);
                        result.map(str::to_string).map_err(str::to_string)
                    }))
                }
                None => None,
            },
            ..Options::default()
        };
        let got = resolve(None, &opts);
        assert_eq!(got, case.want, "{}", case.name);
        assert_eq!(
            (pane_calls.get(), thread_calls.get()),
            (case.pane_calls, case.thread_calls),
            "{}: pane/thread calls",
            case.name
        );
        if case.non_authoritative {
            assert!(
                !got.authoritative(),
                "{}: {} became authoritative",
                case.name,
                got.source
            );
        }
    }
}

/// With no TMUX in the environment, `tmux display-message -p '#S'` answers
/// for whichever client is attached — a spectator. It must not even be asked.
#[test]
fn resolve_never_consults_tmux_outside_pane() {
    let variants: Vec<fn() -> Options<'static>> = vec![
        Options::default,
        || Options {
            from: "cron:/srv/kavram/x.py".into(),
            ..Options::default()
        },
        || Options {
            infer: true,
            ancestors: fixed_ancestors(&[&["/usr/sbin/CRON", "-f"]]),
            ..Options::default()
        },
        || Options {
            fallback: "server-main".into(),
            ..Options::default()
        },
    ];
    for make in variants {
        let mut opts = make();
        opts.origin = no_codex();
        opts.env = Some(env_of(&[("TMUX", "")]));
        let client = FakeSession::new("server-main");
        let got = resolve(Some(&client), &opts);
        assert_eq!(
            client.calls.get(),
            0,
            "display_session consulted with TMUX empty"
        );
        assert!(
            !(got.label == "server-main" && opts.fallback.is_empty()),
            "attached spectator leaked into the label: {got:?}"
        );
    }
}

#[test]
fn inferred_labels_are_never_agent_shaped() {
    let chains: &[&[&[&str]]] = &[
        &[&["/usr/sbin/CRON", "-f"]],
        &[
            &[
                "/bin/sh",
                "-c",
                "/srv/kavram/outreach/workers/inbox_watcher.py",
            ],
            &["/usr/sbin/cron", "-f"],
        ],
        &[&["python3", "/srv/kavram/tools/report.py"]],
        &[&["/srv/kavram/outreach/workers/inbox_watcher.py"]],
        &[&["-zsh"]],
        &[&["/lib/systemd/systemd", "--user"]],
        &[&["weird[name]", "arg\nwith\ncontrol"]],
    ];
    for chain in chains {
        let opts = Options {
            origin: no_codex(),
            env: Some(env_of(&[("TMUX", "")])),
            infer: true,
            ancestors: fixed_ancestors(chain),
            ..Options::default()
        };
        let got = resolve(Some(&FakeSession::new("")), &opts);
        assert!(
            !got.certain,
            "chain {chain:?} produced a certain identity: {got:?}"
        );
        if got.label == UNKNOWN {
            continue;
        }
        assert!(
            got.label.contains(INFER_MARK),
            "chain {chain:?} label {:?}",
            got.label
        );
        assert!(
            !valid_name(&got.label),
            "chain {chain:?} agent-shaped {:?}",
            got.label
        );
        assert!(
            !got.label.contains(['[', ']', '\n', '\t']),
            "unsafe label {:?}",
            got.label
        );
    }
}

#[test]
fn valid_from_rejects_forgery() {
    let long = "a".repeat(MAX_LABEL + 1);
    let bad: &[(&str, &[u8])] = &[
        ("open bracket", b"[server-main"),
        ("closing bracket", b"server-main]"),
        ("full envelope", b"[server-main] hello"),
        ("newline", b"cron\nserver-main"),
        ("carriage return", b"cron\rserver-main"),
        ("tab", b"cron\tserver-main"),
        ("nul byte", b"cron\x00server-main"),
        ("delete byte", b"cron\x7f"),
        ("escape sequence", b"\x1b[31mserver-main"),
        ("empty", b""),
        ("leading space", b" cron:/srv/x.py"),
        ("trailing space", b"cron:/srv/x.py "),
        ("invalid utf8", b"cron:\xff\xfe"),
        ("absurdly long", long.as_bytes()),
    ];
    for (name, value) in bad {
        assert!(
            valid_from_bytes(value).is_err(),
            "{name}: accepted {value:?}"
        );
    }
    assert_eq!(
        valid_from_bytes(b"cron:\xff\xfe"),
        Err(FromError::InvalidUtf8)
    );
    assert_eq!(
        valid_from("[x").unwrap_err().to_string(),
        "--from must not contain [ or ]: they are the envelope's own markers"
    );
    for value in [
        "cron:/srv/kavram/outreach/workers/inbox_watcher.py",
        "/srv/kavram/outreach/workers/inbox_watcher.py",
        "cron?:inbox_watcher.py",
        "kavram-outreach",
        "tuna (telefon)",
    ] {
        assert_eq!(valid_from(value), Ok(()), "{value}");
    }
}

#[test]
fn caller_pane_context_is_readable_but_never_authoritative() {
    let mut opts = Options {
        origin: no_codex(),
        env: Some(env_of(&[])),
        known: Some(known_set(&["writer"])),
        pane: Some(Box::new(|| Ok("writer".to_string()))),
        ..Options::default()
    };
    let who = resolve(None, &opts);
    assert!(
        who.label == "writer?"
            && who.source == "pane-process-context"
            && !who.certain
            && !who.authoritative(),
        "{who:?}"
    );
    opts.origin = Some(Box::new(|| Origin {
        codex_detected: true,
        ..Origin::default()
    }));
    let who = resolve(None, &opts);
    assert!(
        who.label == UNKNOWN && !who.authoritative(),
        "shared Codex used a pane fallback: {who:?}"
    );
}

// ---- daemon_child_test.go ----

const FAKE_CALLER_PID: i32 = 400;
const FAKE_BRIDGE_PID: i32 = 300;
const FAKE_DAEMON_PID: i32 = 200;

/// Port of Go's writeFakeProcess: status with PPid, NUL-joined cmdline and an
/// exe symlink under `<proc_root>/<pid>`.
fn write_fake_process(proc_root: &Path, pid: i32, parent: i32, executable: &str, args: &[&str]) {
    let dir = proc_root.join(pid.to_string());
    std::fs::create_dir_all(&dir).unwrap();
    let status = format!("Name:\ttest\nState:\tS (sleeping)\nPPid:\t{parent}\n");
    std::fs::write(dir.join("status"), status).unwrap();
    if !args.is_empty() {
        std::fs::write(dir.join("cmdline"), format!("{}\0", args.join("\0"))).unwrap();
    }
    if !executable.is_empty() {
        std::os::unix::fs::symlink(executable, dir.join("exe")).unwrap();
    }
}

fn write_proc_environment(proc_root: &Path, pid: i32, entries: &[&str]) {
    std::fs::write(
        proc_root.join(pid.to_string()).join("environ"),
        format!("{}\0", entries.join("\0")),
    )
    .unwrap();
}

fn write_valid_daemon_child(root: &Path, executable: &str, daemon_arg: &str, daemon_parent: i32) {
    write_fake_process(root, FAKE_CALLER_PID, FAKE_BRIDGE_PID, "", &["bp", "msg"]);
    write_fake_process(
        root,
        FAKE_BRIDGE_PID,
        FAKE_DAEMON_PID,
        "",
        &["node", "/srv/whatsapp/bridge.js"],
    );
    write_fake_process(
        root,
        FAKE_DAEMON_PID,
        daemon_parent,
        executable,
        &["/srv/blueprint/bp", daemon_arg],
    );
}

#[test]
fn daemon_child_identity_resolution() {
    type Setup = fn(&Path);
    let cases: &[(&str, Setup, bool)] = &[
        (
            "daemon grandparent resolves a certain whatsapp identity",
            |root| write_valid_daemon_child(root, "/srv/blueprint/bp", "daemon", 1),
            true,
        ),
        (
            "daemon as great-grandparent is not verified",
            |root| {
                write_fake_process(root, FAKE_CALLER_PID, FAKE_BRIDGE_PID, "", &["bp", "msg"]);
                write_fake_process(
                    root,
                    FAKE_BRIDGE_PID,
                    250,
                    "",
                    &["node", "/srv/whatsapp/bridge.js"],
                );
                write_fake_process(root, 250, FAKE_DAEMON_PID, "", &["wrapper"]);
                write_fake_process(
                    root,
                    FAKE_DAEMON_PID,
                    1,
                    "/srv/blueprint/bp",
                    &["/srv/blueprint/bp", "daemon"],
                );
            },
            false,
        ),
        (
            "bp grandparent with a different command is not verified",
            |root| write_valid_daemon_child(root, "/srv/blueprint/bp", "msg", 1),
            false,
        ),
        (
            "daemon whose parent is not pid one is not verified",
            |root| write_valid_daemon_child(root, "/srv/blueprint/bp", "daemon", 99),
            false,
        ),
        (
            "deleted daemon executable is accepted",
            |root| write_valid_daemon_child(root, "/srv/blueprint/bp (deleted)", "daemon", 1),
            true,
        ),
        ("unreadable proc fails closed without panic", |_| {}, false),
    ];
    for (name, setup, want) in cases {
        let dir = tempfile::tempdir().unwrap();
        let root = dir.path().to_path_buf();
        setup(&root);
        let checked = Cell::new(0);
        let opts = Options {
            env: Some(env_of(&[("AGENT", "whatsapp")])),
            origin: no_codex(),
            daemon_child: Some(Box::new(|pid| {
                checked.set(pid);
                daemon_child_at(&root, FAKE_CALLER_PID)
            })),
            ..Options::default()
        };
        let who = resolve(None, &opts);
        assert_eq!(
            checked.get(),
            std::process::id() as i32,
            "{name}: checked pid"
        );
        if *want {
            assert!(
                who.label == "whatsapp" && who.certain && who.source == "bp-daemon-child",
                "{name}: {who:?}"
            );
        } else {
            assert!(
                who.label == "agent?:whatsapp" && !who.certain && who.source == "AGENT",
                "{name}: {who:?}"
            );
        }
    }
}

// ---- issue9_test.go ----

const EMPTY_ENV: &[(&str, &str)] = &[
    ("TMUX", ""),
    ("AGENT", ""),
    ("SUDO_USER", ""),
    ("USER", ""),
    ("LOGNAME", ""),
];

/// Issue #9: the detail came from the cwd-tracking temp file that Claude
/// Code appends to every tool command it runs through `zsh -c`.
#[test]
fn issue9_shell_command_string_is_not_the_sender() {
    let opts = Options {
        env: Some(env_of(EMPTY_ENV)),
        origin: no_codex(),
        infer: true,
        ancestors: fixed_ancestors(&[
            &[
                "/usr/bin/zsh",
                "-c",
                "source /root/.claude/shell-snapshots/snapshot-zsh-1.sh 2>/dev/null || true && eval 'bp msg writer-astra hello' < /dev/null && pwd -P >| /tmp/claude-e558-cwd",
            ],
            &["claude", "--dangerously-skip-permissions"],
        ]),
        ..Options::default()
    };
    let got = resolve(Some(&FakeSession::new("")), &opts);
    assert!(
        !got.label.contains("claude-e558-cwd") && !got.label.contains("/tmp"),
        "{:?}",
        got.label
    );
    assert!(!got.certain, "{got:?}");
}

#[test]
fn issue9_shell_script_path_still_named() {
    let opts = Options {
        env: Some(env_of(EMPTY_ENV)),
        origin: no_codex(),
        infer: true,
        ancestors: fixed_ancestors(&[
            &["/bin/sh", "-c", "/srv/tools/inbox_watcher.py"],
            &["/usr/sbin/cron", "-f"],
        ]),
        ..Options::default()
    };
    let got = resolve(Some(&FakeSession::new("")), &opts);
    assert_eq!(got.label, "cron?:inbox_watcher.py");
}

// ---- origin_test.go ----

#[test]
fn codex_origin_app_server_thread_hint() {
    const THREAD: &str = "thread-T";
    struct Case {
        name: &'static str,
        hint: &'static str,
        server_args: &'static [&'static str],
        shell_env: &'static [&'static str],
        self_env: &'static [&'static str],
        direct_parent: bool,
        want_server: bool,
    }
    let base = Case {
        name: "",
        hint: THREAD,
        server_args: &["codex", "app-server"],
        shell_env: &[],
        self_env: &[],
        direct_parent: false,
        want_server: false,
    };
    let cases = [
        Case {
            name: "matching shell environment",
            server_args: &["codex", "app-server", "--managed-daemon"],
            shell_env: &["CODEX_THREAD_ID=thread-T"],
            want_server: true,
            ..base
        },
        Case {
            name: "different shell environment",
            shell_env: &["CODEX_THREAD_ID=other-thread"],
            ..base
        },
        Case {
            name: "missing shell environment",
            ..base
        },
        Case {
            name: "app-server is direct parent",
            self_env: &["CODEX_THREAD_ID=thread-T"],
            direct_parent: true,
            want_server: true,
            ..base
        },
        Case {
            name: "exec-server",
            server_args: &["codex", "exec-server"],
            shell_env: &["CODEX_THREAD_ID=thread-T"],
            ..base
        },
        Case {
            name: "naked codex",
            server_args: &["codex", "--yolo"],
            shell_env: &["CODEX_THREAD_ID=thread-T"],
            ..base
        },
        Case {
            name: "empty hint",
            hint: "",
            shell_env: &["CODEX_THREAD_ID=thread-T"],
            ..base
        },
    ];
    for case in cases {
        let dir = tempfile::tempdir().unwrap();
        let root = dir.path();
        const SELF: i32 = 100;
        const SHELL: i32 = 200;
        const SERVER: i32 = 300;
        let parent = if case.direct_parent {
            write_fake_process(root, SELF, SERVER, "", &["bp", "msg"]);
            write_proc_environment(root, SELF, case.self_env);
            SERVER
        } else {
            write_fake_process(root, SELF, SHELL, "", &["bp", "msg"]);
            write_fake_process(root, SHELL, SERVER, "", &["zsh", "-l"]);
            if !case.shell_env.is_empty() {
                write_proc_environment(root, SHELL, case.shell_env);
            }
            SHELL
        };
        write_fake_process(root, SERVER, 1, "/opt/codex/codex", case.server_args);
        let got = codex_origin_chain(case.hint, SELF, parent, root);
        assert!(
            got.server_thread == case.want_server
                && !got.verified
                && got.codex_detected
                && got.thread_id == case.hint,
            "{}: origin={got:?}, want server_thread={}",
            case.name,
            case.want_server
        );
    }
}

#[test]
fn codex_origin_does_not_trust_shared_daemon_or_command_environment() {
    const REAL: &str = "11111111-1111-1111-1111-111111111111";
    const ROOT: &str = "22222222-2222-2222-2222-222222222222";
    let cases: &[(&str, &str, &str, &str, bool, bool)] = &[
        (
            "execution helper",
            "codex-linux-sandbox",
            REAL,
            REAL,
            true,
            true,
        ),
        (
            "forged helper argv",
            "codex-linux-sandbox",
            REAL,
            REAL,
            false,
            false,
        ),
        (
            "spoofed command env",
            "codex-linux-sandbox",
            REAL,
            ROOT,
            false,
            true,
        ),
        (
            "shared daemon startup env",
            "codex\0app-server",
            ROOT,
            ROOT,
            false,
            true,
        ),
        (
            "shared daemon other thread",
            "codex\0app-server",
            ROOT,
            REAL,
            false,
            true,
        ),
        (
            "shared execution server",
            "codex\0exec-server",
            ROOT,
            ROOT,
            false,
            true,
        ),
        ("standalone codex", "codex\0--yolo", REAL, REAL, false, true),
        ("plain shell declaration", "sh", REAL, REAL, false, true),
    ];
    for (name, command, env, hint, verified, detected) in cases {
        let dir = tempfile::tempdir().unwrap();
        let p = dir.path().join("12");
        std::fs::create_dir(&p).unwrap();
        let executable = if *name == "forged helper argv" {
            "/bin/bash"
        } else {
            "/opt/codex/codex"
        };
        std::os::unix::fs::symlink(executable, p.join("exe")).unwrap();
        std::fs::write(p.join("cmdline"), format!("{command}\0")).unwrap();
        std::fs::write(
            p.join("environ"),
            format!("TMUX=stale\0TMUX_PANE=%161\0CODEX_THREAD_ID={env}\0"),
        )
        .unwrap();
        std::fs::write(p.join("status"), "PPid:\t0\n").unwrap();
        let got = codex_origin_chain(hint, 0, 12, dir.path());
        assert!(
            got.verified == *verified && got.codex_detected == *detected && got.thread_id == *hint,
            "{name}: {got:?}"
        );
    }
    let empty = tempfile::tempdir().unwrap();
    assert!(
        !codex_origin_chain(REAL, 0, 99, empty.path()).verified,
        "missing process accepted"
    );
}

#[test]
fn codex_origin_detects_missing_thread_without_trusting_fallbacks() {
    let dir = tempfile::tempdir().unwrap();
    let p = dir.path().join("12");
    std::fs::create_dir(&p).unwrap();
    std::os::unix::fs::symlink("/opt/codex/codex", p.join("exe")).unwrap();
    std::fs::write(p.join("cmdline"), "codex\0--yolo\0").unwrap();
    std::fs::write(p.join("status"), "PPid:\t0\n").unwrap();
    let got = codex_origin_chain("", 0, 12, dir.path());
    assert!(
        got.codex_detected && !got.verified && got.thread_id.is_empty(),
        "{got:?}"
    );

    let client = FakeSession::new("server-main");
    let origin = got.clone();
    let opts = Options {
        env: Some(env_of(&[
            ("TMUX", "same-daemon"),
            ("AGENT", "server-main"),
            ("USER", "server-main"),
        ])),
        from: "server-main".into(),
        fallback: "server-main".into(),
        origin: Some(Box::new(move || origin.clone())),
        ..Options::default()
    };
    let who = resolve(Some(&client), &opts);
    assert!(
        who.label == UNKNOWN
            && who.source == "codex-unverified"
            && !who.authoritative()
            && client.calls.get() == 0,
        "{who:?} calls={}",
        client.calls.get()
    );
}

#[test]
fn ps_fallback_detects_codex_but_never_verifies() {
    let parents: HashMap<i32, (i32, &str)> = HashMap::from([
        (10, (9, "zsh")),
        (9, (8, "/opt/bin/codex")),
        (8, (1, "node")),
    ]);
    let inspect = |pid: i32| parents.get(&pid).map(|(p, n)| (*p, n.to_string()));
    assert!(
        codex_ancestry_ps(10, &inspect),
        "Codex ancestor not detected"
    );
    assert!(
        !codex_ancestry_ps(10, &|_| Some((1, "sh".to_string()))),
        "plain shell reported as Codex"
    );
}

#[test]
fn local_hint_stays_explicitly_uncertain() {
    const ID: &str = "11111111-1111-1111-1111-111111111111";
    let opts = Options {
        env: Some(env_of(&[])),
        origin: Some(Box::new(|| Origin {
            thread_id: ID.into(),
            codex_detected: true,
            ..Origin::default()
        })),
        thread: Some(Box::new(|_: &str| Identity {
            label: format!("local/subagent:{ID}?"),
            parent: "local".into(),
            source: "codex-local-hint".into(),
            ..Identity::default()
        })),
        ..Options::default()
    };
    let got = resolve(None, &opts);
    assert!(
        got.label == format!("local/subagent:{ID}?")
            && got.thread_id == ID
            && !got.certain
            && !got.authoritative()
            && got.source == "codex-local-hint",
        "{got:?}"
    );
}

#[test]
fn unverified_codex_never_falls_through_to_root_claims() {
    for verified in [false, true] {
        let client = FakeSession::new("server-main");
        let opts = Options {
            env: Some(env_of(&[
                ("TMUX", "same-daemon"),
                ("AGENT", "server-main"),
                ("USER", "server-main"),
            ])),
            from: "server-main".into(),
            fallback: "server-main".into(),
            origin: Some(Box::new(move || Origin {
                thread_id: "unknown-thread".into(),
                verified,
                ..Origin::default()
            })),
            ..Options::default()
        };
        let got = resolve(Some(&client), &opts);
        assert!(
            got.label != "server-main"
                && !got.authoritative()
                && !got.certain
                && client.calls.get() == 0,
            "verified={verified}: {got:?} calls={}",
            client.calls.get()
        );
    }
}

#[test]
fn declared_labels_cannot_authorize() {
    for name in ["server-main", "whatsapp", "bp", "ordinary-agent"] {
        let opts = Options {
            env: Some(env_of(&[("AGENT", name)])),
            origin: no_codex(),
            ..Options::default()
        };
        // daemon_child stays None: the real /proc check runs against the test
        // process, which is never a bp daemon child (as in Go).
        let got = resolve(None, &opts);
        assert!(
            got.label != name && !got.certain && !got.authoritative(),
            "claim became identity: {got:?}"
        );
    }
}

#[test]
fn proc_helpers_read_fake_tree() {
    let dir = tempfile::tempdir().unwrap();
    let root = dir.path();
    write_fake_process(root, 30, 20, "", &["/bin/sh", "-c", "/srv/x.py"]);
    write_fake_process(root, 20, 10, "", &["/usr/sbin/cron", "-f"]);
    write_fake_process(root, 10, 1, "", &["init"]);
    assert_eq!(proc_parent_at(root, 30), Some(20));
    assert_eq!(proc_parent_at(root, 99), None);
    let chain = proc_ancestors_at(root, 30, 8);
    assert_eq!(chain.len(), 3);
    assert_eq!(inferred(&chain), "cron?:x.py");
    assert!(calling_pane_at(root, 30, 10, &|_| None));
    assert!(!calling_pane_at(root, 30, 77, &|_| None));
    write_fake_process(root, 40, 30, "", &["codex", "app-server"]);
    write_fake_process(root, 41, 40, "", &["bp", "msg"]);
    assert!(
        !calling_pane_at(root, 41, 10, &|_| None),
        "app-server is never a pane identity"
    );
}
