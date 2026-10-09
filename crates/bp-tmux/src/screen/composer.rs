//! Port of internal/tmux/composer.go (the composer BOX view, paste
//! classification and the `BlockedBy*` reasons) plus the composer helpers of
//! tmux.go that build on it: `composerFilled`, `composerClearBudget`,
//! `wrappedMessageRows`, `composerRuneWidth`, `composerSnapshot` and the pure
//! verdict of `pasteIntegrity`.

use super::gostr::{rune_len, strip_space};
use super::harness::claude::{claude_composer_box_at, claude_composer_tail_matches};
use super::harness::codex::{codex_composer_box, codex_composer_viewport_fills_pane};
use super::harness::hermes::{
    HERMES_CLEAR_ATTEMPTS_MAX, HERMES_CLEAR_MARGIN, HERMES_CLEAR_PRESSES_PER_ROW,
    hermes_clear_budget, hermes_composer_box, hermes_dialog,
};
use super::harness::opencode::{opencode_composer_box, opencode_paste_chip};
use super::region::{busy, claude_paste_chip, codex_paste_chip, composer_content, typing};
use super::unicode_tables::is_mark_nonspacing_or_enclosing;
use crate::error::Error;

/// `composerBoxMaxRows`: bound of the upward search for a box's top border.
pub(crate) const COMPOSER_BOX_MAX_ROWS: usize = 60;

/// `composerBoxScrollRows`: interior height above which a box is assumed to
/// be scrolling.
pub(crate) const COMPOSER_BOX_SCROLL_ROWS: usize = 30;

/// `pasteRelatedMin`: shared characters before "related" means anything.
pub(crate) const PASTE_RELATED_MIN: usize = 32;

/// `composerBoxScrolled`: the box may be showing only part of its content.
pub(crate) fn composer_box_scrolled(pane: &str, box_text: &str, top: usize) -> bool {
    top <= 1
        || composer_box_rows(box_text) >= COMPOSER_BOX_SCROLL_ROWS
        || codex_composer_viewport_fills_pane(pane, top)
}

/// `composerBox`: the full rendered composer content, or `None` when the box
/// structure is not recognised.
pub(crate) fn composer_box(pane: &str) -> Option<String> {
    composer_box_at(pane).map(|(text, _)| text)
}

/// `composerBoxAt`: [`composer_box`] plus the row index of the box's top.
/// Readers are tried in order: Claude, Codex, opencode, Hermes.
pub(crate) fn composer_box_at(pane: &str) -> Option<(String, usize)> {
    claude_composer_box_at(pane)
        .or_else(|| codex_composer_box(pane))
        .or_else(|| opencode_composer_box(pane))
        .or_else(|| hermes_composer_box(pane))
}

/// `composerBoxText`: whitespace-stripped full box content.
pub(crate) fn composer_box_text(pane: &str) -> Option<String> {
    composer_box(pane).map(|text| strip_space(&text))
}

/// `composerJudgeText`: box content only while the box is a COMPLETE view.
pub(crate) fn composer_judge_text(pane: &str) -> Option<String> {
    let (text, top) = composer_box_at(pane)?;
    if composer_box_scrolled(pane, &text, top) {
        return None;
    }
    Some(strip_space(&text))
}

/// `composerBoxRows`: interior rows of the box.
pub(crate) fn composer_box_rows(box_text: &str) -> usize {
    if box_text.is_empty() {
        return 0;
    }
    box_text.matches('\n').count() + 1
}

/// `pasteVerdict`: whose text a non-empty composer is holding.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub(crate) enum PasteVerdict {
    Foreign,
    Exact,
    Damaged,
}

/// `classifyPaste` judges a non-empty composer against the texts bp is
/// responsible for and returns the matching text ("" when foreign).
pub(crate) fn classify_paste<'a, S: AsRef<str>>(
    pane: &str,
    texts: &'a [S],
) -> (PasteVerdict, &'a str) {
    if paste_chip(pane) {
        return (PasteVerdict::Foreign, "");
    }
    let Some(got) = composer_box_text(pane) else {
        return (PasteVerdict::Foreign, "");
    };
    if got.is_empty() {
        return (PasteVerdict::Foreign, "");
    }
    for text in texts {
        let text = text.as_ref();
        if strip_space(text) == got {
            return (PasteVerdict::Exact, text);
        }
    }
    for text in texts {
        let text = text.as_ref();
        let want = strip_space(text);
        if !related_paste(&got, &want) {
            continue;
        }
        if has_wide_runes(text) {
            return (PasteVerdict::Exact, text);
        }
        return (PasteVerdict::Damaged, text);
    }
    (PasteVerdict::Foreign, "")
}

/// `hasWideRunes`: characters a terminal may render wider than one column.
pub(crate) fn has_wide_runes(text: &str) -> bool {
    text.chars().any(|c| {
        let r = c as u32;
        r == 0xFE0F
            || r == 0x200D
            || (0x1F000..=0x1FAFF).contains(&r)
            || (0x2600..=0x27BF).contains(&r)
            || (0x1100..=0x11FF).contains(&r)
            || (0x2E80..=0xA4CF).contains(&r)
            || (0xAC00..=0xD7A3).contains(&r)
            || (0xF900..=0xFAFF).contains(&r)
    })
}

/// `relatedPaste`: `got` looks like a mutilated render of `want`.
pub(crate) fn related_paste(got: &str, want: &str) -> bool {
    let g: Vec<char> = got.chars().collect();
    let w: Vec<char> = want.chars().collect();
    if g.len() < PASTE_RELATED_MIN || w.len() < PASTE_RELATED_MIN {
        return false;
    }
    if want.contains(got) || got.contains(want) {
        return true;
    }
    common_prefix(&g, &w) >= PASTE_RELATED_MIN || common_suffix(&g, &w) >= PASTE_RELATED_MIN
}

fn common_prefix(a: &[char], b: &[char]) -> usize {
    a.iter().zip(b).take_while(|(x, y)| x == y).count()
}

fn common_suffix(a: &[char], b: &[char]) -> usize {
    a.iter()
        .rev()
        .zip(b.iter().rev())
        .take_while(|(x, y)| x == y)
        .count()
}

/// `BlockedByPasteChip`.
pub const BLOCKED_BY_PASTE_CHIP: &str =
    "composer contains an unreadable paste (chip); bp will not alter it";
/// `BlockedByForeignText`.
pub const BLOCKED_BY_FOREIGN_TEXT: &str = "composer contains foreign text";
/// `BlockedByBusyPane`.
pub const BLOCKED_BY_BUSY_PANE: &str = "pane is working (esc to interrupt)";
/// `BlockedByDialog`.
pub const BLOCKED_BY_DIALOG: &str =
    "confirmation/selection screen is open — only a human may answer";
/// `BlockedByPaneLock` (panelock.go).
pub const BLOCKED_BY_PANE_LOCK: &str = "another bp process is writing to this pane (pane lock)";

/// `ComposerBlockReason` names why a pane cannot take a message, or "".
pub fn composer_block_reason<S: AsRef<str>>(pane: &str, texts: &[S]) -> &'static str {
    if busy(pane) {
        return BLOCKED_BY_BUSY_PANE;
    }
    composer_content_block_reason(pane, texts)
}

/// `ComposerContentBlockReason`: [`composer_block_reason`] without the busy
/// question.
pub fn composer_content_block_reason<S: AsRef<str>>(pane: &str, texts: &[S]) -> &'static str {
    if hermes_dialog(pane) {
        return BLOCKED_BY_DIALOG;
    }
    if !composer_filled(pane) {
        return "";
    }
    if paste_chip(pane) {
        return BLOCKED_BY_PASTE_CHIP;
    }
    if stuck_paste(pane, texts).is_some() {
        return "";
    }
    BLOCKED_BY_FOREIGN_TEXT
}

/// `StuckPaste`: which of `texts` a non-empty composer is holding as our own
/// unsubmitted paste (exact or damaged). `None` for a foreign, unreadable or
/// empty composer.
pub fn stuck_paste<'a, S: AsRef<str>>(pane: &str, texts: &'a [S]) -> Option<&'a str> {
    let (verdict, text) = classify_paste(pane, texts);
    (verdict != PasteVerdict::Foreign).then_some(text)
}

/// `ExactPaste`: a complete composer view holds one of `texts` exactly.
pub fn exact_paste<S: AsRef<str>>(pane: &str, texts: &[S]) -> bool {
    if composer_judge_text(pane).is_none() {
        return false;
    }
    classify_paste(pane, texts).0 == PasteVerdict::Exact
}

/// `ComposerEmpty`: a readable composer box whose content is empty.
pub fn composer_empty(pane: &str) -> bool {
    composer_box_text(pane).is_some_and(|text| text.is_empty()) && !paste_chip(pane)
}

/// `DamagedPaste`: a complete composer view holds a mutilated copy of one of
/// `texts`.
pub fn damaged_paste<S: AsRef<str>>(pane: &str, texts: &[S]) -> bool {
    if composer_judge_text(pane).is_none() {
        return false;
    }
    classify_paste(pane, texts).0 == PasteVerdict::Damaged
}

/// `pasteChip`: the composer shows a placeholder instead of its text.
pub(crate) fn paste_chip(pane: &str) -> bool {
    codex_paste_chip(pane) || claude_paste_chip(pane) || opencode_paste_chip(pane)
}

/// `composerFilled`: the composer holds anything visible.
pub(crate) fn composer_filled(pane: &str) -> bool {
    if let Some(text) = composer_box_text(pane) {
        return !text.is_empty() || paste_chip(pane);
    }
    typing(pane) || paste_chip(pane)
}

/// `composerClearMargin`.
pub(crate) const COMPOSER_CLEAR_MARGIN: i64 = 4;
/// `composerClearAttemptsMax`.
pub(crate) const COMPOSER_CLEAR_ATTEMPTS_MAX: i64 = 64;

/// `composerClearBudget` sizes one clear pass from the full message.
pub(crate) fn composer_clear_budget(pane: &str, message: &str, width: i64) -> Result<i64, Error> {
    let rows = wrapped_message_rows(message, width)?;
    let (presses_per_row, margin, limit) = if hermes_clear_budget(pane) > 0 {
        (
            HERMES_CLEAR_PRESSES_PER_ROW,
            HERMES_CLEAR_MARGIN,
            HERMES_CLEAR_ATTEMPTS_MAX,
        )
    } else {
        (1, COMPOSER_CLEAR_MARGIN, COMPOSER_CLEAR_ATTEMPTS_MAX)
    };
    Ok((rows * presses_per_row + margin).min(limit))
}

/// `composerIndent`.
pub(crate) const COMPOSER_INDENT: i64 = 2;
/// `composerRightMargin`.
pub(crate) const COMPOSER_RIGHT_MARGIN: i64 = 2;

/// `composerContentWidth`: cells a composer row holds.
pub(crate) fn composer_content_width(pane_width: i64) -> i64 {
    pane_width - COMPOSER_INDENT - COMPOSER_RIGHT_MARGIN
}

/// `wrappedMessageRows`: rows Claude renders the message in (word wrap).
pub(crate) fn wrapped_message_rows(message: &str, pane_width: i64) -> Result<i64, Error> {
    let content_width = composer_content_width(pane_width);
    if content_width < 1 {
        return Err(Error::other(format!("invalid pane width {pane_width}")));
    }
    let mut rows = 0;
    for line in message.split('\n') {
        let mut line_rows = 1;
        let mut column = 0;
        for (i, word) in line.split(' ').enumerate() {
            let mut cells: i64 = word.chars().map(composer_rune_width).sum();
            if i > 0 {
                if column + 1 + cells <= content_width {
                    column += 1 + cells;
                    continue;
                }
                if cells == 0 {
                    continue;
                }
                line_rows += 1;
                column = 0;
            }
            while column + cells > content_width {
                cells -= content_width - column;
                line_rows += 1;
                column = 0;
            }
            column += cells;
        }
        rows += line_rows;
    }
    Ok(rows)
}

/// `composerRuneWidth`: Go's own width table (deliberately not
/// `unicode-width`).
pub(crate) fn composer_rune_width(c: char) -> i64 {
    if is_mark_nonspacing_or_enclosing(c) || c == '\u{200d}' {
        return 0;
    }
    let r = c as u32;
    if (0x1100..=0x11ff).contains(&r)
        || (0x2e80..=0xa4cf).contains(&r)
        || (0xac00..=0xd7a3).contains(&r)
        || (0xf900..=0xfaff).contains(&r)
        || (0xfe10..=0xfe6f).contains(&r)
        || (0xff01..=0xff60).contains(&r)
        || (0xffe0..=0xffe6).contains(&r)
        || (0x1f300..=0x1faff).contains(&r)
    {
        2
    } else {
        1
    }
}

/// `composerSnapshot`: the composer state a verdict rests on.
pub(crate) fn composer_snapshot(pane: &str) -> String {
    if paste_chip(pane) {
        return "chip".to_string();
    }
    if let Some(text) = composer_judge_text(pane) {
        return format!("box:{text}");
    }
    format!("row:{}", composer_content(pane))
}

/// `pasteVerification`: checkPaste's reading of a pane.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub(crate) enum PasteVerification {
    Unreadable,
    Intact,
    Mangled,
    Broken,
    Incomplete,
}

/// `(*Client).pasteIntegrity`, which reads no client state. `pane_width` is
/// Go's optional `paneWidths[0]` (`None` when not passed).
pub(crate) fn paste_integrity(
    pane: &str,
    message: &str,
    pane_width: Option<i64>,
) -> PasteVerification {
    if paste_chip(pane) {
        return PasteVerification::Unreadable;
    }
    let Some((text, top)) = composer_box_at(pane) else {
        return PasteVerification::Unreadable;
    };
    let got = strip_space(&text);
    let want = strip_space(message);
    if got == want {
        return PasteVerification::Intact;
    }
    if got.is_empty() {
        return PasteVerification::Unreadable;
    }
    if composer_box_rows(&text) >= COMPOSER_BOX_SCROLL_ROWS || top <= 1 {
        return PasteVerification::Unreadable;
    }
    if want.starts_with(&got) {
        return PasteVerification::Incomplete;
    }
    if let Some(width) = pane_width
        && claude_composer_tail_matches(pane, message, width)
    {
        return PasteVerification::Intact;
    }
    if codex_composer_viewport_fills_pane(pane, top) {
        return PasteVerification::Unreadable;
    }
    if related_paste(&got, &want) {
        return PasteVerification::Mangled;
    }
    PasteVerification::Broken
}

/// `len([]rune(s)) >= pasteRelatedMin`.
pub(crate) fn long_enough(s: &str) -> bool {
    rune_len(s) >= PASTE_RELATED_MIN
}
