//! Port of the error values of internal/tmux/tmux.go and panelock.go
//! (ErrTyping, ErrBusy, ErrNotAgent, ErrNotReady, ErrUsageLimited,
//! ErrUnverified and its causes, ErrDialog, ErrPaneLocked).
//!
//! Go chains these with `fmt.Errorf("%w: …")` and callers branch with
//! `errors.Is`. Here every error carries an optional [`ErrorKind`] and
//! [`Error::is`] follows the same chain: `Dialog` and `PaneLocked` wrap
//! `Busy` in Go, so `is(ErrorKind::Busy)` holds for them too.

use std::fmt;

/// The sentinel kinds callers branch on.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Hash)]
pub enum ErrorKind {
    /// `ErrTyping`: the composer is not empty.
    Typing,
    /// `ErrBusy`: the pane is mid-turn.
    Busy,
    /// `ErrNotAgent`: the target pane is not an agent CLI.
    NotAgent,
    /// `ErrNotReady`: provably not delivered; the caller must queue it.
    NotReady,
    /// `ErrUsageLimited`: a wait outcome while the usage-limit notice shows.
    UsageLimited,
    /// `ErrUnverified`: injected, but delivery could not be confirmed.
    Unverified,
    /// `ErrDialog`: a human decision is pending (wraps `Busy`).
    Dialog,
    /// `ErrPaneLocked`: another bp process holds the pane lock (wraps `Busy`).
    PaneLocked,
}

impl ErrorKind {
    /// The Go sentinel's `Error()` text.
    pub fn message(self) -> &'static str {
        match self {
            ErrorKind::Typing => "composer is not empty",
            ErrorKind::Busy => "pane is working (esc to interrupt)",
            ErrorKind::NotAgent => "target pane is not an agent CLI",
            ErrorKind::NotReady => "message was not delivered",
            ErrorKind::UsageLimited => "target usage limit reached",
            ErrorKind::Unverified => "delivery could not be verified",
            ErrorKind::Dialog => DIALOG_MESSAGE,
            ErrorKind::PaneLocked => PANE_LOCKED_MESSAGE,
        }
    }

    /// Whether `errors.Is(<this sentinel>, target)` holds in Go.
    pub fn is(self, target: ErrorKind) -> bool {
        self == target
            || (target == ErrorKind::Busy
                && matches!(self, ErrorKind::Dialog | ErrorKind::PaneLocked))
    }
}

const DIALOG_MESSAGE: &str = "pane is working (esc to interrupt): confirmation/selection screen is open — only a human may answer";
const PANE_LOCKED_MESSAGE: &str =
    "pane is working (esc to interrupt): another bp process is writing to this pane (pane lock)";

/// The named causes of an unverified delivery (`Unverified*` in Go).
#[derive(Debug, Clone, Copy, PartialEq, Eq, Hash)]
pub enum UnverifiedCause {
    ClientActive,
    SubmitUnconfirmed,
    ComposerOwnership,
}

/// `UnverifiedClientActive`.
pub const UNVERIFIED_CLIENT_ACTIVE: &str = "screen unreadable or keyboard active in pane — Enter WAS NOT PRESSED; text may remain in the composer";
/// `UnverifiedSubmitUnconfirmed`.
pub const UNVERIFIED_SUBMIT_UNCONFIRMED: &str = "submission from composer was unverified";
/// `UnverifiedComposerOwnership`.
pub const UNVERIFIED_COMPOSER_OWNERSHIP: &str =
    "could not prove the composer text belongs to this message; no extra Enter/Tab was sent";

impl UnverifiedCause {
    pub fn as_str(self) -> &'static str {
        match self {
            UnverifiedCause::ClientActive => UNVERIFIED_CLIENT_ACTIVE,
            UnverifiedCause::SubmitUnconfirmed => UNVERIFIED_SUBMIT_UNCONFIRMED,
            UnverifiedCause::ComposerOwnership => UNVERIFIED_COMPOSER_OWNERSHIP,
        }
    }
}

impl fmt::Display for UnverifiedCause {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.write_str(self.as_str())
    }
}

/// `authExpiredReason`.
pub const AUTH_EXPIRED_REASON: &str = "pane session ended (Login expired / run /login)";
/// `composerOtherReason`.
pub const COMPOSER_OTHER_REASON: &str = "composer contains other text: paste never entered";
/// `composerBrokenReason`.
pub const COMPOSER_BROKEN_REASON: &str =
    "paste entered the composer incorrectly (rewritten, still does not match)";

/// An error of this crate. Display text is byte-identical to the Go error
/// chain it replaces.
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum Error {
    Typing,
    Busy,
    NotAgent,
    /// `fmt.Errorf("%w: %s", ErrNotReady, reason)`, or the bare sentinel.
    NotReady {
        reason: Option<String>,
    },
    /// `ErrUsageLimited`, optionally followed by Go's suffix
    /// (`" (limit resets …)"`), appended verbatim.
    UsageLimited {
        suffix: Option<String>,
    },
    /// `fmt.Errorf("%w: %s", ErrUnverified, cause)`, or the bare sentinel.
    Unverified {
        cause: Option<UnverifiedCause>,
    },
    Dialog,
    PaneLocked,
    /// `fmt.Errorf("%w: <detail>", source)`.
    Detail {
        source: Box<Error>,
        detail: String,
    },
    /// `fmt.Errorf("<context>: %w", source)`.
    Context {
        context: String,
        source: Box<Error>,
    },
    /// An error without a sentinel kind (tmux exec failures, validation).
    Other(String),
}

impl Error {
    /// A plain error carrying no kind.
    pub fn other(message: impl Into<String>) -> Self {
        Error::Other(message.into())
    }

    /// Wraps self as `fmt.Errorf("%w: <detail>", self)`.
    pub fn with_detail(self, detail: impl Into<String>) -> Self {
        Error::Detail {
            source: Box::new(self),
            detail: detail.into(),
        }
    }

    /// Wraps self as `fmt.Errorf("<context>: %w", self)`.
    pub fn with_context(self, context: impl Into<String>) -> Self {
        Error::Context {
            context: context.into(),
            source: Box::new(self),
        }
    }

    /// The innermost sentinel this error carries, if any.
    pub fn kind(&self) -> Option<ErrorKind> {
        match self {
            Error::Typing => Some(ErrorKind::Typing),
            Error::Busy => Some(ErrorKind::Busy),
            Error::NotAgent => Some(ErrorKind::NotAgent),
            Error::NotReady { .. } => Some(ErrorKind::NotReady),
            Error::UsageLimited { .. } => Some(ErrorKind::UsageLimited),
            Error::Unverified { .. } => Some(ErrorKind::Unverified),
            Error::Dialog => Some(ErrorKind::Dialog),
            Error::PaneLocked => Some(ErrorKind::PaneLocked),
            Error::Detail { source, .. } | Error::Context { source, .. } => source.kind(),
            Error::Other(_) => None,
        }
    }

    /// Go's `errors.Is(err, <sentinel of target>)`.
    pub fn is(&self, target: ErrorKind) -> bool {
        self.kind().is_some_and(|kind| kind.is(target))
    }

    /// `errors.Is(err, ErrBusy)`: also true for `Dialog` and `PaneLocked`.
    pub fn is_busy(&self) -> bool {
        self.is(ErrorKind::Busy)
    }
}

impl fmt::Display for Error {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            Error::Typing => f.write_str(ErrorKind::Typing.message()),
            Error::Busy => f.write_str(ErrorKind::Busy.message()),
            Error::NotAgent => f.write_str(ErrorKind::NotAgent.message()),
            Error::NotReady { reason } => {
                f.write_str(ErrorKind::NotReady.message())?;
                if let Some(reason) = reason {
                    write!(f, ": {reason}")?;
                }
                Ok(())
            }
            Error::UsageLimited { suffix } => {
                f.write_str(ErrorKind::UsageLimited.message())?;
                if let Some(suffix) = suffix {
                    f.write_str(suffix)?;
                }
                Ok(())
            }
            Error::Unverified { cause } => {
                f.write_str(ErrorKind::Unverified.message())?;
                if let Some(cause) = cause {
                    write!(f, ": {cause}")?;
                }
                Ok(())
            }
            Error::Dialog => f.write_str(ErrorKind::Dialog.message()),
            Error::PaneLocked => f.write_str(ErrorKind::PaneLocked.message()),
            Error::Detail { source, detail } => write!(f, "{source}: {detail}"),
            Error::Context { context, source } => write!(f, "{context}: {source}"),
            Error::Other(message) => f.write_str(message),
        }
    }
}

impl std::error::Error for Error {}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::screen::composer::{BLOCKED_BY_DIALOG, BLOCKED_BY_PANE_LOCK};

    #[test]
    fn display_matches_go_chains() {
        assert_eq!(
            Error::Dialog.to_string(),
            format!("pane is working (esc to interrupt): {BLOCKED_BY_DIALOG}")
        );
        assert_eq!(
            Error::PaneLocked.to_string(),
            format!("pane is working (esc to interrupt): {BLOCKED_BY_PANE_LOCK}")
        );
        assert_eq!(
            Error::NotReady {
                reason: Some(AUTH_EXPIRED_REASON.into())
            }
            .to_string(),
            "message was not delivered: pane session ended (Login expired / run /login)"
        );
        assert_eq!(
            Error::Unverified {
                cause: Some(UnverifiedCause::SubmitUnconfirmed)
            }
            .to_string(),
            "delivery could not be verified: submission from composer was unverified"
        );
        assert_eq!(
            Error::UsageLimited {
                suffix: Some(" (limit resets 5pm)".into())
            }
            .to_string(),
            "target usage limit reached (limit resets 5pm)"
        );
        assert_eq!(
            Error::Typing
                .with_detail("composer clear stopped")
                .to_string(),
            "composer is not empty: composer clear stopped"
        );
        assert_eq!(
            Error::Busy
                .with_context("mangled-paste repair stopped")
                .to_string(),
            "mangled-paste repair stopped: pane is working (esc to interrupt)"
        );
    }

    #[test]
    fn dialog_and_pane_lock_count_as_busy() {
        for err in [Error::Busy, Error::Dialog, Error::PaneLocked] {
            assert!(err.is_busy(), "{err:?}");
            assert!(err.clone().with_detail("x").is_busy());
            assert!(err.with_context("y").is_busy());
        }
        assert!(Error::Dialog.is(ErrorKind::Dialog));
        assert!(!Error::Busy.is(ErrorKind::Dialog));
        assert!(!Error::Typing.is_busy());
        assert!(!Error::other("tmux failed").is_busy());
        assert!(
            Error::Unverified { cause: None }
                .with_context("z")
                .is(ErrorKind::Unverified)
        );
    }
}
