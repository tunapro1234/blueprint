//! Port of internal/tmux/opencode_test.go (all pure).

use super::fixtures::*;
use crate::screen::composer::*;
use crate::screen::harness::hermes::is_agent_pane;
use crate::screen::harness::opencode::*;
use crate::screen::region::{busy, typing};

/// `openCodePane`: the TUI as blueprint-ox-test drew it (100x30).
pub fn opencode_pane(composer: &[&str], busy: bool) -> String {
    let mut lines: Vec<String> = vec![
        "  ┃  my previous message is drawn with the same rail in the transcript".into(),
        String::new(),
        "     ▣  Build · Ox Alpha (stealth)".into(),
        String::new(),
        "  ┃".into(),
    ];
    for row in composer {
        lines.push(format!("  ┃  {row}"));
    }
    lines.push("  ┃".into());
    lines.push("  ┃  Build · Ox Alpha (stealth) OpenRouter".into());
    lines.push(format!("  ╹{}", "▀".repeat(60)));
    if busy {
        lines.push("   ⬝⬝⬝⬝⬝■■■  esc interrupt               tab agents  ctrl+p commands".into());
    } else {
        lines.push("  tab agents  ctrl+p commands".into());
    }
    lines.push(String::new());
    lines.push("  /srv/compec/mail-ox:master                                 1.18.23".into());
    lines.push(String::new());
    lines.join("\n")
}

#[test]
fn opencode_recognition() {
    let pane = opencode_pane(
        &["Ask anything... \"What is the tech stack of this project?\""],
        false,
    );
    assert!(super::super::opencode_pane(&pane));
    assert!(is_agent_pane("opencode", &pane));
    assert!(!is_agent_pane("opencode", "  $ ls\n  bp  README.md\n"));
    assert!(!super::super::opencode_pane(&claude_pane(&["❯ "])));
}

#[test]
fn opencode_busy_row() {
    assert!(busy(&opencode_pane(&["Ask anything..."], true)));
    assert!(!busy(&opencode_pane(&["Ask anything..."], false)));
}

#[test]
fn opencode_composer_reader() {
    let idle = opencode_pane(
        &["Ask anything... \"What is the tech stack of this project?\""],
        false,
    );
    assert!(!typing(&idle), "the idle placeholder read as typed text");
    if let Some(text) = composer_box_text(&idle) {
        assert_eq!(text, "");
    }
    let message = "[server-main] this message remains in the composer and wraps across lines; it looks exactly like this.";
    let held = opencode_pane(&[&message[..56], &message[56..]], false);
    assert_eq!(stuck_paste(&held, &[message]), Some(message));
    assert_eq!(
        stuck_paste(
            &held,
            &["a completely different message, extended to make it long enough"]
        ),
        None
    );
}

#[test]
fn opencode_chip_is_unreadable() {
    let pane = opencode_pane(&["[Pasted ~3 lines]"], false);
    assert!(opencode_paste_chip(&pane) && paste_chip(&pane));
    assert_eq!(
        stuck_paste(&pane, &["line one\nline two\nline three"]),
        None
    );
    assert_eq!(
        composer_content_block_reason::<&str>(&pane, &[]),
        BLOCKED_BY_PASTE_CHIP
    );
}
