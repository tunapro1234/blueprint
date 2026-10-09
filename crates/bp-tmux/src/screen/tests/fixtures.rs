//! Port of the shared pane fixtures of the Go tests (composer_test.go,
//! parse_test.go). Text is copied verbatim.

/// `boxBorderTop`: the labelled top border.
pub const BOX_BORDER_TOP: &str = "\u{2500}\u{2500}\u{2500}\u{2500}\u{2500}\u{2500}\u{2500}\u{2500}\u{2500}\u{2500}\u{2500}\u{2500}\u{2500}\u{2500}\u{2500}\u{2500}\u{2500}\u{2500}\u{2500}\u{2500} probot-main \u{2500}\u{2500}";
/// `boxBorderBottom`: a pure rule.
pub const BOX_BORDER_BOTTOM: &str = "\u{2500}\u{2500}\u{2500}\u{2500}\u{2500}\u{2500}\u{2500}\u{2500}\u{2500}\u{2500}\u{2500}\u{2500}\u{2500}\u{2500}\u{2500}\u{2500}\u{2500}\u{2500}\u{2500}\u{2500}\u{2500}\u{2500}\u{2500}\u{2500}\u{2500}\u{2500}\u{2500}\u{2500}\u{2500}\u{2500}\u{2500}\u{2500}\u{2500}\u{2500}\u{2500}";
/// `boxStatus`.
pub const BOX_STATUS: &str =
    "  -- INSERT -- \u{23f5}\u{23f5} bypass permissions on (shift+tab to cycle)    /rc";
/// `boxChip`.
pub const BOX_CHIP: &str = "  \u{29c9}  fon-panosu";
/// `emptyRow`: the prompt marker plus a single NBSP.
pub const EMPTY_ROW: &str = "\u{276f}\u{00a0}";

/// `stuckMessage`.
pub const STUCK_MESSAGE: &str = "[server-main] roadmap review: finish the goal-system section today; hierarchical assignment is missing";

/// `composerBorder` (parse_test.go).
pub const COMPOSER_BORDER: &str = "──────────────────────────────\n";

/// `claudePane`: composer rows wrapped in the full live structure, with a
/// rule-shaped transcript row above it.
pub fn claude_pane(rows: &[&str]) -> String {
    let mut lines = vec![
        "  agent: output left from the previous turn",
        "  \u{2500}\u{2500}\u{2500}\u{2500}\u{2500}\u{2500}\u{2500} summary \u{2500}\u{2500}\u{2500}\u{2500}\u{2500}\u{2500}\u{2500}",
        BOX_BORDER_TOP,
    ];
    lines.extend_from_slice(rows);
    lines.extend_from_slice(&[BOX_BORDER_BOTTOM, BOX_STATUS, BOX_CHIP, ""]);
    lines.join("\n")
}

/// `claudePane` for a single owned row.
pub fn claude_pane1(row: &str) -> String {
    claude_pane(&[row])
}

/// `screenFillingPane`: the box's top border is the first row.
pub fn screen_filling_pane(rows: &[&str]) -> String {
    let mut lines = vec![BOX_BORDER_TOP];
    lines.extend_from_slice(rows);
    lines.extend_from_slice(&[BOX_BORDER_BOTTOM, BOX_STATUS, BOX_CHIP, ""]);
    lines.join("\n")
}

/// `collapsedPane`: only ONE border above the status line.
pub fn collapsed_pane() -> String {
    [
        "  agent: output left from the previous turn",
        EMPTY_ROW,
        BOX_BORDER_BOTTOM,
        BOX_STATUS,
        BOX_CHIP,
        "",
    ]
    .join("\n")
}

/// `pickerPane`: a modal picker replaced the composer footer.
pub fn picker_pane() -> String {
    [
        "  agent: output left from the previous turn",
        BOX_BORDER_BOTTOM,
        "  Which file should we open?",
        "\u{276f} 1. first option",
        "  2. second option",
        BOX_BORDER_BOTTOM,
        "  Enter to select \u{00b7} Tab/Arrow keys to navigate \u{00b7} Esc to cancel",
        "",
    ]
    .join("\n")
}

/// `pickerOverStatusPane`: a picker while the permission footer survives.
pub fn picker_over_status_pane() -> String {
    [
        "  agent: output left from the previous turn",
        BOX_BORDER_TOP,
        "\u{276f} 1. first option",
        BOX_BORDER_BOTTOM,
        BOX_STATUS,
        "  Enter to select \u{00b7} Esc to cancel",
        "",
    ]
    .join("\n")
}

/// `busyPane`.
pub fn busy_pane(rows: &[&str]) -> String {
    format!("✻ Working… (23s · Esc to interrupt)\n{}", claude_pane(rows))
}

/// `spinnerPane`: transcript rows above an empty composer box.
pub fn spinner_pane(transcript: &[&str]) -> String {
    format!("{}\n{}", transcript.join("\n"), claude_pane(&[EMPTY_ROW]))
}

/// `filler`: transcript padding.
pub fn filler(n: usize) -> Vec<&'static str> {
    vec!["  agent: intermediate line"; n]
}

/// `quotedSpinner`.
pub const QUOTED_SPINNER: &str =
    "  signature ✻ Symbioting… (29s · ↓ 163 tokens). So the door is not closed.";

/// `collapsedChip`.
pub const COLLAPSED_CHIP: &str =
    "\x1b[1m›\x1b[0m \x1b[38;5;6m[Pasted Content 1024 chars]\x1b[39m\n  gpt-5.6-sol low · /tmp\n";
/// `expandedChip`.
pub const EXPANDED_CHIP: &str = "\x1b[1m›\x1b[0m \x1b[38;5;6m[Pasted Content 1024\x1b[39m\n  \x1b[38;5;6mchars]\x1b[39mAAAAAAAAAAAAAAAAAAAA\n  gpt-5.6-sol low · /tmp\n";
/// `ghostComposer`.
pub const GHOST_COMPOSER: &str =
    "\x1b[1m›\x1b[0m \x1b[2mExplain this codebase\x1b[22m\n  gpt-5.6-sol low · /tmp\n";
/// `busyQueueChip`.
pub const BUSY_QUEUE_CHIP: &str = concat!(
    "\x1b[2mWorking (12s · esc to interrupt)\x1b[0m\n",
    "\x1b[0;1m›\x1b[0m \x1b[38;5;6m[Pasted Content 1021 chars]\x1b[39mthe popup star is the order flow.\n",
    "  \x1b[2mtab to queue message\x1b[0m                                      \x1b[2m30% context left\x1b[0m\n"
);

/// Go `s[a:b]` on an ASCII string.
pub fn sub(s: &str, from: usize, to: usize) -> &str {
    &s[from..to]
}
