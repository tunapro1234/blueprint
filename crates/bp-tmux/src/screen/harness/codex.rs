//! Port of internal/tmux/codex.go: Codex screen recognition, the Codex
//! composer reader, `paneDialog`/`Dialog`, the navigation menu and search
//! detection.

use std::sync::LazyLock;

use regex::Regex;

use crate::screen::ansi::{strip_ansi, strip_dim};
use crate::screen::class::{NWS, WS};
use crate::screen::composer::{COMPOSER_BOX_MAX_ROWS, long_enough};
use crate::screen::gostr::{SPACE_TAB_NBSP, go_lower, strip_space, strip_space1};
use crate::screen::harness::hermes::{hermes_dialog, hermes_region};
use crate::screen::region::{CODEX_CHIP, PROMPT_LINE, split_rows, strip_prompt};

/// `codexIdleText`.
const CODEX_IDLE_TEXT: &str = "Ask Codex";

/// `codexPlaceholder`.
static CODEX_PLACEHOLDER: LazyLock<Regex> = LazyLock::new(|| {
    Regex::new(&format!(r"^[›❯]{WS}+{}", regex::escape(CODEX_IDLE_TEXT))).expect("codexPlaceholder")
});

/// `codexWorking`.
static CODEX_WORKING: LazyLock<Regex> =
    LazyLock::new(|| Regex::new(&format!(r"^[•·◦]{WS}+Working(?-u:\b)")).expect("codexWorking"));

/// `codexFooter`: "gpt-5.6-sol low · /tmp".
static CODEX_FOOTER: LazyLock<Regex> = LazyLock::new(|| {
    Regex::new(&format!(
        r"^(?i:gpt-|o[1-9]){NWS}*(?:{WS}+[^·]+)?{WS}+·{WS}+/{NWS}"
    ))
    .expect("codexFooter")
});

/// `IsCodexCommand`: commands a Codex pane may report.
pub fn is_codex_command(cmd: &str) -> bool {
    matches!(cmd, "node" | "codex" | "bwrap")
}

/// `CodexPane` reports whether the live tail of the screen is Codex's TUI.
pub fn codex_pane(pane: &str) -> bool {
    hermes_region(pane).iter().any(|raw| {
        let line = strip_space1(raw);
        CODEX_PLACEHOLDER.is_match(&line)
            || CODEX_WORKING.is_match(&line)
            || CODEX_FOOTER.is_match(&line)
            || CODEX_CHIP.is_match(&line)
    })
}

/// `codexComposerBox`: the Codex composer (prompt row through the row above
/// the live footer) and the prompt row index.
pub(crate) fn codex_composer_box(pane: &str) -> Option<(String, usize)> {
    if !codex_pane(pane) {
        return None;
    }
    let lines = split_rows(pane);
    let footer = lines
        .iter()
        .rposition(|line| CODEX_FOOTER.is_match(&strip_space1(&strip_ansi(line))))?;
    if footer == 0 {
        return None;
    }
    let mut bottom = footer - 1;
    while strip_space(&strip_dim(lines[bottom])).is_empty() {
        if bottom == 0 {
            return None;
        }
        bottom -= 1;
    }
    let mut start = None;
    let mut j = bottom as isize;
    while j >= 0 && bottom - j as usize <= COMPOSER_BOX_MAX_ROWS {
        if PROMPT_LINE.is_match(lines[j as usize]) {
            start = Some(j as usize);
            break;
        }
        j -= 1;
    }
    let start = start?;
    let rows = &lines[start..=bottom];
    if rows.is_empty() || !PROMPT_LINE.is_match(rows[0]) {
        return None;
    }
    let mut out = Vec::with_capacity(rows.len());
    for (k, row) in rows.iter().enumerate() {
        let mut clean = strip_dim(row);
        if k == 0 {
            clean = strip_prompt(&clean);
            if codex_placeholder_only(&clean) {
                clean.clear();
            }
        } else if PROMPT_LINE.is_match(row) {
            return None;
        }
        out.push(clean.trim_end_matches(SPACE_TAB_NBSP).to_string());
    }
    Some((out.join("\n"), start))
}

/// `codexComposerViewportFillsPane`: a Codex composer whose prompt row is the
/// first non-blank row of the capture (a short pane showing a later part).
pub(crate) fn codex_composer_viewport_fills_pane(pane: &str, top: usize) -> bool {
    let Some((_, parsed_top)) = codex_composer_box(pane) else {
        return false;
    };
    if parsed_top != top {
        return false;
    }
    let lines = split_rows(pane);
    if top > lines.len() {
        return false;
    }
    lines[..top]
        .iter()
        .all(|line| strip_space(&strip_ansi(&strip_dim(line))).is_empty())
}

/// `codexComposerTailMatches`: a short Codex pane shows a proper suffix of
/// our message.
pub(crate) fn codex_composer_tail_matches(pane: &str, want: &str) -> bool {
    let Some((box_text, top)) = codex_composer_box(pane) else {
        return false;
    };
    if !codex_composer_viewport_fills_pane(pane, top) {
        return false;
    }
    let got = strip_space(&box_text);
    let want = strip_space(want);
    long_enough(&got) && !want.starts_with(&got) && want.ends_with(&got)
}

/// `codexPlaceholderOnly`.
pub(crate) fn codex_placeholder_only(text: &str) -> bool {
    text.trim().starts_with(CODEX_IDLE_TEXT)
}

/// `paneDialog`: a screen waiting on a human decision.
pub(crate) fn pane_dialog(pane: &str) -> bool {
    if hermes_dialog(pane) || codex_search_active(pane) || codex_navigation_menu(pane) {
        return true;
    }
    hermes_region(pane).iter().any(|line| {
        let text = go_lower(line);
        (text.contains("esc to cancel") || text.contains("esc to go back"))
            && (text.contains("enter to") || text.contains("confirm"))
    })
}

/// `Dialog` is `paneDialog` for callers outside the package.
pub fn dialog(pane: &str) -> bool {
    pane_dialog(pane)
}

/// `codexNavigationMenu`: Codex's numbered new-chat / command-center menu.
pub(crate) fn codex_navigation_menu(pane: &str) -> bool {
    const CHOICES: [&str; 3] = [
        "1. new chat",
        "2. agent command center",
        "3. resume another chat",
    ];
    let mut next = 0;
    for line in hermes_region(pane) {
        let text = go_lower(line.trim().trim_matches(['›', '❯', '>', ' ']));
        if text == CHOICES[next] {
            next += 1;
            if next == CHOICES.len() {
                return true;
            }
        }
    }
    false
}

/// `codexSearchActive`: Codex's search/command line is open on the last row.
pub(crate) fn codex_search_active(pane: &str) -> bool {
    let stripped = strip_ansi(pane);
    let lines = split_rows(stripped.trim());
    let (last, earlier) = lines.split_last().expect("split yields one row");
    let prompt = earlier.iter().any(|line| line.trim().starts_with('›'));
    if !prompt && !codex_pane(pane) {
        return false;
    }
    let last = last.trim();
    last.starts_with('/') || (last.starts_with('?') && !codex_shortcut_hint(last))
}

/// `codexShortcutHint`: the "? for shortcuts" footer, not a search.
fn codex_shortcut_hint(row: &str) -> bool {
    match row.strip_prefix("? for shortcuts") {
        Some(rest) => rest.chars().next().is_none_or(char::is_whitespace),
        None => false,
    }
}
