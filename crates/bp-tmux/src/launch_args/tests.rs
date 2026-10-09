//! Port of the launch-command assertions of internal/tmux/launch_args_test.go,
//! open_test.go and resume_test.go. Go checks the `send-keys` call Open
//! makes; here the same strings are checked on [`launch_command`].

use super::*;

const ID: &str = "019a0d02-a847-76d1-ba01-8b67fbe755c1";

fn strings(args: &[&str]) -> Vec<String> {
    args.iter().map(|s| s.to_string()).collect()
}

fn launch(session: &str, opts: &OpenOptions) -> String {
    launch_command(session, opts)
        .expect("launch_command")
        .command
}

#[test]
fn munge_project_path_table() {
    for (input, want) in [
        ("/srv/probot-business", "-srv-probot-business"),
        ("/srv/kitap/.worktrees/x", "-srv-kitap--worktrees-x"),
        ("/srv/some_dir", "-srv-some-dir"),
        ("/srv", "-srv"),
        ("/tmp/claude-0/-srv-x/scrat", "-tmp-claude-0--srv-x-scrat"),
    ] {
        assert_eq!(munge_project_path(input), want, "{input}");
    }
}

/// `TestOpenPassesRecordedArgsAfterBinaryWithoutDuplicates`.
#[test]
fn recorded_args_follow_binary_without_duplicates() {
    let opts = OpenOptions {
        codex: true,
        no_sandbox: true,
        resume: true,
        resume_id: ID.into(),
        no_prompt: true,
        args: strings(&["--yolo", "-m", "gpt-x"]),
        ..Default::default()
    };
    let got = launch("claude-w", &opts);
    let want = format!(
        "CODEX_BWRAPPED=1 command codex '-m' 'gpt-x' --dangerously-bypass-approvals-and-sandbox resume '{ID}'"
    );
    assert!(got.contains(&want), "launch={got:?} want {want:?}");

    let opts = OpenOptions {
        no_prompt: true,
        args: strings(&["--dangerously-skip-permissions", "--model", "opus"]),
        ..Default::default()
    };
    let got = launch("claude-main", &opts);
    assert_eq!(
        got.matches("--dangerously-skip-permissions").count(),
        1,
        "{got}"
    );
    assert!(
        got.contains("PREFIX=claude-main command claude '--model' 'opus' --dangerously-skip-permissions --name 'claude-main'"),
        "{got}"
    );
}

/// `TestRemoteCodexResumeDropsPermissionOverridesButKeepsOtherFlags`.
#[test]
fn remote_codex_resume_drops_permission_overrides() {
    let args = strings(&[
        "--yolo",
        "--dangerously-bypass-approvals-and-sandbox",
        "--full-auto",
        "-s",
        "workspace-write",
        "-s=read-only",
        "--sandbox",
        "read-only",
        "-a",
        "on-request",
        "-a=never",
        "--ask-for-approval=never",
        "--ask-for-approval",
        "untrusted",
        "-c",
        "sandbox_mode=workspace-write",
        "-c=sandbox_workspace_write.writable_roots=[\"/tmp\"]",
        "--config=approval_policy=never",
        "--config",
        "permission_profile.default=untrusted",
        "-c",
        "default_permissions=workspace",
        "-c=permissions.filesystem=[]",
        "--add-dir",
        "/private/a",
        "--add-dir=/private/b",
        "-m",
        "gpt-6-sol",
        "--profile",
        "worker",
        "-c",
        "model=gpt-6-sol",
        "--config=model_reasoning_effort=medium",
        "--search",
    ]);
    let opts = OpenOptions {
        codex: true,
        no_sandbox: true,
        resume: true,
        resume_id: ID.into(),
        remote: "unix://".into(),
        no_prompt: true,
        args,
        ..Default::default()
    };
    let got = launch("agent", &opts);
    for want in [
        "CODEX_BWRAPPED=1 command codex".to_string(),
        "--remote 'unix://'".to_string(),
        format!("resume '{ID}'"),
        "'-c' 'model=gpt-6-sol'".to_string(),
        "'--config=model_reasoning_effort=medium'".to_string(),
        "'--search'".to_string(),
    ] {
        assert!(got.contains(&want), "{got:?} missing {want:?}");
    }
    for forbidden in [
        "--dangerously-bypass-approvals-and-sandbox",
        "--yolo",
        "--full-auto",
        "--sandbox",
        "workspace-write",
        "read-only",
        "--ask-for-approval",
        "on-request",
        "never",
        "untrusted",
        "approval_policy",
        "sandbox_mode",
        "sandbox_workspace_write",
        "permission_profile",
        "default_permissions",
        "permissions.filesystem",
        "--add-dir",
        "/private/a",
        "/private/b",
    ] {
        assert!(!got.contains(forbidden), "retained {forbidden:?}: {got}");
    }
}

/// `TestRemoteFreshAndLocalCodexResumeKeepBypassBehavior`.
#[test]
fn remote_fresh_and_local_codex_resume_keep_bypass() {
    let args = strings(&["--full-auto", "-c", "sandbox_mode=workspace-write"]);
    let fresh = OpenOptions {
        codex: true,
        no_sandbox: true,
        remote: "unix://".into(),
        no_prompt: true,
        args: args.clone(),
        ..Default::default()
    };
    let local = OpenOptions {
        codex: true,
        no_sandbox: true,
        resume: true,
        resume_id: ID.into(),
        no_prompt: true,
        args,
        ..Default::default()
    };
    for (name, opts, resume) in [
        ("fresh remote", fresh, false),
        ("local resume", local, true),
    ] {
        let got = launch("agent", &opts);
        assert!(
            got.contains("CODEX_BWRAPPED=1 command codex")
                && got.contains("--dangerously-bypass-approvals-and-sandbox"),
            "{name}: {got}"
        );
        assert!(
            got.contains("--full-auto") && got.contains("sandbox_mode=workspace-write"),
            "{name}: {got}"
        );
        if resume {
            assert!(got.contains(&format!("resume '{ID}'")), "{name}: {got}");
        } else {
            assert!(got.contains("--remote 'unix://'"), "{name}: {got}");
        }
    }
}

/// `TestDirectLaunchBypassesInteractiveShellAliases`, including the real
/// bash/zsh alias check when those shells exist.
#[test]
fn direct_launch_bypasses_interactive_shell_aliases() {
    use std::os::unix::fs::PermissionsExt;
    use std::process::Command;

    let opts = OpenOptions {
        no_prompt: true,
        args: strings(&["--model", "opus"]),
        ..Default::default()
    };
    let got = launch("agent", &opts);
    assert!(got.contains("PREFIX=agent command claude"), "{got}");

    let root = std::env::temp_dir().join(format!("bp-tmux-alias-{}", std::process::id()));
    let bin_dir = root.join("bin");
    let home = root.join("home");
    std::fs::create_dir_all(&bin_dir).unwrap();
    std::fs::create_dir_all(&home).unwrap();
    let args_path = root.join("args");
    let env_path = root.join("prefix");
    let fake = bin_dir.join("claude");
    std::fs::write(
        &fake,
        "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$CAPTURE_ARGS\"\nprintf '%s\\n' \"$CLAUDE_REMOTE_CONTROL_SESSION_NAME_PREFIX\" > \"$CAPTURE_ENV\"\n",
    )
    .unwrap();
    std::fs::set_permissions(&fake, std::fs::Permissions::from_mode(0o700)).unwrap();
    std::os::unix::fs::symlink(&fake, bin_dir.join("fake-claude")).unwrap();
    let rc = "claude() { fake-claude --function-added \"$@\"; }\nalias claude='fake-claude --alias-added'\n";
    for file in [".bashrc", ".zshrc"] {
        std::fs::write(home.join(file), rc).unwrap();
    }
    let path = format!(
        "{}:{}",
        bin_dir.display(),
        std::env::var("PATH").unwrap_or_default()
    );
    for (name, shell_args) in [
        ("bash", vec!["--noprofile", "-i", "-c"]),
        ("zsh", vec!["-i", "-c"]),
    ] {
        let Some(shell) = std::env::split_paths(&path)
            .map(|dir| dir.join(name))
            .find(|p| p.is_file())
        else {
            eprintln!("{name} unavailable; skipping shell alias check");
            continue;
        };
        let run = |script: &str| {
            Command::new(&shell)
                .args(&shell_args)
                .arg(script)
                .env_clear()
                .env("HOME", &home)
                .env("ZDOTDIR", &home)
                .env("PATH", &path)
                .env("CAPTURE_ARGS", &args_path)
                .env("CAPTURE_ENV", &env_path)
                .env("TERM", "dumb")
                .output()
                .unwrap()
        };
        let probe = run("CLAUDE_REMOTE_CONTROL_SESSION_NAME_PREFIX=agent claude --probe");
        assert!(
            probe.status.success(),
            "{name} alias control failed: {probe:?}"
        );
        let data = std::fs::read_to_string(&args_path).unwrap();
        assert_eq!(
            data.trim().split('\n').collect::<Vec<_>>(),
            ["--alias-added", "--probe"],
            "{name} control did not prove alias expansion is active"
        );
        std::fs::remove_file(&args_path).unwrap();
        let out = run(&got);
        assert!(out.status.success(), "{name} launch failed: {out:?}");
        let data = std::fs::read_to_string(&args_path).unwrap();
        assert_eq!(
            data.trim().split('\n').collect::<Vec<_>>(),
            [
                "--model",
                "opus",
                "--dangerously-skip-permissions",
                "--name",
                "agent"
            ],
            "{name} alias/function changed argv"
        );
        assert_eq!(std::fs::read_to_string(&env_path).unwrap().trim(), "agent");
        std::fs::remove_file(&args_path).unwrap();
    }
    std::fs::remove_dir_all(&root).unwrap();
}

/// The command half of `TestClaudeResumeDoesNotRepeatRecordedPermissionFlag`
/// (the transcript lookup that resolves the id needs the client).
#[test]
fn claude_resume_does_not_repeat_recorded_permission_flag() {
    let opts = OpenOptions {
        resume: true,
        resume_id: ID.into(),
        no_prompt: true,
        args: strings(&["--dangerously-skip-permissions", "--model", "opus"]),
        ..Default::default()
    };
    let got = launch("claude-worker", &opts);
    assert_eq!(
        got.matches("--dangerously-skip-permissions").count(),
        1,
        "{got}"
    );
    assert!(got.contains(&format!("--resume '{ID}'")), "{got}");
    assert!(got.contains("'--model' 'opus'"), "{got}");
}

/// The command half of `TestCodexResumeLaunchPreservesThreadAndSettings`.
#[test]
fn codex_resume_launch_preserves_thread_and_settings() {
    let id = "01a0617e-29f9-79a3-be66-72ea1dec4718";
    for remote in ["", "unix://"] {
        let opts = OpenOptions {
            codex: true,
            resume: true,
            resume_id: id.into(),
            remote: remote.into(),
            no_sandbox: !remote.is_empty(),
            no_prompt: true,
            ..Default::default()
        };
        let got = launch("agent", &opts);
        assert!(
            got.contains(&format!("resume '{id}'")),
            "lost thread: {got}"
        );
        for bad in [
            "model_reasoning_effort",
            "--model",
            "service_tier",
            "/rename",
            "/remote-control",
        ] {
            assert!(!got.contains(bad), "overrode thread settings: {got}");
        }
    }
}

/// `TestRemoteSandboxRefusalPrecedesAnyMutation`: the command is refused.
#[test]
fn remote_sandbox_refusal() {
    let opts = OpenOptions {
        codex: true,
        remote: "unix://".into(),
        ..Default::default()
    };
    let err = launch_command("agent", &opts).expect_err("sandbox escape was not refused");
    assert_eq!(
        err.to_string(),
        "remote execution leaves the TUI sandbox; use an explicitly approved --no-sandbox launch"
    );
}

/// The command half of `TestFreshCodexOnboardingNeedsNoTranscriptOrPostStartPaste`.
#[test]
fn fresh_codex_onboarding_is_a_native_argument() {
    for no_prompt in [false, true] {
        let opts = OpenOptions {
            codex: true,
            no_prompt,
            ..Default::default()
        };
        let got = launch_command("agent", &opts).unwrap();
        assert_eq!(got.native_onboarding, !no_prompt);
        assert_eq!(
            got.command.contains("bp agent."),
            !no_prompt,
            "{}",
            got.command
        );
        assert!(
            !got.command.contains("--model") && !got.command.contains("model_reasoning_effort")
        );
    }
}

#[test]
fn validate_rejects_bad_combinations() {
    let cases: Vec<(OpenOptions, &str)> = vec![
        (
            OpenOptions {
                codex: true,
                hermes: true,
                ..Default::default()
            },
            "choose one harness",
        ),
        (
            OpenOptions {
                no_sandbox: true,
                ..Default::default()
            },
            "--no-sandbox requires Codex",
        ),
        (
            OpenOptions {
                codex: true,
                no_sandbox: true,
                remote: "tcp://x".into(),
                ..Default::default()
            },
            "--remote requires Codex and a unix:// endpoint",
        ),
        (
            OpenOptions {
                resume_id: "nope".into(),
                ..Default::default()
            },
            "invalid session/thread id",
        ),
    ];
    for (opts, want) in cases {
        assert_eq!(opts.validate().unwrap_err().to_string(), want);
    }
    assert!(OpenOptions::default().validate().is_ok());
    assert_eq!(
        launch_command(
            "a",
            &OpenOptions {
                codex: true,
                resume: true,
                ..Default::default()
            }
        )
        .unwrap_err()
        .to_string(),
        "Codex resume requires a verified thread id"
    );
}

#[test]
fn codex_socket_endpoints() {
    assert_eq!(
        codex_socket("/home/u", "unix://"),
        "/home/u/app-server-control/app-server-control.sock"
    );
    assert_eq!(codex_socket("/home/u", "unix:///run/x.sock"), "/run/x.sock");
    assert_eq!(codex_socket("/home/u", "tcp://x"), "");
}

#[test]
fn launcher_wraps_and_config_dir_prefixes() {
    let opts = OpenOptions {
        no_prompt: true,
        claude_config_dir: "/home/u/.claude-b".into(),
        launcher: "'/usr/bin/run'".into(),
        ..Default::default()
    };
    let got = launch("a", &opts);
    assert!(
        got.starts_with("exec '/usr/bin/run' 'CLAUDE_CONFIG_DIR="),
        "{got}"
    );
    assert!(!got.contains("command claude"), "{got}");
}

#[test]
fn open_options_json_field_names() {
    let opts = OpenOptions {
        resume: true,
        resume_id: ID.into(),
        open_code: true,
        no_sandbox: true,
        claude_account: "b".into(),
        claude_config_dir: "/c".into(),
        no_prompt: true,
        ..Default::default()
    };
    let json = serde_json::to_string(&opts).unwrap();
    assert_eq!(
        json,
        format!(
            r#"{{"resume":true,"resumeId":"{ID}","opencode":true,"noSandbox":true,"claudeAccount":"b","claudeConfigDir":"/c"}}"#
        )
    );
    let back: OpenOptions = serde_json::from_str(&json).unwrap();
    assert_eq!(
        back,
        OpenOptions {
            no_prompt: false,
            ..opts
        }
    );
}

/// The command halves of `TestOpenLaunchesHermesAndWaitsForItsIdleComposer`
/// and `TestOpenCodeDirectLaunchBypassesShellAliases`.
#[test]
fn hermes_and_opencode_launches_bypass_shell_aliases() {
    let hermes = OpenOptions {
        hermes: true,
        resume: true,
        no_prompt: true,
        ..Default::default()
    };
    assert_eq!(launch("agent", &hermes), "command hermes");
    let opencode = OpenOptions {
        open_code: true,
        no_prompt: true,
        args: strings(&["--model", "ox"]),
        ..Default::default()
    };
    assert_eq!(
        launch("agent", &opencode),
        "command opencode '--model' 'ox'"
    );
}
