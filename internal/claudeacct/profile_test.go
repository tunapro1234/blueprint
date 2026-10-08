package claudeacct

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// profileLogin writes a login into a profile the way `claude auth login`
// run with CLAUDE_CONFIG_DIR=<profile> would, keeping the seeded keys.
func (f *fixture) profileLogin(dir string, l login) {
	f.t.Helper()
	creds := fmt.Sprintf(`{"claudeAiOauth":{"accessToken":%q,"refreshToken":%q,"expiresAt":%d,"scopes":["user:inference"],"subscriptionType":"pro"}}`, l.access, l.refresh, l.expiresAt)
	if err := os.WriteFile(filepath.Join(dir, ".credentials.json"), []byte(creds), 0o600); err != nil {
		f.t.Fatal(err)
	}
	path := filepath.Join(dir, ".claude.json")
	data, err := os.ReadFile(path)
	if err != nil {
		data = []byte("{}")
	}
	obj, err := parseOrderedObject(data)
	if err != nil {
		f.t.Fatal(err)
	}
	obj.set("oauthAccount", json.RawMessage(fmt.Sprintf(`{"accountUuid":%q,"emailAddress":%q,"organizationUuid":%q}`, l.account, l.email, l.org)))
	out, _ := obj.marshal(true)
	if err := os.WriteFile(path, out, 0o600); err != nil {
		f.t.Fatal(err)
	}
}

func TestEnsureProfileLinksSharedStateAndSeedsConfig(t *testing.T) {
	for _, configDir := range []bool{false, true} {
		t.Run(fmt.Sprintf("configDir=%v", configDir), func(t *testing.T) {
			f := newFixture(t, configDir)
			f.addAccounts(2)
			if err := os.MkdirAll(filepath.Join(f.paths.ConfigHome, "projects", "-x"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(f.paths.ConfigHome, "settings.json"), []byte(`{"model":"x"}`), 0o644); err != nil {
				t.Fatal(err)
			}

			p, err := f.m.EnsureProfile("1")
			if err != nil {
				t.Fatal(err)
			}
			if p.Dir != f.m.Store.ProfileDir("acct-1") || p.Slot.Email != "user1@example.com" {
				t.Fatalf("profile = %+v", p)
			}
			if p.HasLogin || p.Ready() || !strings.Contains(p.Problem(), "bp account login 1") {
				t.Fatalf("fresh profile must need a login: %+v %q", p, p.Problem())
			}
			assertMode(t, p.Dir, 0o700)
			for _, name := range []string{"projects", "skills", "settings.json"} {
				dest, err := os.Readlink(filepath.Join(p.Dir, name))
				if err != nil || dest != filepath.Join(f.paths.ConfigHome, name) {
					t.Fatalf("%s link = %q, %v", name, dest, err)
				}
			}
			// A shared file the base home lacks is not linked.
			if _, err := os.Lstat(filepath.Join(p.Dir, "CLAUDE.md")); !os.IsNotExist(err) {
				t.Fatalf("CLAUDE.md was created: %v", err)
			}
			for _, name := range []string{".credentials.json"} {
				if _, err := os.Lstat(filepath.Join(p.Dir, name)); !os.IsNotExist(err) {
					t.Fatalf("profile got credentials it never logged into: %v", err)
				}
			}

			seeded := filepath.Join(p.Dir, ".claude.json")
			assertMode(t, seeded, 0o600)
			data, err := os.ReadFile(seeded)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(data), "oauthAccount") || !strings.Contains(string(data), `"numStartups": 42`) || !strings.Contains(string(data), `"/x"`) {
				t.Fatalf("seeded config = %s", data)
			}
			assertNoToken(t, "seeded config", string(data))

			marker, ok := readProfileMarker(p.Dir)
			wantBase := ""
			if configDir {
				wantBase = f.paths.ConfigHome
			}
			if !ok || marker.Email != "user1@example.com" || marker.AccountUUID != "acct-1" || marker.Base != wantBase {
				t.Fatalf("marker = %+v %v", marker, ok)
			}

			// A login for the wrong account is not ready.
			f.profileLogin(p.Dir, f.account(2))
			if _, _, err := f.m.LaunchDir("user1@example.com"); err == nil || !strings.Contains(err.Error(), "logged in as user2@example.com") {
				t.Fatalf("mismatched login launched: %v", err)
			}
			f.profileLogin(p.Dir, f.account(1))
			dir, slot, err := f.m.LaunchDir("user1@example.com")
			if err != nil || dir != p.Dir || slot.Number != 1 {
				t.Fatalf("LaunchDir = %q %+v %v", dir, slot, err)
			}
			assertNoToken(t, "launch error", fmt.Sprint(err))

			// Re-ensuring never overwrites the profile's own config or login.
			again, err := f.m.EnsureProfile("1")
			if err != nil || !again.Ready() || again.Subscribed != "pro" {
				t.Fatalf("re-ensure = %+v %v", again, err)
			}
			if !strings.Contains(string(mustRead(t, seeded)), "acct-1") {
				t.Fatal("re-ensure overwrote the profile login")
			}
			if tokens := f.liveTokens(); tokens.AccessToken != f.account(2).access {
				t.Fatal("profile login touched the default home")
			}

			profiles, err := f.m.Profiles()
			if err != nil || len(profiles) != 1 || profiles[0].Slot.Number != 1 {
				t.Fatalf("Profiles = %+v %v", profiles, err)
			}
			encoded, _ := json.Marshal(profiles)
			assertNoToken(t, "profiles json", string(encoded))
		})
	}
}

func TestEnsureProfileKeepsDivergedEntries(t *testing.T) {
	f := newFixture(t, false)
	f.addAccounts(1)
	dir := f.m.Store.ProfileDir("acct-1")
	if err := os.MkdirAll(filepath.Join(dir, "projects"), 0o700); err != nil {
		t.Fatal(err)
	}
	own := filepath.Join(dir, "projects", "only-copy.jsonl")
	if err := os.WriteFile(own, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/nonexistent-elsewhere", filepath.Join(dir, "skills")); err != nil {
		t.Fatal(err)
	}
	p, err := f.m.EnsureProfile("user1@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(p.Diverged, ",") != "projects,skills" {
		t.Fatalf("diverged = %v", p.Diverged)
	}
	if _, err := os.Stat(own); err != nil {
		t.Fatalf("diverged entry was removed: %v", err)
	}
	if dest, _ := os.Readlink(filepath.Join(dir, "skills")); dest != "/nonexistent-elsewhere" {
		t.Fatalf("wrong link was replaced: %q", dest)
	}
}

func TestLaunchDirDefaultAndUnknown(t *testing.T) {
	f := newFixture(t, false)
	f.addAccounts(1)
	for _, binding := range []string{"", "default", " Default "} {
		if dir, _, err := f.m.LaunchDir(binding); dir != "" || err != nil {
			t.Fatalf("LaunchDir(%q) = %q %v", binding, dir, err)
		}
	}
	if _, _, err := f.m.LaunchDir("nobody@example.com"); err == nil {
		t.Fatal("unknown account resolved")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(f.m.Store.ProfileDir("x")))); !os.IsNotExist(err) {
		t.Fatalf("a profile was created for the default binding: %v", err)
	}
}

// A process inside a profile resolves the default home, not its profile, so
// switching and the agents it opens keep using the default login.
func TestPathsInsideProfileResolveBase(t *testing.T) {
	for _, configDir := range []bool{false, true} {
		t.Run(fmt.Sprintf("configDir=%v", configDir), func(t *testing.T) {
			f := newFixture(t, configDir)
			f.addAccounts(1)
			p, err := f.m.EnsureProfile("1")
			if err != nil {
				t.Fatal(err)
			}
			inside := Env{Getenv: func(k string) string {
				switch k {
				case "HOME":
					return f.home
				case "CLAUDE_CONFIG_DIR":
					return p.Dir
				}
				return ""
			}, UserHome: func() (string, error) { return f.home, nil }}
			paths, err := inside.Paths()
			if err != nil || paths != f.paths {
				t.Fatalf("paths inside profile = %+v %v, want %+v", paths, err, f.paths)
			}
			value, set := inside.DefaultConfigDir()
			if set != configDir || (configDir && value != f.paths.ConfigHome) {
				t.Fatalf("DefaultConfigDir = %q %v", value, set)
			}
			if !IsProfile(p.Dir) || IsProfile(f.paths.ConfigHome) {
				t.Fatal("IsProfile is wrong")
			}
		})
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
