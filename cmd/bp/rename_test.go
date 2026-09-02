package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	bpconfig "blueprint/internal/config"
	bptmux "blueprint/internal/tmux"
)

// mungeProject mirrors Claude Code's cwd -> project-directory encoding, so a
// test can put a transcript exactly where ResumeSessionPath will look for it.
func mungeProject(dir string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			return r
		}
		return '-'
	}, dir)
}

type renameFixture struct {
	app        *app
	calls      func() string
	book       string
	usage      string
	transcript string
	folder     string
}

type renameOptions struct {
	live    bool   // the session exists
	command string // what the pane runs ("claude", "codex")
	pane    string // shell snippet printing a capture
	retitle string // title the agent records when something is pasted; "" = never
	// bookless leaves the agentbook entry without a folder, so the transcript can
	// only be found through the directory tmux started the session in.
	bookless bool
}

// renameTmux writes a scripted stand-in for the tmux binary. It answers the
// handful of questions bp rename asks (does the session exist, what does the
// pane run, what does it look like), logs every call so a test can prove what
// did NOT happen, and — when retitle is set — appends a custom-title record to
// the transcript the moment something is pasted into the pane, which is how a
// live agent reacts to /rename.
func renameTmux(t *testing.T, session, transcript, folder string, opts renameOptions) (*bptmux.Client, func() string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "tmux")
	log := filepath.Join(dir, "calls")

	target := "=" + session
	if !opts.live {
		target = "=no-such-session"
	}
	command := opts.command
	if command == "" {
		command = "claude"
	}
	retitle := ":"
	if opts.retitle != "" {
		retitle = `printf '{"type":"custom-title","customTitle":"` + opts.retitle + `","sessionId":"sess-1"}\n' >> ` + transcript
	}
	script := "#!/bin/sh\n" +
		"echo \"$*\" >> " + log + "\n" +
		"case \"$1\" in\n" +
		"has-session) [ \"$3\" = \"" + target + "\" ] ;;\n" +
		"display-message) echo " + command + " ;;\n" +
		"list-panes) case \"$4\" in\n" +
		"  *pane_current_path*) printf '" + session + "\\t" + folder + "\\t" + folder + "\\n' ;;\n" +
		"  *) printf '" + session + "\\t1\\t" + command + "\\n' ;;\n" +
		"esac ;;\n" +
		"capture-pane) " + opts.pane + " ;;\n" +
		"list-clients) : ;;\n" +
		"load-buffer) cat > /dev/null ;;\n" +
		"paste-buffer) " + retitle + " ;;\n" +
		"*) : ;;\n" +
		"esac\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	calls := func() string {
		data, err := os.ReadFile(log)
		if err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		return string(data)
	}
	return &bptmux.Client{Bin: path, Sleep: func(time.Duration) {}, Now: time.Now}, calls
}

// newRenameFixture builds a worktrack-main agent that exists everywhere bp
// looks: an agentbook, a usage state file and a Claude transcript titled with
// its current name, under a HOME the test owns.
func newRenameFixture(t *testing.T, opts renameOptions) *renameFixture {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)

	folder := t.TempDir()
	projectDir := filepath.Join(home, ".claude", "projects", mungeProject(folder))
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	transcript := filepath.Join(projectDir, "sess-1.jsonl")
	if err := os.WriteFile(transcript, []byte(`{"type":"custom-title","customTitle":"worktrack-main","sessionId":"sess-1"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	stateDir := t.TempDir()
	bookPath := filepath.Join(stateDir, "agentbook.json")
	entry := `{"name":"worktrack-main","folder":"` + folder + `"}`
	if opts.bookless {
		entry = `{"name":"worktrack-main"}`
	}
	if err := os.WriteFile(bookPath, []byte(`{"agents":[`+entry+`]}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	usagePath := filepath.Join(stateDir, "state.json")
	if err := os.WriteFile(usagePath, []byte(`{"limited":{"worktrack-main":7}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	client, calls := renameTmux(t, "worktrack-main", transcript, folder, opts)
	return &renameFixture{
		app: &app{
			ctx: context.Background(),
			config: bpconfig.Config{
				Agentbooks:   []string{bookPath},
				UsageHistory: filepath.Join(stateDir, "history.json"),
			},
			tmux: client,
			out:  testOutput(t),
			err:  testOutput(t),
		},
		calls:      calls,
		book:       bookPath,
		usage:      usagePath,
		transcript: transcript,
		folder:     folder,
	}
}

func readFixtureFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// The whole point of the reorder: the pane goes first and everything else is
// downstream of a transcript that actually carries the new title.
func TestRenameSendsToThePaneBeforeAnythingElse(t *testing.T) {
	fix := newRenameFixture(t, renameOptions{live: true, pane: `printf '❯ \n──────────\n'`, retitle: "worktrack"})
	if err := fix.app.rename([]string{"worktrack-main", "worktrack"}); err != nil {
		t.Fatal(err)
	}

	calls := fix.calls()
	paste, renameSession := strings.Index(calls, "paste-buffer"), strings.Index(calls, "rename-session")
	if paste < 0 || renameSession < 0 {
		t.Fatalf("both steps must run:\n%s", calls)
	}
	if paste > renameSession {
		t.Fatalf("the pane step must run before rename-session:\n%s", calls)
	}
	if !strings.Contains(calls, "send-keys -t =worktrack-main: C-u") {
		t.Fatalf("the composer was not cleared before the send:\n%s", calls)
	}
	// Escape is RETRACTED (2026-08-11): on a pane whose Busy detection was wrong
	// it cancels a running turn and destroys work. C-u is the only clearing key
	// bp may press, and this asserts it over the WHOLE call log of the rename.
	if strings.Contains(calls, "Escape") {
		t.Fatalf("Escape was sent to an agent pane:\n%s", calls)
	}
	// The /rename goes to the OLD session name: the tmux rename has not happened
	// yet, so addressing the new one would hit nothing.
	if !strings.Contains(calls, "paste-buffer -b bp-agentmsg") || !strings.Contains(calls, "-t =worktrack-main:") {
		t.Fatalf("the paste did not target the old session:\n%s", calls)
	}

	want := []string{
		"send /rename worktrack to the worktrack-main pane and wait for its transcript title",
		"rename tmux session worktrack-main -> worktrack",
		"update agentbook.json: name worktrack-main -> worktrack",
		"update state.json.limited",
		"renamed worktrack-main -> worktrack",
	}
	got := readTestOutput(t, fix.app.out)
	position := -1
	for _, line := range want {
		index := strings.Index(got, line)
		if index < 0 {
			t.Fatalf("output does not contain %q:\n%s", line, got)
		}
		if index < position {
			t.Fatalf("output is out of order at %q:\n%s", line, got)
		}
		position = index
	}
	if book := readFixtureFile(t, fix.book); !strings.Contains(book, `"worktrack"`) || strings.Contains(book, "worktrack-main") {
		t.Fatalf("agentbook was not renamed:\n%s", book)
	}
	if usage := readFixtureFile(t, fix.usage); !strings.Contains(usage, `"worktrack"`) || strings.Contains(usage, "worktrack-main") {
		t.Fatalf("usage state was not renamed:\n%s", usage)
	}
}

// The reported bug, from the other side: the /rename bounces off a composer that
// is not empty. Nothing else may happen — a half-renamed agent is worse than an
// unrenamed one, because bp can no longer find its transcript at all.
func TestRenameAbortsWhenThePaneRefusesTheCommand(t *testing.T) {
	fix := newRenameFixture(t, renameOptions{live: true, pane: `printf '❯ half-written question\n──────────\n'`})
	book, usage := readFixtureFile(t, fix.book), readFixtureFile(t, fix.usage)

	err := fix.app.rename([]string{"worktrack-main", "worktrack"})
	if err == nil {
		t.Fatal("rename succeeded, want a non-zero exit")
	}
	if !errors.Is(err, errReported) {
		t.Fatalf("err=%v, want errReported (the explanation is printed, not duplicated)", err)
	}

	calls := fix.calls()
	if strings.Contains(calls, "rename-session") {
		t.Fatalf("the tmux session was renamed after a failed pane step:\n%s", calls)
	}
	if strings.Contains(calls, "paste-buffer") {
		t.Fatalf("a message was pasted into a composer holding someone's text:\n%s", calls)
	}
	if got := readFixtureFile(t, fix.book); got != book {
		t.Fatalf("agentbook was written:\n%s", got)
	}
	if got := readFixtureFile(t, fix.usage); got != usage {
		t.Fatalf("usage state was written:\n%s", got)
	}
	if title, ok := bptmux.ReadCustomTitle(fix.transcript); !ok || title != "worktrack-main" {
		t.Fatalf("transcript title=%q,%v", title, ok)
	}

	stderr := readTestOutput(t, fix.app.err)
	for _, want := range []string{
		bptmux.ErrTyping.Error(),
		"nothing was renamed: tmux session, agentbooks and usage state are all still on worktrack-main.",
		"/rename worktrack",
		"then run: bp rename worktrack-main worktrack",
	} {
		if !strings.Contains(stderr, want) {
			t.Fatalf("stderr does not contain %q:\n%s", want, stderr)
		}
	}
}

// Screen-scraping would call this a success: the keystrokes went in and the
// composer cleared. The transcript is the only witness that counts, and here it
// never picks up the new title.
func TestRenameAbortsWhenTheTranscriptTitleNeverChanges(t *testing.T) {
	fix := newRenameFixture(t, renameOptions{live: true, pane: `printf '❯ \n──────────\n'`})
	book := readFixtureFile(t, fix.book)

	err := fix.app.rename([]string{"worktrack-main", "worktrack"})
	if !errors.Is(err, errReported) {
		t.Fatalf("err=%v, want a reported failure", err)
	}
	if calls := fix.calls(); strings.Contains(calls, "rename-session") {
		t.Fatalf("the tmux session was renamed without a retitled transcript:\n%s", calls)
	}
	if got := readFixtureFile(t, fix.book); got != book {
		t.Fatalf("agentbook was written:\n%s", got)
	}
	if stderr := readTestOutput(t, fix.app.err); !strings.Contains(stderr, "did not retitle its transcript") {
		t.Fatalf("stderr does not name the transcript:\n%s", stderr)
	}
}

// A working agent is never interrupted, for a rename least of all: Escape would
// cancel its turn.
func TestRenameRefusesABusyPaneBeforeTouchingAnything(t *testing.T) {
	fix := newRenameFixture(t, renameOptions{live: true, pane: `printf '✻ Working… (23s · esc to interrupt)\n❯ \n──────────\n'`})
	book := readFixtureFile(t, fix.book)

	err := fix.app.rename([]string{"worktrack-main", "worktrack"})
	if !errors.Is(err, errReported) {
		t.Fatalf("err=%v, want a reported failure", err)
	}
	calls := fix.calls()
	if strings.Contains(calls, "send-keys") || strings.Contains(calls, "paste-buffer") {
		t.Fatalf("keys were sent to a working pane:\n%s", calls)
	}
	if strings.Contains(calls, "rename-session") {
		t.Fatalf("a busy pane still got renamed:\n%s", calls)
	}
	if got := readFixtureFile(t, fix.book); got != book {
		t.Fatalf("agentbook was written:\n%s", got)
	}
	if stderr := readTestOutput(t, fix.app.err); !strings.Contains(stderr, "bp peek worktrack-main") {
		t.Fatalf("stderr does not say to wait:\n%s", stderr)
	}
}

// An agentbook-only agent has no pane to type into: the rename is a pure file
// edit and must still work.
func TestRenameWithoutALiveSessionSkipsThePaneStep(t *testing.T) {
	fix := newRenameFixture(t, renameOptions{live: false, pane: ":"})
	if err := fix.app.rename([]string{"worktrack-main", "worktrack"}); err != nil {
		t.Fatal(err)
	}
	if calls := fix.calls(); strings.Contains(calls, "paste-buffer") || strings.Contains(calls, "rename-session") {
		t.Fatalf("a bookkeeping rename touched tmux:\n%s", calls)
	}
	got := readTestOutput(t, fix.app.out)
	if !strings.Contains(got, "no live tmux session for worktrack-main (agentbook only)") {
		t.Fatalf("output=%q", got)
	}
	if book := readFixtureFile(t, fix.book); !strings.Contains(book, `"worktrack"`) || strings.Contains(book, "worktrack-main") {
		t.Fatalf("agentbook was not renamed:\n%s", book)
	}
}

// /rename is a Claude command. At a codex pane it would be typed as text, so the
// pane step is skipped entirely and the rest of the rename still runs.
func TestRenameSkipsThePaneStepForANonClaudeAgent(t *testing.T) {
	fix := newRenameFixture(t, renameOptions{live: true, command: "codex", pane: `printf '› \n'`})
	if err := fix.app.rename([]string{"worktrack-main", "worktrack"}); err != nil {
		t.Fatal(err)
	}
	calls := fix.calls()
	if strings.Contains(calls, "paste-buffer") || strings.Contains(calls, "send-keys") {
		t.Fatalf("a codex pane was typed into:\n%s", calls)
	}
	if !strings.Contains(calls, "rename-session") {
		t.Fatalf("the tmux session was not renamed:\n%s", calls)
	}
	if got := readTestOutput(t, fix.app.out); !strings.Contains(got, "the worktrack-main pane is not a Claude agent") {
		t.Fatalf("output=%q", got)
	}
	if book := readFixtureFile(t, fix.book); !strings.Contains(book, `"worktrack"`) {
		t.Fatalf("agentbook was not renamed:\n%s", book)
	}
}

// An agentbook entry that names no folder is not a dead end: the directory tmux
// opened the session in is the cwd Claude munged into its projects path, so the
// transcript is still found and the rename still verifies.
func TestRenameFindsTheTranscriptThroughTheSessionDirectory(t *testing.T) {
	fix := newRenameFixture(t, renameOptions{live: true, bookless: true, pane: `printf '❯ \n──────────\n'`, retitle: "worktrack"})
	if err := fix.app.rename([]string{"worktrack-main", "worktrack"}); err != nil {
		t.Fatal(err)
	}
	if title, ok := bptmux.ReadCustomTitle(fix.transcript); !ok || title != "worktrack" {
		t.Fatalf("transcript title=%q,%v", title, ok)
	}
	if book := readFixtureFile(t, fix.book); !strings.Contains(book, `"worktrack"`) {
		t.Fatalf("agentbook was not renamed:\n%s", book)
	}
}

// The dry run describes the new order and touches nothing.
func TestRenameDryRunDescribesTheNewOrder(t *testing.T) {
	fix := newRenameFixture(t, renameOptions{live: true, pane: `printf '❯ \n──────────\n'`})
	book, usage := readFixtureFile(t, fix.book), readFixtureFile(t, fix.usage)

	if err := fix.app.rename([]string{"worktrack-main", "worktrack", "--dry-run"}); err != nil {
		t.Fatal(err)
	}
	want := "would send /rename worktrack to the worktrack-main pane and wait for its transcript title\n" +
		"would rename tmux session worktrack-main -> worktrack\n" +
		"would update agentbook.json: name worktrack-main -> worktrack\n" +
		"would update state.json.limited\n" +
		"\ndry run: nothing was changed\n"
	if got := readTestOutput(t, fix.app.out); got != want {
		t.Fatalf("output:\n%s\nwant:\n%s", got, want)
	}
	if calls := fix.calls(); strings.Contains(calls, "send-keys") || strings.Contains(calls, "paste-buffer") || strings.Contains(calls, "rename-session") {
		t.Fatalf("the dry run changed something:\n%s", calls)
	}
	if got := readFixtureFile(t, fix.book); got != book {
		t.Fatalf("agentbook was written:\n%s", got)
	}
	if got := readFixtureFile(t, fix.usage); got != usage {
		t.Fatalf("usage state was written:\n%s", got)
	}
}

// The rename this command could not do until 2026-09-02: an agent renaming
// ITSELF. Step 1 types /rename into the target pane and refuses one that is
// mid-turn — and an agent asking for its own rename is by definition mid-turn.
// --no-retitle skips that step, does everything else, and says loudly what is
// missing: the transcript keeps the OLD title, which is what resolves an agent
// to its session file.
func TestRenameNoRetitleSkipsThePaneAndSaysWhatIsMissing(t *testing.T) {
	fix := newRenameFixture(t, renameOptions{live: true, pane: `printf '✻ Working… (23s · esc to interrupt)\n❯ \n──────────\n'`})

	if err := fix.app.rename([]string{"worktrack-main", "worktrack", "--no-retitle"}); err != nil {
		t.Fatalf("err=%v, want the rename to proceed on a busy pane", err)
	}
	calls := fix.calls()
	if strings.Contains(calls, "paste-buffer") || strings.Contains(calls, "send-keys") {
		t.Fatalf("--no-retitle typed into the pane anyway:\n%s", calls)
	}
	if !strings.Contains(calls, "rename-session") {
		t.Fatalf("the tmux session was not renamed:\n%s", calls)
	}
	if book := readFixtureFile(t, fix.book); !strings.Contains(book, `"worktrack"`) || strings.Contains(book, "worktrack-main") {
		t.Fatalf("agentbook was not renamed:\n%s", book)
	}
	stderr := readTestOutput(t, fix.app.err)
	if !strings.Contains(stderr, "/rename worktrack") {
		t.Fatalf("stderr does not tell the agent the one command it still owes:\n%s", stderr)
	}
	if !strings.Contains(stderr, "CACHE") {
		t.Fatalf("stderr does not say what breaks until then:\n%s", stderr)
	}
}
