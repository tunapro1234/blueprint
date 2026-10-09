//! bp-tmux: see PLAN.md §4 for the Go packages this crate replaces.
//!
//! Wave 1 holds the pure parts: screen parsing of captured panes
//! (`internal/tmux` screen functions), launch-argument builders, the error
//! kinds, the desktop compositor adapters (`internal/compositor`) and the
//! window-to-agent mapping (`internal/windowmap`).

pub mod compositor;
pub mod error;
pub mod launch_args;
pub mod screen;
pub mod types;
pub mod windowmap;

pub use error::{Error, ErrorKind, UnverifiedCause};
pub use types::{AttachedClient, Location, PaneProcess, SessionAttachment};
