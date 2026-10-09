//! Port of internal/tmux/hermes.go: Hermes screen recognition (idle, busy,
//! dialog), the live-tail region shared by every harness recognizer, the
//! Hermes composer reader and its clear budget, and `IsAgentPane`.

use std::sync::LazyLock;

use regex::Regex;

use crate::screen::ansi::{strip_ansi, strip_dim, strip_italic};
use crate::screen::class::{NWS, WS};
use crate::screen::composer::{COMPOSER_BOX_MAX_ROWS, composer_box_rows};
use crate::screen::gostr::{SPACE_TAB_NBSP, strip_space};
use crate::screen::harness::codex::{codex_pane, is_codex_command};
use crate::screen::harness::opencode::{is_opencode_command, opencode_pane};
use crate::screen::region::{
    PROMPT_LINE, is_agent_command, is_composer_border, split_rows, strip_prompt,
};

/// `hermesBin`.
pub(crate) const HERMES_BIN: &str = "hermes";

/// `hermesPlaceholderText`.
const HERMES_PLACEHOLDER_TEXT: &str = "Ask anything, or type / for commands";

static HERMES_IDLE_COMPOSER: LazyLock<Regex> = LazyLock::new(|| {
    Regex::new(&format!(
        r"^[❯›]{WS}+{}",
        regex::escape(HERMES_PLACEHOLDER_TEXT)
    ))
    .expect("hermesIdleComposer")
});

static HERMES_BUSY_COMPOSER: LazyLock<Regex> =
    LazyLock::new(|| Regex::new(&format!(r"^⚕{WS}*[❯›]")).expect("hermesBusyComposer"));

static HERMES_STATUS_ROW: LazyLock<Regex> = LazyLock::new(|| {
    Regex::new(&format!(r"^⚕{WS}+{NWS}.*·{WS}*(?:[0-9]+%|--){WS}*·")).expect("hermesStatusRow")
});

static HERMES_DIALOG_COMPOSER: LazyLock<Regex> =
    LazyLock::new(|| Regex::new(&format!(r"^⚠{WS}*[❯›]")).expect("hermesDialogComposer"));

static HERMES_SELECT_AFFORDANCE: LazyLock<Regex> =
    LazyLock::new(|| Regex::new(r"(?i)to select.*to confirm").expect("hermesSelectAffordance"));

/// `hermesDialog`: a Hermes permission/selection prompt is up.
pub(crate) fn hermes_dialog(pane: &str) -> bool {
    if !hermes_pane(pane) {
        return false;
    }
    hermes_region(pane).iter().any(|line| {
        HERMES_DIALOG_COMPOSER.is_match(line) || HERMES_SELECT_AFFORDANCE.is_match(line)
    })
}

/// `hermesTailRows`.
const HERMES_TAIL_ROWS: usize = 14;

/// `hermesRegion`: the last 14 non-trailing-blank rows, ANSI stripped and
/// left-trimmed of spaces, tabs and NBSP.
pub(crate) fn hermes_region(pane: &str) -> Vec<String> {
    let rows = split_rows(pane);
    let lines = trim_trailing_blank(&rows);
    let start = lines.len().saturating_sub(HERMES_TAIL_ROWS);
    lines[start..]
        .iter()
        .map(|line| {
            strip_ansi(line)
                .trim_start_matches(SPACE_TAB_NBSP)
                .to_string()
        })
        .collect()
}

/// `trimTrailingBlank`: drops trailing rows that are blank after ANSI
/// stripping.
pub(crate) fn trim_trailing_blank<S: AsRef<str>>(lines: &[S]) -> &[S] {
    let mut end = lines.len();
    while end > 0 && strip_ansi(lines[end - 1].as_ref()).trim().is_empty() {
        end -= 1;
    }
    &lines[..end]
}

/// `HermesPane` reports whether the live tail of the screen is Hermes' TUI.
pub fn hermes_pane(pane: &str) -> bool {
    hermes_region(pane).iter().any(|line| {
        HERMES_IDLE_COMPOSER.is_match(line)
            || HERMES_BUSY_COMPOSER.is_match(line)
            || HERMES_STATUS_ROW.is_match(line)
    })
}

/// `HermesIdle`: Hermes' idle composer placeholder is on screen.
pub fn hermes_idle(pane: &str) -> bool {
    hermes_region(pane)
        .iter()
        .any(|line| HERMES_IDLE_COMPOSER.is_match(line))
}

/// `hermesBusy`: Hermes' busy composer row.
pub(crate) fn hermes_busy(pane: &str) -> bool {
    hermes_region(pane)
        .iter()
        .any(|line| HERMES_BUSY_COMPOSER.is_match(line))
}

/// `hermesForceRefused`.
pub(crate) fn hermes_force_refused(pane: &str) -> bool {
    hermes_busy(pane) && hermes_pane(pane)
}

/// `hermesPlaceholderOnly`.
pub(crate) fn hermes_placeholder_only(row: &str) -> bool {
    let stripped = strip_space(row);
    if stripped.is_empty() {
        return false;
    }
    stripped.starts_with(&strip_space(HERMES_PLACEHOLDER_TEXT))
}

/// `IsHermesCommand`.
pub fn is_hermes_command(cmd: &str) -> bool {
    matches!(cmd, "python" | "python3" | "hermes")
}

/// `IsAgentPane`: command AND screen together.
pub fn is_agent_pane(cmd: &str, pane: &str) -> bool {
    if is_agent_command(cmd) {
        return true;
    }
    if is_hermes_command(cmd) && hermes_pane(pane) {
        return true;
    }
    if is_codex_command(cmd) && codex_pane(pane) {
        return true;
    }
    is_opencode_command(cmd) && opencode_pane(pane)
}

/// `hermesComposerBox`: the box between the last two pure rules, on a proven
/// Hermes screen.
pub(crate) fn hermes_composer_box(pane: &str) -> Option<(String, usize)> {
    if !hermes_pane(pane) {
        return None;
    }
    let lines = split_rows(pane);
    let is_rule = |line: &str| is_composer_border(&strip_space(&strip_dim(line)));
    let bottom = lines.iter().rposition(|line| is_rule(line))?;
    if bottom == 0 {
        return None;
    }
    let mut top = None;
    let mut j = bottom;
    while j > 0 && bottom - (j - 1) <= COMPOSER_BOX_MAX_ROWS {
        j -= 1;
        if is_rule(lines[j]) {
            top = Some(j);
            break;
        }
    }
    let top = top?;
    let rows = &lines[top + 1..bottom];
    if rows.is_empty() || !PROMPT_LINE.is_match(rows[0]) {
        return None;
    }
    let mut out = Vec::with_capacity(rows.len());
    for (k, row) in rows.iter().enumerate() {
        let mut clean = strip_dim(&strip_italic(row));
        if k == 0 {
            clean = strip_prompt(&clean);
            if hermes_placeholder_only(&clean) {
                clean.clear();
            }
        } else if PROMPT_LINE.is_match(row) {
            return None;
        }
        out.push(clean.trim_end_matches(SPACE_TAB_NBSP).to_string());
    }
    Some((out.join("\n"), top))
}

/// `hermesClearPressesPerRow`.
pub(crate) const HERMES_CLEAR_PRESSES_PER_ROW: i64 = 2;
/// `hermesClearMargin`.
pub(crate) const HERMES_CLEAR_MARGIN: i64 = 4;
/// `hermesClearAttemptsMax`.
pub(crate) const HERMES_CLEAR_ATTEMPTS_MAX: i64 = 48;

/// `hermesClearBudget`: C-u presses for a Hermes composer, 0 when the pane
/// has no readable Hermes box.
pub(crate) fn hermes_clear_budget(pane: &str) -> i64 {
    let Some((box_text, _)) = hermes_composer_box(pane) else {
        return 0;
    };
    let budget =
        composer_box_rows(&box_text) as i64 * HERMES_CLEAR_PRESSES_PER_ROW + HERMES_CLEAR_MARGIN;
    budget.min(HERMES_CLEAR_ATTEMPTS_MAX)
}
