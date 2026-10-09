//! Port of the plain data types in internal/tmux/tmux.go (Location,
//! PaneProcess, AttachedClient, SessionAttachment).

/// Records both the current pane directory and the directory in which its
/// tmux session was created.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct Location {
    pub session: String,
    pub current_dir: String,
    pub start_dir: String,
}

/// Identifies the active process in a session's target pane.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct PaneProcess {
    pub command: String,
    pub pid: i32,
}

/// Identifies the OS process and session of one tmux client.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct AttachedClient {
    pub pid: i32,
    pub session: String,
}

/// Records whether a session has at least one attached client. `id` is tmux's
/// session id (`$N`); `process` is what `PaneProcess` would return for the
/// session, with pid zero when the listing carried no usable pane row.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct SessionAttachment {
    pub name: String,
    pub attached: i32,
    pub id: String,
    pub process: PaneProcess,
}
