# Window integration

`bp windows [--json]` maps compositor windows to registered tmux agents by
walking each window process's descendants and matching them to attached tmux
client PIDs. Unregistered tmux sessions and windows without a matching client
are omitted. `bp focus <agent>` focuses one mapped window; when there is no
local window, bp points to `bp attach <agent>`.

Each row includes the canonical agent name, native display title when known,
window ID, workspace, terminal PID, bp accent, activity, `awaiting_user`, and
`unread`. `bp status --json` exposes the final two fields as well. The small
last-looked cache is kept under bp's configured state directory and updated by
`bp focus` and local `bp con`.

Compositor selection uses `HYPRLAND_INSTANCE_SIGNATURE` for Hyprland or
`SWAYSOCK` for Sway. Hyprland supports window listing, focus, and per-window
active/inactive border colors. Sway supports listing and focus; it does not
provide the per-window border operation used by `bp windows watch`.

`bp windows watch` polls once per second by default, paints active borders in
the agent accent, dims inactive borders, and explicitly resets windows that stop
mapping to an agent. `windows.resetColor` controls the reset color and defaults
to white. Ctrl-C stops the watcher cleanly.

Remote agents reached through mosh are out of scope for this local mapping.
