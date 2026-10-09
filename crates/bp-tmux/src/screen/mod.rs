//! Pure screen parsing of captured tmux panes (the screen half of
//! internal/tmux: tmux.go, composer.go, codex.go, hermes.go, opencode.go,
//! claude_trust.go).
//!
//! Every function here takes captured pane text — plain or with ANSI escapes
//! (`capture-pane -e`) — and returns a verdict. Nothing here touches tmux.
//!
//! Go semantics that matter (PLAN.md §5.2): regex `\s`, `\d`, `\b` are ASCII
//! (spelled out explicitly here), rows are split with `split('\n')`, and
//! whitespace tests follow `unicode.IsSpace` (NBSP included).

// The send/clear/submit halves (composer_trail, paste_integrity, the clear
// budgets, ...) are consumed by the tmux client layer of the next port wave;
// the unit tests below already exercise every one of them.
#![allow(dead_code, reason = "pure helpers of the not-yet-ported tmux client")]

pub mod ansi;
pub mod composer;
pub(crate) mod gostr;
pub mod harness;
pub mod region;
pub mod trust;
mod unicode_tables;

#[cfg(test)]
mod tests;

pub use ansi::strip_dim;
pub use composer::{
    BLOCKED_BY_BUSY_PANE, BLOCKED_BY_DIALOG, BLOCKED_BY_FOREIGN_TEXT, BLOCKED_BY_PANE_LOCK,
    BLOCKED_BY_PASTE_CHIP, composer_block_reason, composer_content_block_reason, composer_empty,
    damaged_paste, exact_paste, stuck_paste,
};
pub use harness::claude::{claude_composer_holds_onboarding, claude_empty_composer};
pub use harness::codex::{codex_pane, dialog, is_codex_command};
pub use harness::hermes::{hermes_idle, hermes_pane, is_agent_pane, is_hermes_command};
pub use harness::opencode::{is_opencode_command, opencode_pane};
pub use region::{
    RemoteControl, auth_expired, busy, is_agent_command, is_shell_command, launch_wait,
    remote_control_menu, remote_control_status, typing, usage_limit_reason,
};

/// Regex fragments for Go's ASCII-only Perl classes.
pub(crate) mod class {
    /// Go `\s`: `[\t\n\f\r ]`.
    pub const WS: &str = r"[\t\n\x0C\r ]";
    /// Go `\S`.
    pub const NWS: &str = r"[^\t\n\x0C\r ]";
}
