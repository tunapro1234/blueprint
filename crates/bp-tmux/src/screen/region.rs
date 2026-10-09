//! Port of the screen functions in internal/tmux/tmux.go: prompt rows,
//! composer row/tail/trail, busy regions and `Busy`, `Typing`, the status
//! footer, `AuthExpired`, `UsageLimitReason`, Remote Control detection,
//! command recognizers and `launchWait`.

use std::sync::LazyLock;

use regex::Regex;

use super::ansi::{strip_ansi, strip_dim, strip_italic};
use super::class::{NWS, WS};
use super::composer::{
    composer_box_at, composer_box_text, composer_judge_text, has_wide_runes, paste_chip,
    related_paste,
};
use super::gostr::{SPACE_TAB_NBSP, go_lower, strip_space};
use super::harness::claude::{claude_composer_box_at, claude_composer_tail_matches};
use super::harness::codex::{
    codex_composer_box, codex_composer_tail_matches, codex_pane, codex_placeholder_only,
};
use super::harness::hermes::{hermes_busy, hermes_pane, hermes_placeholder_only};
use super::harness::opencode::opencode_busy;
use crate::error::{Error, ErrorKind};

/// `promptLine`: a composer row starts with the prompt marker, optionally
/// preceded by SGR codes and indentation.
pub(crate) static PROMPT_LINE: LazyLock<Regex> = LazyLock::new(|| {
    Regex::new("^(?:\x1b\\[[0-9;]*m|[\t ])*[❯›](?:\x1b\\[[0-9;]*m)?").expect("promptLine")
});

pub(crate) fn split_rows(pane: &str) -> Vec<&str> {
    pane.split('\n').collect()
}

/// `promptLine.ReplaceAllString(s, "")`.
pub(crate) fn strip_prompt(s: &str) -> String {
    PROMPT_LINE.replace_all(s, "").into_owned()
}

/// Index of the last row that carries a prompt marker.
fn last_prompt_row(lines: &[&str]) -> Option<usize> {
    lines.iter().rposition(|line| PROMPT_LINE.is_match(line))
}

/// `Typing` reports whether the final rendered composer line contains real
/// text. Older prompt lines are deliberately ignored.
pub fn typing(pane: &str) -> bool {
    if let Some((composer, _)) = codex_composer_box(pane) {
        return !strip_space(&composer).is_empty();
    }
    if let Some((composer, _)) = claude_composer_box_at(pane) {
        return !strip_space(&composer).is_empty();
    }
    !composer_content(pane).is_empty()
}

/// `composerContent` returns the whitespace-stripped text of the final rendered
/// composer line, after the prompt marker, with dim placeholder/ghost text and
/// ANSI colour removed. NBSP counts as whitespace.
pub(crate) fn composer_content(pane: &str) -> String {
    let lines = split_rows(pane);
    let Some(last) = last_prompt_row(&lines) else {
        return String::new();
    };
    let mut composer = lines[last].to_string();
    if composer.is_empty() {
        return String::new();
    }
    if hermes_pane(pane) {
        // Hermes renders placeholder and ghost text ITALIC, not dim; on a
        // screen proven to be Hermes the italic segments are not input.
        composer = strip_italic(&composer);
    }
    let composer = strip_dim(&composer);
    let after = strip_prompt(&composer);
    if codex_pane(pane) && codex_placeholder_only(&after) {
        return String::new();
    }
    if hermes_placeholder_only(&after) {
        return String::new();
    }
    strip_space(&after)
}

/// `codexChip`: `\[Pasted Content\s+\d+\s+chars\]`.
pub(crate) static CODEX_CHIP: LazyLock<Regex> = LazyLock::new(|| {
    Regex::new(&format!(r"\[Pasted Content{WS}+[0-9]+{WS}+chars\]")).expect("codexChip")
});

/// `codexPasteChip` reports whether Codex's large-paste placeholder chip
/// ("[Pasted Content N chars]", collapsed or expanded/wrapped) is present in
/// the composer.
pub(crate) fn codex_paste_chip(pane: &str) -> bool {
    composer_tail(pane).is_some_and(|tail| CODEX_CHIP.is_match(&tail))
}

/// `claudeChip`: Claude Code's paste placeholder ("[Pasted text #1 +2 lines]").
static CLAUDE_CHIP: LazyLock<Regex> =
    LazyLock::new(|| Regex::new(r"\[Pasted text[^\]]*\]").expect("claudeChip"));

/// `claudePasteChip` reports whether the composer shows Claude's paste
/// placeholder.
pub(crate) fn claude_paste_chip(pane: &str) -> bool {
    composer_tail(pane).is_some_and(|tail| CLAUDE_CHIP.is_match(&tail))
}

/// `composerTail` joins every rendered row from the final prompt line to the
/// end of the pane with dim/ANSI and the prompt marker removed, each row
/// followed by a space. `None` when the pane has no prompt line at all.
pub(crate) fn composer_tail(pane: &str) -> Option<String> {
    let lines = split_rows(pane);
    let last = last_prompt_row(&lines)?;
    let mut out = String::new();
    for line in &lines[last..] {
        out.push_str(&strip_prompt(&strip_dim(line)));
        out.push(' ');
    }
    Some(out)
}

/// `codexBusyQueuePhrase`.
pub(crate) const CODEX_BUSY_QUEUE_PHRASE: &str = "tab to queue message";

/// `codexBusyQueue` reports whether the pane shows Codex's busy-composer queue
/// affordance (a dim footer, so it must disappear under `StripDim`).
pub(crate) fn codex_busy_queue(pane: &str) -> bool {
    if !pane.contains(CODEX_BUSY_QUEUE_PHRASE) {
        return false;
    }
    !strip_dim(pane).contains(CODEX_BUSY_QUEUE_PHRASE)
}

/// `composerHoldsMessage`: the composer still holds exactly our unsubmitted
/// message (box, final row, or a paste chip standing in for it).
pub(crate) fn composer_holds_message(pane: &str, want: &str) -> bool {
    composer_holds_message_at_width(pane, want, 0)
}

/// `composerHoldsMessageAtWidth` takes the message as injected; a Claude
/// viewport is judged by how the real text wraps at `pane_width` (0 = unknown).
pub(crate) fn composer_holds_message_at_width(pane: &str, message: &str, pane_width: i64) -> bool {
    let want = strip_space(message);
    if composer_box_text(pane).is_some_and(|text| text == want) {
        return true;
    }
    if claude_composer_tail_matches(pane, message, pane_width) {
        return true;
    }
    if codex_composer_tail_matches(pane, &want) {
        return true;
    }
    composer_content(pane) == want || paste_chip(pane)
}

/// `composerVerdict`: what the composer holds right after a paste.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub(crate) enum ComposerVerdict {
    /// Nothing is typed.
    Cleared,
    /// Our message, in one of the forms a TUI renders it in.
    Mine,
    /// Content unrelated to our message: the paste never landed.
    Other,
}

/// `classifyComposer`.
pub(crate) fn classify_composer(pane: &str, want: &str) -> ComposerVerdict {
    classify_composer_at_width(pane, want, 0)
}

/// `classifyComposerAtWidth`.
pub(crate) fn classify_composer_at_width(
    pane: &str,
    message: &str,
    pane_width: i64,
) -> ComposerVerdict {
    let want = strip_space(message);
    if paste_chip(pane) {
        return ComposerVerdict::Mine;
    }
    if claude_composer_tail_matches(pane, message, pane_width) {
        return ComposerVerdict::Mine;
    }
    if codex_composer_tail_matches(pane, &want) {
        return ComposerVerdict::Mine;
    }
    let got = composer_judge_text(pane).unwrap_or_else(|| composer_content(pane));
    if got.is_empty() {
        return ComposerVerdict::Cleared;
    }
    if got == want || want.starts_with(&got) || got.starts_with(&want) {
        return ComposerVerdict::Mine;
    }
    if has_wide_runes(&want) && related_paste(&got, &want) {
        return ComposerVerdict::Mine;
    }
    ComposerVerdict::Other
}

/// `composerTrail` inspects the rows between the final prompt line and the
/// composer's bottom border. Returns `(empty, foreign, found)`.
pub(crate) fn composer_trail(pane: &str) -> (usize, bool, bool) {
    let lines = split_rows(pane);
    let Some(last) = last_prompt_row(&lines) else {
        return (0, false, false);
    };
    let mut gap = Vec::new();
    let mut found = false;
    for line in &lines[last + 1..] {
        let s = strip_space(&strip_dim(line));
        if is_composer_border(&s) {
            found = true;
            break;
        }
        gap.push(s);
    }
    if !found {
        return (0, false, false);
    }
    if gap.iter().any(|s| !s.is_empty()) {
        return (0, true, true);
    }
    (gap.len(), false, true)
}

/// `isComposerBorder`: a PURE run of at least three `─`/`━`.
pub(crate) fn is_composer_border(s: &str) -> bool {
    let mut n = 0;
    for r in s.chars() {
        if r != '─' && r != '━' {
            return false;
        }
        n += 1;
    }
    n >= 3
}

/// `busyIndicator`: the legacy live-indicator signature next to "esc to
/// interrupt".
static BUSY_INDICATOR: LazyLock<Regex> = LazyLock::new(|| {
    Regex::new(&format!(
        r"\({WS}*(?:[0-9]+h{WS}+)?(?:[0-9]+m{WS}+)?[0-9]+(?:\.[0-9]+)?s?{WS}*[·•]|⏵"
    ))
    .expect("busyIndicator")
});

/// `busySpinner`: the spinner row Claude Code 2.1.233+ draws above its
/// composer while a turn runs (column 0, one symbol glyph, a single word with
/// an ellipsis, and optionally a counter that ends the row).
static BUSY_SPINNER: LazyLock<Regex> = LazyLock::new(|| {
    Regex::new(
        r"^[^\p{L}\p{N}\t\n\x0C\r ] +[^\t\n\x0C\r ()]*(?:…|\.\.\.) *(?:\((?:[0-9]+h +)?(?:[0-9]+m +)?[0-9]+(?:\.[0-9]+)?s[^()]*\))?$",
    )
    .expect("busySpinner")
});

/// `busySpinnerLookback`.
const BUSY_SPINNER_LOOKBACK: usize = 8;
/// `busyTailRows`.
const BUSY_TAIL_ROWS: usize = 12;

/// `busyRegion` returns the rows in which a LIVE spinner may appear: the
/// strip just above the composer box, or the tail of the capture when the box
/// is not readable.
pub(crate) fn busy_region(pane: &str) -> Vec<&str> {
    let lines = split_rows(pane);
    if let Some((_, top)) = composer_box_at(pane) {
        let start = top.saturating_sub(BUSY_SPINNER_LOOKBACK);
        return lines[start..top].to_vec();
    }
    let start = lines.len().saturating_sub(BUSY_TAIL_ROWS);
    lines[start..].to_vec()
}

/// `Busy` reports whether the pane is mid-turn, from one frame alone.
///
/// Four signatures: Hermes' busy composer row, opencode's "esc interrupt",
/// the Claude spinner row in the busy region, and the legacy "esc to
/// interrupt" affordance with a live indicator on the same row.
pub fn busy(pane: &str) -> bool {
    if hermes_busy(pane) {
        return true;
    }
    if opencode_busy(pane) {
        return true;
    }
    for line in busy_region(pane) {
        // Only ANSI colour is stripped, never dim segments.
        let clean = strip_ansi(line);
        if BUSY_SPINNER.is_match(clean.trim_end_matches(SPACE_TAB_NBSP)) {
            return true;
        }
    }
    let interrupt_rows = if codex_pane(pane) {
        busy_region(pane)
    } else {
        split_rows(pane)
    };
    for raw in interrupt_rows {
        let line = go_lower(&strip_ansi(raw));
        if !line.contains("esc to interrupt") || line.contains("shell") {
            continue;
        }
        if BUSY_INDICATOR.is_match(&line) {
            return true;
        }
    }
    false
}

/// `authExpiredMarkers`.
const AUTH_EXPIRED_MARKERS: [&str; 2] = ["login expired", "run /login"];
/// `authStatusBullet`.
const AUTH_STATUS_BULLET: &str = "●";

/// `usageLimitNotice`.
pub(crate) static USAGE_LIMIT_NOTICE: LazyLock<Regex> = LazyLock::new(|| {
    Regex::new(&format!(
        r"(?i)^⚠(?:\x{{FE0F}})?{WS}+usage{WS}+limit{WS}+reached(?:(?-u:\b)|$)"
    ))
    .expect("usageLimitNotice")
});

/// `usageLimitReset`.
static USAGE_LIMIT_RESET: LazyLock<Regex> = LazyLock::new(|| {
    Regex::new(&format!(r"(?i)(?-u:\b)limit{WS}+resets?{WS}+([^·…]+)")).expect("usageLimitReset")
});

/// `AuthExpired` reports whether the pane's LIVE STATUS FOOTER shows the
/// expired-credentials banner (with its status bullet).
pub fn auth_expired(pane: &str) -> bool {
    for line in status_footer(pane) {
        let clean = strip_ansi(line);
        if !clean.contains(AUTH_STATUS_BULLET) {
            continue;
        }
        let clean = go_lower(&clean);
        if AUTH_EXPIRED_MARKERS
            .iter()
            .any(|marker| clean.contains(marker))
        {
            return true;
        }
    }
    false
}

/// `UsageLimitReason` returns a readable reason only when a Claude composer is
/// present and its live footer contains the anchored usage-limit notice.
pub fn usage_limit_reason(pane: &str) -> String {
    if claude_composer_box_at(pane).is_none() {
        return String::new();
    }
    let base = ErrorKind::UsageLimited.message();
    for line in status_footer(pane) {
        let clean = strip_ansi(line);
        if !USAGE_LIMIT_NOTICE.is_match(clean.trim()) {
            continue;
        }
        if let Some(captures) = USAGE_LIMIT_RESET.captures(&clean) {
            let reset = captures.get(1).map_or("", |m| m.as_str()).trim();
            if !reset.is_empty() {
                return format!("{base} (limit resets {reset})");
            }
        }
        return base.to_string();
    }
    String::new()
}

/// `usageLimitError`: `ErrUsageLimited` carrying the visible reset suffix.
pub(crate) fn usage_limit_error(pane: &str) -> Error {
    let reason = usage_limit_reason(pane);
    let base = ErrorKind::UsageLimited.message();
    if reason.is_empty() || reason == base {
        return Error::UsageLimited { suffix: None };
    }
    let suffix = reason.strip_prefix(base).unwrap_or(&reason);
    Error::UsageLimited {
        suffix: Some(suffix.to_string()),
    }
}

/// `statusFooter` returns the rendered rows BELOW the live composer (below
/// its pure bottom border when it has one).
pub(crate) fn status_footer(pane: &str) -> Vec<&str> {
    let lines = split_rows(pane);
    let Some(last) = last_prompt_row(&lines) else {
        return Vec::new();
    };
    let rest = &lines[last + 1..];
    for (i, line) in rest.iter().enumerate() {
        if is_composer_border(&strip_space(&strip_dim(line))) {
            return rest[i + 1..].to_vec();
        }
    }
    rest.to_vec()
}

/// `IsAgentCommand`: the command-only whitelist of agent runtimes.
pub fn is_agent_command(cmd: &str) -> bool {
    matches!(cmd, "claude" | "codex" | "bwrap")
}

/// `isShellCommand` / `IsShellCommand`: an interactive shell, the state a pane
/// is left in when an agent exits.
pub fn is_shell_command(cmd: &str) -> bool {
    matches!(cmd, "zsh" | "bash" | "sh" | "dash" | "fish")
}

/// `RemoteControlMenu` reports whether the pane shows the Remote Control modal.
pub fn remote_control_menu(pane: &str) -> bool {
    if !pane.contains("Enter to select") {
        return false;
    }
    pane.contains("Disconnect this session") || pane.contains("Remote Control")
}

/// What a Claude pane last said about Remote Control.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Default)]
pub enum RemoteControl {
    /// The pane never showed a Remote Control status line.
    #[default]
    Unknown,
    /// The latest status line says the session is reachable.
    Active,
    /// Claude Code dropped the session after it was active.
    Disconnected,
}

static REMOTE_URL_PATTERN: LazyLock<Regex> = LazyLock::new(|| {
    Regex::new(&format!(r"https://claude\.ai/code/{NWS}+")).expect("remoteURLPattern")
});
static REMOTE_ACTIVE_LINE: LazyLock<Regex> = LazyLock::new(|| {
    Regex::new(&format!(
        r"^{WS}*(?:⎿{WS}*)?/remote-control is active(?-u:\b)"
    ))
    .expect("remoteActiveLine")
});
static REMOTE_DISCONNECT_ROW: LazyLock<Regex> = LazyLock::new(|| {
    Regex::new(&format!(r"^{WS}*●{WS}*Remote Control disconnected(?-u:\b)"))
        .expect("remoteDisconnectRow")
});

/// `RemoteControlStatus` reads Claude Code's own status lines, newest last.
/// Returns `(state, url, reason)`.
pub fn remote_control_status(pane: &str) -> (RemoteControl, String, String) {
    let mut state = RemoteControl::Unknown;
    let mut url = String::new();
    let mut reason = String::new();
    for line in pane.split('\n') {
        if REMOTE_DISCONNECT_ROW.is_match(line) {
            state = RemoteControl::Disconnected;
            url.clear();
            reason = line
                .trim()
                .strip_prefix('●')
                .unwrap_or(line.trim())
                .trim()
                .to_string();
        } else if REMOTE_ACTIVE_LINE.is_match(line) {
            state = RemoteControl::Active;
            reason.clear();
        }
        if state != RemoteControl::Disconnected
            && let Some(last) = REMOTE_URL_PATTERN.find_iter(line).last()
        {
            url = last.as_str().trim_end_matches(['.', ',', ')']).to_string();
        }
    }
    (state, url, reason)
}

/// `launchWait` names what a booting harness is visibly waiting on, or ""
/// while it is simply starting. Only for display.
pub fn launch_wait(pane: &str) -> &'static str {
    let lower = go_lower(pane);
    if lower.contains("trust")
        && (lower.contains("folder") || lower.contains("directory") || lower.contains("workspace"))
    {
        return "harness trust prompt";
    }
    if remote_control_menu(pane) {
        return "remote-control menu";
    }
    if lower.contains("resume from summary") || lower.contains("resume full session") {
        return "resume choice";
    }
    if lower.contains("enter to confirm") || lower.contains("esc to cancel") {
        return "an interactive prompt";
    }
    ""
}
