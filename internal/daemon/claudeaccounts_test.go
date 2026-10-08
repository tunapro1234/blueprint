package daemon

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"blueprint/internal/claudeacct"
	"blueprint/internal/config"
)

const daemonTokenMarker = "SECRETTOKEN"

func TestClaudeAccountAutoLogsAndNotifiesOncePerSwitch(t *testing.T) {
	root := t.TempDir()
	configHome := filepath.Join(root, "claude")
	env := claudeacct.Env{
		Getenv:   func(key string) string { return map[string]string{"CLAUDE_CONFIG_DIR": configHome}[key] },
		UserHome: func() (string, error) { return filepath.Join(root, "home"), nil },
	}
	token := func(n int) string { return fmt.Sprintf("%s-access-%d", daemonTokenMarker, n) }
	var mu sync.Mutex
	usage := map[string][2]float64{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		u, ok := usage[strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")]
		if r.URL.Path != "/usage" || !ok {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		fmt.Fprintf(w, `{"five_hour":{"utilization":%g},"seven_day":{"utilization":%g}}`, u[0], u[1])
	}))
	t.Cleanup(server.Close)
	setUsage := func(n int, five float64) {
		mu.Lock()
		defer mu.Unlock()
		usage[token(n)] = [2]float64{five, 5}
	}

	now := time.Now()
	clock := func() time.Time { return now }
	login := func(n int) {
		t.Helper()
		if err := os.MkdirAll(configHome, 0o700); err != nil {
			t.Fatal(err)
		}
		creds := fmt.Sprintf(`{"claudeAiOauth":{"accessToken":%q,"refreshToken":"%s-refresh-%d","expiresAt":%d}}`, token(n), daemonTokenMarker, n, now.Add(48*time.Hour).UnixMilli())
		global := fmt.Sprintf(`{"oauthAccount":{"accountUuid":"a%d","emailAddress":"u%d@example.com","organizationUuid":"o%d"}}`, n, n, n)
		if os.WriteFile(filepath.Join(configHome, ".credentials.json"), []byte(creds), 0o600) != nil ||
			os.WriteFile(filepath.Join(configHome, ".claude.json"), []byte(global), 0o600) != nil {
			t.Fatal("write live login")
		}
	}

	cfg := config.Config{StateDir: filepath.Join(root, "state"), ClaudeAccounts: config.DefaultClaudeAccounts()}
	cfg.ClaudeAccounts.AutoSwitch = true
	var logs bytes.Buffer
	auto := newClaudeAccountAuto(cfg, log.New(&logs, "", 0))
	auto.manager.Env = env
	auto.manager.Now = clock
	auto.manager.Client = &claudeacct.Client{TokenURL: server.URL + "/token", UsageURL: server.URL + "/usage", Now: clock}
	auto.manager.LockTimeout = 300 * time.Millisecond
	auto.now = clock
	var notes []string
	auto.notify = func(_ context.Context, text string) error {
		notes = append(notes, text)
		return nil
	}
	ctx := context.Background()
	for _, n := range []int{1, 2} {
		login(n)
		if _, err := auto.manager.Add(ctx, 0, ""); err != nil {
			t.Fatal(err)
		}
	}
	// Live is slot 2 at 95%; slot 1 is at 20%.
	setUsage(2, 95)
	setUsage(1, 20)

	pass := func() {
		t.Helper()
		if err := auto.pass(ctx); err != nil {
			t.Fatal(err)
		}
	}
	// Pass 1 polls only the active account: the other is unknown, so no target.
	pass()
	if !strings.Contains(logs.String(), "no viable Claude account") || len(notes) != 0 {
		t.Fatalf("pass 1 logs=%q notes=%v", logs.String(), notes)
	}
	// Pass 2 polls slot 1 a minute later and switches.
	now = now.Add(time.Minute)
	pass()
	if len(notes) != 1 || !strings.Contains(notes[0], "to slot 1 (u1@example.com, 20%)") {
		t.Fatalf("notes=%v logs=%q", notes, logs.String())
	}
	if strings.Count(logs.String(), "switched from slot 2") != 1 {
		t.Fatalf("switch log lines: %q", logs.String())
	}
	// Cooling down: nothing more, even with slot 1 now exhausted.
	setUsage(1, 99)
	now = now.Add(time.Minute)
	pass()
	if len(notes) != 1 {
		t.Fatalf("switched during cooldown: %v", notes)
	}
	// Both exhausted after the cooldown: logged once per hour, no notification.
	before := strings.Count(logs.String(), "no viable Claude account")
	for i := 0; i < 20; i++ {
		now = now.Add(time.Minute)
		pass()
	}
	if got := strings.Count(logs.String(), "no viable Claude account") - before; got != 1 || len(notes) != 1 {
		t.Fatalf("exhausted logged %d times, notes=%v:\n%s", got, notes, logs.String())
	}
	now = now.Add(time.Hour)
	pass()
	if got := strings.Count(logs.String(), "no viable Claude account") - before; got != 2 {
		t.Fatalf("exhausted not re-logged after an hour: %d", got)
	}
	if strings.Contains(logs.String(), daemonTokenMarker) || strings.Contains(strings.Join(notes, "\n"), daemonTokenMarker) {
		t.Fatalf("token value in daemon output: %q %v", logs.String(), notes)
	}
}

func TestClaudeAccountAutoIsOptIn(t *testing.T) {
	var logs bytes.Buffer
	s := &Service{config: config.Config{StateDir: t.TempDir(), ClaudeAccounts: config.DefaultClaudeAccounts()}, log: log.New(&logs, "", 0)}
	ctx, cancel := context.WithCancel(context.Background())
	s.startClaudeAccountAuto(ctx)
	cancel()
	s.wg.Wait()
	if logs.Len() != 0 {
		t.Fatalf("disabled job logged: %q", logs.String())
	}
}

func TestClaudeAccountKeepAliveWithoutAutoSwitch(t *testing.T) {
	root := t.TempDir()
	configHome := filepath.Join(root, "claude")
	env := claudeacct.Env{
		Getenv:   func(key string) string { return map[string]string{"CLAUDE_CONFIG_DIR": configHome}[key] },
		UserHome: func() (string, error) { return filepath.Join(root, "home"), nil },
	}
	token := fmt.Sprintf("%s-access-1", daemonTokenMarker)
	now := time.Now().Truncate(time.Second)
	clock := func() time.Time { return now }
	var mu sync.Mutex
	started := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer "+token {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if !started {
			fmt.Fprint(w, `{"five_hour":{"utilization":0,"resets_at":null},"seven_day":null}`)
			return
		}
		fmt.Fprintf(w, `{"five_hour":{"utilization":1,"resets_at":%q},"seven_day":null}`, now.Add(5*time.Hour).Format(time.RFC3339))
	}))
	t.Cleanup(server.Close)
	if err := os.MkdirAll(configHome, 0o700); err != nil {
		t.Fatal(err)
	}
	creds := fmt.Sprintf(`{"claudeAiOauth":{"accessToken":%q,"refreshToken":"%s-refresh-1","expiresAt":%d}}`, token, daemonTokenMarker, now.Add(48*time.Hour).UnixMilli())
	if os.WriteFile(filepath.Join(configHome, ".credentials.json"), []byte(creds), 0o600) != nil ||
		os.WriteFile(filepath.Join(configHome, ".claude.json"), []byte(`{"oauthAccount":{"accountUuid":"a1","emailAddress":"u1@example.com","organizationUuid":"o1"}}`), 0o600) != nil {
		t.Fatal("write live login")
	}

	cfg := config.Config{StateDir: filepath.Join(root, "state"), ClaudeAccounts: config.DefaultClaudeAccounts()}
	cfg.ClaudeAccounts.KeepAlive = true
	cfg.ClaudeAccounts.Limits = map[string]int{"u1@example.com": 40}
	var logs bytes.Buffer
	auto := newClaudeAccountAuto(cfg, log.New(&logs, "", 0))
	if auto.autoSwitch || auto.keepAlive == nil || auto.keepAlive.Model != "haiku" || auto.options.Policy.Limits["u1@example.com"] != 40 {
		t.Fatalf("options %+v %+v", auto.keepAlive, auto.options.Policy)
	}
	auto.manager.Env = env
	auto.manager.Now = clock
	auto.manager.Client = &claudeacct.Client{TokenURL: server.URL + "/token", UsageURL: server.URL + "/usage", Now: clock}
	auto.manager.LockTimeout = 300 * time.Millisecond
	auto.now = clock
	var pings []string
	auto.manager.Pinger = func(_ context.Context, req claudeacct.PingRequest) error {
		pings = append(pings, req.Model)
		mu.Lock()
		started = true
		mu.Unlock()
		return nil
	}
	ctx := context.Background()
	if _, err := auto.manager.Add(ctx, 0, ""); err != nil {
		t.Fatal(err)
	}
	if err := auto.pass(ctx); err != nil {
		t.Fatal(err)
	}
	if len(pings) != 1 || !strings.Contains(logs.String(), "keepalive started the five-hour window of slot 1 (u1@example.com)") {
		t.Fatalf("pings %v logs %q", pings, logs.String())
	}
	now = now.Add(time.Minute)
	if err := auto.pass(ctx); err != nil {
		t.Fatal(err)
	}
	if len(pings) != 1 {
		t.Fatalf("pinged a running window again")
	}
	if strings.Contains(logs.String(), daemonTokenMarker) {
		t.Fatalf("token value in daemon log: %q", logs.String())
	}
}
