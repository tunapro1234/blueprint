//! Port of the pure tests of internal/tmux/codex_test.go,
//! codex_short_pane_test.go, codex_gpt6_footer_test.go,
//! codex_footer_style_test.go, codex_menu_test.go and the screen half of
//! vim_test.go.

use super::fixtures::*;
use super::hermes::{HERMES_IDLE_TYPED_ROW, hermes_pane};
use super::opencode::opencode_pane;
use crate::screen::composer::*;
use crate::screen::gostr::strip_space;
use crate::screen::harness::codex::*;
use crate::screen::region::{
    ComposerVerdict, busy, classify_composer, codex_busy_queue, composer_holds_message, typing,
};

/// `codexPaneWith`.
pub fn codex_pane_with(rows: &[&str]) -> String {
    let rule = "─".repeat(70);
    let mut lines: Vec<String> = vec![
        "• Ran 6 commands · ctrl + t to view transcript".into(),
        String::new(),
        rule,
        String::new(),
    ];
    for (i, row) in rows.iter().enumerate() {
        if i == 0 {
            lines.push(format!("› {row}"));
        } else {
            lines.push(format!("  {row}"));
        }
    }
    lines.extend([
        String::new(),
        "  gpt-5.6-sol medium fast · /srv/probot/out-codex".into(),
        String::new(),
    ]);
    lines.join("\n")
}

/// `wrapText`: byte-wise split (fixtures are ASCII).
pub fn wrap_text(text: &str, width: usize) -> Vec<&str> {
    let mut rows = Vec::new();
    let mut text = text;
    while text.len() > width {
        rows.push(&text[..width]);
        text = &text[width..];
    }
    rows.push(text);
    rows
}

/// `modernCodexPane`.
pub fn modern_codex_pane(text: &str) -> String {
    format!("• Previous answer\n\n› {text}\n  gpt-6-astra high · /srv/blueprint\n")
}

/// `codexNavigationFixture`.
pub const CODEX_NAVIGATION_FIXTURE: &str =
    "\n› 1. New chat\n  2. Agent command center\n  3. Resume another chat\n";

#[test]
fn codex_composer_reads_wrapped_paste() {
    let message = "[probot-outreach] blueprint fixed your bp sending failure (16ed445): delivery was not the problem; the spool was. bp opened the spool for writing to add announcements, and your sandbox mounted /srv/blueprint read-only, so it failed at the first step. Now, if the spool is read-only, the message is sent ALONE without announcements. VERIFICATION REQUEST: send me a short test message when convenient.";
    let pane = codex_pane_with(&wrap_text(message, 68));
    assert!(
        composer_box_text(&pane).is_some(),
        "codex composer unreadable"
    );
    assert_eq!(stuck_paste(&pane, &[message]), Some(message));
    assert_eq!(
        classify_composer(&pane, &strip_space(message)),
        ComposerVerdict::Mine
    );
}

#[test]
fn codex_idle_composer_reads_empty() {
    let pane = codex_pane_with(&["Ask Codex to do anything"]);
    assert!(!typing(&pane));
    if let Some(text) = composer_box_text(&pane) {
        assert_eq!(text, "");
    }
}

#[test]
fn codex_foreign_composer_stays_foreign() {
    let pane = codex_pane_with(&wrap_text(
        "could you check this; we need to review the attribute field for the numbers in the list",
        68,
    ));
    assert_eq!(
        stuck_paste(
            &pane,
            &["[bp] a completely different message, extended to make it long enough."]
        ),
        None
    );
}

#[test]
fn codex_current_footer_and_long_running_turn() {
    for timer in ["0s", "1m 11s", "2h 4m 9s"] {
        let pane = format!(
            "◦ Working ({timer} • esc to interrupt)\n{}",
            modern_codex_pane("Ask Codex to do anything")
        );
        assert!(
            codex_pane(&pane) && busy(&pane) && !typing(&pane),
            "incorrect current Codex state: {pane:?}"
        );
    }
    let text = "[server-main] first line\n  second line with English content";
    let pane = modern_codex_pane(text);
    assert!(stuck_paste(&pane, &[text]).is_some());
    assert!(typing(&modern_codex_pane("the user's unfinished text")));
}

#[test]
fn codex_historical_working_line_does_not_block_fresh_composer() {
    let old = "old transcript output\n".repeat(40);
    let pane = format!(
        "◦ Working (23s • esc to interrupt)\n{old}{}",
        modern_codex_pane("Ask Codex to do anything")
    );
    assert!(
        !busy(&pane),
        "historical Working text treated as current turn"
    );
    let live = format!(
        "{old}◦ Working (23s • esc to interrupt)\n{}",
        modern_codex_pane("Ask Codex to do anything")
    );
    assert!(busy(&live), "live Working row lost");
}

/// `codex36x15`.
pub fn codex36x15(transcript: &str, rows: &[&str]) -> String {
    assert!(rows.len() <= 11, "composer does not fit the fixture");
    let mut lines = vec![String::new(); 15];
    lines[0] = transcript.to_string();
    for (i, row) in rows.iter().enumerate() {
        lines[i + 2] = if i == 0 {
            format!("› {row}")
        } else {
            format!("  {row}")
        };
    }
    lines[13] = "  GPT-6-Luna max · /srv/project".to_string();
    lines.join("\n")
}

fn short_pane_message() -> (String, usize) {
    let tail = format!(
        "already uses https://example.test/docs. {}",
        "Verify each response and report any mismatch with supporting detail. ".repeat(4)
    );
    let message = format!(
        "[lead] {}{tail}",
        "The request includes background context and asks for an explicit verification of each item. "
            .repeat(5)
    );
    let start = message
        .find("already uses https://")
        .expect("endpoint tail");
    (message, start)
}

/// Pure half of `TestCodexShortPaneTailIsSubmittedAndVerified`.
#[test]
fn codex_short_pane_tail_is_scrolled_and_unreadable() {
    let (message, start) = short_pane_message();
    let rows = wrap_text(&message[start..], 34);
    let pane_tail = codex36x15("", &rows);
    let split: Vec<&str> = pane_tail.split('\n').collect();
    assert_eq!(split.len(), 15);
    assert!(split[2].starts_with("› already uses https://"));
    let (text, top) = codex_composer_box(&pane_tail).expect("tail box");
    assert_eq!(top, 2);
    assert!(composer_box_scrolled(&pane_tail, &text, top));
    assert_eq!(
        paste_integrity(&pane_tail, &message, None),
        PasteVerification::Unreadable
    );
    // The submit-time ownership the client relies on for this viewport.
    assert!(composer_holds_message(&pane_tail, &strip_space(&message)));
}

/// Pure half of `TestCodexShortPaneTailWithTranscriptAboveIsRefused`.
#[test]
fn codex_short_pane_tail_with_transcript_above_is_refused() {
    let (message, start) = short_pane_message();
    let rows = wrap_text(&message[start..], 34);
    let pane_tail = codex36x15(
        "Earlier transcript: the request asks for background and a safety review.",
        &rows,
    );
    let (_, top) = codex_composer_box(&pane_tail).expect("box");
    assert!(
        !codex_composer_viewport_fills_pane(&pane_tail, top),
        "real transcript above the tail was treated as an empty screen"
    );
    assert_eq!(
        paste_integrity(&pane_tail, &message, None),
        PasteVerification::Mangled
    );
}

#[test]
fn codex_short_pane_prefix_remains_incomplete() {
    let message = format!(
        "[lead] {}",
        "The beginning of a message must not be submitted while the rest is missing. ".repeat(8)
    );
    let rows = wrap_text(&message[..160], 34);
    let pane = codex36x15("", &rows);
    assert_eq!(
        paste_integrity(&pane, &message, None),
        PasteVerification::Incomplete
    );
}

#[test]
fn codex_short_pane_rule_leaves_other_composer_readers_unchanged() {
    let tail = "visible composer tail content ".repeat(3);
    let cases = [
        ("claude", claude_pane1(&format!("❯ {tail}"))),
        ("opencode", opencode_pane(&[&tail], false)),
        ("hermes", hermes_pane(&[HERMES_IDLE_TYPED_ROW])),
    ];
    for (name, pane) in &cases {
        let (text, top) = composer_box_at(pane).expect(name);
        assert!(
            !composer_box_scrolled(pane, &text, top),
            "unmeasured {name} composer was newly treated as scrolled"
        );
    }
}

/// `gpt6Pane`.
fn gpt6_pane(composer: &[&str]) -> String {
    let mut pane = String::from("\x1b[2m  12:28 AM\x1b[0m\n\n\n");
    for (i, row) in composer.iter().enumerate() {
        if i == 0 {
            pane.push_str(&format!("\x1b[1m\x1b[38;5;215m›\x1b[0m {row}\n"));
        } else {
            pane.push_str(&format!("  {row}\n"));
        }
    }
    pane + "\n  \x1b[38;5;223mGPT-6-Luna max\x1b[39m · \x1b[38;5;151m/work/project\x1b[39m · \x1b[38;5;211mthread title\x1b[39m"
}

/// Pure half of `TestIssue16GPT6FooterKeepsWrappedComposerVisible`.
#[test]
fn issue16_gpt6_footer_keeps_wrapped_composer_visible() {
    let msg = "[lead] Your task is ready: read /work/project/brief.md and follow it. Ask the lead if anything is unclear.";
    let pane = gpt6_pane(&[
        "[lead] Your task is ready: read /work/project/brief.md and follow",
        "it. Ask the lead if anything is unclear.",
    ]);
    assert!(codex_pane(&pane));
    assert!(
        composer_holds_message(&pane, &strip_space(msg)),
        "pasted message not found: {:?}",
        codex_composer_box(&pane)
    );
    let empty = gpt6_pane(&["\x1b[2mAsk Codex to do anything\x1b[0m"]);
    assert!(codex_pane(&empty) && !typing(&empty) && composer_empty(&empty));
}

#[test]
fn issue16_gpt6_foreign_draft_is_not_ours() {
    let pane = gpt6_pane(&["half typed user draft"]);
    assert!(codex_pane(&pane));
    assert!(!composer_holds_message(
        &pane,
        &strip_space("[lead] something else")
    ));
}

/// Pure half of `TestCodexDimFooterSeparatorPreservesWrappedComposerOwnership`.
#[test]
fn codex_dim_footer_separator_preserves_wrapped_composer_ownership() {
    let footer = "  \x1b[38;2;246;226;183mgpt-5.6-sol high\x1b[2m\x1b[39m · \x1b[0m\x1b[38;2;171;223;167m/srv\x1b[39m    Vim: Insert";
    let pane = format!(
        "• Context compacted\n\x1b[1m›\x1b[0m [cron?:watch.sh] first row\n  second row\n\n{footer}"
    );
    let want = "[cron?:watch.sh] first row second row";
    assert!(composer_holds_message(&pane, &strip_space(want)));
    let empty = format!("• Context compacted\n› \x1b[2mAsk Codex to do anything\x1b[0m\n{footer}");
    assert!(composer_empty(&empty));
    let edited = pane.replacen("second row", "second row USER DRAFT", 1);
    assert!(!composer_holds_message(&edited, &strip_space(want)));
}

/// Screen half of `TestCodexNavigationDoesNotReceiveMessages`.
#[test]
fn codex_navigation_is_a_dialog() {
    let styled = CODEX_NAVIGATION_FIXTURE.replacen("New chat", "\x1b[36mNew chat\x1b[0m", 1);
    assert!(pane_dialog(&format!("{styled}\n\n")), "styled menu missed");
    assert!(!pane_dialog(&modern_codex_pane(
        "Tell me about New chat and Agent command center"
    )));
    for mode in ["Normal", "Insert"] {
        let pane = modern_codex_pane("[bp] existing message").replacen(
            " · /srv/blueprint",
            &format!(" · /srv/blueprint     Vim: {mode}"),
            1,
        ) + CODEX_NAVIGATION_FIXTURE;
        assert!(dialog(&pane), "menu under Vim {mode} missed");
    }
}

/// Screen half of `TestCodexNavigationOverridesNativeQueueHint`.
#[test]
fn codex_navigation_overrides_native_queue_hint() {
    let pane = format!(
        "{}  \x1b[2mtab to queue message\x1b[0m\n{CODEX_NAVIGATION_FIXTURE}",
        modern_codex_pane("[bp] message")
    );
    assert!(codex_busy_queue(&pane), "fixture lacks native queue hint");
    assert!(pane_dialog(&pane));
}

/// The search footer `codexTerminal` renders (vim_test.go): the footer row
/// replaced by the query.
fn codex_search_screen(draft: &str, search: &str) -> String {
    modern_codex_pane(draft).replacen(
        "  gpt-6-astra high · /srv/blueprint",
        &format!("  \x1b[36m{search}\x1b[0m"),
        1,
    )
}

/// Screen half of `TestCodexVimSearchDoesNotReceiveMessages`.
#[test]
fn codex_vim_search_is_a_dialog() {
    for query in ["/", "?", "/draft", "?previous"] {
        let pane = codex_search_screen("Ask Codex to do anything", query);
        assert!(codex_search_active(&pane), "search {query:?} missed");
        assert!(dialog(&pane), "search {query:?} not a dialog");
    }
}

/// Screen half of `TestCodexShortcutHintWithWarningIsNotSearch`.
#[test]
fn codex_shortcut_hint_with_warning_is_not_search() {
    for hint in [
        "  ? for shortcuts",
        "  ? for shortcuts                                        ⚠ 1 warning · f2 to view",
        "  ? for shortcuts  \x1b[38;5;179m⚠ 1 warning\x1b[39m · \x1b[1mf2\x1b[0m to view",
    ] {
        let pane = format!("{}{hint}\n", modern_codex_pane("Ask Codex to do anything"));
        assert!(
            !codex_search_active(&pane) && !pane_dialog(&pane),
            "shortcut hint read as a dialog: {hint:?}"
        );
    }
    for search in ["?", "?for", "?previous  "] {
        let pane = format!("{}  {search}\n", modern_codex_pane("draft"));
        assert!(codex_search_active(&pane), "search {search:?} not detected");
    }
}

/// The approval modal `codexTerminal` appends (`modal: true`).
#[test]
fn codex_approval_modal_is_a_dialog() {
    let pane = format!(
        "{}Press enter to confirm or esc to cancel\n",
        modern_codex_pane("Ask Codex to do anything")
    );
    assert!(dialog(&pane));
}
