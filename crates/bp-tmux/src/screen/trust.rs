//! Port of internal/tmux/claude_trust.go: recognizers for Claude's workspace
//! trust modal and Codex's folder trust screen.

use crate::screen::region::is_composer_border;

/// `claudeTrustAction`: the one key Open may press on Claude's trust modal.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum ClaudeTrustAction {
    /// Not the recognised modal: press nothing.
    None,
    /// Approval selected: Enter.
    Confirm,
    /// Refusal selected, approval right below: Down.
    Select,
}

/// `claudeTrustMaxKeys`.
pub const CLAUDE_TRUST_MAX_KEYS: usize = 6;

/// `codexTrustMaxEnters`.
pub const CODEX_TRUST_MAX_ENTERS: usize = 5;

/// `claudeTrustModal` recognizes only the initial workspace picker for the
/// explicitly requested directory.
pub fn claude_trust_modal(pane: &str, dir: &str) -> ClaudeTrustAction {
    let lines: Vec<&str> = pane.trim().split('\n').collect();
    let (mut header, mut path, mut question, mut accept, mut refuse, mut footer) =
        (-1isize, -1isize, -1isize, -1isize, -1isize, -1isize);
    let (mut accept_selected, mut refuse_selected) = (false, false);
    for (i, raw) in lines.iter().enumerate() {
        let i = i as isize;
        let line = raw.trim();
        if is_composer_border(line) && header < 0 {
            continue;
        }
        if is_composer_border(line)
            || line.contains("bypass permissions")
            || line.contains("-- INSERT --")
        {
            return ClaudeTrustAction::None;
        }
        let selected = line.starts_with('❯');
        let option = line.strip_prefix('❯').unwrap_or(line).trim();
        if line == "Accessing workspace:" {
            header = i;
        } else if line == dir {
            path = i;
        } else if line
            .starts_with("Quick safety check: Is this a project you created or one you trust?")
        {
            question = i;
        } else if option == "1. Yes, I trust this folder" || option == "Yes, I trust this folder" {
            accept = i;
            accept_selected = selected;
        } else if option == "2. No, exit"
            || option == "2. No, continue without these permissions"
            || option == "No, exit"
            || option == "No, continue without these permissions"
        {
            refuse = i;
            refuse_selected = selected;
        } else if line == "Enter to confirm · Esc to cancel"
            || line == "Enter to select · Esc to cancel"
        {
            footer = i;
        } else if selected || line.starts_with('›') {
            return ClaudeTrustAction::None;
        }
    }
    if header < 0
        || path <= header
        || question <= path
        || accept <= question
        || refuse <= question
        || footer != lines.len() as isize - 1
        || footer <= accept
        || footer <= refuse
    {
        return ClaudeTrustAction::None;
    }
    if accept_selected && !refuse_selected && (refuse == accept + 1 || accept == refuse + 1) {
        return ClaudeTrustAction::Confirm;
    }
    if refuse_selected && !accept_selected && accept == refuse + 1 {
        return ClaudeTrustAction::Select;
    }
    ClaudeTrustAction::None
}

/// `codexTrustModal` recognizes Codex's first-run folder trust screen for the
/// requested directory with "Trust and continue" selected.
pub fn codex_trust_modal(pane: &str, dir: &str) -> bool {
    let (mut header, mut path, mut question, mut accept, mut back, mut footer) =
        (-1isize, -1isize, -1isize, -1isize, -1isize, -1isize);
    for (i, raw) in pane.trim().split('\n').enumerate() {
        let i = i as isize;
        let line = raw.trim();
        if line == "Folder access" {
            header = i;
        } else if line == dir {
            path = i;
        } else if line.starts_with("Trust this folder?") {
            question = i;
        } else if line == "› 1. Trust and continue" {
            accept = i;
        } else if line.starts_with("2. ") {
            back = i;
        } else if line == "enter continue · esc back" {
            footer = i;
        } else if line.starts_with('›') {
            return false;
        }
    }
    header >= 0
        && path > header
        && question > path
        && accept > question
        && back == accept + 1
        && footer > back
}

/// `codexTrustScreen`: any visible Codex trust question, recognized or not.
pub fn codex_trust_screen(pane: &str) -> bool {
    if !crate::screen::gostr::go_lower(pane).contains("trust") {
        return false;
    }
    pane.split('\n').any(|raw| raw.trim().starts_with("› 1. "))
}
