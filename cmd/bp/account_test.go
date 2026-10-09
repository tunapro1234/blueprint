package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"blueprint/internal/claudeacct"
	bpconfig "blueprint/internal/config"
)

// accountSecret is part of every fake token; no command output may contain it.
const accountSecret = "SECRETTOKEN"

type accountAPI struct {
	server *httptest.Server
	mu     sync.Mutex
	usage  map[string][2]float64
	status map[string][2]string
	// idle tokens report no running five-hour window.
	idle map[string]bool
}

func newAccountAPI(t *testing.T) *accountAPI {
	api := &accountAPI{usage: map[string][2]float64{}, status: map[string][2]string{}, idle: map[string]bool{}}
	mux := http.NewServeMux()
	rotations := 0
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		api.mu.Lock()
		defer api.mu.Unlock()
		rotations++
		fmt.Fprintf(w, `{"access_token":"%s-access-r%d","refresh_token":"%s-refresh-r%d","expires_in":28800}`, accountSecret, rotations, accountSecret, rotations)
	})
	mux.HandleFunc("/usage", func(w http.ResponseWriter, r *http.Request) {
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		api.mu.Lock()
		defer api.mu.Unlock()
		if forced, ok := api.status[token]; ok {
			w.Header().Set("Retry-After", forced[1])
			var code int
			fmt.Sscan(forced[0], &code)
			w.WriteHeader(code)
			return
		}
		u, ok := api.usage[token]
		if !ok {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if api.idle[token] {
			fmt.Fprintf(w, `{"five_hour":{"utilization":0,"resets_at":null},"seven_day":{"utilization":%g,"resets_at":"2099-01-02T00:00:00Z"}}`, u[1])
			return
		}
		fmt.Fprintf(w, `{"five_hour":{"utilization":%g,"resets_at":"2099-01-01T00:00:00Z"},"seven_day":{"utilization":%g,"resets_at":"2099-01-02T00:00:00Z"}}`, u[0], u[1])
	})
	api.server = httptest.NewServer(mux)
	t.Cleanup(api.server.Close)
	return api
}

func (api *accountAPI) set(n int, five, seven float64) {
	api.mu.Lock()
	defer api.mu.Unlock()
	api.usage[accountToken("access", n)] = [2]float64{five, seven}
}

func accountToken(kind string, n int) string { return fmt.Sprintf("%s-%s-%d", accountSecret, kind, n) }

type accountEnv struct {
	t          *testing.T
	configDir  string
	state      string
	api        *accountAPI
	transcript strings.Builder
	// pinged lists the slots keepalive prompted, in order.
	pinged []int
	// now, when set, pins every manager's clock so a test that reasons about the
	// five-hour phase grid is not wall-clock dependent.
	now time.Time
}

// newAccountEnv points HOME and CLAUDE_CONFIG_DIR at a temporary directory and
// routes the OAuth endpoints to an httptest server.
func newAccountEnv(t *testing.T) *accountEnv {
	root := t.TempDir()
	e := &accountEnv{t: t, configDir: filepath.Join(root, "claude"), state: filepath.Join(root, "state"), api: newAccountAPI(t)}
	t.Setenv("HOME", filepath.Join(root, "home"))
	t.Setenv("CLAUDE_CONFIG_DIR", e.configDir)
	paths, err := claudeacct.OSEnv().Paths()
	if err != nil || !strings.HasPrefix(paths.ConfigHome, root) || !strings.HasPrefix(paths.GlobalConfig, root) {
		t.Fatalf("Claude paths escaped the temp dir: %+v %v", paths, err)
	}
	previous := newAccountManager
	newAccountManager = func(config bpconfig.Config) *claudeacct.Manager {
		m := previous(config)
		m.Client = &claudeacct.Client{TokenURL: e.api.server.URL + "/token", UsageURL: e.api.server.URL + "/usage"}
		m.LockTimeout = 300 * time.Millisecond
		m.LockTouch = 50 * time.Millisecond
		if !e.now.IsZero() {
			fixed := e.now
			m.Now = func() time.Time { return fixed }
			m.Client.Now = func() time.Time { return fixed }
		}
		// Never run the real claude binary: a prompt starts the fake window.
		m.Pinger = func(ctx context.Context, req claudeacct.PingRequest) error {
			e.api.mu.Lock()
			defer e.api.mu.Unlock()
			for n := 1; n <= 9; n++ {
				if req.Token == accountToken("access", n) {
					e.pinged = append(e.pinged, n)
				}
			}
			delete(e.api.idle, req.Token)
			return nil
		}
		return m
	}
	t.Cleanup(func() { newAccountManager = previous })
	return e
}

func (e *accountEnv) login(n int) {
	e.t.Helper()
	if err := os.MkdirAll(e.configDir, 0o700); err != nil {
		e.t.Fatal(err)
	}
	creds := fmt.Sprintf(`{"claudeAiOauth":{"accessToken":%q,"refreshToken":%q,"expiresAt":%d,"scopes":["user:inference"],"subscriptionType":"max"}}`,
		accountToken("access", n), accountToken("refresh", n), time.Now().Add(6*time.Hour).UnixMilli())
	if err := os.WriteFile(filepath.Join(e.configDir, ".credentials.json"), []byte(creds), 0o600); err != nil {
		e.t.Fatal(err)
	}
	global := fmt.Sprintf(`{"numStartups":1,"oauthAccount":{"accountUuid":"acct-%d","emailAddress":"user%d@example.com","organizationUuid":"org-%d","organizationName":"Org %d"}}`, n, n, n, n)
	if err := os.WriteFile(filepath.Join(e.configDir, ".claude.json"), []byte(global), 0o600); err != nil {
		e.t.Fatal(err)
	}
}

func (e *accountEnv) app(cooldown int) *app {
	cfg := bpconfig.DefaultClaudeAccounts()
	cfg.CooldownMinutes = cooldown
	return &app{ctx: context.Background(), config: bpconfig.Config{StateDir: e.state, ClaudeAccounts: cfg, ModulesSet: true, Modules: map[string]bool{"accounts": true}}}
}

// run executes one bp account command and returns stdout, stderr and the
// exit code main would use.
func (e *accountEnv) run(a *app, args ...string) (string, int) {
	e.t.Helper()
	a.out, a.err = testOutput(e.t), testOutput(e.t)
	err := a.run(append([]string{"account"}, args...))
	code := 0
	message := ""
	var exitErr *commandExitError
	switch {
	case errors.As(err, &exitErr):
		code, message = exitErr.code, exitErr.message
	case err != nil:
		code, message = 1, err.Error()
	}
	out := readTestOutput(e.t, a.out) + readTestOutput(e.t, a.err) + message
	fmt.Fprintf(&e.transcript, "$ bp account %s\n%s\n", strings.Join(args, " "), out)
	return out, code
}

func (e *accountEnv) liveAccessIs(n int) bool {
	data, err := os.ReadFile(filepath.Join(e.configDir, ".credentials.json"))
	return err == nil && strings.Contains(string(data), accountToken("access", n)+`"`)
}

func TestAccountCLIEndToEnd(t *testing.T) {
	e := newAccountEnv(t)
	a := e.app(5)
	expect := func(out string, code, wantCode int, want ...string) {
		t.Helper()
		if code != wantCode {
			t.Fatalf("exit %d, want %d: %s", code, wantCode, out)
		}
		for _, w := range want {
			if !strings.Contains(out, w) {
				t.Fatalf("output lacks %q:\n%s", w, out)
			}
		}
	}

	out, code := e.run(a, "list")
	expect(out, code, 0, "no stored Claude accounts")
	e.login(1)
	out, code = e.run(a, "add", "--alias", "work")
	expect(out, code, 0, "stored the live Claude login as slot 1 (user1@example.com)")
	e.login(2)
	out, code = e.run(a, "add")
	expect(out, code, 0, "slot 2 (user2@example.com)")
	out, code = e.run(a, "add", "--slot", "1")
	expect(out, code, 1, "already stored in slot 2")

	e.api.set(1, 30, 40)
	e.api.set(2, 70, 20)
	out, code = e.run(a, "list")
	expect(out, code, 0, "slot 1 [work]", "* slot 2", "5h 30%", "5h 70%")
	out, code = e.run(a, "list", "--json")
	var overview claudeacct.Overview
	if err := json.Unmarshal([]byte(out), &overview); code != 0 || err != nil || len(overview.Slots) != 2 || overview.LiveSlot != 2 {
		t.Fatalf("list --json code=%d err=%v: %s", code, err, out)
	}
	out, code = e.run(a, "status")
	expect(out, code, 0, "active: slot 2", "stored: 2 accounts, 2 usable", "auto switch: off (threshold 90%")

	out, code = e.run(a, "switch", "work", "--dry-run")
	expect(out, code, 0, "would switch from slot 2 to slot 1")
	if !e.liveAccessIs(2) {
		t.Fatal("dry run changed the live login")
	}
	out, code = e.run(a, "switch", "work")
	expect(out, code, 0, "switched to slot 1 (user1@example.com); running Claude agents pick it up on their next request")
	if !e.liveAccessIs(1) {
		t.Fatal("switch did not install slot 1")
	}
	out, code = e.run(a, "switch", "1")
	expect(out, code, 1, "already the active Claude login")
	out, code = e.run(a, "switch", "--strategy", "fastest")
	expect(out, code, 1, "unknown strategy")

	out, code = e.run(a, "alias", "2", "home")
	expect(out, code, 0, `slot 2 (user2@example.com) is now "home"`)
	out, code = e.run(a, "disable", "2")
	expect(out, code, 0, "disabled")
	out, code = e.run(a, "switch", "home")
	expect(out, code, 1, "is disabled")
	out, code = e.run(a, "enable", "2")
	expect(out, code, 0, "slot 2 (user2@example.com) enabled")
	out, code = e.run(a, "alias", "2", "--unset")
	expect(out, code, 0, "has no alias")

	// The manual switch started the cooldown.
	e.api.set(1, 95, 10)
	e.api.set(2, 20, 30)
	_, _ = e.run(a, "list", "--refresh")
	out, code = e.run(a, "auto", "--once")
	expect(out, code, accountAutoNothing, "cooling down")
	out, code = e.run(a, "auto", "--once", "--threshold", "99")
	expect(out, code, accountAutoNothing, "below threshold 99%")

	a = e.app(0)
	out, code = e.run(a, "auto", "--once", "--dry-run")
	expect(out, code, accountAutoSwitched, "would switch from slot 1 (95%) to slot 2 (30%)")
	if !e.liveAccessIs(1) {
		t.Fatal("auto dry run changed the live login")
	}
	out, code = e.run(a, "auto", "--once", "--json")
	var result claudeacct.AutoResult
	if err := json.Unmarshal([]byte(out), &result); code != 0 || err != nil || result.Switched == nil || result.Switched.To.Number != 2 {
		t.Fatalf("auto --once --json code=%d err=%v: %s", code, err, out)
	}
	if !e.liveAccessIs(2) {
		t.Fatal("auto switch did not install slot 2")
	}

	e.api.set(1, 96, 10)
	e.api.set(2, 97, 10)
	_, _ = e.run(a, "list", "--refresh")
	out, code = e.run(a, "auto", "--once")
	expect(out, code, accountAutoNoTarget, "no viable account")
	// A rate-limited usage fetch is reported per account and never fails list.
	e.api.mu.Lock()
	e.api.status[accountToken("access", 1)] = [2]string{"429", "240"}
	e.api.mu.Unlock()
	out, code = e.run(a, "list", "--refresh")
	expect(out, code, 0, "usage: error (http-429, retry 4m)")
	e.api.mu.Lock()
	delete(e.api.status, accountToken("access", 1))
	e.api.mu.Unlock()

	out, code = e.run(a, "auto", "--once", "--threshold", "abc")
	expect(out, code, 1, "invalid --threshold")

	out, code = e.run(a, "remove", "2")
	expect(out, code, 1, "is the active Claude login")
	out, code = e.run(a, "remove", "1")
	expect(out, code, 0, "removed slot 1 (user1@example.com)")
	out, code = e.run(a, "frobnicate")
	expect(out, code, 1, "unknown account command")

	if strings.Contains(e.transcript.String(), accountSecret) {
		t.Fatalf("a token value reached command output:\n%s", e.transcript.String())
	}
}

func TestAccountCLIKeepAliveAndLimits(t *testing.T) {
	e := newAccountEnv(t)
	a := e.app(0)
	for n := 1; n <= 2; n++ {
		e.login(n)
		if out, code := e.run(a, "add"); code != 0 {
			t.Fatalf("add %d: %s", n, out)
		}
	}
	e.api.set(1, 50, 10)
	e.api.set(2, 10, 10)
	e.api.idle[accountToken("access", 1)] = true
	_, _ = e.run(a, "list", "--refresh")

	// Slot 2 is live at 10%; its own 5% limit makes auto look for room.
	out, code := e.run(a, "auto", "--once", "--dry-run")
	if code != accountAutoNothing {
		t.Fatalf("without limits: code=%d %s", code, out)
	}
	a.config.ClaudeAccounts.Limits = map[string]int{"user2@example.com": 5}
	out, code = e.run(a, "status")
	if code != 0 || !strings.Contains(out, "account limits: user2@example.com 5%") || !strings.Contains(out, "keepalive: off") {
		t.Fatalf("status: %s", out)
	}
	out, code = e.run(a, "auto", "--once", "--dry-run")
	if code != accountAutoSwitched || !strings.Contains(out, "its 5% limit") {
		t.Fatalf("with a limit: code=%d %s", code, out)
	}
	out, code = e.run(a, "switch", "--dry-run")
	if code != 0 || !strings.Contains(out, "to slot 1") {
		t.Fatalf("switch with a limit: code=%d %s", code, out)
	}

	// Pin the clock before reasoning about the phase grid. The active slot resets
	// at a fixed 2099 instant while the idle slot's window is null (ready = now),
	// so the offset between them — and thus whether the idle slot reads "due now"
	// or "next start HH:MM" — was pure wall-clock noise. Choosing now on the same
	// five-hour grid as that reset puts the idle slot at grid position 0 (due now)
	// deterministically; staying within one period of the real clock keeps the
	// six-hour login tokens fresh so no refresh path is taken.
	reset := time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC)
	realNow := time.Now()
	phase := reset.Sub(realNow) % claudeacct.KeepAlivePeriod
	if phase < 0 {
		phase += claudeacct.KeepAlivePeriod
	}
	e.now = realNow.Add(phase)

	// The plan alone pings nothing; --once pings the idle slot 1.
	out, code = e.run(a, "keepalive")
	if code != 0 || !strings.Contains(out, "staggered every 2h30m") || !strings.Contains(out, "due now (--once pings it)") || len(e.pinged) != 0 {
		t.Fatalf("keepalive plan: code=%d pinged=%v %s", code, e.pinged, out)
	}
	out, code = e.run(a, "keepalive", "--once")
	if code != 0 || !strings.Contains(out, "started its window") || len(e.pinged) != 1 || e.pinged[0] != 1 {
		t.Fatalf("keepalive --once: code=%d pinged=%v %s", code, e.pinged, out)
	}
	out, code = e.run(a, "keepalive", "--once", "--json")
	var result claudeacct.KeepAliveResult
	if err := json.Unmarshal([]byte(out), &result); code != 0 || err != nil || len(result.Slots) != 2 || len(e.pinged) != 1 {
		t.Fatalf("keepalive --json code=%d err=%v pinged=%v: %s", code, err, e.pinged, out)
	}
	out, code = e.run(a, "keepalive", "--model", "")
	if code != 1 || !strings.Contains(out, "invalid --model") {
		t.Fatalf("empty model: code=%d %s", code, out)
	}
	if strings.Contains(e.transcript.String(), accountSecret) {
		t.Fatalf("a token value reached command output:\n%s", e.transcript.String())
	}
}

func TestAccountCLIRefusesMacOS(t *testing.T) {
	e := newAccountEnv(t)
	previous := accountGOOS
	accountGOOS = "darwin"
	t.Cleanup(func() { accountGOOS = previous })
	out, code := e.run(e.app(5), "list")
	if code != 1 || !strings.Contains(out, "macOS") {
		t.Fatalf("code=%d out=%s", code, out)
	}
}
