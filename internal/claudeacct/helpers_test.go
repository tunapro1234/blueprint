package claudeacct

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// tokenMarker is part of every fake token value; no output may contain it.
const tokenMarker = "SECRETTOKEN"

func fakeToken(kind string, n int) string {
	return fmt.Sprintf("%s-%s-%d-x9", tokenMarker, kind, n)
}

// fakeAPI is an httptest OAuth token and usage endpoint.
type fakeAPI struct {
	t      *testing.T
	server *httptest.Server
	mu     sync.Mutex
	// usage by access token; a missing entry answers 401.
	usage map[string]string
	// usageStatus forces a status (with optional Retry-After) per token.
	usageStatus map[string][2]string
	// refreshFail maps a refresh token to a status and OAuth error code.
	refreshFail map[string][2]string
	rotations   int
	refreshes   []string
	usageCalls  []string
}

func newFakeAPI(t *testing.T) *fakeAPI {
	api := &fakeAPI{t: t, usage: map[string]string{}, usageStatus: map[string][2]string{}, refreshFail: map[string][2]string{}}
	mux := http.NewServeMux()
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			GrantType    string `json:"grant_type"`
			RefreshToken string `json:"refresh_token"`
			ClientID     string `json:"client_id"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		api.mu.Lock()
		defer api.mu.Unlock()
		api.refreshes = append(api.refreshes, body.RefreshToken)
		if body.GrantType != "refresh_token" || body.ClientID != ClientID {
			w.WriteHeader(400)
			fmt.Fprint(w, `{"error":"invalid_request"}`)
			return
		}
		if fail, ok := api.refreshFail[body.RefreshToken]; ok {
			var status int
			fmt.Sscan(fail[0], &status)
			w.WriteHeader(status)
			fmt.Fprintf(w, `{"error":%q,"error_description":"echo %s"}`, fail[1], body.RefreshToken)
			return
		}
		api.rotations++
		n := 100 + api.rotations
		fmt.Fprintf(w, `{"access_token":%q,"refresh_token":%q,"expires_in":28800,"scope":"user:inference user:profile"}`, fakeToken("access", n), fakeToken("refresh", n))
	})
	mux.HandleFunc("/usage", func(w http.ResponseWriter, r *http.Request) {
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if r.Header.Get("anthropic-beta") != oauthBeta {
			w.WriteHeader(400)
			return
		}
		api.mu.Lock()
		defer api.mu.Unlock()
		api.usageCalls = append(api.usageCalls, token)
		if forced, ok := api.usageStatus[token]; ok {
			if forced[1] != "" {
				w.Header().Set("Retry-After", forced[1])
			}
			var status int
			fmt.Sscan(forced[0], &status)
			w.WriteHeader(status)
			return
		}
		body, ok := api.usage[token]
		if !ok {
			w.WriteHeader(401)
			return
		}
		fmt.Fprint(w, body)
	})
	api.server = httptest.NewServer(mux)
	t.Cleanup(api.server.Close)
	return api
}

func (a *fakeAPI) setUsage(token string, five, seven float64) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.usage[token] = fmt.Sprintf(`{"five_hour":{"utilization":%g,"resets_at":"2099-01-01T00:00:00Z"},"seven_day":{"utilization":%g,"resets_at":"2099-01-02T00:00:00Z"}}`, five, seven)
}

func (a *fakeAPI) calls() (refreshes, usage []string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.refreshes...), append([]string(nil), a.usageCalls...)
}

// fixture is a temporary HOME with Claude Code files and a bp state dir.
type fixture struct {
	t     *testing.T
	home  string
	paths ClaudePaths
	api   *fakeAPI
	m     *Manager
	now   time.Time
}

func newFixture(t *testing.T, configDir bool) *fixture {
	t.Helper()
	root := t.TempDir()
	home := filepath.Join(root, "home")
	env := map[string]string{"HOME": home}
	if configDir {
		env["CLAUDE_CONFIG_DIR"] = filepath.Join(root, "claude-config")
	}
	f := &fixture{t: t, home: home, api: newFakeAPI(t), now: time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)}
	e := Env{Getenv: func(k string) string { return env[k] }, UserHome: func() (string, error) { return home, nil }}
	paths, err := e.Paths()
	if err != nil {
		t.Fatal(err)
	}
	if strings.HasPrefix(paths.ConfigHome, "/root") || !strings.HasPrefix(paths.ConfigHome, root) {
		t.Fatalf("fixture escaped its temp dir: %s", paths.ConfigHome)
	}
	f.paths = paths
	clock := func() time.Time { return f.now }
	f.m = &Manager{
		Store:        NewStore(filepath.Join(root, "bp-state")),
		Env:          e,
		Client:       &Client{TokenURL: f.api.server.URL + "/token", UsageURL: f.api.server.URL + "/usage", Now: clock},
		Now:          clock,
		LockTimeout:  300 * time.Millisecond,
		LockTouch:    50 * time.Millisecond,
		UsageTimeout: 2 * time.Second,
	}
	return f
}

type login struct {
	email, account, org string
	access, refresh     string
	expiresAt           int64
}

func (f *fixture) account(n int) login {
	return login{
		email:     fmt.Sprintf("user%d@example.com", n),
		account:   fmt.Sprintf("acct-%d", n),
		org:       fmt.Sprintf("org-%d", n),
		access:    fakeToken("access", n),
		refresh:   fakeToken("refresh", n),
		expiresAt: f.now.Add(6 * time.Hour).UnixMilli(),
	}
}

// writeLive installs a login as Claude Code would have written it, with
// machine-shared credential keys and unrelated global config keys.
func (f *fixture) writeLive(l login) {
	f.t.Helper()
	if err := os.MkdirAll(f.paths.ConfigHome, 0o700); err != nil {
		f.t.Fatal(err)
	}
	creds := fmt.Sprintf(`{"claudeAiOauth":{"accessToken":%q,"refreshToken":%q,"expiresAt":%d,"scopes":["user:inference"],"subscriptionType":"max","rateLimitTier":"tier-x"},"mcpOAuth":{"server":{"token":"machine-mcp"}}}`, l.access, l.refresh, l.expiresAt)
	if err := os.WriteFile(f.paths.CredentialsFile(), []byte(creds), 0o600); err != nil {
		f.t.Fatal(err)
	}
	account := fmt.Sprintf(`{"accountUuid":%q,"emailAddress":%q,"organizationUuid":%q,"organizationName":"Org %s","displayName":"User"}`, l.account, l.email, l.org, l.org)
	global := []byte("{\n  \"numStartups\": 42,\n  \"oauthAccount\": " + account + ",\n  \"projects\": {\"/x\": {\"allowedTools\": []}},\n  \"zLast\": true\n}\n")
	if current, err := os.ReadFile(f.paths.GlobalConfig); err == nil {
		obj, err := parseOrderedObject(current)
		if err != nil {
			f.t.Fatal(err)
		}
		obj.set("oauthAccount", json.RawMessage(account))
		global, _ = obj.marshal(true)
	}
	mode := os.FileMode(0o644)
	if info, err := os.Stat(f.paths.GlobalConfig); err == nil {
		mode = info.Mode().Perm()
	}
	if err := os.WriteFile(f.paths.GlobalConfig, global, mode); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) liveTokens() Tokens {
	f.t.Helper()
	data, err := os.ReadFile(f.paths.CredentialsFile())
	if err != nil {
		f.t.Fatal(err)
	}
	creds, err := ParseCredentials(data)
	if err != nil {
		f.t.Fatal(err)
	}
	return creds.Tokens()
}

func (f *fixture) liveIdentity() Identity {
	f.t.Helper()
	data, err := os.ReadFile(f.paths.GlobalConfig)
	if err != nil {
		f.t.Fatal(err)
	}
	obj, err := parseOrderedObject(data)
	if err != nil {
		f.t.Fatal(err)
	}
	raw, _ := obj.get("oauthAccount")
	id, err := parseOAuthAccount(raw)
	if err != nil {
		f.t.Fatal(err)
	}
	return id
}

func (f *fixture) slotTokens(n int) Tokens {
	f.t.Helper()
	creds, err := f.m.Store.ReadCredentials(n)
	if err != nil {
		f.t.Fatal(err)
	}
	return creds.Tokens()
}

// addAccounts logs in and adds accounts 1..n; account n stays live.
func (f *fixture) addAccounts(n int) {
	f.t.Helper()
	for i := 1; i <= n; i++ {
		f.writeLive(f.account(i))
		if _, err := f.m.Add(testCtx(f.t), 0, ""); err != nil {
			f.t.Fatal(err)
		}
	}
}

func (f *fixture) setCachedUsage(n int, five, seven float64) {
	f.t.Helper()
	accounts, err := f.m.Store.Load()
	if err != nil {
		f.t.Fatal(err)
	}
	reset := f.now.Add(time.Hour)
	accounts.Slot(n).LastUsage = &UsageCache{Usage: &Usage{FiveHour: &Window{five, reset}, SevenDay: &Window{seven, reset}}, FetchedAt: f.now, AttemptAt: f.now}
	if err := f.m.Store.Save(accounts); err != nil {
		f.t.Fatal(err)
	}
}

func assertNoToken(t *testing.T, label string, outputs ...string) {
	t.Helper()
	for _, out := range outputs {
		if strings.Contains(out, tokenMarker) {
			t.Fatalf("%s leaked a token value: %s", label, out)
		}
	}
}

func assertMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != want {
		t.Fatalf("%s mode %v, want %v", path, info.Mode().Perm(), want)
	}
}

func render(o *Overview) string {
	var buf bytes.Buffer
	RenderList(&buf, o)
	return buf.String()
}
