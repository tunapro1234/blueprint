# bp demo: two real agents, one busy, and a message that waits for the lull.
#
# Record from the repository root:  vhs docs/demo/demo.tape
# It starts a Codex agent (alice) and a Claude Code agent (bob) through bp run,
# in a throwaway bp home and a private tmux server, so your own agents and
# your real ~/.blueprint are not touched. Read docs/demo/README.md first.

Output docs/demo/demo.gif

Require bp
Require tmux
Require claude
Require codex

Set Shell "bash"
Set FontSize 16
Set Width 1280
Set Height 720
Set Padding 24
Set TypingSpeed 40ms
Set WaitTimeout 2m
Set Theme { "name": "bp", "black": "#1f2328", "red": "#cf222e", "green": "#1a7f37", "yellow": "#9a6700", "blue": "#2b5cd9", "magenta": "#8250df", "cyan": "#1b7c83", "white": "#6e7781", "brightBlack": "#57606a", "brightRed": "#a40e26", "brightGreen": "#2da44e", "brightYellow": "#bf8700", "brightBlue": "#2b5cd9", "brightMagenta": "#a475f9", "brightCyan": "#3192aa", "brightWhite": "#8c959f", "background": "#ffffff", "foreground": "#1f2328", "selection": "#dbe5fb", "cursor": "#2b5cd9" }

# Setup, not recorded: a throwaway bp home with every module off, a private
# tmux server, and two agents started with bp run. bp run attaches to the
# agent it starts, so each one gets a detached launcher session; closing the
# launcher afterwards only detaches, and the agent keeps running.
Hide
Type 'export DEMO=$(mktemp -d /tmp/bp-demo.XXXXXX)' Enter
Type 'export BP_HOME="$DEMO/home" TMUX_TMPDIR="$DEMO" BP_NO_UPDATE_CHECK=1' Enter
Type `mkdir -p "$BP_HOME" && printf 'modules: {}\n' > "$BP_HOME/config.yaml"` Enter
Type "tmux new-session -d -s alice-terminal -x 160 -y 48 'env -u TMUX bp run --name alice codex'" Enter
Type "tmux new-session -d -s bob-terminal -x 160 -y 48 'env -u TMUX bp run --name bob claude'" Enter
Type "until bp status | grep -q '^alice ' && bp status | grep -q '^bob  *idle'; do sleep 2; done" Enter
Wait
Type "tmux kill-session -t alice-terminal; tmux kill-session -t bob-terminal; clear" Enter
Wait
Show

Sleep 1s
Type "bp status" Sleep 300ms Enter
Sleep 3s

Type `bp msg bob 'List the TODO comments under cmd/ and summarize them.'` Sleep 300ms Enter
Wait+Screen /RESULT=/
Sleep 4s

# bob is working now, so the next message waits for the lull.
Type `bp msg bob 'When you are done, also count the Go test files.'` Sleep 300ms Enter
Wait+Screen /RESULT=queued/
Sleep 3s

# Not recorded: keep the channel id bp just printed in $ch.
Hide
Type `ch=$(ls -t "$BP_HOME"/msgq/*/q*.json | head -n 1 | xargs basename | sed 's/[.]json$//')` Enter
Show

Type "bp qstat $ch" Sleep 300ms Enter
Sleep 4s

Type "bp wait bob --until idle --timeout 10m" Sleep 300ms Enter
Wait+Screen@10m /reached idle/
Sleep 5s

Type "bp qstat $ch" Sleep 300ms Enter
Wait+Screen /DELIVERED/
Sleep 3s

Type "bp peek bob 12" Sleep 300ms Enter
Sleep 6s

# Cleanup, not recorded: stop the private tmux server and remove the demo home.
Hide
Type 'tmux kill-server; rm -rf -- "${DEMO:?}"' Enter
