//! Port of the Claude Code composer box reader of internal/tmux/composer.go
//! (`isComposerBoxBorder`, `isComposerStatus`, `claudeComposerBoxAt`,
//! `claudeComposerTailMatches`, `ClaudeEmptyComposer`,
//! `ClaudeComposerHoldsOnboarding`).

use crate::launch_args::onboarding_prompt;
use crate::screen::ansi::{strip_ansi, strip_dim};
use crate::screen::composer::{
    COMPOSER_BOX_MAX_ROWS, PasteVerdict, classify_paste, composer_box_rows, composer_content_width,
    long_enough, wrapped_message_rows,
};
use crate::screen::gostr::{SPACE_TAB_NBSP, rune_len, strip_space};
use crate::screen::harness::codex::dialog;
use crate::screen::region::{PROMPT_LINE, USAGE_LIMIT_NOTICE, busy, split_rows, strip_prompt};

/// `composerStatusMarkers`: the status/footer line under a Claude composer.
const COMPOSER_STATUS_MARKERS: [&str; 3] = ["-- INSERT --", "-- NORMAL --", "bypass permissions"];

/// `composerBorderRunes`.
fn is_border_rune(c: char) -> bool {
    c == '─' || c == '━'
}

/// `composerBorderMin`.
const COMPOSER_BORDER_MIN: usize = 6;

/// `composerDialogMarkers`: footer hints of a modal picker.
const COMPOSER_DIALOG_MARKERS: [&str; 2] = ["Enter to select", "Esc to cancel"];

/// `isComposerBoxBorder`: a row that begins and ends with a rule and carries
/// at least six of them (the top border carries the agent's name).
pub(crate) fn is_composer_box_border(line: &str) -> bool {
    let core: Vec<char> = strip_space(&strip_ansi(line)).chars().collect();
    let (Some(&first), Some(&last)) = (core.first(), core.last()) else {
        return false;
    };
    if !is_border_rune(first) || !is_border_rune(last) {
        return false;
    }
    core.iter().filter(|c| is_border_rune(**c)).count() >= COMPOSER_BORDER_MIN
}

/// `isComposerStatus`.
fn is_composer_status(line: &str) -> bool {
    let clean = strip_ansi(line);
    COMPOSER_STATUS_MARKERS
        .iter()
        .any(|marker| clean.contains(marker))
}

/// `claudeComposerBoxAt`: the Claude composer box content and its top
/// border row.
pub(crate) fn claude_composer_box_at(pane: &str) -> Option<(String, usize)> {
    let lines = split_rows(pane);
    let status = lines.iter().rposition(|line| is_composer_status(line))?;
    for line in &lines[status..] {
        let clean = strip_ansi(line);
        if COMPOSER_DIALOG_MARKERS
            .iter()
            .any(|marker| clean.contains(marker))
        {
            return None;
        }
    }
    let mut bottom = None;
    let mut warning_rows = false;
    let mut usage_notice = false;
    for i in (0..status).rev() {
        if strip_space(&strip_dim(lines[i])).is_empty() {
            continue;
        }
        if is_composer_box_border(lines[i]) {
            bottom = Some(i);
            break;
        }
        if is_claude_warning_notice_line(lines[i]) {
            warning_rows = true;
            if is_claude_usage_limit_notice_line(lines[i]) {
                usage_notice = true;
            }
            continue;
        }
        return None;
    }
    let bottom = bottom?;
    if warning_rows && !usage_notice {
        return None;
    }
    let mut top = None;
    let mut j = bottom;
    while j > 0 && bottom - (j - 1) <= COMPOSER_BOX_MAX_ROWS {
        j -= 1;
        if is_composer_box_border(lines[j]) {
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
        let mut clean = strip_dim(row);
        if k == 0 {
            clean = strip_prompt(&clean);
        } else if PROMPT_LINE.is_match(row) {
            return None;
        }
        out.push(clean.trim_end_matches(SPACE_TAB_NBSP).to_string());
    }
    Some((out.join("\n"), top))
}

/// `isClaudeUsageLimitNoticeLine`.
fn is_claude_usage_limit_notice_line(line: &str) -> bool {
    USAGE_LIMIT_NOTICE.is_match(strip_ansi(line).trim())
}

/// `isClaudeWarningNoticeLine`.
fn is_claude_warning_notice_line(line: &str) -> bool {
    strip_ansi(line).trim().starts_with('⚠')
}

/// `claudeComposerTailMatches`: the visible tail of our message in a clipped
/// Claude composer viewport, proven at the measured pane width.
pub(crate) fn claude_composer_tail_matches(pane: &str, want: &str, pane_width: i64) -> bool {
    if pane_width <= 0 {
        return false;
    }
    let Some((box_text, _)) = claude_composer_box_at(pane) else {
        return false;
    };
    let got = strip_space(&box_text);
    let stripped = strip_space(want);
    if !long_enough(&got) || stripped.starts_with(&got) || !stripped.ends_with(&got) {
        return false;
    }
    match wrapped_message_rows(want, pane_width) {
        Ok(expected) if expected > composer_box_rows(&box_text) as i64 => {}
        _ => return false,
    }
    let hidden = rune_len(&stripped) as i64 - rune_len(&got) as i64;
    hidden * 2 >= composer_content_width(pane_width)
}

/// `ClaudeEmptyComposer`: the native bordered composer and footer, empty, on
/// a pane that is neither busy nor showing a dialog.
pub fn claude_empty_composer(pane: &str) -> bool {
    claude_composer_box_at(pane).is_some_and(|(text, _)| strip_space(&text).is_empty())
        && !busy(pane)
        && !dialog(pane)
}

/// `ClaudeComposerHoldsOnboarding`: a Claude composer holding nothing but
/// bp open's own onboarding prompt for `session`, unsubmitted.
pub fn claude_composer_holds_onboarding(pane: &str, session: &str) -> bool {
    if claude_composer_box_at(pane).is_none() || busy(pane) || dialog(pane) {
        return false;
    }
    let texts = [onboarding_prompt(session)];
    classify_paste(pane, &texts).0 == PasteVerdict::Exact
}
