# Recording the demo

[`demo.tape`](demo.tape) is a [VHS](https://github.com/charmbracelet/vhs)
script that records the README story with real agents: a Codex agent
(`alice`) and a Claude Code agent (`bob`) start through `bp run`, bob gets a
task, and a second message waits until bob's turn ends before bp delivers it.

## What you need

- VHS 0.11 or newer, with its `ttyd` and `ffmpeg` dependencies
  (`brew install vhs`, or `go install github.com/charmbracelet/vhs@v0.11.0`
  plus the two tools).
- The `bp` build you want to show on `PATH`, plus tmux, Claude Code and Codex,
  both signed in.
- This repository as the working directory, already trusted by Claude Code and
  Codex. Open each CLI here once beforehand if they still ask.
- On macOS, `bp status` shows alice (Codex) as `unknown` until the Codex fix
  lands (see Known issues in the README). The tape only sends to bob, so it
  records either way.

## Record

```sh
vhs docs/demo/demo.tape
```

The output is `docs/demo/demo.gif`.

## What the tape does

Set up off camera:

1. Creates a throwaway bp home under `/tmp/bp-demo.*` with every module off,
   and points `TMUX_TMPDIR` there, so the demo runs on a private tmux server.
   Your own agents, your tmux sessions and `~/.blueprint` are not touched. The
   agents still use your normal Claude Code and Codex logins and settings.
2. Starts `bp run --name alice codex` and `bp run --name bob claude` in two
   detached launcher sessions, waits until `bp status` lists alice and shows
   bob idle, then closes the launchers. That only detaches; the agents keep
   running.

On camera:

3. `bp status` shows the two agents.
4. `bp msg bob 'List the TODO comments under cmd/ and summarize them.'` is
   delivered at once, and bob starts working.
5. A second `bp msg` arrives while bob is busy, so it is `QUEUED`.
6. `bp qstat $ch` shows it pending. `$ch` is the channel id bp just printed,
   captured off camera from the newest queue record.
7. `bp wait bob --until idle` waits for the end of bob's turn, and
   `bp qstat $ch` then reports `DELIVERED`.
8. `bp peek bob 12` shows the end of bob's screen.

Cleanup kills the private tmux server and deletes the throwaway home.

## Checking the result

- Running it costs a little model quota: bob (Claude Code) does two short
  read-only tasks. alice stays idle.
- If bob answers the first task before the second message is sent, the second
  one is delivered at once and the waiting step does not show. Record again.
- If Claude Code asks for a permission, bp waits as it always does for a
  dialog, and the tape stops at `bp wait` until its ten-minute timeout. Answer
  the prompt from another terminal (export the same `BP_HOME` and
  `TMUX_TMPDIR`, then `bp attach bob`), or allow read-only tools in this
  folder beforehand.
- Before committing the GIF, look through it for anything private: your login
  name is the sender label (`[<login>] …`), and bob's screen shows paths and
  model names. Keep it small (for example `gifsicle -O3 --lossy=80`).

The README shows the static diagram in [`../assets/`](../assets/) until a
recorded GIF is committed.
