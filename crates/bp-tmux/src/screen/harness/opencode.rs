//! Port of internal/tmux/opencode.go: opencode screen recognition, its busy
//! row, composer reader and paste chip.

use std::sync::LazyLock;

use regex::Regex;

use crate::screen::ansi::strip_dim;
use crate::screen::class::{NWS, WS};
use crate::screen::composer::COMPOSER_BOX_MAX_ROWS;
use crate::screen::gostr::strip_space1;
use crate::screen::harness::hermes::{hermes_region, trim_trailing_blank};
use crate::screen::region::split_rows;

/// `openCodeIdleText`.
const OPENCODE_IDLE_TEXT: &str = "Ask anything...";

static OPENCODE_RAIL: LazyLock<Regex> = LazyLock::new(|| Regex::new(r"^┃").expect("openCodeRail"));
static OPENCODE_BOTTOM: LazyLock<Regex> =
    LazyLock::new(|| Regex::new(r"^╹▀+").expect("openCodeBottom"));
static OPENCODE_MODE: LazyLock<Regex> =
    LazyLock::new(|| Regex::new(&format!(r"^┃{WS}+{NWS}+{WS}+·{WS}+{NWS}")).expect("openCodeMode"));
static OPENCODE_HINTS: LazyLock<Regex> = LazyLock::new(|| {
    Regex::new(&format!(r"tab agents{WS}+ctrl\+p commands")).expect("openCodeHints")
});
static OPENCODE_FOOTER: LazyLock<Regex> =
    LazyLock::new(|| Regex::new(&format!(r"^/{NWS}*:{NWS}+")).expect("openCodeFooter"));
pub(crate) static OPENCODE_CHIP: LazyLock<Regex> = LazyLock::new(|| {
    Regex::new(&format!(r"\[Pasted{WS}+~?[0-9]+{WS}+lines?\]")).expect("openCodeChip")
});
static OPENCODE_BUSY_ROW: LazyLock<Regex> =
    LazyLock::new(|| Regex::new(&format!(r"esc{WS}+interrupt")).expect("openCodeBusyRow"));

/// `IsOpenCodeCommand`.
pub fn is_opencode_command(cmd: &str) -> bool {
    cmd == "opencode"
}

/// `OpenCodePane` reports whether the live tail of the screen is opencode.
pub fn opencode_pane(pane: &str) -> bool {
    hermes_region(pane).iter().any(|raw| {
        let line = strip_space1(raw);
        OPENCODE_BOTTOM.is_match(&line)
            || OPENCODE_MODE.is_match(&line)
            || OPENCODE_HINTS.is_match(&line)
            || OPENCODE_FOOTER.is_match(&line)
    })
}

/// `openCodeBusy`: opencode's "esc interrupt" row on a proven opencode
/// screen.
pub(crate) fn opencode_busy(pane: &str) -> bool {
    if !opencode_pane(pane) {
        return false;
    }
    hermes_region(pane)
        .iter()
        .any(|raw| OPENCODE_BUSY_ROW.is_match(&strip_space1(raw)))
}

/// `openCodeComposerBox`: the rail rows above the mode row, and the row
/// index just above them.
pub(crate) fn opencode_composer_box(pane: &str) -> Option<(String, usize)> {
    if !opencode_pane(pane) {
        return None;
    }
    let lines = split_rows(pane);
    let bottom = lines
        .iter()
        .rposition(|line| OPENCODE_BOTTOM.is_match(&strip_space1(&strip_dim(line))))?;
    if bottom == 0 {
        return None;
    }
    let mut mode = None;
    let mut j = bottom;
    while j > 0 && bottom - (j - 1) <= COMPOSER_BOX_MAX_ROWS {
        j -= 1;
        if OPENCODE_MODE.is_match(&strip_space1(&strip_dim(lines[j]))) {
            mode = Some(j);
            break;
        }
    }
    let mode = mode?;
    let mut top = None;
    let mut j = mode;
    while j > 0 && mode - (j - 1) <= COMPOSER_BOX_MAX_ROWS {
        j -= 1;
        if !OPENCODE_RAIL.is_match(&strip_space1(&strip_dim(lines[j]))) {
            top = Some(j);
            break;
        }
    }
    let top = top?;
    let mut out = Vec::with_capacity(mode - top);
    for row in &lines[top + 1..mode] {
        let dimless = strip_dim(row);
        let mut clean = OPENCODE_RAIL
            .replace_all(dimless.trim(), "")
            .trim()
            .to_string();
        if opencode_placeholder_only(&clean) {
            clean.clear();
        }
        out.push(clean);
    }
    let text = trim_trailing_blank(&out).join("\n");
    Some((text.trim().to_string(), top))
}

/// `openCodePlaceholderOnly`.
fn opencode_placeholder_only(text: &str) -> bool {
    text.trim().starts_with(OPENCODE_IDLE_TEXT)
}

/// `openCodePasteChip`: opencode's paste placeholder inside its composer box.
pub(crate) fn opencode_paste_chip(pane: &str) -> bool {
    opencode_composer_box(pane).is_some_and(|(text, _)| OPENCODE_CHIP.is_match(&text))
}
