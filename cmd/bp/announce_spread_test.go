package main

import (
	"errors"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"blueprint/internal/book"
	bpcache "blueprint/internal/cache"
	bpconfig "blueprint/internal/config"
	"blueprint/internal/msgq"
	"blueprint/internal/pending"
)

// announceFixture is a lead with three warm agents and one cold one. The queue
// is a private directory and there is no tmux client, so the command's own
// delivery pass does nothing and every record stays as it was queued.
func announceFixture(t *testing.T, rate int) (*app, *[]string) {
	t.Helper()
	t.Setenv("AGENT", "lead")
	t.Setenv("TMUX", "")
	fleet := book.Fleet{
		Root:  "lead",
		Order: []string{"lead", "worker-1", "worker-2", "worker-3", "cold"},
		Agents: map[string]book.Agent{
			"lead":     {Name: "lead"},
			"worker-1": {Name: "worker-1", Folder: "/work/one"},
			"worker-2": {Name: "worker-2", Folder: "/work/two"},
			"worker-3": {Name: "worker-3", Folder: "/work/three"},
			"cold":     {Name: "cold", Folder: "/work/cold"},
		},
		Parents: map[string]string{"lead": "", "worker-1": "lead", "worker-2": "lead", "worker-3": "lead", "cold": "lead"},
	}
	states := map[string]book.State{"worker-1": {Alive: true}, "worker-2": {Alive: true}, "worker-3": {Alive: true}, "cold": {Alive: true}}
	warm := bpcache.State{Known: true, Age: 10 * time.Minute, CacheTTL: time.Hour, CacheAge: 10 * time.Minute, CtxTokens: 100_000}
	var sent []string
	a := &app{
		config: bpconfig.Config{StateDir: t.TempDir(), Announce: bpconfig.AnnounceConfig{RatePerMinute: rate}},
		queue:  msgq.New(t.TempDir()),
		out:    testOutput(t),
		err:    testOutput(t),
		loadFleet: func() (book.Fleet, map[string]book.State, error) {
			return fleet, states, nil
		},
		loadCache: func(map[string]string) map[string]bpcache.State {
			return map[string]bpcache.State{
				"worker-1": warm, "worker-2": warm, "worker-3": warm,
				"cold": {Known: true, Age: 36 * time.Minute, CtxTokens: 300_000},
			}
		},
		sessionExists: func(string) bool { return true },
		deliverMessage: func(name, from, message string) (bool, string, error) {
			sent = append(sent, name+":"+message)
			return false, "", nil
		},
	}
	trustedSenderFixture(a)
	return a, &sent
}

func queuedRecords(t *testing.T, a *app) []msgq.Message {
	t.Helper()
	rows, err := a.queue.List()
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

func TestAnnounceSpreadsWarmTargetsByDefault(t *testing.T) {
	a, sent := announceFixture(t, bpconfig.DefaultAnnounceRate)
	before := time.Now()
	if err := a.announce([]string{"release", "freeze", "at", "noon"}); err != nil {
		t.Fatal(err)
	}
	if len(*sent) != 0 {
		t.Fatalf("spread mode used the immediate path: %v", *sent)
	}
	out := readTestOutput(t, a.out)
	if !regexp.MustCompile(`^queued: 3 \(6/min, last in ~(19|20)s\), deferred: 1\n$`).MatchString(out) {
		t.Fatalf("output=%q", out)
	}
	rows := queuedRecords(t, a)
	if len(rows) != 3 {
		t.Fatalf("records=%+v", rows)
	}
	for index, row := range rows {
		if row.Kind != msgq.KindAnnounce || row.Msg != "[ANNOUNCE lead] release freeze at noon" || row.To != []string{"worker-1", "worker-2", "worker-3"}[index] {
			t.Fatalf("record %d=%+v", index, row)
		}
		slot := time.Unix(0, int64(row.NotBefore*1e9))
		want := before.Add(time.Duration(index) * 10 * time.Second)
		if slot.Before(want) || slot.After(want.Add(5*time.Second)) {
			t.Fatalf("record %d slot %v, want about %v", index, slot, want)
		}
	}
	snapshot, err := pending.Load(a.config.StateDir, "cold")
	if err != nil || len(snapshot.Entries) != 1 || snapshot.Entries[0].Text != "release freeze at noon" {
		t.Fatalf("cold spool=%+v err=%v", snapshot.Entries, err)
	}
}

func TestAnnounceNowKeepsTheImmediateOutput(t *testing.T) {
	a, sent := announceFixture(t, bpconfig.DefaultAnnounceRate)
	if err := a.announce([]string{"--now", "hello"}); err != nil {
		t.Fatal(err)
	}
	if len(*sent) != 3 {
		t.Fatalf("sent=%v", *sent)
	}
	if got := readTestOutput(t, a.out); got != "sent: 3, deferred: 1\n" {
		t.Fatalf("output=%q", got)
	}
	if rows := queuedRecords(t, a); len(rows) != 0 {
		t.Fatalf("--now queued spread records: %+v", rows)
	}
}

func TestAnnounceRateZeroInConfigDeliversAtOnce(t *testing.T) {
	a, sent := announceFixture(t, 0)
	if err := a.announce([]string{"hello"}); err != nil {
		t.Fatal(err)
	}
	if len(*sent) != 3 || readTestOutput(t, a.out) != "sent: 3, deferred: 1\n" {
		t.Fatalf("sent=%v", *sent)
	}
}

func TestAnnounceRateFlag(t *testing.T) {
	a, _ := announceFixture(t, bpconfig.DefaultAnnounceRate)
	if err := a.announce([]string{"--rate", "30", "hello"}); err != nil {
		t.Fatal(err)
	}
	if out := readTestOutput(t, a.out); !regexp.MustCompile(`^queued: 3 \(30/min, last in ~[34]s\), deferred: 1\n$`).MatchString(out) {
		t.Fatalf("output=%q", out)
	}
	rows := queuedRecords(t, a)
	if len(rows) != 3 || rows[2].NotBefore-rows[0].NotBefore != 4 {
		t.Fatalf("slots %+v, want 2s apart", rows)
	}
}

func TestAnnounceRateValidation(t *testing.T) {
	for _, args := range [][]string{
		{"--rate", "0", "hello"},
		{"--rate", "-2", "hello"},
		{"--rate", "1.5", "hello"},
		{"--rate", "six", "hello"},
		{"--rate=0", "hello"},
		{"--rate"},
		{"--dry-run", "--rate", "0", "hello"},
		{"--now", "--rate", "5", "hello"},
		{"--rate", "5", "--now", "hello"},
		{"--rate", "5"},
	} {
		a, sent := announceFixture(t, bpconfig.DefaultAnnounceRate)
		err := a.announce(args)
		if err == nil || !strings.Contains(err.Error(), "usage: bp announce") {
			t.Errorf("%q: err=%v, want a usage error", args, err)
		}
		if len(*sent) != 0 || len(queuedRecords(t, a)) != 0 {
			t.Errorf("%q: something was sent or queued", args)
		}
		if snapshot, _ := pending.Load(a.config.StateDir, "cold"); len(snapshot.Entries) != 0 {
			t.Errorf("%q: the cold target was spooled", args)
		}
	}
	options, err := parseAnnounceArgs([]string{"--rate=12", "hello", "--dry-run"}, 6)
	if err != nil || options.rate != 12 || !options.dryRun || strings.Join(options.words, " ") != "hello" {
		t.Fatalf("options=%+v err=%v", options, err)
	}
}

func TestAnnounceFlagsCountOnlyBeforeTheMessage(t *testing.T) {
	a, sent := announceFixture(t, bpconfig.DefaultAnnounceRate)
	if err := a.announce([]string{"hello", "use", "--now", "please"}); err != nil {
		t.Fatal(err)
	}
	rows := queuedRecords(t, a)
	if len(*sent) != 0 || len(rows) != 3 || rows[0].Msg != "[ANNOUNCE lead] hello use --now please" || !strings.HasPrefix(readTestOutput(t, a.out), "queued: 3 (6/min") {
		t.Fatalf("text --now changed the mode or lost a word: sent=%v rows=%+v", *sent, rows)
	}

	a, sent = announceFixture(t, bpconfig.DefaultAnnounceRate)
	if err := a.announce([]string{"--now", "hello", "--rate", "3"}); err != nil {
		t.Fatal(err)
	}
	if len(*sent) != 3 || !strings.HasSuffix((*sent)[0], ":[ANNOUNCE lead] hello --rate 3") || readTestOutput(t, a.out) != "sent: 3, deferred: 1\n" {
		t.Fatalf("sent=%v", *sent)
	}
	if snapshot, _ := pending.Load(a.config.StateDir, "cold"); len(snapshot.Entries) != 1 || snapshot.Entries[0].Text != "hello --rate 3" {
		t.Fatalf("cold spool=%+v", snapshot.Entries)
	}

	a, sent = announceFixture(t, bpconfig.DefaultAnnounceRate)
	if err := a.announce([]string{"--", "--now", "is", "text"}); err != nil {
		t.Fatal(err)
	}
	rows = queuedRecords(t, a)
	if len(*sent) != 0 || len(rows) != 3 || rows[0].Msg != "[ANNOUNCE lead] --now is text" {
		t.Fatalf("-- did not end flag parsing: sent=%v rows=%+v", *sent, rows)
	}

	options, err := parseAnnounceArgs([]string{"--rate", "3", "hello", "--", "x", "--dry-run"}, 6)
	if err != nil || options.rate != 3 || !options.dryRun || strings.Join(options.words, " ") != "hello -- x" {
		t.Fatalf("options=%+v err=%v", options, err)
	}
}

func TestAnnounceDryRunShowsTheScheduleAndQueuesNothing(t *testing.T) {
	a, sent := announceFixture(t, bpconfig.DefaultAnnounceRate)
	if err := a.announce([]string{"--dry-run", "hello"}); err != nil {
		t.Fatal(err)
	}
	out := readTestOutput(t, a.out)
	lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	if len(lines) != 3 || lines[0] != "would send: 3, would defer: 1" ||
		lines[1] != "deferred context: ~300k tokens (4 targets: 3 estimated warm, 1 cold/unknown)" ||
		!regexp.MustCompile(`^schedule: 6/min, last at ~\d\d:\d\d:\d\d \(in ~(19|20)s\)$`).MatchString(lines[2]) {
		t.Fatalf("output=%q", out)
	}
	if len(*sent) != 0 || len(queuedRecords(t, a)) != 0 {
		t.Fatal("dry run sent or queued")
	}
	if _, err := os.Stat(a.queue.Root); !errors.Is(err, os.ErrNotExist) {
		if entries, _ := os.ReadDir(a.queue.Root); len(entries) != 0 {
			t.Fatalf("dry run wrote to the queue: %v", entries)
		}
	}
	if snapshot, _ := pending.Load(a.config.StateDir, "cold"); len(snapshot.Entries) != 0 {
		t.Fatal("dry run spooled the cold target")
	}

	a, _ = announceFixture(t, bpconfig.DefaultAnnounceRate)
	if err := a.announce([]string{"--dry-run", "--now", "hello"}); err != nil {
		t.Fatal(err)
	}
	if out := readTestOutput(t, a.out); !strings.HasSuffix(out, "\nschedule: all 3 at once (--now)\n") {
		t.Fatalf("output=%q", out)
	}
}

func TestQueueListShowsAHeldAnnouncement(t *testing.T) {
	a, _ := announceFixture(t, bpconfig.DefaultAnnounceRate)
	if _, _, err := a.queue.EnqueueSpread("worker-1", "lead", "[ANNOUNCE lead] first", 10*time.Second, time.Minute); err != nil {
		t.Fatal(err)
	}
	_, slot, err := a.queue.EnqueueSpread("worker-2", "lead", "[ANNOUNCE lead] first", 10*time.Second, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.queueList(nil); err != nil {
		t.Fatal(err)
	}
	out := readTestOutput(t, a.out)
	want := "    ~ held until " + slot.Local().Format("15:04:05") + " (spread announcement; a message to worker-2 releases it earlier)\n"
	if !strings.Contains(out, want) || strings.Count(out, "~ held until") != 1 {
		t.Fatalf("bp q output=%q, want one %q", out, want)
	}
}
