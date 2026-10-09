//! Port of the pure tests of internal/tmux/hermes_test.go (everything before
//! the delivery section). Fixtures are quoted verbatim from the Go file.

use super::fixtures::*;
use crate::screen::composer::*;
use crate::screen::harness::hermes::*;
use crate::screen::region::{busy, composer_content, is_agent_command, typing};

pub const HERMES_STATUS_LINE: &str =
    " ⚕ x-preview-f-free · 2% · 12m               ─ Say and /srv directory...";
pub const HERMES_RULE: &str =
    "────────────────────────────────────────────────────────────────────";
pub const HERMES_IDLE_ROW: &str = "❯ Ask anything, or type / for commands…";
pub const HERMES_IDLE_ROW_ANSI: &str =
    "\x1b[38;5;230m❯ \x1b[3m\x1b[38;5;136mAsk anything, or type / for commands…\x1b[0m";
pub const HERMES_GHOST_ROW_ANSI: &str =
    "\x1b[38;5;230m❯ \x1b[3m\x1b[38;5;136mDraft a reply to the last email in my inbox\x1b[0m";
pub const HERMES_BUSY_ROW: &str = "⚕ ❯ msg=interrupt · /queue · /bg · /steer · Ctrl+C cancel";
pub const HERMES_BUSY_TYPED_ROW: &str = "⚕ ❯ there is also a third line";
pub const HERMES_IDLE_TYPED_ROW: &str = "❯ there is also a third line";
pub const HERMES_KAOMOJI: &str = "  (¬_¬) processing...";

pub fn hermes_transcript() -> Vec<&'static str> {
    vec![
        "╭─ ⚕ Hermes ───────────────────────────────────────────────────────╮",
        "the /srv directory contains folders such as blueprint, kavram, and outpost.",
        "╰──────────────────────────────────────────────────────────────────╯",
        "────────────────────────────────────────",
        "● Please run `sleep 45` with your terminal tool, then say it is done.",
        "────────────────────────────────────────",
        "",
    ]
}

/// `hermesPane`: composer rows in the live IDLE structure.
pub fn hermes_pane(rows: &[&str]) -> String {
    let mut lines = hermes_transcript();
    lines.extend_from_slice(&[HERMES_STATUS_LINE, HERMES_RULE]);
    lines.extend_from_slice(rows);
    lines.extend_from_slice(&[HERMES_RULE, ""]);
    lines.join("\n")
}

/// `hermesBusyPane`: the live BUSY structure.
pub fn hermes_busy_pane(composer: &str) -> String {
    let mut lines = hermes_transcript();
    lines.extend_from_slice(&[
        HERMES_KAOMOJI,
        "",
        HERMES_STATUS_LINE,
        HERMES_RULE,
        composer,
        HERMES_RULE,
        "",
    ]);
    lines.join("\n")
}

/// `claudeStyle`: a minimal non-Hermes pane holding one composer row.
fn claude_style(row: &str) -> String {
    format!("some transcript above\n{row}\n")
}

#[test]
fn hermes_pane_recognises_the_measured_screens() {
    let cases: Vec<(&str, String, bool)> = vec![
        ("idle composer", hermes_pane(&[HERMES_IDLE_ROW]), true),
        (
            "idle composer with ansi",
            hermes_pane(&[HERMES_IDLE_ROW_ANSI]),
            true,
        ),
        (
            "idle composer holding text",
            hermes_pane(&[HERMES_IDLE_TYPED_ROW]),
            true,
        ),
        ("busy composer", hermes_busy_pane(HERMES_BUSY_ROW), true),
        (
            "busy composer holding text",
            hermes_busy_pane(HERMES_BUSY_TYPED_ROW),
            true,
        ),
        (
            "status row only",
            [HERMES_STATUS_LINE, HERMES_RULE, "", HERMES_RULE].join("\n"),
            true,
        ),
        ("claude pane", claude_pane(&[EMPTY_ROW]), false),
        ("empty capture", String::new(), false),
        (
            "transcript quoting the placeholder",
            claude_pane(&["❯ the Hermes composer says '❯ Ask anything, or type / for commands'"]),
            false,
        ),
        (
            "answer box header only",
            ["╭─ ⚕ Hermes ──╮", "answer", "╰──╯"].join("\n"),
            false,
        ),
    ];
    for (name, pane, want) in &cases {
        assert_eq!(super::super::hermes_pane(pane), *want, "{name}");
    }
}

#[test]
fn hermes_status_row_alone_identifies_the_pane() {
    assert!(super::super::hermes_pane(&format!(
        "{HERMES_STATUS_LINE}\n{HERMES_RULE}\n"
    )));
}

#[test]
fn hermes_idle_placeholder_is_not_typed_text() {
    let pane = hermes_pane(&[HERMES_IDLE_ROW_ANSI]);
    assert!(
        !typing(&pane),
        "placeholder typed: {:?}",
        composer_content(&pane)
    );
    assert!(!composer_filled(&pane));
    assert_eq!(composer_block_reason::<&str>(&pane, &[]), "");
    assert!(typing(&hermes_pane(&[HERMES_IDLE_TYPED_ROW])));
    let ghost = hermes_pane(&[HERMES_GHOST_ROW_ANSI]);
    assert!(
        !typing(&ghost),
        "ghost typed: {:?}",
        composer_content(&ghost)
    );
    assert_eq!(composer_block_reason::<&str>(&ghost, &[]), "");
    assert!(typing(&claude_style(
        "❯ \x1b[3mDraft a reply to the last email in my inbox\x1b[0m"
    )));
}

#[test]
fn fresh_hermes_pane_is_recognised_before_its_first_turn() {
    let fresh = concat!(
        "\x1b[38;5;250m\x1b[48;5;234m ⚕ \x1b[1m\x1b[38;5;220mx-preview-f-free\x1b[0m\x1b[38;5;101m\x1b[48;5;234m · -- · 3s\x1b[38;5;250m \x1b[39m\x1b[49m\n",
        "────────────────────────────────────────\n",
        "\x1b[38;5;230m❯ \x1b[3m\x1b[38;5;136mResearch this topic and write me a brief\x1b[0m\n",
        "────────────────────────────────────────\n"
    );
    assert!(super::super::hermes_pane(fresh));
    assert!(!typing(fresh), "{:?}", composer_content(fresh));
    assert_eq!(composer_block_reason::<&str>(fresh, &[]), "");
    let padded = format!("{fresh}{}", "\n".repeat(20));
    assert!(super::super::hermes_pane(&padded));
    assert_eq!(composer_block_reason::<&str>(&padded, &[]), "");
    let status_only = "\x1b[38;5;250m ⚕ x-preview-f-free · -- · 3s\x1b[0m\n";
    assert!(super::super::hermes_pane(status_only));
    let typed = concat!(
        "\x1b[38;5;250m ⚕ x-preview-f-free · -- · 9s\x1b[0m\n",
        "────────────────────────────────────────\n",
        "\x1b[38;5;230m❯ \x1b[39minsan yazisi testi\n",
        "────────────────────────────────────────\n"
    );
    assert!(
        typing(typed),
        "a human's text on a fresh Hermes pane was discarded"
    );
}

#[test]
fn hermes_permission_prompt_is_never_idle() {
    let dialog = [
        "│ ❯ 1. Allow once                                                │",
        "│   2. Allow for this session                                    │",
        "│   3. Add to permanent allowlist                                │",
        "│   4. Deny                                                      │",
        "╰────────────────────────────────────────────────────────────────╯",
        "  💻 sleep 30 + 27 commands  (04m22s · ↓ 960 tok)",
        "  ↑/↓ to select, Enter to confirm  (62s)",
        " ⚕ x-preview-f-free · 40% · 1.2d             ─ kuanta.md task d...",
        HERMES_RULE,
        "⚠ ❯",
        HERMES_RULE,
        "",
    ]
    .join("\n");
    assert!(super::super::hermes_pane(&dialog));
    assert!(hermes_dialog(&dialog), "permission prompt not detected");
    assert_eq!(
        composer_block_reason::<&str>(&dialog, &[]),
        BLOCKED_BY_DIALOG
    );
    assert_eq!(
        composer_content_block_reason::<&str>(&dialog, &[]),
        BLOCKED_BY_DIALOG
    );
    let only_marker = [HERMES_STATUS_LINE, HERMES_RULE, "⚠ ❯", HERMES_RULE, ""].join("\n");
    assert!(hermes_dialog(&only_marker));
    assert!(!hermes_dialog(&hermes_pane(&[HERMES_IDLE_ROW])));
    assert!(!hermes_dialog(&claude_pane(&["⚠ ❯"])));
}

#[test]
fn busy_reads_the_hermes_composer_row() {
    let cases: Vec<(&str, String, bool)> = vec![
        (
            "busy, empty composer",
            hermes_busy_pane(HERMES_BUSY_ROW),
            true,
        ),
        (
            "busy, composer holding text",
            hermes_busy_pane(HERMES_BUSY_TYPED_ROW),
            true,
        ),
        ("idle, placeholder", hermes_pane(&[HERMES_IDLE_ROW]), false),
        (
            "idle, holding text",
            hermes_pane(&[HERMES_IDLE_TYPED_ROW]),
            false,
        ),
        (
            "kaomoji only",
            [HERMES_KAOMOJI, "", HERMES_RULE].join("\n"),
            false,
        ),
    ];
    for (name, pane, want) in &cases {
        assert_eq!(busy(pane), *want, "{name}");
    }
}

#[test]
fn hermes_idle_is_readiness_not_mere_recognition() {
    assert!(hermes_idle(&hermes_pane(&[HERMES_IDLE_ROW])));
    assert!(!hermes_idle(&hermes_busy_pane(HERMES_BUSY_ROW)));
    assert!(!hermes_idle(&claude_pane(&[EMPTY_ROW])));
}

#[test]
fn hermes_composer_box_reads_a_multi_row_paste() {
    let pane = hermes_pane(&[
        "❯ the first line is the first part of a message",
        "the second line continues here",
        "there is also a third line",
    ]);
    let text = composer_box(&pane).expect("no box read from a live-shaped Hermes pane");
    assert_eq!(text.split('\n').count(), 3, "{text:?}");
    assert!(!text.contains('❯'));
    assert!(!text.contains("Hermes") && !text.contains("x-preview"));
    assert_eq!(
        composer_box_text(&hermes_pane(&[HERMES_IDLE_ROW_ANSI])),
        Some(String::new())
    );
    assert_eq!(composer_box(&hermes_busy_pane(HERMES_BUSY_TYPED_ROW)), None);
}

#[test]
fn hermes_clear_budget_grows_with_the_rendered_rows() {
    let mut rows = vec!["❯ line one"];
    rows.extend(std::iter::repeat_n("continuation line", 9));
    let got = hermes_clear_budget(&hermes_pane(&rows));
    let floor = 2 * rows.len() as i64 - 1;
    assert!(got >= floor, "budget={got}, want at least {floor}");
    assert!(got <= HERMES_CLEAR_ATTEMPTS_MAX);
    assert_eq!(hermes_clear_budget(&claude_pane(&[EMPTY_ROW])), 0);
}

#[test]
fn is_agent_pane_needs_both_the_command_and_the_screen() {
    let cases: Vec<(&str, &str, String, bool)> = vec![
        ("claude needs no screen", "claude", String::new(), true),
        ("codex needs no screen", "codex", String::new(), true),
        (
            "hermes python with hermes screen",
            "python",
            hermes_pane(&[HERMES_IDLE_ROW]),
            true,
        ),
        (
            "hermes python while working",
            "python",
            hermes_busy_pane(HERMES_BUSY_ROW),
            true,
        ),
        (
            "python3 with hermes screen",
            "python3",
            hermes_pane(&[HERMES_IDLE_ROW]),
            true,
        ),
        (
            "a plain python script",
            "python",
            "traceback... loop 44/100\n".to_string(),
            false,
        ),
        (
            "python with an unreadable screen",
            "python",
            String::new(),
            false,
        ),
        (
            "a python REPL",
            "python3",
            ">>> for x in range(3):\n...     print(x)\n".to_string(),
            false,
        ),
        (
            "a shell showing a hermes transcript",
            "zsh",
            hermes_pane(&[HERMES_IDLE_ROW]),
            false,
        ),
        ("vim", "vim", hermes_pane(&[HERMES_IDLE_ROW]), false),
        ("nothing at all", "", String::new(), false),
    ];
    for (name, cmd, pane, want) in &cases {
        assert_eq!(is_agent_pane(cmd, pane), *want, "{name}");
    }
}

#[test]
fn is_hermes_command_is_not_a_whitelist() {
    for cmd in ["python", "python3", "hermes"] {
        assert!(is_hermes_command(cmd), "{cmd}");
        assert!(!is_agent_command(cmd), "{cmd}");
    }
    for cmd in ["claude", "codex", "bwrap", "zsh", "node", ""] {
        assert!(!is_hermes_command(cmd), "{cmd}");
    }
}
