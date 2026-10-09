//! Asserts the Rust screen parsers agree with the Go reference results in
//! testdata/golden/tmux/*.json (written by `go run ./tools/goldenexport/tmux`).

use std::collections::BTreeMap;
use std::path::PathBuf;

use bp_tmux::launch_args::{OpenOptions, claude_config_prefix, codex_socket, onboarding_prompt};
use bp_tmux::screen::{self, RemoteControl};
use serde::Deserialize;
use serde_json::Value;

fn golden(name: &str) -> String {
    let path = PathBuf::from(env!("CARGO_MANIFEST_DIR"))
        .join("../../testdata/golden/tmux")
        .join(name);
    std::fs::read_to_string(&path).unwrap_or_else(|err| panic!("{}: {err}", path.display()))
}

#[derive(Debug, Deserialize, PartialEq)]
#[serde(rename_all = "camelCase")]
struct Screen {
    name: String,
    pane: String,
    texts: Vec<String>,
    typing: bool,
    busy: bool,
    strip_dim: String,
    composer_block_reason: String,
    composer_content_block_reason: String,
    stuck_paste_text: String,
    stuck_paste_ours: bool,
    exact_paste: bool,
    composer_empty: bool,
    damaged_paste: bool,
    auth_expired: bool,
    usage_limit_reason: String,
    remote_control_menu: bool,
    remote_state: i64,
    #[serde(rename = "remoteURL")]
    remote_url: String,
    remote_reason: String,
    dialog: bool,
    codex_pane: bool,
    hermes_pane: bool,
    hermes_idle: bool,
    open_code_pane: bool,
    claude_empty_composer: bool,
    claude_holds_onboarding: bool,
    launch_wait: String,
    is_agent_pane: BTreeMap<String, bool>,
}

fn compute(name: &str, pane: &str, texts: &[String]) -> Screen {
    let stuck = screen::stuck_paste(pane, texts);
    let (state, url, reason) = screen::remote_control_status(pane);
    Screen {
        name: name.to_string(),
        pane: pane.to_string(),
        texts: texts.to_vec(),
        typing: screen::typing(pane),
        busy: screen::busy(pane),
        strip_dim: screen::strip_dim(pane),
        composer_block_reason: screen::composer_block_reason(pane, texts).to_string(),
        composer_content_block_reason: screen::composer_content_block_reason(pane, texts)
            .to_string(),
        stuck_paste_text: stuck.unwrap_or_default().to_string(),
        stuck_paste_ours: stuck.is_some(),
        exact_paste: screen::exact_paste(pane, texts),
        composer_empty: screen::composer_empty(pane),
        damaged_paste: screen::damaged_paste(pane, texts),
        auth_expired: screen::auth_expired(pane),
        usage_limit_reason: screen::usage_limit_reason(pane),
        remote_control_menu: screen::remote_control_menu(pane),
        remote_state: match state {
            RemoteControl::Unknown => 0,
            RemoteControl::Active => 1,
            RemoteControl::Disconnected => 2,
        },
        remote_url: url,
        remote_reason: reason,
        dialog: screen::dialog(pane),
        codex_pane: screen::codex_pane(pane),
        hermes_pane: screen::hermes_pane(pane),
        hermes_idle: screen::hermes_idle(pane),
        open_code_pane: screen::opencode_pane(pane),
        claude_empty_composer: screen::claude_empty_composer(pane),
        claude_holds_onboarding: screen::claude_composer_holds_onboarding(pane, "agent"),
        launch_wait: screen::launch_wait(pane).to_string(),
        is_agent_pane: [
            "claude", "codex", "bwrap", "node", "python3", "hermes", "opencode", "zsh", "",
        ]
        .into_iter()
        .map(|cmd| (cmd.to_string(), screen::is_agent_pane(cmd, pane)))
        .collect(),
    }
}

#[test]
fn screens_match_go() {
    let want: Vec<Screen> = serde_json::from_str(&golden("screens.json")).unwrap();
    assert!(want.len() > 300, "corpus shrank to {}", want.len());
    let mut failures = Vec::new();
    for case in &want {
        let got = compute(&case.name, &case.pane, &case.texts);
        if got != *case {
            failures.push(format!("{}:\n  go:   {case:?}\n  rust: {got:?}", case.name));
        }
    }
    assert!(
        failures.is_empty(),
        "{} of {} screens differ from Go:\n{}",
        failures.len(),
        want.len(),
        failures.join("\n")
    );
}

#[derive(Deserialize)]
#[serde(rename_all = "camelCase")]
struct Command {
    command: String,
    is_agent_command: bool,
    is_shell_command: bool,
    is_codex_command: bool,
    is_hermes_command: bool,
    is_open_code_command: bool,
}

#[test]
fn commands_match_go() {
    let want: Vec<Command> = serde_json::from_str(&golden("commands.json")).unwrap();
    for c in &want {
        let cmd = c.command.as_str();
        assert_eq!(
            [
                screen::is_agent_command(cmd),
                screen::is_shell_command(cmd),
                screen::is_codex_command(cmd),
                screen::is_hermes_command(cmd),
                screen::is_opencode_command(cmd),
            ],
            [
                c.is_agent_command,
                c.is_shell_command,
                c.is_codex_command,
                c.is_hermes_command,
                c.is_open_code_command,
            ],
            "{cmd:?}"
        );
    }
}

#[derive(Deserialize)]
#[serde(rename_all = "camelCase")]
struct Launch {
    options: Value,
    claude_config_prefix: String,
}

#[test]
fn launch_options_match_go() {
    let want: Vec<Launch> = serde_json::from_str(&golden("launch.json")).unwrap();
    for l in want {
        let opts: OpenOptions = serde_json::from_value(l.options.clone()).unwrap();
        assert_eq!(serde_json::to_value(&opts).unwrap(), l.options);
        assert_eq!(
            claude_config_prefix(&opts),
            l.claude_config_prefix,
            "{opts:?}"
        );
    }
}

#[derive(Deserialize)]
struct Socket {
    home: String,
    endpoint: String,
    socket: String,
}

#[test]
fn sockets_and_onboarding_match_go() {
    let sockets: Vec<Socket> = serde_json::from_str(&golden("sockets.json")).unwrap();
    for s in sockets {
        assert_eq!(
            codex_socket(&s.home, &s.endpoint),
            s.socket,
            "{}",
            s.endpoint
        );
    }
    let prompts: BTreeMap<String, String> =
        serde_json::from_str(&golden("onboarding.json")).unwrap();
    for (session, prompt) in prompts {
        assert_eq!(onboarding_prompt(&session), prompt, "{session}");
    }
}
