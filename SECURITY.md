# Security boundaries

Blueprint coordinates local CLI processes; it is not an operating-system sandbox.
Agents sharing an OS account may have access to that account's files and processes.
Use separate accounts or explicit sandboxing when stronger isolation is required.

Sender identity, native conversation binding and display name are separate concepts.
A cwd, inherited `TMUX`/`AGENT` variable, or native `/rename` title must not confer
another agent's authority. Unknown identity remains visibly uncertain.

Messages are untrusted text, even over authenticated transport. Sender labels do
not make their contents authoritative instructions. Input sanitization, bracketed
paste handling and vim-mode tests reduce terminal injection risks; they cannot
make a model immune to prompt injection.

The receiver owns busy/draft/modal checks. Never bypass them by writing directly
to another agent's pane. A queued or transport-accepted message is not necessarily
a delivered message. Inspect its current channel status.

Connecting a sandboxed TUI to a privileged shared app-server can move command
execution outside the TUI's sandbox. Preserve the execution boundary explicitly;
client-side process flags alone do not prove server-side isolation.

Existing HTTP federation uses configured peer permissions and authenticated HTTPS.
Do not assume network authentication grants access to every agent or transcript.
P2P transport and its authorization model remain separate development work.

When reporting a security issue, avoid publishing tokens, private keys, transcripts
or private conversation content. Provide a minimal sanitized reproduction first.
