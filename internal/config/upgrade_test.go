package config_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"blueprint/internal/config"
	"blueprint/internal/modules"

	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
)

func peerID(t *testing.T) string {
	t.Helper()
	_, public, err := crypto.GenerateEd25519Key(nil)
	if err != nil {
		t.Fatal(err)
	}
	id, err := peer.IDFromPublicKey(public)
	if err != nil {
		t.Fatal(err)
	}
	return id.String()
}

// serverConfig has the shape of the owner's server config.json in October
// 2026 (keys and types, invented values): fed, two books, p2p with two peers,
// codex disabled, account switching with keepalive, no waBridge key (the
// legacy default turns the bridge on) and no bar key.
func serverConfig(t *testing.T) string {
	return `{
  "fed": {
    "mode": "hub",
    "listen": "127.0.0.1:1",
    "peerName": "server"
  },
  "agentbooks": [
    "/srv/server-main/agentbook.json",
    "/srv/probot/.orchestration/agentbook.json"
  ],
  "p2p": {
    "enabled": true,
    "relay": true,
    "listen": ["/ip4/127.0.0.1/tcp/0"],
    "advertise": ["/ip4/127.0.0.1/tcp/0"],
    "peers": {
      "laptop": {"id": "` + peerID(t) + `", "expose": ["main"]},
      "friend": {"id": "` + peerID(t) + `", "expose": ["gate"]}
    }
  },
  "codex": {
    "disabled": true
  },
  "claudeAccounts": {
    "autoSwitch": true,
    "threshold": 90,
    "cooldownMinutes": 5,
    "pollMinutes": 5,
    "limits": {"one": 80, "two": 95},
    "keepAlive": true
  }
}
`
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}

// The behavior the server had before modules existed, gate by gate.
var serverGates = map[string]bool{
	modules.Sessions: true, // daemon keepalive reopened the coordinator
	modules.Bar:      true, // daemon bar renderer always ran
	modules.Accounts: true, // claudeAccounts.autoSwitch/keepAlive
	modules.WA:       true, // legacy default waBridge with /srv/whatsapp/outbox
	modules.UI:       true, // dash-server always ran
	modules.Monitor:  true, // /srv/monitor jobs always ran
	// New with modules: the owner server must not gain the tool-call hook.
	modules.GuardHooks: false,
}

func TestUpgradeOfServerInstallChangesNothing(t *testing.T) {
	liveBefore, liveErr := os.Stat("/srv/blueprint/config.json")
	root := t.TempDir()
	t.Setenv("BP_HOME", filepath.Join(root, "unused"))
	t.Setenv("HOME", filepath.Join(root, "root"))
	original := serverConfig(t)
	configPath := filepath.Join(root, "srv/blueprint/config.json")
	writeFile(t, configPath, original)
	writeFile(t, filepath.Join(root, "srv/server-main/agentbook.json"), `{"orchestrator":"server-main","agents":[{"name":"server-main","folder":"/srv"}]}`)
	writeFile(t, filepath.Join(root, "srv/probot/.orchestration/agentbook.json"), `{"orchestrator":"probot-main","agents":[]}`)
	writeFile(t, filepath.Join(root, "srv/whatsapp/bridge.js"), "// bridge\n")
	if err := os.MkdirAll(filepath.Join(root, "srv/blueprint/state/claude-accounts/slots/1"), 0700); err != nil {
		t.Fatal(err)
	}

	cfg, err := config.LoadLegacyFixture(root)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Legacy || cfg.ModulesSet || !strings.HasPrefix(cfg.Path, root) || !strings.HasPrefix(cfg.StateDir, root) {
		t.Fatalf("fixture is not a rebased legacy install: legacy=%v set=%v path=%s state=%s", cfg.Legacy, cfg.ModulesSet, cfg.Path, cfg.StateDir)
	}
	// Before the migration write, detection already answers like the old code.
	for name, want := range serverGates {
		if got := modules.EnabledIn(cfg, name); got != want {
			t.Errorf("before migration %s = %v, want %v", name, got, want)
		}
	}

	migrated := modules.Init(cfg)
	for name, want := range serverGates {
		if got := modules.EnabledIn(migrated, name); got != want {
			t.Errorf("after migration %s = %v, want %v", name, got, want)
		}
	}

	// Only the modules member was added; every other byte is unchanged.
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	inserted := `,
  "modules": {"accounts": true, "bar": true, "monitor": true, "sessions": true, "ui": true, "wa": true}`
	if !bytes.Contains(data, []byte(inserted)) || string(bytes.Replace(data, []byte(inserted), nil, 1)) != original {
		t.Fatalf("config changed beyond the modules key:\n%s", data)
	}
	info, err := os.Stat(configPath)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("mode = %v, %v", info.Mode(), err)
	}

	// The next start reads the recorded modules and every other setting the
	// same way as before the upgrade.
	again, err := config.LoadLegacyFixture(root)
	if err != nil {
		t.Fatal(err)
	}
	if !again.ModulesSet {
		t.Fatal("modules not recorded")
	}
	for name, want := range serverGates {
		if got := modules.EnabledIn(again, name); got != want {
			t.Errorf("reloaded %s = %v, want %v", name, got, want)
		}
	}
	again.Modules, again.ModulesSet = nil, false
	cfg.Modules, cfg.ModulesSet = nil, false
	if a, b := fingerprint(t, cfg), fingerprint(t, again); a != b {
		t.Fatalf("settings changed by the upgrade:\nbefore %s\nafter  %s", a, b)
	}
	// A second start does not write again.
	before, _ := os.Stat(configPath)
	modules.Init(again)
	after, _ := os.Stat(configPath)
	if !before.ModTime().Equal(after.ModTime()) {
		t.Fatal("second start rewrote the config")
	}

	// Nothing outside the fixture root was written.
	if liveErr == nil {
		liveAfter, err := os.Stat("/srv/blueprint/config.json")
		if err != nil || !liveAfter.ModTime().Equal(liveBefore.ModTime()) {
			t.Fatal("the live server config was touched")
		}
	}
	filepath.Walk(filepath.Join(root, "srv/blueprint/state/modules"), func(path string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() && filepath.Ext(path) == ".json" {
			data, _ := os.ReadFile(path)
			if bytes.Contains(data, []byte(`"/srv/`)) && !bytes.Contains(data, []byte(root)) {
				t.Errorf("journal %s points outside the fixture: %s", path, data)
			}
		}
		return nil
	})
}

func fingerprint(t *testing.T, cfg config.Config) string {
	t.Helper()
	cfg.Path = ""
	var out bytes.Buffer
	encoder := newEncoder(&out)
	if err := encoder.Encode(cfg); err != nil {
		t.Fatal(err)
	}
	return out.String()
}

func newEncoder(out *bytes.Buffer) interface{ Encode(any) error } {
	return jsonEncoder{out}
}

type jsonEncoder struct{ out *bytes.Buffer }

func (e jsonEncoder) Encode(value any) error {
	data, err := json.Marshal(value)
	e.out.Write(data)
	return err
}
