package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"blueprint/internal/book"
	bpcache "blueprint/internal/cache"
	"blueprint/internal/codexrpc"
	bpconfig "blueprint/internal/config"
	"blueprint/internal/dashboard"
	"blueprint/internal/identity"
	"blueprint/internal/msgq"
	"blueprint/internal/ntfy"
	"blueprint/internal/pending"
	bptmux "blueprint/internal/tmux"
	"blueprint/internal/wa"
	"blueprint/internal/worktree"
)

func TestFedCommandRejectsInvalidConfigFallback(t *testing.T) {
	a := &app{config: bpconfig.Config{InvalidConfig: "parse config.json: unexpected EOF"}}
	err := a.run([]string{"fed", "status"})
	if err == nil || !strings.Contains(err.Error(), "federation disabled because config.json is invalid") {
		t.Fatalf("error=%v", err)
	}
}

func TestWhatsAppSendIgnoresNtfyFailure(t *testing.T) {
	t.Setenv("AGENT", "agent")
	t.Setenv("TMUX", "")
	var body string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		data, err := io.ReadAll(request.Body)
		if err != nil {
			t.Fatal(err)
		}
		body = string(data)
		http.Error(writer, "unavailable", http.StatusServiceUnavailable)
	}))
	defer server.Close()

	outbox := t.TempDir()
	output, errOutput := testOutput(t), testOutput(t)
	a := &app{
		ctx:         context.Background(),
		originProbe: func(context.Context) identity.Origin { return identity.Origin{} },
		config:      bpconfig.Config{WAOutbox: outbox, Ntfy: &ntfy.Config{URL: server.URL, Topic: "alerts"}},
		out:         output,
		err:         errOutput,
	}
	if err := a.whatsapp([]string{"send", "hello"}); err != nil {
		t.Fatal(err)
	}
	if body != "[agent?:agent] hello" {
		t.Fatalf("ntfy body=%q, want %q", body, "[agent?:agent] hello")
	}
	entries, err := os.ReadDir(outbox)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("WhatsApp outbox entries=%d, want 1", len(entries))
	}
	if warning := readTestOutput(t, errOutput); !strings.Contains(warning, "WARNING: ntfy notification failed") {
		t.Fatalf("stderr=%q", warning)
	}
}

func TestStatusIsUnchangedWithoutCodexConfig(t *testing.T) {
	out := testOutput(t)
	fleet := book.Fleet{
		Root:  "root",
		Order: []string{"root"},
		Agents: map[string]book.Agent{
			"root": {Name: "root", Status: "open"},
		},
		Parents: map[string]string{"root": ""},
	}
	a := &app{
		out: out,
		loadFleet: func() (book.Fleet, map[string]book.State, error) {
			return fleet, map[string]book.State{"root": {Alive: true}}, nil
		},
		loadCache: func(map[string]string) map[string]bpcache.State { return nil },
		loadCodex: func() []codexrpc.Thread {
			t.Fatal("Codex loader called without config")
			return nil
		},
	}
	if err := a.status(nil); err != nil {
		t.Fatal(err)
	}
	want := fmt.Sprintf("%-24s %-10s %-20s %-10s %s\n", "AGENT", "TMUX", "CACHE", "LAST-TALK", "AGENTBOOK") +
		fmt.Sprintf("%-24s %-10s %-20s %-10s %-10s\n", "root", "idle", "-", "-", "open")
	if got := readTestOutput(t, out); got != want {
		t.Fatalf("output=%q, want %q", got, want)
	}
}

func TestStatusIgnoresDeadCodexSocket(t *testing.T) {
	out := testOutput(t)
	fleet := book.Fleet{
		Root:    "root",
		Order:   []string{"root"},
		Agents:  map[string]book.Agent{"root": {Name: "root", Status: "open"}},
		Parents: map[string]string{"root": ""},
	}
	a := &app{
		ctx:    context.Background(),
		config: bpconfig.Config{Codex: &bpconfig.CodexConfig{Sockets: []string{filepath.Join(t.TempDir(), "missing.sock")}}},
		out:    out,
		loadFleet: func() (book.Fleet, map[string]book.State, error) {
			return fleet, map[string]book.State{"root": {Alive: true}}, nil
		},
		loadCache: func(map[string]string) map[string]bpcache.State { return nil },
	}
	if err := a.status(nil); err != nil {
		t.Fatal(err)
	}
	if got := readTestOutput(t, out); strings.Contains(got, "CODEX") || !strings.Contains(got, "root") {
		t.Fatalf("output=%q", got)
	}
}

func TestCodexRenderersShowReadOnlyThreadState(t *testing.T) {
	out := testOutput(t)
	window := int64(200_000)
	threads := []codexrpc.Thread{
		{
			ID:     "thread-1",
			Name:   "builder",
			CWD:    "/srv/project",
			Status: codexrpc.ThreadStatus{Type: "active"},
			TokenUsage: &codexrpc.ThreadTokenUsage{
				Last:               codexrpc.TokenUsage{TotalTokens: 12_300},
				ModelContextWindow: &window,
			},
		},
		{ID: "thread-2", AgentNickname: "reader", CWD: "/srv/docs", Status: codexrpc.ThreadStatus{Type: "idle"}},
	}
	a := &app{
		config:    bpconfig.Config{Codex: &bpconfig.CodexConfig{Sockets: []string{"/run/codex.sock"}}},
		out:       out,
		loadCodex: func() []codexrpc.Thread { return threads },
	}
	gotThreads := a.codexThreads()
	if !reflect.DeepEqual(gotThreads, threads) {
		t.Fatalf("threads=%+v", gotThreads)
	}
	a.renderCodexStatus(gotThreads)
	a.renderCodexTree(gotThreads)
	got := readTestOutput(t, out)
	for _, want := range []string{
		"CODEX THREADS",
		"builder",
		"working",
		"12.3k/200k",
		"/srv/project",
		"reader",
		"idle",
		"Codex\n",
		"└── reader [idle] /srv/docs",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("output does not contain %q:\n%s", want, got)
		}
	}
}

func TestDeliveryTallyClassifiesNonAgentAsSkip(t *testing.T) {
	var tally deliveryTally
	// A real send (no error, not queued) counts as delivered.
	if !tally.record("alpha", false, "", nil) {
		t.Fatal("nil-error send should count as a delivery")
	}
	// A queued send records its channel and counts as delivered.
	if !tally.record("beta", true, "q1", nil) {
		t.Fatal("queued send should count as a delivery")
	}
	// A non-agent target (even when wrapped) is a SKIP, not a delivery or error.
	if tally.record("gamma", false, "", fmt.Errorf("wrap: %w", bptmux.ErrNotAgent)) {
		t.Fatal("ErrNotAgent must not count as a delivery")
	}
	// A genuine failure is a hard error.
	if tally.record("delta", false, "", errors.New("boom")) {
		t.Fatal("a real error must not count as a delivery")
	}
	if tally.sent != 1 {
		t.Fatalf("sent=%d, want 1", tally.sent)
	}
	if len(tally.channels) != 1 || tally.channels[0] != "q1" {
		t.Fatalf("channels=%v, want [q1]", tally.channels)
	}
	if tally.skipped != 1 {
		t.Fatalf("skipped=%d, want 1", tally.skipped)
	}
	if len(tally.errs) != 1 || !strings.Contains(tally.errs[0].Error(), "delta") {
		t.Fatalf("errs=%v, want one error mentioning delta", tally.errs)
	}
}

func TestAnnouncementTargetsFollowHierarchy(t *testing.T) {
	fleet := book.Fleet{
		Root:  "server-main",
		Order: []string{"server-main", "alpha", "alpha-child", "alpha-grandchild", "beta", "orphan", "lab-scratch", "closed-agent"},
		Agents: map[string]book.Agent{
			"server-main":      {Name: "server-main"},
			"alpha":            {Name: "alpha"},
			"alpha-child":      {Name: "alpha-child"},
			"alpha-grandchild": {Name: "alpha-grandchild"},
			"beta":             {Name: "beta"},
			"orphan":           {Name: "orphan"},
			"lab-scratch":      {Name: "lab-scratch"},
			"closed-agent":     {Name: "closed-agent"},
		},
		Parents: map[string]string{
			"server-main":      "",
			"alpha":            "server-main",
			"alpha-child":      "alpha",
			"alpha-grandchild": "alpha-child",
			"beta":             "server-main",
			"orphan":           "",
			"lab-scratch":      "alpha",
			"closed-agent":     "alpha",
		},
	}
	states := map[string]book.State{}
	for _, name := range fleet.Order {
		states[name] = book.State{Alive: true}
	}
	delete(states, "closed-agent")

	if got, want := announcementTargets(fleet, states, "alpha"), []string{"alpha-child", "alpha-grandchild"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("alpha targets=%v, want %v", got, want)
	}
	if got, want := announcementTargets(fleet, states, "server-main"), []string{"alpha", "alpha-child", "alpha-grandchild", "beta", "orphan"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("root targets=%v, want %v", got, want)
	}
}

func TestFormatDigest(t *testing.T) {
	entries := []pending.Entry{
		{TS: time.Date(2026, 7, 26, 11, 2, 0, 0, time.UTC).Unix(), From: "ada", Kind: "announce", Text: "ilk"},
		{TS: time.Date(2026, 7, 27, 15, 40, 0, 0, time.UTC).Unix(), From: "alp", Kind: "msg", Text: "ikinci"},
	}
	want := "[2 birikmis duyuru — 26-27 Tem]\n" +
		"1) (26 Tem 14:02) ilk\n" +
		"2) (27 Tem 18:40, alp) ikinci\n" +
		"(+1 eski duyuru dusuldu)"
	if got := formatDigest(entries, 1); got != want {
		t.Fatalf("digest:\n%q\nwant:\n%q", got, want)
	}
}

func TestAnnounceDefersColdAndSendsWarm(t *testing.T) {
	t.Setenv("AGENT", "server-main")
	t.Setenv("TMUX", "")
	stateDir := t.TempDir()
	out := testOutput(t)
	fleet := book.Fleet{
		Root:  "server-main",
		Order: []string{"server-main", "warm", "cold"},
		Agents: map[string]book.Agent{
			"server-main": {Name: "server-main"},
			"warm":        {Name: "warm", Folder: "/srv/warm"},
			"cold":        {Name: "cold", Folder: "/srv/cold"},
		},
		Parents: map[string]string{"server-main": "", "warm": "server-main", "cold": "server-main"},
	}
	states := map[string]book.State{"warm": {Alive: true}, "cold": {Alive: true}}
	var sent []string
	a := &app{
		config: bpconfig.Config{StateDir: stateDir},
		out:    out,
		loadFleet: func() (book.Fleet, map[string]book.State, error) {
			return fleet, states, nil
		},
		loadCache: func(map[string]string) map[string]bpcache.State {
			return map[string]bpcache.State{
				"warm": {Known: true, Age: 10 * time.Minute, CacheTTL: time.Hour, CacheAge: 10 * time.Minute, CtxTokens: 100_000},
				// Recent usage without a TTL must not trigger a warm-cache send.
				"cold": {Known: true, Age: 36 * time.Minute, CtxTokens: 300_000},
			}
		},
		deliverMessage: func(name, from, message string) (bool, string, error) {
			sent = append(sent, name+":"+message)
			return false, "", nil
		},
	}
	trustedSenderFixture(a)
	if err := a.announce([]string{"hello"}); err != nil {
		t.Fatal(err)
	}
	if len(sent) != 1 || !strings.HasPrefix(sent[0], "warm:") {
		t.Fatalf("sent=%v, want only warm", sent)
	}
	entries, _, err := pending.Load(stateDir, "cold")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Text != "hello" || entries[0].Kind != "announce" {
		t.Fatalf("cold pending=%+v", entries)
	}
	if got := readTestOutput(t, out); got != "sent: 1, deferred: 1\n" {
		t.Fatalf("output=%q", got)
	}
}

func TestMessageQueuesOfflineAndAttachesPendingOnce(t *testing.T) {
	t.Setenv("AGENT", "ada")
	t.Setenv("TMUX", "")
	t.Run("offline", func(t *testing.T) {
		stateDir := t.TempDir()
		out := testOutput(t)
		a := &app{
			config:        bpconfig.Config{StateDir: stateDir},
			out:           out,
			sessionExists: func(string) bool { return false },
		}
		trustedSenderFixture(a)
		if err := a.message([]string{"alp", "hello"}); err != nil {
			t.Fatal(err)
		}
		entries, _, err := pending.Load(stateDir, "alp")
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != 1 || entries[0].Kind != "msg" || entries[0].From != "ada" || entries[0].Text != "hello" {
			t.Fatalf("pending=%+v", entries)
		}
		if got := readTestOutput(t, out); got != "queued for alp (offline; delivered when it opens)\n" {
			t.Fatalf("output=%q", got)
		}
	})

	t.Run("digest and clear", func(t *testing.T) {
		stateDir := t.TempDir()
		if err := pending.Append(stateDir, "alp", pending.Entry{TS: time.Now().Unix(), From: "ada", Kind: "announce", Text: "news"}); err != nil {
			t.Fatal(err)
		}
		out := testOutput(t)
		calls := 0
		var delivered string
		a := &app{
			config:        bpconfig.Config{StateDir: stateDir},
			queue:         msgq.New(filepath.Join(stateDir, "msgq")),
			out:           out,
			sessionExists: func(string) bool { return true },
			deliverMessage: func(name, from, message string) (bool, string, error) {
				calls++
				delivered = message
				return false, "", nil
			},
		}
		trustedSenderFixture(a)
		if err := a.message([]string{"alp", "direct"}); err != nil {
			t.Fatal(err)
		}
		if calls != 1 || !strings.Contains(delivered, "birikmis duyuru") || !strings.HasSuffix(delivered, "\n\n[ada] direct") {
			t.Fatalf("calls=%d delivered=%q", calls, delivered)
		}
		entries, _, err := pending.Load(stateDir, "alp")
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != 0 {
			t.Fatalf("pending was not cleared: %+v", entries)
		}
	})

	t.Run("online envelope", func(t *testing.T) {
		var delivered string
		a := &app{
			config:        bpconfig.Config{StateDir: t.TempDir()},
			out:           testOutput(t),
			sessionExists: func(string) bool { return true },
			deliverMessage: func(name, from, message string) (bool, string, error) {
				delivered = message
				return false, "", nil
			},
		}
		trustedSenderFixture(a)
		// The envelope is added exactly once. A caller-written label is body
		// text; silently stripping it could rewrite a quote or a command.
		for _, body := range []string{"hello", "[ada] hello", "[server-main] quoted claim"} {
			if err := a.message([]string{"alp", body}); err != nil {
				t.Fatal(err)
			}
			if want := "[ada] " + body; delivered != want {
				t.Fatalf("delivered=%q, want %q", delivered, want)
			}
		}
	})

	// ada is above alp, oz is a sibling of alp: only ada (and root) may drive
	// alp's CLI with bare slash commands.
	slashFleet := func() (book.Fleet, map[string]book.State, error) {
		return book.Fleet{
			Root:  "server-main",
			Order: []string{"server-main", "ada", "alp", "oz"},
			Agents: map[string]book.Agent{
				"server-main": {Name: "server-main"},
				"ada":         {Name: "ada"},
				"alp":         {Name: "alp"},
				"oz":          {Name: "oz"},
			},
			Parents: map[string]string{"server-main": "", "ada": "server-main", "alp": "ada", "oz": "ada"},
		}, nil, nil
	}

	t.Run("slash command stays bare downward", func(t *testing.T) {
		var delivered string
		a := &app{
			config:        bpconfig.Config{StateDir: t.TempDir()},
			out:           testOutput(t),
			sessionExists: func(string) bool { return true },
			loadFleet:     slashFleet,
			deliverMessage: func(name, from, message string) (bool, string, error) {
				delivered = message
				return false, "", nil
			},
		}
		trustedSenderFixture(a)
		if err := a.message([]string{"alp", "/compact"}); err != nil {
			t.Fatal(err)
		}
		if delivered != "/compact" {
			t.Fatalf("delivered=%q, want %q", delivered, "/compact")
		}
	})

	// A slash command carries no digest, so it must not touch the spool: the
	// waiting messages (and any that are over the cap) stay for the delivery
	// that will actually show them, drop count and all.
	t.Run("slash command leaves pending untouched", func(t *testing.T) {
		stateDir := t.TempDir()
		for i := 0; i < 25; i++ {
			entry := pending.Entry{TS: time.Now().Add(time.Duration(i-25) * time.Minute).Unix(), From: "ada", Kind: "announce", Text: fmt.Sprint(i)}
			if err := pending.Append(stateDir, "alp", entry); err != nil {
				t.Fatal(err)
			}
		}
		spool := filepath.Join(stateDir, "pending", "alp.jsonl")
		before, err := os.Stat(spool)
		if err != nil {
			t.Fatal(err)
		}
		a := &app{
			config:        bpconfig.Config{StateDir: stateDir},
			out:           testOutput(t),
			sessionExists: func(string) bool { return true },
			loadFleet:     slashFleet,
			deliverMessage: func(name, from, message string) (bool, string, error) {
				return false, "", nil
			},
		}
		trustedSenderFixture(a)
		if err := a.message([]string{"alp", "/compact"}); err != nil {
			t.Fatal(err)
		}
		after, err := os.Stat(spool)
		if err != nil {
			t.Fatal(err)
		}
		if after.Size() != before.Size() || !after.ModTime().Equal(before.ModTime()) {
			t.Fatalf("a slash command pruned the spool: %d/%v -> %d/%v",
				before.Size(), before.ModTime(), after.Size(), after.ModTime())
		}
	})

	t.Run("goal carries the sender inside the payload", func(t *testing.T) {
		var delivered string
		a := &app{
			config:        bpconfig.Config{StateDir: t.TempDir()},
			out:           testOutput(t),
			sessionExists: func(string) bool { return true },
			loadFleet:     slashFleet,
			deliverMessage: func(name, from, message string) (bool, string, error) {
				delivered = message
				return false, "", nil
			},
		}
		trustedSenderFixture(a)
		if err := a.message([]string{"alp", "/goal", "finish", "report"}); err != nil {
			t.Fatal(err)
		}
		if want := "/goal [ada] finish report"; delivered != want {
			t.Fatalf("delivered=%q, want %q", delivered, want)
		}
	})

	t.Run("bare goal stays bare", func(t *testing.T) {
		var delivered string
		a := &app{
			config:        bpconfig.Config{StateDir: t.TempDir()},
			out:           testOutput(t),
			sessionExists: func(string) bool { return true },
			loadFleet:     slashFleet,
			deliverMessage: func(name, from, message string) (bool, string, error) {
				delivered = message
				return false, "", nil
			},
		}
		trustedSenderFixture(a)
		if err := a.message([]string{"alp", "/goal"}); err != nil {
			t.Fatal(err)
		}
		if delivered != "/goal" {
			t.Fatalf("delivered=%q, want %q", delivered, "/goal")
		}
	})

	t.Run("goal refused sideways", func(t *testing.T) {
		t.Setenv("AGENT", "oz")
		delivered := false
		a := &app{
			config:        bpconfig.Config{StateDir: t.TempDir()},
			out:           testOutput(t),
			sessionExists: func(string) bool { return true },
			loadFleet:     slashFleet,
			deliverMessage: func(name, from, message string) (bool, string, error) {
				delivered = true
				return false, "", nil
			},
		}
		trustedSenderFixture(a)
		err := a.message([]string{"alp", "/goal", "finish", "report"})
		if err == nil || !strings.Contains(err.Error(), "hierarchy") {
			t.Fatalf("err=%v, want hierarchy refusal", err)
		}
		if delivered {
			t.Fatal("refused goal was still delivered")
		}
	})

	t.Run("slash command refused sideways", func(t *testing.T) {
		t.Setenv("AGENT", "oz")
		delivered := false
		a := &app{
			config:        bpconfig.Config{StateDir: t.TempDir()},
			out:           testOutput(t),
			sessionExists: func(string) bool { return true },
			loadFleet:     slashFleet,
			deliverMessage: func(name, from, message string) (bool, string, error) {
				delivered = true
				return false, "", nil
			},
		}
		trustedSenderFixture(a)
		err := a.message([]string{"alp", "/compact"})
		if err == nil || !strings.Contains(err.Error(), "hierarchy") {
			t.Fatalf("err=%v, want hierarchy refusal", err)
		}
		if delivered {
			t.Fatal("refused slash command was still delivered")
		}
	})
}

// policyTestInputs builds a fleet where exactly one agent (alpha-child) clears
// the standing 24h/200k policy, with every other rejection reason represented.
func policyTestInputs(t *testing.T) (book.Fleet, map[string]book.State, map[string]bpcache.State, map[string]string) {
	t.Helper()
	fleet, states := compactTestFleet()
	commands := map[string]string{}
	cacheStates := map[string]bpcache.State{}
	for _, name := range fleet.Order {
		commands[name] = "claude"
		cacheStates[name] = bpcache.State{Known: true, LastHumanAge: 25 * time.Hour, CtxTokens: 200_001}
	}
	cacheStates["alpha"] = bpcache.State{Known: true, LastHumanAge: 24 * time.Hour, CtxTokens: 900_000}
	cacheStates["beta"] = bpcache.State{Known: true, LastHumanAge: 48 * time.Hour, CtxTokens: 200_000}
	cacheStates["lab-scratch"] = bpcache.State{LastHumanAge: -1}
	commands["orphan"] = "codex"
	states["alpha-grandchild"] = book.State{Alive: true, Busy: true}
	return fleet, states, cacheStates, commands
}

func policyTargetNames(rows []compactDecision) []string {
	var names []string
	for _, row := range rows {
		if row.Send {
			names = append(names, row.Name)
		}
	}
	return names
}

func TestPolicyDecisionsThresholds(t *testing.T) {
	fleet, states, cacheStates, commands := policyTestInputs(t)
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)

	rows := policyDecisions(fleet, states, cacheStates, commands, nil, defaultCompactOptions(), now)
	if want := []string{"alpha-child"}; !reflect.DeepEqual(policyTargetNames(rows), want) {
		t.Fatalf("targets=%v, want %v", policyTargetNames(rows), want)
	}
	want := []compactDecision{
		{Name: "alpha", Age: 24 * time.Hour, Tokens: 900_000, Reason: "taze konusma (<24sa)"},
		{Name: "alpha-child", Age: 25 * time.Hour, Tokens: 200_001, Send: true, Reason: "gonderilecek"},
		{Name: "alpha-grandchild", Age: 25 * time.Hour, Tokens: 200_001, Reason: "MESGUL, atlandi"},
		{Name: "beta", Age: 48 * time.Hour, Tokens: 200_000, Reason: "context kucuk"},
		{Name: "orphan", Age: 25 * time.Hour, Tokens: 200_001, Reason: "claude degil (codex)"},
		{Name: "lab-scratch", Age: -1, Tokens: -1, Reason: "transcript okunamadi"},
		{Name: "closed-agent", Age: 25 * time.Hour, Tokens: 200_001, Reason: "kapali"},
	}
	if !reflect.DeepEqual(rows, want) {
		t.Fatalf("rows=%+v\nwant=%+v", rows, want)
	}

	// A target compacted inside the --min-age window is left alone.
	lastCompact := map[string]time.Time{"alpha-child": now.Add(-5 * time.Minute)}
	rows = policyDecisions(fleet, states, cacheStates, commands, lastCompact, defaultCompactOptions(), now)
	if names := policyTargetNames(rows); len(names) != 0 {
		t.Fatalf("targets=%v, want none", names)
	}
	if got := rows[1].Reason; got != "yakinda compact edildi (5m once)" {
		t.Fatalf("alpha-child reason=%q", got)
	}

	// Lower thresholds widen the selection; the reason text follows --idle-hours.
	opts := defaultCompactOptions()
	opts.idle, opts.minCtx = 12*time.Hour, 100_000
	rows = policyDecisions(fleet, states, cacheStates, commands, nil, opts, now)
	if want := []string{"alpha", "alpha-child", "beta"}; !reflect.DeepEqual(policyTargetNames(rows), want) {
		t.Fatalf("targets=%v, want %v", policyTargetNames(rows), want)
	}
	opts.idle = 48 * time.Hour
	rows = policyDecisions(fleet, states, cacheStates, commands, nil, opts, now)
	if got := rows[0].Reason; got != "taze konusma (<48sa)" {
		t.Fatalf("alpha reason=%q", got)
	}
}

func testOutput(t *testing.T) *os.File {
	t.Helper()
	file, err := os.CreateTemp(t.TempDir(), "output")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = file.Close() })
	return file
}

func readTestOutput(t *testing.T, file *os.File) string {
	t.Helper()
	if _, err := file.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(file.Name())
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func compactTestFleet() (book.Fleet, map[string]book.State) {
	fleet := book.Fleet{
		Root:  "server-main",
		Order: []string{"server-main", "alpha", "alpha-child", "alpha-grandchild", "beta", "orphan", "lab-scratch", "closed-agent"},
		Agents: map[string]book.Agent{
			"server-main":      {Name: "server-main"},
			"alpha":            {Name: "alpha"},
			"alpha-child":      {Name: "alpha-child"},
			"alpha-grandchild": {Name: "alpha-grandchild"},
			"beta":             {Name: "beta"},
			"orphan":           {Name: "orphan"},
			"lab-scratch":      {Name: "lab-scratch"},
			"closed-agent":     {Name: "closed-agent"},
		},
		Parents: map[string]string{
			"server-main":      "",
			"alpha":            "server-main",
			"alpha-child":      "alpha",
			"alpha-grandchild": "alpha-child",
			"beta":             "server-main",
			"orphan":           "",
			"lab-scratch":      "alpha",
			"closed-agent":     "alpha",
		},
	}
	states := map[string]book.State{}
	for _, name := range fleet.Order {
		states[name] = book.State{Alive: true}
	}
	delete(states, "closed-agent")
	return fleet, states
}

func TestSelectCompactTargetsHardExclusions(t *testing.T) {
	fleet, states := compactTestFleet()
	now := time.Now()

	// server-main sees the whole fleet but never itself.
	plan := selectCompactTargets(fleet, states, "server-main", nil, nil, 30*time.Minute, now)
	if want := []string{"alpha", "alpha-child", "alpha-grandchild", "beta", "orphan"}; !reflect.DeepEqual(plan.Send, want) {
		t.Fatalf("root send=%v, want %v", plan.Send, want)
	}
	if len(plan.SkippedRecent) != 0 || len(plan.Excluded) != 0 {
		t.Fatalf("expected no skips for root, got %+v", plan)
	}

	// A non-root sender only sees strict descendants (server-main is never a target here anyway).
	plan = selectCompactTargets(fleet, states, "alpha", nil, nil, 30*time.Minute, now)
	if want := []string{"alpha-child", "alpha-grandchild"}; !reflect.DeepEqual(plan.Send, want) {
		t.Fatalf("alpha send=%v, want %v", plan.Send, want)
	}
}

func TestSelectCompactTargetsUserExclude(t *testing.T) {
	fleet, states := compactTestFleet()
	now := time.Now()

	plan := selectCompactTargets(fleet, states, "server-main", []string{"beta", "does-not-exist"}, nil, 30*time.Minute, now)
	if want := []string{"alpha", "alpha-child", "alpha-grandchild", "orphan"}; !reflect.DeepEqual(plan.Send, want) {
		t.Fatalf("send=%v, want %v", plan.Send, want)
	}
	if want := []string{"beta"}; !reflect.DeepEqual(plan.Excluded, want) {
		t.Fatalf("excluded=%v, want %v", plan.Excluded, want)
	}
	if want := []string{"does-not-exist"}; !reflect.DeepEqual(plan.UnknownExcludes, want) {
		t.Fatalf("unknown=%v, want %v", plan.UnknownExcludes, want)
	}
}

func TestSelectCompactTargetsMinAge(t *testing.T) {
	fleet, states := compactTestFleet()
	now := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)
	lastCompact := map[string]time.Time{
		"alpha":       now.Add(-5 * time.Minute),  // recent -> skipped
		"alpha-child": now.Add(-40 * time.Minute), // old enough -> sent
	}

	plan := selectCompactTargets(fleet, states, "server-main", nil, lastCompact, 30*time.Minute, now)
	if want := []string{"alpha-child", "alpha-grandchild", "beta", "orphan"}; !reflect.DeepEqual(plan.Send, want) {
		t.Fatalf("send=%v, want %v", plan.Send, want)
	}
	if len(plan.SkippedRecent) != 1 || plan.SkippedRecent[0].Name != "alpha" {
		t.Fatalf("skippedRecent=%+v, want only alpha", plan.SkippedRecent)
	}
	if got := formatAge(plan.SkippedRecent[0].Age); got != "5m" {
		t.Fatalf("age=%q, want 5m", got)
	}
}

func TestParseCompactArgs(t *testing.T) {
	opts, err := parseCompactArgs(nil)
	if err != nil {
		t.Fatal(err)
	}
	// The owner's standing policy: 24h idle, 200k context. Listing, not sending.
	if !reflect.DeepEqual(opts, defaultCompactOptions()) {
		t.Fatalf("defaults=%+v, want %+v", opts, defaultCompactOptions())
	}

	// --dry-run and --policy survive as no-op synonyms of the new defaults.
	opts, err = parseCompactArgs([]string{"--dry-run", "--policy"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(opts, defaultCompactOptions()) {
		t.Fatalf("synonyms changed behaviour: %+v", opts)
	}

	opts, err = parseCompactArgs([]string{"--all", "--apply", "--min-age=15", "--exclude", "a, b ,c", "--exclude=d", "--idle-hours", "6", "--min-ctx=50000"})
	if err != nil {
		t.Fatal(err)
	}
	if opts.minAge != 15*time.Minute || opts.idle != 6*time.Hour || opts.minCtx != 50_000 {
		t.Fatalf("thresholds=%+v", opts)
	}
	if !opts.apply || !opts.all {
		t.Fatalf("apply=%v all=%v, want both true", opts.apply, opts.all)
	}
	if want := []string{"a", "b", "c", "d"}; !reflect.DeepEqual(opts.exclude, want) {
		t.Fatalf("exclude=%v, want %v", opts.exclude, want)
	}

	// Combinations that mean something and must keep parsing.
	for _, args := range [][]string{
		{"--apply"}, {"--dry-run"}, {"--policy"}, {"--policy", "--apply"}, {"--all", "--apply"},
		{"--all", "--exclude", "a"}, {"--idle-hours", "6", "--min-ctx=50000"},
	} {
		if _, err := parseCompactArgs(args); err != nil {
			t.Errorf("parseCompactArgs(%v) = %v, want success", args, err)
		}
	}

	for _, args := range [][]string{
		{"--min-age"}, {"--min-age", "-1"}, {"--min-age", "x"}, {"--min-age", "1", "--min-age", "2"},
		{"--idle-hours"}, {"--idle-hours", "-1"}, {"--idle-hours", "x"}, {"--idle-hours", "1", "--idle-hours=2"},
		{"--min-ctx"}, {"--min-ctx", "-1"}, {"--min-ctx", "x"}, {"--min-ctx=1", "--min-ctx", "2"},
		{"--exclude"}, {"--apply=yes"}, {"--all=1"}, {"--bogus"},
	} {
		if _, err := parseCompactArgs(args); err == nil {
			t.Errorf("parseCompactArgs(%v) succeeded, want error", args)
		}
	}

	// Self-contradictory pairs: a no-op synonym naming the default must never
	// lose silently to the flag that overrides that default. Both orders must
	// fail, and the message must name both flags so the typo is obvious.
	for _, contradiction := range []struct{ args, mention []string }{
		{[]string{"--apply", "--dry-run"}, []string{"--apply", "--dry-run"}},
		{[]string{"--dry-run", "--apply"}, []string{"--apply", "--dry-run"}},
		{[]string{"--all", "--policy"}, []string{"--all", "--policy"}},
		{[]string{"--policy", "--all"}, []string{"--all", "--policy"}},
	} {
		_, err := parseCompactArgs(contradiction.args)
		if err == nil {
			t.Errorf("parseCompactArgs(%v) succeeded, want error", contradiction.args)
			continue
		}
		for _, flag := range contradiction.mention {
			if !strings.Contains(err.Error(), flag) {
				t.Errorf("parseCompactArgs(%v) error %q does not mention %s", contradiction.args, err, flag)
			}
		}
	}
}

// The contradiction is caught in parsing: bp compact --apply --dry-run must
// fail before the fleet is read, before any pane is captured and above all
// before anything is delivered.
func TestCompactApplyWithDryRunNeverReachesFleet(t *testing.T) {
	for _, args := range [][]string{{"--apply", "--dry-run"}, {"--dry-run", "--apply"}, {"--all", "--policy", "--apply"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			t.Setenv("AGENT", "server-main")
			t.Setenv("TMUX", "")
			a := &app{
				config: bpconfig.Config{StateDir: t.TempDir()},
				out:    testOutput(t),
				loadFleet: func() (book.Fleet, map[string]book.State, error) {
					t.Fatalf("compact %v loaded the fleet", args)
					return book.Fleet{}, nil, nil
				},
				loadCache: func(map[string]string) map[string]bpcache.State {
					t.Fatalf("compact %v read the cache", args)
					return nil
				},
				loadCommands: func() (map[string]string, error) { t.Fatalf("compact %v listed panes", args); return nil, nil },
				capturePane: func(string) (string, error) {
					t.Fatalf("compact %v captured a pane", args)
					return "", nil
				},
				deliverMessage: func(name, from, message string) (bool, string, error) {
					t.Fatalf("compact %v delivered %s to %s", args, message, name)
					return false, "", nil
				},
			}
			trustedSenderFixture(a)
			if err := a.compact(args); err == nil {
				t.Fatalf("compact %v succeeded, want error", args)
			}
			if got := readTestOutput(t, a.out); got != "" {
				t.Fatalf("compact %v printed:\n%s", args, got)
			}
		})
	}
}

// compactApp wires a fleet with two policy-eligible agents (alpha-child and
// lab-scratch) to in-memory hooks, so no test can reach tmux.
func compactApp(t *testing.T, sent *[]string, panes map[string]string) *app {
	t.Helper()
	t.Setenv("AGENT", "server-main")
	t.Setenv("TMUX", "")
	fleet, states, cacheStates, commands := policyTestInputs(t)
	return &app{
		config: bpconfig.Config{StateDir: t.TempDir()},
		out:    testOutput(t),
		loadFleet: func() (book.Fleet, map[string]book.State, error) {
			return fleet, states, nil
		},
		loadCache:    func(map[string]string) map[string]bpcache.State { return cacheStates },
		loadCommands: func() (map[string]string, error) { return commands, nil },
		clearPane:    func(string) error { return nil },
		capturePane: func(name string) (string, error) {
			if pane, ok := panes[name]; ok {
				return pane, nil
			}
			return idlePane, nil
		},
		deliverMessage: func(name, from, message string) (bool, string, error) {
			if sent == nil {
				t.Fatalf("compact delivered %s to %s without --apply", message, name)
			}
			*sent = append(*sent, name+":"+message)
			return false, "", nil
		},
	}
}

const (
	idlePane  = "❯ \n  ready\n"
	busyPane  = "✻ Working… (23s · Esc to interrupt)\n❯ \n"
	typedPane = "❯ half-written question\n"
)

func TestCompactListsWithoutSendingByDefault(t *testing.T) {
	a := compactApp(t, nil, nil)
	trustedSenderFixture(a)
	if err := a.compact(nil); err != nil {
		t.Fatal(err)
	}
	got := readTestOutput(t, a.out)
	want := fmt.Sprintf("%-24s %-10s %-10s %s\n", "AGENT", "KONUSMA", "CONTEXT", "KARAR") +
		fmt.Sprintf("%-24s %-10s %-10s %s\n", "alpha", "24h", "900k", "taze konusma (<24sa)") +
		fmt.Sprintf("%-24s %-10s %-10s %s\n", "alpha-child", "25h", "200k", "gonderilecek") +
		fmt.Sprintf("%-24s %-10s %-10s %s\n", "alpha-grandchild", "25h", "200k", "MESGUL, atlandi") +
		fmt.Sprintf("%-24s %-10s %-10s %s\n", "beta", "2d", "200k", "context kucuk") +
		fmt.Sprintf("%-24s %-10s %-10s %s\n", "orphan", "25h", "200k", "claude degil (codex)") +
		fmt.Sprintf("%-24s %-10s %-10s %s\n", "lab-scratch", "-", "-", "transcript okunamadi") +
		fmt.Sprintf("%-24s %-10s %-10s %s\n", "closed-agent", "25h", "200k", "kapali") +
		"gonderilecek: 1, atlanan: 6 (gondermek icin: bp compact --apply)\n"
	if got != want {
		t.Fatalf("output:\n%s\nwant:\n%s", got, want)
	}
	if _, err := os.Stat(filepath.Join(a.config.StateDir, "compact.json")); !os.IsNotExist(err) {
		t.Fatalf("listing touched compact.json: %v", err)
	}
}

// --dry-run and --policy are synonyms of the default, so they must print the
// same table and still send nothing.
func TestCompactSynonymsMatchDefault(t *testing.T) {
	first := compactApp(t, nil, nil)
	trustedSenderFixture(first)
	if err := first.compact(nil); err != nil {
		t.Fatal(err)
	}
	second := compactApp(t, nil, nil)
	trustedSenderFixture(second)
	if err := second.compact([]string{"--policy", "--dry-run"}); err != nil {
		t.Fatal(err)
	}
	if got, want := readTestOutput(t, second.out), readTestOutput(t, first.out); got != want {
		t.Fatalf("output:\n%s\nwant:\n%s", got, want)
	}
}

func TestCompactApplySendsAndRecordsState(t *testing.T) {
	var sent []string
	a := compactApp(t, &sent, nil)
	trustedSenderFixture(a)
	if err := a.compact([]string{"--apply"}); err != nil {
		t.Fatal(err)
	}
	if want := []string{"alpha-child:/compact"}; !reflect.DeepEqual(sent, want) {
		t.Fatalf("sent=%v, want %v", sent, want)
	}
	got := readTestOutput(t, a.out)
	if !strings.Contains(got, fmt.Sprintf("%-24s %-10s %-10s %s\n", "alpha-child", "25h", "200k", "gonderildi")) {
		t.Fatalf("table does not report the send:\n%s", got)
	}
	if !strings.HasSuffix(got, "gonderildi: 1, atlanan: 6\n") {
		t.Fatalf("summary missing:\n%s", got)
	}
	state := loadCompactState(filepath.Join(a.config.StateDir, "compact.json"))
	if _, ok := state["alpha-child"]; !ok || len(state) != 1 {
		t.Fatalf("compact state=%v", state)
	}
}

func TestCompactPolicyCannotSendSidewaysOrUpward(t *testing.T) {
	var sent []string
	a := compactApp(t, &sent, nil)
	t.Setenv("AGENT", "beta")
	trustedSenderFixture(a)
	if err := a.compact([]string{"--apply"}); err != nil {
		t.Fatal(err)
	}
	if len(sent) != 0 {
		t.Fatalf("policy escaped sender hierarchy: %v", sent)
	}
}

// /compact is a slash command bp types itself, so it takes the same clearing
// step as bp rename: a composer left in a state that only LOOKS empty makes the
// send bounce off, and a /compact that never lands is invisible — the agent just
// keeps growing.
func TestCompactApplyClearsTheComposerBeforeSending(t *testing.T) {
	var sent []string
	a := compactApp(t, &sent, nil)
	var events []string
	a.clearPane = func(name string) error {
		events = append(events, "clear:"+name)
		return nil
	}
	deliver := a.deliverMessage
	a.deliverMessage = func(name, from, message string) (bool, string, error) {
		events = append(events, "send:"+name+":"+message)
		return deliver(name, from, message)
	}
	trustedSenderFixture(a)
	if err := a.compact([]string{"--apply"}); err != nil {
		t.Fatal(err)
	}
	want := []string{"clear:alpha-child", "send:alpha-child:/compact"}
	if !reflect.DeepEqual(events, want) {
		t.Fatalf("events=%v, want %v", events, want)
	}
}

// A pane that refuses to be cleared is skipped, never queued: a /compact
// delivered after the next turn compacts the wrong conversation.
func TestCompactSkipsPanesThatRefuseToClear(t *testing.T) {
	cases := map[string]struct {
		err    error
		reason string
	}{
		"busy":      {bptmux.ErrBusy, compactBusy},
		"typing":    {bptmux.ErrTyping, compactBusy},
		"not agent": {bptmux.ErrNotAgent, compactNotAgent},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			var sent []string
			a := compactApp(t, &sent, nil)
			a.clearPane = func(string) error { return tc.err }
			trustedSenderFixture(a)
			if err := a.compact([]string{"--apply"}); err != nil {
				t.Fatal(err)
			}
			if len(sent) != 0 {
				t.Fatalf("sent=%v, want nothing", sent)
			}
			got := readTestOutput(t, a.out)
			if !strings.Contains(got, fmt.Sprintf("%-24s %-10s %-10s %s\n", "alpha-child", "25h", "200k", tc.reason)) {
				t.Fatalf("table does not report the skip:\n%s", got)
			}
			if _, err := os.Stat(filepath.Join(a.config.StateDir, "compact.json")); !os.IsNotExist(err) {
				t.Fatalf("a skipped compact wrote state: %v", err)
			}
		})
	}
}

func TestCompactSkipsBusyAndTypedTargets(t *testing.T) {
	for name, pane := range map[string]string{"busy": busyPane, "typing": typedPane} {
		t.Run(name, func(t *testing.T) {
			var sent []string
			a := compactApp(t, &sent, map[string]string{"alpha-child": pane})
			trustedSenderFixture(a)
			if err := a.compact([]string{"--apply"}); err != nil {
				t.Fatal(err)
			}
			if len(sent) != 0 {
				t.Fatalf("sent=%v, want nothing", sent)
			}
			got := readTestOutput(t, a.out)
			if !strings.Contains(got, fmt.Sprintf("%-24s %-10s %-10s %s\n", "alpha-child", "25h", "200k", "MESGUL, atlandi")) {
				t.Fatalf("table does not report the skip:\n%s", got)
			}
			if !strings.HasSuffix(got, "gonderildi: 0, atlanan: 7\n") {
				t.Fatalf("summary:\n%s", got)
			}
			if _, err := os.Stat(filepath.Join(a.config.StateDir, "compact.json")); !os.IsNotExist(err) {
				t.Fatalf("a skipped compact wrote state: %v", err)
			}
		})
	}
}

// An unreadable pane counts as busy: bp never interrupts what it cannot see.
func TestCompactSkipsUnreadablePane(t *testing.T) {
	var sent []string
	a := compactApp(t, &sent, nil)
	a.capturePane = func(string) (string, error) { return "", errors.New("no such session") }
	trustedSenderFixture(a)
	if err := a.compact([]string{"--apply"}); err != nil {
		t.Fatal(err)
	}
	if len(sent) != 0 {
		t.Fatalf("sent=%v, want nothing", sent)
	}
}

func TestCompactAllKeepsTheDescendantSweep(t *testing.T) {
	var sent []string
	// --all has no idle or context thresholds, so only the pane says "busy".
	a := compactApp(t, &sent, map[string]string{"alpha-grandchild": busyPane})
	trustedSenderFixture(a)
	if err := a.compact([]string{"--all", "--exclude", "beta,does-not-exist", "--apply"}); err != nil {
		t.Fatal(err)
	}
	// Every live descendant of server-main except the excluded one — no idle or
	// context thresholds, exactly as --all behaved before.
	want := []string{"alpha:/compact", "alpha-child:/compact", "orphan:/compact"}
	if !reflect.DeepEqual(sent, want) {
		t.Fatalf("sent=%v, want %v", sent, want)
	}
	got := readTestOutput(t, a.out)
	for _, line := range []string{
		fmt.Sprintf("%-24s %-10s %-10s %s\n", "alpha-grandchild", "25h", "200k", "MESGUL, atlandi"),
		fmt.Sprintf("%-24s %-10s %-10s %s\n", "beta", "2d", "200k", "haric tutuldu"),
		fmt.Sprintf("%-24s %-10s %-10s %s\n", "closed-agent", "25h", "200k", "kapali"),
		"bilinmeyen exclude: does-not-exist\n",
		"gonderildi: 3, atlanan: 3\n",
	} {
		if !strings.Contains(got, line) {
			t.Fatalf("output missing %q:\n%s", line, got)
		}
	}
}

func statusTestApp(t *testing.T) *app {
	t.Helper()
	fleet := book.Fleet{
		Root:  "server-main",
		Order: []string{"server-main", "alpha", "closed-agent"},
		Agents: map[string]book.Agent{
			"server-main":  {Name: "server-main", Status: "open", Folder: "/srv"},
			"alpha":        {Name: "alpha", Status: "open", Folder: "/srv/alpha"},
			"closed-agent": {Name: "closed-agent", Status: "closed", Folder: "/srv/closed"},
		},
		Parents: map[string]string{"server-main": "", "alpha": "server-main", "closed-agent": "alpha"},
	}
	states := map[string]book.State{"server-main": {Alive: true}, "alpha": {Alive: true, Busy: true, ScreenBusy: true}}
	cacheStates := map[string]bpcache.State{
		"alpha": {Known: true, Age: 90 * time.Second, CtxTokens: 312_000, LastHumanAge: 25 * time.Hour, Model: "claude-opus-5"},
		// Unknown transcript: no numbers may be invented for it.
		"closed-agent": {LastHumanAge: -1},
	}
	return &app{
		out:       testOutput(t),
		loadFleet: func() (book.Fleet, map[string]book.State, error) { return fleet, states, nil },
		loadCache: func(map[string]string) map[string]bpcache.State { return cacheStates },
	}
}

// The human table is what every agent and script reads today: --json must be
// additive, never a reformat.
func TestStatusHumanOutputIsUnchanged(t *testing.T) {
	a := statusTestApp(t)
	if err := a.status(nil); err != nil {
		t.Fatal(err)
	}
	want := fmt.Sprintf("%-24s %-10s %-20s %-10s %s\n", "AGENT", "TMUX", "CACHE", "LAST-TALK", "AGENTBOOK") +
		fmt.Sprintf("%-24s %-10s %-20s %-10s %-10s%s\n", "alpha", "working", "age 1m 312k", "25h", "open", "") +
		fmt.Sprintf("%-24s %-10s %-20s %-10s %-10s%s\n", "closed-agent", "closed", "-", "-", "closed", "") +
		fmt.Sprintf("%-24s %-10s %-20s %-10s %-10s%s\n", "server-main", "idle", "-", "-", "open", "")
	if got := readTestOutput(t, a.out); got != want {
		t.Fatalf("output:\n%q\nwant:\n%q", got, want)
	}
}

func TestStatusJSON(t *testing.T) {
	a := statusTestApp(t)
	if err := a.status([]string{"--json"}); err != nil {
		t.Fatal(err)
	}
	raw := readTestOutput(t, a.out)
	var report struct {
		Agents []map[string]any `json:"agents"`
		Codex  []map[string]any `json:"codex"`
	}
	if err := json.Unmarshal([]byte(raw), &report); err != nil {
		t.Fatalf("not one JSON object: %v\n%s", err, raw)
	}
	if len(report.Agents) != 3 || len(report.Codex) != 0 {
		t.Fatalf("report=%+v", report)
	}
	alpha := report.Agents[0]
	want := map[string]any{
		"name": "alpha", "tmux": "working", "status": "open", "folder": "/srv/alpha", "parent": "server-main",
		"usage_scope": "last_context_snapshot",
		"ctx_tokens":  float64(312_000), "cache_age_seconds": float64(90), "last_human_age_seconds": float64(90_000),
		"model": "claude-opus-5",
		// The composite's two components, exposed so "which gate spoke?" is
		// answerable from outside (2026-08-18): alpha's verdict came from the
		// screen, and the transcript gate's silence is a fact, not an omission.
		"busy_screen": true, "busy_turnopen": false,
	}
	if !reflect.DeepEqual(alpha, want) {
		t.Fatalf("alpha=%+v\nwant=%+v", alpha, want)
	}
	// Unknown numbers are omitted, never emitted as zeros that read as facts.
	closed := report.Agents[1]
	for _, key := range []string{"ctx_tokens", "cache_age_seconds", "last_human_age_seconds", "model"} {
		if _, ok := closed[key]; ok {
			t.Fatalf("closed-agent carries %s: %+v", key, closed)
		}
	}
	if closed["tmux"] != "closed" || closed["name"] != "closed-agent" {
		t.Fatalf("closed-agent=%+v", closed)
	}
}

func TestStatusRejectsUnknownOption(t *testing.T) {
	a := &app{}
	if err := a.status([]string{"--nope"}); err == nil || !strings.Contains(err.Error(), "unknown status option") {
		t.Fatalf("error=%v", err)
	}
	if err := a.status([]string{"--json", "--json"}); err == nil {
		t.Fatal("repeated --json accepted")
	}
}

// "bp status --help" once registered "--help" into the agentbook: help is
// answered in the dispatcher, before any command sees its arguments.
func TestHelpIsAnsweredBeforeCommandLogic(t *testing.T) {
	for _, args := range [][]string{
		{"status", "--help"}, {"status", "-h"}, {"tree", "--help"}, {"compact", "--help"},
		{"peek", "-h"}, {"close", "--help"}, {"open", "--help"}, {"msg", "--help"},
		{"compact", "--apply", "--help"}, {"fed", "--help"},
	} {
		out := testOutput(t)
		a := &app{
			out: out,
			loadFleet: func() (book.Fleet, map[string]book.State, error) {
				t.Fatalf("%v reached command logic", args)
				return book.Fleet{}, nil, nil
			},
			deliverMessage: func(string, string, string) (bool, string, error) {
				t.Fatalf("%v reached delivery", args)
				return false, "", nil
			},
		}
		if err := a.run(args); err != nil {
			t.Fatalf("run(%v)=%v", args, err)
		}
		if got := readTestOutput(t, out); got != usage+"\n" {
			t.Fatalf("run(%v) printed %q", args, got)
		}
	}
}

// A message whose text happens to contain --help is still a message.
func TestHelpDoesNotSwallowMessageText(t *testing.T) {
	t.Setenv("AGENT", "ada")
	t.Setenv("TMUX", "")
	var delivered string
	a := &app{
		config:        bpconfig.Config{StateDir: t.TempDir()},
		out:           testOutput(t),
		sessionExists: func(string) bool { return true },
		deliverMessage: func(name, from, message string) (bool, string, error) {
			delivered = message
			return false, "", nil
		},
	}
	trustedSenderFixture(a)
	if err := a.run([]string{"msg", "alp", "run", "--help"}); err != nil {
		t.Fatal(err)
	}
	if want := "[ada] run --help"; delivered != want {
		t.Fatalf("delivered=%q, want %q", delivered, want)
	}
}

// An argument starting with "-" must never be taken for an agent name.
func TestAgentNameArgumentsRejectFlags(t *testing.T) {
	a := &app{ctx: context.Background(), config: bpconfig.Config{StateDir: t.TempDir()}, out: testOutput(t)}
	cases := []struct {
		run  func() error
		want string
	}{
		{func() error { return a.tree([]string{"--nope"}) }, "unknown tree option: --nope"},
		{func() error { return a.close([]string{"--nope"}) }, "unknown close option: --nope"},
		{func() error { return a.peek([]string{"--nope"}) }, "unknown peek option: --nope"},
		{func() error { return a.message([]string{"--nope", "hi"}) }, "unknown msg option: --nope"},
		{func() error { return a.open([]string{"--nope", "/tmp"}) }, "unknown open option: --nope"},
	}
	for _, tc := range cases {
		if err := tc.run(); err == nil || err.Error() != tc.want {
			t.Errorf("error=%v, want %q", err, tc.want)
		}
	}
}

func TestLoadConnectConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config")
	if err := os.WriteFile(path, []byte("# laptop target\nREMOTE = ops@example.com\nREMOTE_METHOD=SSH\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := loadConnectConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	want := connectConfig{Remote: "ops@example.com", Method: "ssh"}
	if got != want {
		t.Fatalf("config=%+v, want %+v", got, want)
	}
}

func TestLoadConnectConfigDefaultsToMosh(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config")
	if err := os.WriteFile(path, []byte("REMOTE=server.example.com\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := loadConnectConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Method != "mosh" {
		t.Fatalf("method=%q, want mosh", got.Method)
	}
}

func TestLoadConnectConfigRejectsInvalidMethod(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config")
	if err := os.WriteFile(path, []byte("REMOTE=server\nREMOTE_METHOD=telnet\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := loadConnectConfig(path)
	if err == nil || !strings.Contains(err.Error(), "mosh or ssh") {
		t.Fatalf("error=%v, want invalid method error", err)
	}
}

func TestLoadDashboardURL(t *testing.T) {
	t.Run("default when missing", func(t *testing.T) {
		got, err := loadDashboardURL(filepath.Join(t.TempDir(), "missing"))
		if err != nil {
			t.Fatal(err)
		}
		if got != dashboard.DefaultURL {
			t.Fatalf("URL=%q, want %q", got, dashboard.DefaultURL)
		}
	})

	t.Run("configured", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "config")
		contents := "REMOTE=ops@example.com\nDASH_URL = 'https://dash.example.com/monitor'\n"
		if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
		got, err := loadDashboardURL(path)
		if err != nil {
			t.Fatal(err)
		}
		if got != "https://dash.example.com/monitor" {
			t.Fatalf("URL=%q, want configured URL", got)
		}
	})
}

func TestParseDashboardPort(t *testing.T) {
	for _, test := range []struct {
		args []string
		want int
	}{
		{want: dashboard.DefaultPort},
		{args: []string{"--port", "9000"}, want: 9000},
		{args: []string{"--port=4321"}, want: 4321},
	} {
		got, err := parseDashboardPort(test.args)
		if err != nil {
			t.Fatalf("parseDashboardPort(%v): %v", test.args, err)
		}
		if got != test.want {
			t.Fatalf("parseDashboardPort(%v)=%d, want %d", test.args, got, test.want)
		}
	}

	for _, args := range [][]string{{"--port"}, {"--port", "0"}, {"--port", "70000"}, {"--listen", "9000"}, {"--port", "1", "--port", "2"}} {
		if _, err := parseDashboardPort(args); err == nil {
			t.Errorf("parseDashboardPort(%v) succeeded, want error", args)
		}
	}
}

func TestSafeSessionName(t *testing.T) {
	for _, name := range []string{"server-main", "agent_2", "build.v3"} {
		if !safeSessionName.MatchString(name) {
			t.Errorf("expected %q to be safe", name)
		}
	}
	for _, name := range []string{"", "-server", "agent name", "agent;whoami"} {
		if safeSessionName.MatchString(name) {
			t.Errorf("expected %q to be rejected", name)
		}
	}
}

func TestSenderPrecedence(t *testing.T) {
	// A human on the box reaches bp as `sudo -n bp msg ...`: outside tmux,
	// no AGENT. Those messages must carry the person's name, never the
	// orchestrator's.
	sessionTmux := func(t *testing.T, session string) *bptmux.Client {
		t.Helper()
		dir := t.TempDir()
		path := filepath.Join(dir, "tmux")
		t.Setenv("TMUX_PANE", "%42")
		script := fmt.Sprintf("#!/bin/sh\nprintf '%%s\\t%%s\\t0\\n' %s %d\n", session, os.Getpid())
		if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
		return &bptmux.Client{Bin: path, Sleep: func(time.Duration) {}, Now: time.Now}
	}

	tests := []struct {
		name    string
		tmuxEnv string
		session string
		env     map[string]string
		want    string
	}{
		{
			name:    "tmux beats everything",
			tmuxEnv: "/tmp/tmux-0/default,123,0",
			session: "lab-scratch",
			env:     map[string]string{"AGENT": "ada", "SUDO_USER": "tunapro", "USER": "tunapro"},
			want:    "lab-scratch",
		},
		{
			name: "agent beats sudo user",
			env:  map[string]string{"AGENT": "tunarch", "SUDO_USER": "tunapro", "USER": "root"},
			want: "agent?:tunarch",
		},
		{
			name: "sudo user names the human",
			env:  map[string]string{"SUDO_USER": "tunapro", "USER": "root", "LOGNAME": "root"},
			want: "tunapro",
		},
		{
			name: "sudo user root falls through to user",
			env:  map[string]string{"SUDO_USER": "root", "USER": "tunapro"},
			want: "tunapro",
		},
		{
			name: "logname when user is root",
			env:  map[string]string{"USER": "root", "LOGNAME": "tunapro"},
			want: "tunapro",
		},
		{
			name: "invalid user is skipped",
			env:  map[string]string{"USER": "two words", "LOGNAME": "ok-name"},
			want: "ok-name",
		},
		{
			name: "control characters are skipped",
			env:  map[string]string{"SUDO_USER": "ada\nserver-main", "USER": "bad\tname"},
			want: identity.Unknown,
		},
		{
			name: "daemon and cron keep the default",
			env:  map[string]string{"USER": "root", "LOGNAME": "root"},
			want: identity.Unknown,
		},
		{
			name: "empty environment keeps the default",
			want: identity.Unknown,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("TMUX", test.tmuxEnv)
			// AGENTBOOK is cleared so the collision rule (a login name that is
			// also an agent) cannot pull the live fleet book into a unit test.
			for _, key := range []string{"AGENT", "SUDO_USER", "USER", "LOGNAME", "AGENTBOOK"} {
				t.Setenv(key, test.env[key])
			}
			a := &app{ctx: context.Background(), tmux: bptmux.New(), originProbe: func(context.Context) identity.Origin { return identity.Origin{} }}
			if test.session != "" {
				a.tmux = sessionTmux(t, test.session)
			}
			if got := a.sender(); got != test.want {
				t.Fatalf("sender()=%q, want %q", got, test.want)
			}
		})
	}
}

// TestSenderNeverAsksTmuxOutsidePane is the regression guard for the incident:
// `tmux display-message -p '#S'` with no target and no TMUX in the environment
// answers for the ATTACHED client, so a cron job that asks gets a spectator's
// name. The fake tmux here records every invocation; outside a pane there must
// be none.
func TestSenderNeverAsksTmuxOutsidePane(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "asked")
	path := filepath.Join(dir, "tmux")
	script := "#!/bin/sh\necho \"$@\" >> " + marker + "\necho compec-outreach\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	for _, key := range []string{"TMUX", "AGENT", "SUDO_USER", "USER", "LOGNAME", "AGENTBOOK"} {
		t.Setenv(key, "")
	}
	a := &app{ctx: context.Background(), tmux: &bptmux.Client{Bin: path, Sleep: func(time.Duration) {}, Now: time.Now}}

	if got := a.sender(); got != identity.Unknown {
		t.Fatalf("sender()=%q, want an unknown sender", got)
	}
	if data, err := os.ReadFile(marker); err == nil {
		t.Fatalf("tmux was consulted with TMUX empty: %q", data)
	}
}

// TestWhatsAppSendFrom covers the flag that lets a cron script state who it is,
// and the two ways it must be refused.
func TestWhatsAppSendFrom(t *testing.T) {
	const script = "cron:/srv/kavram/outreach/workers/inbox_watcher.py"

	newApp := func(t *testing.T, outbox string) (*app, *os.File) {
		t.Helper()
		errOutput := testOutput(t)
		return &app{
			ctx:         context.Background(),
			originProbe: func(context.Context) identity.Origin { return identity.Origin{} },
			config:      bpconfig.Config{WAOutbox: outbox},
			tmux:        bptmux.New(),
			out:         testOutput(t),
			err:         errOutput,
		}, errOutput
	}
	clearEnv := func(t *testing.T) {
		t.Helper()
		for _, key := range []string{"TMUX", "AGENT", "SUDO_USER", "USER", "LOGNAME", "AGENTBOOK"} {
			t.Setenv(key, "")
		}
	}
	queued := func(t *testing.T, outbox string) wa.Outgoing {
		t.Helper()
		entries, err := os.ReadDir(outbox)
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != 1 {
			t.Fatalf("outbox entries=%v, want exactly one", entries)
		}
		data, err := os.ReadFile(filepath.Join(outbox, entries[0].Name()))
		if err != nil {
			t.Fatal(err)
		}
		var record wa.Outgoing
		if err := json.Unmarshal(data, &record); err != nil {
			t.Fatal(err)
		}
		return record
	}

	t.Run("states the sender outside tmux", func(t *testing.T) {
		clearEnv(t)
		outbox := t.TempDir()
		a, errOutput := newApp(t, outbox)
		if err := a.whatsapp([]string{"send", "--from", script, "4 yeni cevap"}); err != nil {
			t.Fatal(err)
		}
		record := queued(t, outbox)
		if record.Agent != script || record.Text != "["+script+"] 4 yeni cevap" {
			t.Fatalf("record=%+v", record)
		}
		if warning := readTestOutput(t, errOutput); warning != "" {
			t.Fatalf("stated sender still warned: %q", warning)
		}
	})

	t.Run("refused inside a pane", func(t *testing.T) {
		clearEnv(t)
		t.Setenv("TMUX", "/tmp/tmux-0/default,4242,0")
		outbox := t.TempDir()
		a, _ := newApp(t, outbox)
		err := a.whatsapp([]string{"send", "--from", "server-main", "hello"})
		if err == nil || !strings.Contains(err.Error(), "not accepted inside tmux") {
			t.Fatalf("error=%v", err)
		}
		if entries, _ := os.ReadDir(outbox); len(entries) != 0 {
			t.Fatalf("a refused send still queued %v", entries)
		}
	})

	for name, value := range map[string]string{
		"bracket":         "[server-main] hi",
		"closing bracket": "cron]",
		"newline":         "cron\nserver-main",
		"control byte":    "cron\x01",
	} {
		t.Run("rejects "+name, func(t *testing.T) {
			clearEnv(t)
			outbox := t.TempDir()
			a, _ := newApp(t, outbox)
			if err := a.whatsapp([]string{"send", "--from", value, "hello"}); err == nil {
				t.Fatalf("--from %q accepted", value)
			}
			if entries, _ := os.ReadDir(outbox); len(entries) != 0 {
				t.Fatalf("a rejected send still queued %v", entries)
			}
		})
	}

	t.Run("an unattributable send confesses instead of signing as server-main", func(t *testing.T) {
		clearEnv(t)
		t.Setenv("USER", "root")
		outbox := t.TempDir()
		a, errOutput := newApp(t, outbox)
		if err := a.whatsapp([]string{"send", "ping"}); err != nil {
			t.Fatal(err)
		}
		record := queued(t, outbox)
		if record.Agent == "server-main" {
			t.Fatalf("server-main signed a message it did not send: %+v", record)
		}
		if !strings.Contains(record.Agent, identity.InferMark) && record.Agent != identity.Unknown {
			t.Fatalf("label %q neither confesses a guess nor says unknown", record.Agent)
		}
		if validAgentName(record.Agent) {
			t.Fatalf("label %q is agent-shaped: it can be mistaken for a real sender", record.Agent)
		}
		if warning := readTestOutput(t, errOutput); !strings.Contains(warning, "sender not established") {
			t.Fatalf("no warning for an unattributable send: %q", warning)
		}
	})
}

func TestRemoteAttachCommandUsesMoshByDefault(t *testing.T) {
	binDir := t.TempDir()
	writeTestExecutable(t, binDir, "mosh")
	writeTestExecutable(t, binDir, "ssh")
	t.Setenv("PATH", binDir)

	got, err := remoteAttachCommand(connectConfig{Remote: "ops@example.com", Method: "mosh"}, "server-main")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"mosh", "ops@example.com", "--", "tmux", "attach", "-t", "server-main"}
	if !reflect.DeepEqual(got.Args, want) {
		t.Fatalf("args=%v, want %v", got.Args, want)
	}
}

func TestRemoteAttachCommandFallsBackToSSH(t *testing.T) {
	binDir := t.TempDir()
	writeTestExecutable(t, binDir, "ssh")
	t.Setenv("PATH", binDir)

	got, err := remoteAttachCommand(connectConfig{Remote: "ops@example.com", Method: "mosh"}, "server-main")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"ssh", "-t", "ops@example.com", "tmux", "attach", "-t", "server-main"}
	if !reflect.DeepEqual(got.Args, want) {
		t.Fatalf("args=%v, want %v", got.Args, want)
	}
}

func writeTestExecutable(t *testing.T, dir, name string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestTranslatePolicyOutput(t *testing.T) {
	input := "usage: 7g %42, fable %10, 5s %8, reset 12.5s, E %25, fresh (1s)\noverride kaldirildi\n"
	want := "usage: 7d %42, fable %10, 5h %8, reset 12.5h, E %25, fresh (1s)\noverride cleared\n"
	if got := translatePolicyOutput(input); got != want {
		t.Fatalf("translated output=%q, want %q", got, want)
	}
}

func TestAgentsByWorktreeMatchesPaneAndSessionDirectories(t *testing.T) {
	root := filepath.Join(t.TempDir(), "repo", ".worktrees")
	entries := []worktree.Info{
		{Path: filepath.Join(root, "shop"), Branch: "shop/dev"},
		{Path: filepath.Join(root, "builder"), Branch: "builder/dev"},
	}
	locations := []bptmux.Location{
		{Session: "shop-agent", CurrentDir: filepath.Join(root, "shop", "cmd")},
		{Session: "builder-agent", CurrentDir: "/tmp", StartDir: filepath.Join(root, "builder")},
		{Session: "unrelated-agent", CurrentDir: filepath.Join(root, "shop-old")},
		{Session: "shop-agent", StartDir: filepath.Join(root, "shop")},
	}

	got := agentsByWorktree(entries, locations)
	if want := []string{"shop-agent"}; !reflect.DeepEqual(got[entries[0].Path], want) {
		t.Fatalf("shop agents=%v, want %v", got[entries[0].Path], want)
	}
	if want := []string{"builder-agent"}; !reflect.DeepEqual(got[entries[1].Path], want) {
		t.Fatalf("builder agents=%v, want %v", got[entries[1].Path], want)
	}
}

// A typo'd --parent would bury the new agent under a name nobody reads, so it
// must fail before a tmux session is started or the agentbook is written.
func TestOpenRejectsUnknownParentBeforeAnythingHappens(t *testing.T) {
	t.Setenv("AGENTBOOK", "")
	dir := t.TempDir()
	bookPath := filepath.Join(dir, "agentbook.json")
	original := "{\"agents\":[{\"name\":\"root\",\"folder\":\"" + dir + "\"}]}\n"
	if err := os.WriteFile(bookPath, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	fleet := book.Fleet{
		Root:    "root",
		Order:   []string{"root"},
		Agents:  map[string]book.Agent{"root": {Name: "root"}},
		Parents: map[string]string{"root": ""},
	}
	out := testOutput(t)
	a := &app{
		ctx:    context.Background(),
		config: bpconfig.Config{Agentbooks: []string{bookPath}},
		// Deliberately unusable: reaching tmux at all would be the bug.
		tmux:      &bptmux.Client{Bin: filepath.Join(dir, "no-such-tmux")},
		out:       out,
		err:       testOutput(t),
		loadFleet: func() (book.Fleet, map[string]book.State, error) { return fleet, nil, nil },
	}

	err := a.open([]string{"ghost", dir, "--parent", "nobody"})
	if err == nil || !strings.Contains(err.Error(), "unknown parent: nobody") {
		t.Fatalf("error=%v, want unknown parent", err)
	}
	after, readErr := os.ReadFile(bookPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(after) != original {
		t.Fatalf("agentbook was written:\n%s", after)
	}
	if got := readTestOutput(t, out); got != "" {
		t.Fatalf("output=%q, want nothing", got)
	}
}

func TestOpenFlagsRequireValues(t *testing.T) {
	a := &app{ctx: context.Background()}
	cases := []struct {
		args []string
		want string
	}{
		{[]string{"ghost", "/tmp", "--parent"}, "--parent requires an agent name"},
		{[]string{"ghost", "/tmp", "--role"}, "--role requires a role text"},
		{[]string{"ghost", "/tmp", "--nope"}, "unknown open option: --nope"},
	}
	for _, tc := range cases {
		if err := a.open(tc.args); err == nil || err.Error() != tc.want {
			t.Errorf("open(%v) error=%v, want %q", tc.args, err, tc.want)
		}
	}
}

// openTestTmux stands in for tmux during `bp open`. It differs from fakeTmux in
// the two things the open flow turns on: has-session answers from a marker file
// that new-session creates, so the fake gains and reports real session state,
// and new-session copies the agentbook aside first, which is the only way to see
// what the book said at the moment the session came up.
//
// newSessionFails makes new-session refuse without leaving a session behind.
func openTestTmux(t *testing.T, bookPath, pane string, newSessionFails bool) (*bptmux.Client, string, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "tmux")
	session := filepath.Join(dir, "session")
	snapshot := filepath.Join(dir, "book-at-new-session.json")
	newSession := "cp " + bookPath + " " + snapshot + "; touch " + session + "; exit 0"
	if newSessionFails {
		newSession = "cp " + bookPath + " " + snapshot + "; exit 1"
	}
	script := "#!/bin/sh\ncase \"$1\" in\n" +
		"has-session) if [ -f " + session + " ]; then exit 0; fi; exit 1 ;;\n" +
		"new-session) " + newSession + " ;;\n" +
		"capture-pane) printf '" + pane + "\\n' ;;\n" +
		"display-message) echo claude ;;\n" +
		"*) : ;;\nesac\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return &bptmux.Client{Bin: path, Sleep: func(time.Duration) {}, Now: time.Now}, session, snapshot
}

func openTestBook(t *testing.T, dir, status string) string {
	t.Helper()
	t.Setenv("AGENTBOOK", "")
	path := filepath.Join(dir, "agentbook.json")
	content := fmt.Sprintf("{\"agents\":[{\"name\":\"server-main\",\"folder\":\"%s\"},"+
		"{\"name\":\"ghost\",\"folder\":\"%s\",\"parent\":\"server-main\",\"status\":\"%s\"}]}\n", dir, dir, status)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func bookStatus(t *testing.T, path, name string) string {
	t.Helper()
	file, err := book.Load(path)
	if err != nil {
		t.Fatalf("load %s: %v", path, err)
	}
	for _, agent := range file.Agents {
		if agent.Name == name {
			return agent.Status
		}
	}
	t.Fatalf("%s is not in %s", name, path)
	return ""
}

func openTestApp(t *testing.T, bookPath string, client *bptmux.Client) *app {
	t.Helper()
	return &app{
		ctx:    context.Background(),
		config: bpconfig.Config{Agentbooks: []string{bookPath}, StateDir: t.TempDir()},
		tmux:   client,
		out:    testOutput(t),
		err:    testOutput(t),
	}
}

// The gap between "tmux session is up" and "the book knows" is where two agents
// were lost: the book still said closed, so bp status hid a running agent. The
// book must say "opening" for the whole of that gap, and only then "open".
func TestOpenRecordsOpeningBeforeTheSessionExists(t *testing.T) {
	dir := t.TempDir()
	bookPath := openTestBook(t, dir, "closed")
	client, session, snapshot := openTestTmux(t, bookPath, "  ›  ", false)
	a := openTestApp(t, bookPath, client)

	if err := a.open([]string{"ghost", dir, "--codex", "--no-prompt"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(session); err != nil {
		t.Fatalf("no session was started: %v", err)
	}
	if got := bookStatus(t, snapshot, "ghost"); got != "opening" {
		t.Fatalf("book said %q when the session came up, want opening", got)
	}
	if got := bookStatus(t, bookPath, "ghost"); got != "open" {
		t.Fatalf("book=%q after a finished open, want open", got)
	}
}

// A refused open must not leave "opening" behind forever: with tmux reachable and
// no session to show for it, "closed" is a fact again.
func TestOpenRollsBackToClosedWhenTmuxRefuses(t *testing.T) {
	dir := t.TempDir()
	bookPath := openTestBook(t, dir, "closed")
	client, session, snapshot := openTestTmux(t, bookPath, "  ›  ", true)
	a := openTestApp(t, bookPath, client)

	if err := a.open([]string{"ghost", dir, "--codex", "--no-prompt"}); err == nil {
		t.Fatal("open succeeded although new-session failed")
	}
	if _, err := os.Stat(session); err == nil {
		t.Fatal("a session was started after all")
	}
	// The snapshot proves the rollback undid a real write rather than nothing.
	if got := bookStatus(t, snapshot, "ghost"); got != "opening" {
		t.Fatalf("book=%q at new-session time, want opening", got)
	}
	if got := bookStatus(t, bookPath, "ghost"); got != "closed" {
		t.Fatalf("book=%q after a refused open, want closed", got)
	}
}

// The measured failure: `bp open` is interrupted after the session exists. The
// entry has to stay "opening" — the honest "I do not know" — and bp status has to
// show it, because "closed" here is the lie that hid a running agent for weeks.
func TestInterruptedOpenLeavesOpeningVisibleInStatus(t *testing.T) {
	dir := t.TempDir()
	bookPath := openTestBook(t, dir, "closed")
	// A pane that never looks ready keeps Open in its wait loop, where the
	// interruption lands — the same place a timeout or a Ctrl-C would.
	client, session, _ := openTestTmux(t, bookPath, "  starting up  ", false)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client.Sleep = func(time.Duration) { cancel() }
	a := openTestApp(t, bookPath, client)
	a.ctx = ctx

	if err := a.open([]string{"ghost", dir, "--codex", "--no-prompt"}); err == nil {
		t.Fatal("interrupted open reported success")
	}
	if _, err := os.Stat(session); err != nil {
		t.Fatalf("the session should have survived the interruption: %v", err)
	}
	if got := bookStatus(t, bookPath, "ghost"); got != "opening" {
		t.Fatalf("book=%q after an interrupted open, want opening", got)
	}

	fleet, err := book.LoadFleet([]string{bookPath})
	if err != nil {
		t.Fatal(err)
	}
	out := testOutput(t)
	status := &app{
		out: out,
		loadFleet: func() (book.Fleet, map[string]book.State, error) {
			return fleet, map[string]book.State{"ghost": {Alive: true}}, nil
		},
		loadCache: func(map[string]string) map[string]bpcache.State { return nil },
	}
	if err := status.status(nil); err != nil {
		t.Fatal(err)
	}
	// Plain "opening": an admitted intermediate state is not a mismatch.
	want := fmt.Sprintf("%-24s %-10s %-20s %-10s %-10s%s\n", "ghost", "idle", "-", "-", "opening", "")
	if got := readTestOutput(t, out); !strings.Contains(got, want) {
		t.Fatalf("status output:\n%s\nwant line:\n%q", got, want)
	}
}

func mismatchStatusApp(t *testing.T) *app {
	t.Helper()
	fleet := book.Fleet{
		Root:  "server-main",
		Order: []string{"server-main", "ghost-running", "ghost-gone", "half-open"},
		Agents: map[string]book.Agent{
			"server-main": {Name: "server-main", Status: "open"},
			// The kitap-manufacturing case: alive for weeks behind a closed entry.
			"ghost-running": {Name: "ghost-running", Status: "closed"},
			"ghost-gone":    {Name: "ghost-gone", Status: "open"},
			"half-open":     {Name: "half-open", Status: "opening"},
		},
		Parents: map[string]string{"server-main": "", "ghost-running": "server-main", "ghost-gone": "server-main", "half-open": "server-main"},
	}
	states := map[string]book.State{
		"server-main":   {Alive: true},
		"ghost-running": {Alive: true},
		"half-open":     {Alive: true},
	}
	return &app{
		out:       testOutput(t),
		loadFleet: func() (book.Fleet, map[string]book.State, error) { return fleet, states, nil },
		loadCache: func(map[string]string) map[string]bpcache.State { return nil },
	}
}

// Both directions of tmux-versus-book disagreement have to be visible on the row
// itself; a reader who has to cross-check two columns will not.
func TestStatusMarksBothDirectionsOfMismatch(t *testing.T) {
	a := mismatchStatusApp(t)
	if err := a.status(nil); err != nil {
		t.Fatal(err)
	}
	got := readTestOutput(t, a.out)
	for _, want := range []string{
		fmt.Sprintf("%-24s %-10s %-20s %-10s %-10s%s\n", "ghost-gone", "closed", "-", "-", "open", " !TMUX YOK"),
		fmt.Sprintf("%-24s %-10s %-20s %-10s %-10s%s\n", "ghost-running", "idle", "-", "-", "closed", " !TMUX ACIK"),
		fmt.Sprintf("%-24s %-10s %-20s %-10s %-10s%s\n", "half-open", "idle", "-", "-", "opening", ""),
		fmt.Sprintf("%-24s %-10s %-20s %-10s %-10s%s\n", "server-main", "idle", "-", "-", "open", ""),
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("status output:\n%s\nwant line:\n%q", got, want)
		}
	}
}

// Scripts read --json, so the alarm has to reach them too: the field appears only
// when the two sides disagree, which is what makes its presence meaningful.
func TestStatusJSONReportsMismatch(t *testing.T) {
	a := mismatchStatusApp(t)
	if err := a.status([]string{"--json"}); err != nil {
		t.Fatal(err)
	}
	var report struct {
		Agents []map[string]any `json:"agents"`
	}
	raw := readTestOutput(t, a.out)
	if err := json.Unmarshal([]byte(raw), &report); err != nil {
		t.Fatalf("not one JSON object: %v\n%s", err, raw)
	}
	want := map[string]string{
		"ghost-gone":    "tmux-closed-book-open",
		"ghost-running": "tmux-open-book-closed",
	}
	seen := map[string]string{}
	for _, agent := range report.Agents {
		name, _ := agent["name"].(string)
		if mismatch, ok := agent["mismatch"]; ok {
			seen[name], _ = mismatch.(string)
		}
	}
	if !reflect.DeepEqual(seen, want) {
		t.Fatalf("mismatch fields=%v, want %v", seen, want)
	}
}

// fakeTmux writes a shell script that stands in for the tmux binary, so delivery
// can be exercised end to end (pane checks, queue records, printed lines) without
// ever touching a live session. Every pane capture returns paneScript's output.
func fakeTmux(t *testing.T, paneScript string) (*bptmux.Client, func() string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "tmux")
	log := filepath.Join(dir, "calls")
	script := "#!/bin/sh\necho \"$1\" >> " + log + "\ncase \"$1\" in\n" +
		"has-session) exit 0 ;;\n" +
		"display-message) echo claude ;;\n" +
		"capture-pane) " + paneScript + " ;;\n" +
		"list-clients) : ;;\n" +
		"load-buffer) cat > " + filepath.Join(dir, "buffer") + " ;;\n" +
		"*) : ;;\nesac\n"
	if err := os.WriteFile(path, []byte(script), 0755); err != nil {
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

func queuedMessages(t *testing.T, root string) []string {
	t.Helper()
	entries, err := filepath.Glob(filepath.Join(root, "pending", "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	return entries
}

// TestMessageDeliveryOutcomes pins the three user-visible outcomes of bp msg and
// the queueing rule behind each: a proven failure is queued (retried, traceable),
// an unverified send is NOT (it may have landed; a retry would duplicate it).
func TestMessageDeliveryOutcomes(t *testing.T) {
	t.Setenv("AGENT", "ada")
	t.Setenv("TMUX", "")

	t.Run("verified prints sent", func(t *testing.T) {
		out := testOutput(t)
		a := &app{
			config:        bpconfig.Config{StateDir: t.TempDir()},
			out:           out,
			sessionExists: func(string) bool { return true },
			deliverMessage: func(string, string, string) (bool, string, error) {
				return false, "", nil
			},
		}
		trustedSenderFixture(a)
		if err := a.message([]string{"alp", "hello"}); err != nil {
			t.Fatal(err)
		}
		// The RESULT line is a STABLE CONTRACT (server-whatsapp branches on it
		// instead of regexing the Turkish prose): last line, fixed keys, fixed
		// verdict words. Changing it breaks the bridge silently.
		if got := readTestOutput(t, out); got != "sent\nRESULT=delivered\n" {
			t.Fatalf("output=%q", got)
		}
	})

	t.Run("proven failure is queued and named", func(t *testing.T) {
		// The 2026-08-01 pane: an expired login. Nothing may be pasted, and the
		// message has to end up in the queue with a record behind it.
		stateDir := t.TempDir()
		msgqRoot := filepath.Join(stateDir, "msgq")
		out := testOutput(t)
		a := &app{
			ctx:           context.Background(),
			config:        bpconfig.Config{StateDir: stateDir},
			queue:         msgq.New(msgqRoot),
			out:           out,
			sessionExists: func(string) bool { return true },
		}
		// Real capture shape: transcript, empty composer, box border, then the
		// status footer carrying the banner.
		tmuxClient, calls := fakeTmux(t, `printf '  earlier output\n❯  \n──────────\n  ⏵⏵ bypass permissions on   ● Login expired · Please run /login\n'`)
		a.tmux = tmuxClient
		trustedSenderFixture(a)
		if err := a.message([]string{"alp", "the whole brief"}); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(calls(), "paste-buffer") || strings.Contains(calls(), "load-buffer") {
			t.Fatalf("pasted into an expired-login pane:\n%s", calls())
		}
		got := readTestOutput(t, out)
		if !strings.HasPrefix(got, "GONDERILEMEDI: alp — ") || !strings.Contains(got, "Login expired") ||
			!strings.Contains(got, "kuyruga alindi (channel: q") {
			t.Fatalf("output=%q", got)
		}
		records := queuedMessages(t, msgqRoot)
		if len(records) != 1 {
			t.Fatalf("queue records=%v, want exactly one", records)
		}
		data, err := os.ReadFile(records[0])
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(data), "the whole brief") {
			t.Fatalf("queued record does not hold the message: %s", data)
		}
	})

	t.Run("unverified is reported and never queued", func(t *testing.T) {
		// Composer empty both before and after the paste: nothing confirms the
		// message landed, nothing contradicts it either.
		stateDir := t.TempDir()
		msgqRoot := filepath.Join(stateDir, "msgq")
		out := testOutput(t)
		a := &app{
			ctx:           context.Background(),
			config:        bpconfig.Config{StateDir: stateDir},
			queue:         msgq.New(msgqRoot),
			out:           out,
			sessionExists: func(string) bool { return true },
		}
		tmuxClient, calls := fakeTmux(t, `printf '❯  \n──────────\n'`)
		a.tmux = tmuxClient
		trustedSenderFixture(a)
		err := a.message([]string{"alp", "the whole brief"})
		if !strings.Contains(calls(), "paste-buffer") {
			t.Fatalf("the unverified case must be a real paste:\n%s", calls())
		}
		if !errors.Is(err, errReported) {
			t.Fatalf("err=%v, want errReported (non-zero exit, no duplicate line)", err)
		}
		want := "gonderildi ama DOGRULANAMADI: alp — pane'de mesaj gorulemedi, tekrar gondermeden once bp peek alp ile bak\nRESULT=unverified\n"
		if got := readTestOutput(t, out); got != want {
			t.Fatalf("output=%q, want %q", got, want)
		}
		if records := queuedMessages(t, msgqRoot); len(records) != 0 {
			t.Fatalf("unverified send was queued (would duplicate): %v", records)
		}
	})

	t.Run("unverified but witnessable is handed to the transcript", func(t *testing.T) {
		// Same doubt, but this message is long enough for the transcript witness to
		// identify. The doubt is therefore given to something that can settle it: a
		// record marked never-paste-again. It cannot duplicate the message (nothing
		// will ever paste it) and it cannot vanish in silence either.
		stateDir := t.TempDir()
		msgqRoot := filepath.Join(stateDir, "msgq")
		out := testOutput(t)
		a := &app{
			ctx:           context.Background(),
			config:        bpconfig.Config{StateDir: stateDir},
			queue:         msgq.New(msgqRoot),
			out:           out,
			sessionExists: func(string) bool { return true },
		}
		tmuxClient, _ := fakeTmux(t, `printf '❯  \n──────────\n'`)
		a.tmux = tmuxClient
		brief := "roadmap incelemesi: hedef sistemi bolumunu bugun bitirelim"
		trustedSenderFixture(a)
		if err := a.message([]string{"alp", brief}); !errors.Is(err, errReported) {
			t.Fatalf("err=%v, want errReported", err)
		}
		got := readTestOutput(t, out)
		if !strings.HasPrefix(got, "TESLIMAT BELIRSIZ: alp") || !strings.Contains(got, "transcript tanigi") {
			t.Fatalf("output=%q", got)
		}
		records := queuedMessages(t, msgqRoot)
		if len(records) != 1 {
			t.Fatalf("expected one held record, got %v", records)
		}
		data, err := os.ReadFile(records[0])
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(data), `"noRepaste":true`) {
			t.Fatalf("the held record may be pasted again: %s", data)
		}
	})
}

func TestDeliveryTallyBucketsUnverified(t *testing.T) {
	var tally deliveryTally
	// Unverified: neither a delivery nor an error — its own bucket.
	if tally.record("alpha", false, "", fmt.Errorf("wrap: %w", bptmux.ErrUnverified)) {
		t.Fatal("unverified must not count as a delivery")
	}
	// A proven failure arrives already queued: it counts, with its channel.
	if !tally.record("beta", true, "q2", fmt.Errorf("%w: login", bptmux.ErrNotReady)) {
		t.Fatal("queued proven failure should count as handled")
	}
	if tally.sent != 0 || len(tally.channels) != 1 || tally.channels[0] != "q2" {
		t.Fatalf("sent=%d channels=%v", tally.sent, tally.channels)
	}
	if len(tally.unverified) != 1 || tally.unverified[0] != "alpha" {
		t.Fatalf("unverified=%v", tally.unverified)
	}
	if len(tally.errs) != 0 {
		t.Fatalf("errs=%v, want none", tally.errs)
	}
	out := testOutput(t)
	tally.report(out)
	if got := readTestOutput(t, out); !strings.Contains(got, "dogrulanamadi: 1 (alpha)") {
		t.Fatalf("summary=%q", got)
	}
	// A clean tally prints nothing, so existing summaries stay byte-identical.
	clean := testOutput(t)
	(&deliveryTally{}).report(clean)
	if got := readTestOutput(t, clean); got != "" {
		t.Fatalf("clean summary=%q, want empty", got)
	}
}

// --- deliver against a scripted tmux --------------------------------------

// deliverPane wraps composer text in the structure of a live Claude Code pane
// (two borders, prompt marker on the first row, the "-- INSERT --" status footer).
// Synthetic text only: real pane content is private mail.
func deliverPane(text string) string {
	// The TOP border carries the agent name on live panes (measured on all 29
	// sessions, 2026-08-11); only the bottom one is a pure rule. A fixture with two
	// pure borders is not what any real pane draws.
	top := "──────────────────────────────────── worker ──"
	border := "──────────────────────────────────────────────"
	row := "❯   "
	if text != "" {
		row = "❯ " + text
	}
	return strings.Join([]string{
		"  agent: onceki turdan kalan cikti",
		top,
		row,
		border,
		"  -- INSERT -- ⏵⏵ bypass permissions on (shift+tab to cycle)",
		"",
	}, "\n")
}

// scriptedPanes writes a stand-in tmux that answers capture-pane with the given
// panes in order (the last one repeats), reports a claude pane, and logs every
// call so a test can prove which keys were and were not sent.
func scriptedPanes(t *testing.T, panes ...string) (*bptmux.Client, func() string) {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "calls")
	counter := filepath.Join(dir, "counter")
	for i, pane := range panes {
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("pane-%d", i+1)), []byte(pane), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	script := "#!/bin/sh\n" +
		"echo \"$*\" >> " + log + "\n" +
		"case \"$1\" in\n" +
		"has-session) exit 0 ;;\n" +
		"display-message) echo claude ;;\n" +
		"capture-pane)\n" +
		"  n=$(cat " + counter + " 2>/dev/null || echo 0); n=$((n+1)); echo $n > " + counter + "\n" +
		"  if [ -f " + dir + "/pane-$n ]; then cat " + dir + "/pane-$n; else cat " + dir + fmt.Sprintf("/pane-%d", len(panes)) + "; fi ;;\n" +
		"list-clients) : ;;\n" +
		"load-buffer) cat > /dev/null ;;\n" +
		"*) : ;;\n" +
		"esac\n"
	path := filepath.Join(dir, "tmux")
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

func deliverApp(t *testing.T, client *bptmux.Client) *app {
	t.Helper()
	return &app{
		ctx:    context.Background(),
		config: bpconfig.Config{StateDir: t.TempDir()},
		tmux:   client,
		queue:  msgq.New(t.TempDir()),
		out:    testOutput(t),
		err:    testOutput(t),
	}
}

// The deadlock, from the CLI side. A message bp queued earlier is still hanging
// in the composer because its Enter never registered. The old gate read that as
// "the agent is busy" and queued the new message behind it — for four days. Now
// the hanging text is recognised as ours, submitted, and its queue record closed;
// the new message is delivered in the same pass and NOTHING is queued.
func TestDeliverFinishesAHangingQueuedPasteInsteadOfQueueingBehindIt(t *testing.T) {
	hanging := "[ada] onceki kuyruk mesaji: bar chip renklerini kontrol eder misin"
	fresh := "[server-main] yeni mesaj: roadmap hedef sistemi bolumunu bugun bitirelim"
	client, calls := scriptedPanes(t,
		deliverPane(hanging), // deliver's own look at the pane
		deliverPane(hanging), // Send's pre-send gate: ours, whole -> Enter
		deliverPane(""),      // submitted
		deliverPane(""),      // readyToSend pass 1
		deliverPane(""),      // readyToSend pass 2
		deliverPane(fresh),   // our paste landed whole -> Enter
		deliverPane(""),      // submitted
	)
	a := deliverApp(t, client)
	channel, err := a.queue.Enqueue("worker", "ada", hanging)
	if err != nil {
		t.Fatal(err)
	}

	queued, _, err := a.deliver("worker", "server-main", fresh)
	if err != nil {
		t.Fatalf("err=%v, want a clean delivery", err)
	}
	if queued {
		t.Fatal("the message was queued behind our own hanging paste again")
	}
	if left := a.queue.PendingFor("worker"); len(left) != 0 {
		t.Fatalf("queue still holds %d record(s) for worker: %v", len(left), left)
	}
	status, err := a.queue.Status(channel)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(status, "DELIVERED") {
		t.Fatalf("the hanging record was not closed: %q", status)
	}
	log := calls()
	if got := strings.Count(log, "send-keys -t =worker: Enter"); got != 2 {
		t.Fatalf("expected 2 Enter presses (hanging paste, then ours), got %d:\n%s", got, log)
	}
	if got := strings.Count(log, "paste-buffer"); got != 1 {
		t.Fatalf("expected exactly 1 paste (the hanging text is never re-pasted), got %d:\n%s", got, log)
	}
	if strings.Contains(log, "Escape") {
		t.Fatalf("Escape was sent to an agent pane:\n%s", log)
	}
}

// The other side of the same gate: text that is NOT ours is left completely
// alone and the message is queued, exactly as before.
func TestDeliverQueuesBehindSomeoneElsesTypingWithoutTouchingThePane(t *testing.T) {
	client, calls := scriptedPanes(t, deliverPane("kendi yarim kalan sorum burada duruyor"))
	a := deliverApp(t, client)
	queued, channel, err := a.deliver("worker", "server-main", "[server-main] roadmap incelemesi bugun bitmeli")
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if !queued || channel == "" {
		t.Fatalf("queued=%v channel=%q, want the message queued", queued, channel)
	}
	log := calls()
	if strings.Contains(log, "send-keys") || strings.Contains(log, "paste-buffer") {
		t.Fatalf("a composer holding someone's text was touched:\n%s", log)
	}
}

// A queued message must say WHY it is waiting, in both places an operator looks.
// Without this, a target blocked by an unreadable paste (or by a fragment too
// short to identify) is indistinguishable from an agent that is merely working —
// which is how one message waited four days under "is still busy".
func TestQueueListAndStatusNameTheReasonAMessageWaits(t *testing.T) {
	client, calls := scriptedPanes(t, deliverPane("[Pasted text #1 +12 lines]"))
	a := deliverApp(t, client)
	queued, channel, err := a.deliver("worker", "server-main", "[server-main] roadmap incelemesi bugun bitmeli")
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if !queued {
		t.Fatal("a composer holding an unreadable paste must queue the message")
	}
	if log := calls(); strings.Contains(log, "send-keys") || strings.Contains(log, "paste-buffer") {
		t.Fatalf("an unreadable paste was touched:\n%s", log)
	}

	if err := a.queueList(nil); err != nil {
		t.Fatal(err)
	}
	list := readTestOutput(t, a.out)
	if !strings.Contains(list, bptmux.BlockedByPasteChip) || !strings.Contains(list, "bp peek worker") {
		t.Fatalf("bp q does not name the reason:\n%s", list)
	}

	a.out = testOutput(t)
	if err := a.queueStatus([]string{channel}); err != nil {
		t.Fatal(err)
	}
	status := readTestOutput(t, a.out)
	if !strings.Contains(status, bptmux.BlockedByPasteChip) {
		t.Fatalf("bp qstat does not name the reason:\n%s", status)
	}
	if strings.Contains(status, "is still busy") {
		t.Fatalf("bp qstat still reports a bare busy state:\n%s", status)
	}
}

// A provable non-delivery names its own cause instead of the composer's state.
func TestQueueReasonNamesAnExpiredLogin(t *testing.T) {
	expired := strings.Replace(deliverPane(""),
		"  -- INSERT -- ⏵⏵ bypass permissions on (shift+tab to cycle)",
		"  -- INSERT -- ⏵⏵ bypass permissions on   ● Login expired · Please run /login", 1)
	client, _ := scriptedPanes(t, expired)
	a := deliverApp(t, client)
	_, channel, err := a.deliver("worker", "server-main", "[server-main] roadmap incelemesi bugun bitmeli")
	if !errors.Is(err, bptmux.ErrNotReady) {
		t.Fatalf("err=%v, want ErrNotReady", err)
	}
	if err := a.queueStatus([]string{channel}); err != nil {
		t.Fatal(err)
	}
	if status := readTestOutput(t, a.out); !strings.Contains(status, "Login expired") {
		t.Fatalf("bp qstat does not name the expired login:\n%s", status)
	}
}

// --- duplicate guard and the pane lock, from the CLI side ---------------------

func TestMessageRefusesADuplicateThatIsStillInFlight(t *testing.T) {
	// The measured retry burst: probot-business sent the same 441 characters to
	// op-main three times in 33 seconds, because the first attempt could only say
	// "TESLIMAT BELIRSIZ". Every copy landed. The second send is refused here — and
	// the refusal carries the record's live status, because a sender that cannot see
	// what happened to its message is exactly the sender that repeats it somewhere
	// bp cannot see at all.
	t.Setenv("AGENT", "ada")
	t.Setenv("TMUX", "")
	queue := msgq.New(t.TempDir())
	id, err := queue.EnqueueReason("alp", "ada", "[ada] ayni metin", bptmux.BlockedByBusyPane)
	if err != nil {
		t.Fatal(err)
	}
	out := testOutput(t)
	delivered := 0
	a := &app{
		ctx:           context.Background(),
		config:        bpconfig.Config{StateDir: t.TempDir()},
		queue:         queue,
		out:           out,
		sessionExists: func(string) bool { return true },
		deliverMessage: func(string, string, string) (bool, string, error) {
			delivered++
			return false, "", nil
		},
	}
	trustedSenderFixture(a)
	if err := a.message([]string{"alp", "ayni metin"}); err != nil {
		t.Fatal(err)
	}
	if delivered != 0 {
		t.Fatal("an identical message already in flight was delivered a second time")
	}
	report := readTestOutput(t, out)
	for _, want := range []string{"AYNI METIN ZATEN YOLDA", id, "Durum:", "bp qcancel " + id} {
		if !strings.Contains(report, want) {
			t.Fatalf("output=%q, want it to contain %q", report, want)
		}
	}
	// A different message is not touched by the guard.
	a.out = testOutput(t)
	trustedSenderFixture(a)
	if err := a.message([]string{"alp", "bambaska bir mesaj"}); err != nil {
		t.Fatal(err)
	}
	if delivered != 1 {
		t.Fatalf("a different message was blocked: %d deliveries", delivered)
	}
}

func TestMessageResendsAfterTheDuplicateWindow(t *testing.T) {
	// The guard is a window, not a ban: "say it again, it never arrived" has to keep
	// working, or the operator loses a channel.
	t.Setenv("AGENT", "ada")
	t.Setenv("TMUX", "")
	queue := msgq.New(t.TempDir())
	queue.Now = func() time.Time { return time.Now().Add(-11 * time.Minute) }
	if _, err := queue.EnqueueReason("alp", "ada", "[ada] ayni metin", bptmux.BlockedByBusyPane); err != nil {
		t.Fatal(err)
	}
	queue.Now = time.Now
	delivered := 0
	a := &app{
		ctx:           context.Background(),
		config:        bpconfig.Config{StateDir: t.TempDir()},
		queue:         queue,
		out:           testOutput(t),
		sessionExists: func(string) bool { return true },
		deliverMessage: func(string, string, string) (bool, string, error) {
			delivered++
			return false, "", nil
		},
	}
	if err := a.message([]string{"alp", "ayni metin"}); err != nil {
		t.Fatal(err)
	}
	if delivered != 1 {
		t.Fatalf("a record older than the window blocked a resend: %d deliveries", delivered)
	}
}

func TestDeliverQueuesWhileAnotherBpHoldsThePane(t *testing.T) {
	// The merge of 2026-08-17, refused: `bp open`'s digest flush is inside the
	// critical section for this pane, so this delivery does not capture it, does not
	// paste into it, and does not press Enter on whatever is in there. It queues,
	// with a reason that names bp itself.
	defer func(previous time.Duration) { bptmux.PaneLockWait = previous }(bptmux.PaneLockWait)
	bptmux.PaneLockWait = 200 * time.Millisecond
	client, calls := scriptedPanes(t, deliverPane(""))
	a := deliverApp(t, client)
	release, err := bptmux.AcquirePaneLock(a.queue.Root, "worker")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	queued, channelID, err := a.deliver("worker", "server-main", "[server-main] kilit tutulurken gelen mesaj")
	if err != nil {
		t.Fatal(err)
	}
	if !queued || channelID == "" {
		t.Fatalf("queued=%v channel=%q", queued, channelID)
	}
	if reason := a.queue.Reason(channelID); reason != bptmux.BlockedByPaneLock {
		t.Fatalf("reason=%q, want %q", reason, bptmux.BlockedByPaneLock)
	}
	if log := calls(); strings.Contains(log, "send-keys") || strings.Contains(log, "load-buffer") {
		t.Fatalf("keys were sent into a pane another bp was holding:\n%s", log)
	}
}

// --- bp msg --force-busy ------------------------------------------------------

func TestCutForceBusyOnlyReadsTheFlagBeforeTheMessage(t *testing.T) {
	// The flag may stand before or after the target name, and NEVER inside the
	// message: an agent quoting the flag to another agent must not force itself.
	tests := []struct {
		name  string
		args  []string
		rest  []string
		force bool
	}{
		{name: "leading", args: []string{"--force-busy", "ada", "selam"}, rest: []string{"ada", "selam"}, force: true},
		{name: "after the name", args: []string{"ada", "--force-busy", "selam"}, rest: []string{"ada", "selam"}, force: true},
		{name: "inside the message", args: []string{"ada", "sunu dene:", "--force-busy"}, rest: []string{"ada", "sunu dene:", "--force-busy"}},
		{name: "absent", args: []string{"ada", "selam"}, rest: []string{"ada", "selam"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			rest, force := cutForceBusy(test.args)
			if force != test.force || strings.Join(rest, " ") != strings.Join(test.rest, " ") {
				t.Fatalf("cutForceBusy(%v)=%v,%v want %v,%v", test.args, rest, force, test.rest, test.force)
			}
		})
	}
}

// forceApp is a CLI with a real queue and no tmux: the force path must never
// need a pane of its own, because it delivers through the dispatch pass.
func forceApp(t *testing.T, out *os.File) *app {
	t.Helper()
	t.Setenv("TMUX", "")
	t.Setenv("AGENTBOOK", "")
	return &app{
		ctx:           context.Background(),
		config:        bpconfig.Config{StateDir: t.TempDir()},
		queue:         msgq.New(t.TempDir()),
		out:           out,
		err:           testOutput(t),
		sessionExists: func(string) bool { return true },
	}
}

func TestForceBusyIsRefusedForAnOrdinaryAgent(t *testing.T) {
	// The gate is habit control, not a security boundary (see allowForceBusy), and
	// this is the habit it controls: an agent deciding its own message is urgent
	// enough to interrupt a working colleague.
	t.Setenv("AGENT", "ada")
	out := testOutput(t)
	a := forceApp(t, out)
	delivered := 0
	a.deliverMessage = func(string, string, string) (bool, string, error) {
		delivered++
		return false, "", nil
	}
	err := a.message([]string{"--force-busy", "alp", "acil bir sey"})
	if err == nil || !strings.Contains(err.Error(), "force-busy tesisata ayrilmis") {
		t.Fatalf("err=%v, want the refusal", err)
	}
	if delivered != 0 {
		t.Fatal("a refused force-busy message was delivered anyway")
	}
	rows, listErr := a.queue.List()
	if listErr != nil || len(rows) != 0 {
		t.Fatalf("a refused force-busy message was queued: %v (%v)", rows, listErr)
	}
}

// The identity the bridge ACTUALLY pins is AGENT=whatsapp (bridge.js:86), not
// "wa" — the first allowlist shipped without it, and the feature would have
// looked live while every bridge call silently fell back to the ordinary queue
// (ada's pre-deploy catch, 2026-08-21).
func TestForceBusyRejectsSelfDeclaredBridgeIdentity(t *testing.T) {
	t.Setenv("AGENT", "whatsapp")
	a := forceApp(t, testOutput(t))
	if err := a.message([]string{"--force-busy", "alp", "Tuna: acil bak"}); err == nil {
		t.Fatal("self-declared bridge received force authority")
	}
	rows, err := a.queue.List()
	if err != nil || len(rows) != 0 {
		t.Fatalf("rows=%v err=%v", rows, err)
	}
}

func TestForceBusyQueuesAForcedRecordForThePlumbing(t *testing.T) {
	// The WhatsApp bridge's own path: the message becomes a FORCED queue record
	// and nothing types into a pane here. That is the whole change — the bridge
	// used to paste into busy panes itself, as a second writer.
	t.Setenv("AGENT", "wa")
	out := testOutput(t)
	a := forceApp(t, out)
	delivered := 0
	a.deliverMessage = func(string, string, string) (bool, string, error) {
		delivered++
		return false, "", nil
	}
	trustedSenderFixture(a)
	if err := a.message([]string{"--force-busy", "alp", "Tuna:", "acil bak"}); err != nil {
		t.Fatal(err)
	}
	if delivered != 0 {
		t.Fatal("the force path went through the ordinary delivery")
	}
	rows, err := a.queue.List()
	if err != nil || len(rows) != 1 {
		t.Fatalf("rows=%v err=%v", rows, err)
	}
	if !rows[0].ForceBusy || rows[0].To != "alp" || rows[0].Msg != "[wa] Tuna: acil bak" {
		t.Fatalf("record=%+v", rows[0])
	}
	report := readTestOutput(t, out)
	for _, want := range []string{"FORCE kuyrukta: alp", rows[0].ID, "bp qstat"} {
		if !strings.Contains(report, want) {
			t.Fatalf("output=%q, want it to contain %q", report, want)
		}
	}
}

func TestForceBusySaysHowManyMessagesItIsJumping(t *testing.T) {
	// Out-of-order delivery is a decision with a cost, so it is stated at the
	// moment it is taken rather than discovered later in bp q.
	t.Setenv("AGENT", "wa")
	out := testOutput(t)
	a := forceApp(t, out)
	for _, text := range []string{"[ada] birinci", "[ada] ikinci"} {
		if _, err := a.queue.EnqueueReason("alp", "ada", text, bptmux.BlockedByBusyPane); err != nil {
			t.Fatal(err)
		}
	}
	trustedSenderFixture(a)
	if err := a.message([]string{"--force-busy", "alp", "Tuna: acil bak"}); err != nil {
		t.Fatal(err)
	}
	if report := readTestOutput(t, out); !strings.Contains(report, "uyari: hedefte 2 bekleyen mesaj var") {
		t.Fatalf("output=%q, want the warning naming the messages being jumped", report)
	}
}

func TestForceBusyStillRefusesADuplicateInFlight(t *testing.T) {
	// The duplicate guard runs BEFORE the flag is honoured. A forced repeat of a
	// message already on its way is the measured 2026-08-17 burst with priority.
	t.Setenv("AGENT", "wa")
	out := testOutput(t)
	a := forceApp(t, out)
	id, err := a.queue.EnqueueReason("alp", "wa", "[wa] Tuna: acil bak", bptmux.BlockedByBusyPane)
	if err != nil {
		t.Fatal(err)
	}
	trustedSenderFixture(a)
	if err := a.message([]string{"--force-busy", "alp", "Tuna: acil bak"}); err != nil {
		t.Fatal(err)
	}
	rows, listErr := a.queue.List()
	if listErr != nil || len(rows) != 1 {
		t.Fatalf("a duplicate was queued anyway: %v (%v)", rows, listErr)
	}
	if report := readTestOutput(t, out); !strings.Contains(report, "AYNI METIN ZATEN YOLDA") || !strings.Contains(report, id) {
		t.Fatalf("output=%q", report)
	}
}

func TestForceBusyIsRefusedForAFederatedAddress(t *testing.T) {
	// A pane on another machine has a busy state this bp cannot see and a queue it
	// does not own: there is nothing here to jump.
	t.Setenv("AGENT", "wa")
	a := forceApp(t, testOutput(t))
	err := a.message([]string{"--force-busy", "ada@yigit", "acil"})
	if err == nil || !strings.Contains(err.Error(), "federe adreste calismaz") {
		t.Fatalf("err=%v, want the federated refusal", err)
	}
}

func TestForceBusyRefusesAnUnestablishedSender(t *testing.T) {
	// "server-main" is the label of last resort here: cron, a systemd unit, a root
	// shell — nothing STATED who is calling. The label alone therefore buys
	// nothing, and the gate asks for a sender that was established.
	for _, key := range []string{"TMUX", "AGENT", "SUDO_USER", "USER", "LOGNAME", "AGENTBOOK"} {
		t.Setenv(key, "")
	}
	a := forceApp(t, testOutput(t))
	if got := a.sender(); got != identity.Unknown {
		t.Fatalf("fixture is not the fallback case: sender()=%q", got)
	}
	err := a.message([]string{"--force-busy", "alp", "acil bir sey"})
	if err == nil || !strings.Contains(err.Error(), "force-busy tesisata ayrilmis") {
		t.Fatalf("err=%v, want the refusal", err)
	}
}

// A flag that works but appears in no help text is a flag nobody will use:
// --no-retitle shipped that way on 2026-09-02 and ada found it only because
// they had just asked for it. So the help block is checked against the flags
// the parsers actually accept, read from the source in this directory rather
// than from a list a new flag would not be added to.
func TestHelpListsEveryFlagTheParsersAccept(t *testing.T) {
	flagCase := regexp.MustCompile(`case "(--[a-z-]+)"`)
	found := map[string]string{}
	for _, file := range []string{"main.go", "rename.go"} {
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		text := string(data)
		for _, match := range flagCase.FindAllStringSubmatchIndex(text, -1) {
			flag := text[match[2]:match[3]]
			// A flag kept only so an old command line still parses is
			// deliberately absent from the help: documenting it would invite its
			// use. The source has to SAY so, right under the case.
			body := text[match[1]:min(match[1]+160, len(text))]
			if strings.Contains(body, "Back-compat no-op") {
				continue
			}
			found[flag] = file
		}
	}
	if len(found) < 5 {
		t.Fatalf("the flag scan found almost nothing (%v) — it has stopped measuring anything", found)
	}
	for flag, file := range found {
		if !strings.Contains(usage, flag) {
			t.Errorf("%s is accepted in %s but appears nowhere in `bp help`", flag, file)
		}
	}
}

// Queue/policy tests inject a verified main-agent identity. AGENT is only the
// fixture's selector here; production does not authenticate from that variable.
func trustedSenderFixture(a *app) {
	a.resolveSender = func() identity.Identity {
		return identity.Identity{Label: os.Getenv("AGENT"), Certain: true, Source: "tmux"}
	}
}
