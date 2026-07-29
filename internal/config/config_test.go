package config

import (
	"bytes"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
)

type fakeFileInfo struct{ os.FileInfo }

func (fakeFileInfo) IsDir() bool { return true }

func TestLegacyModeSelection(t *testing.T) {
	config, err := loadWith(
		func(string) string { return "" },
		func(path string) (os.FileInfo, error) {
			if path != LegacyHome {
				t.Fatalf("stat path=%q, want %q", path, LegacyHome)
			}
			return fakeFileInfo{}, nil
		},
		func() (string, error) { return "", errors.New("must not be called") },
		func(string) ([]byte, error) { return nil, os.ErrNotExist },
	)
	if err != nil {
		t.Fatal(err)
	}
	if !config.Legacy || config.Home != LegacyHome {
		t.Fatalf("legacy config = %#v", config)
	}
	if config.MsgqRoot != "/srv/server-main/msgq" || config.StateDir != "/srv/blueprint/state" || !config.WABridge {
		t.Fatalf("legacy defaults = %#v", config)
	}
	wantBooks := []string{"/srv/server-main/agentbook.json", "/srv/probot/.orchestration/agentbook.json"}
	if !reflect.DeepEqual(config.Agentbooks, wantBooks) {
		t.Fatalf("agentbooks=%q, want %q", config.Agentbooks, wantBooks)
	}
	wantTokenBooks := []string{"/srv/server-main/agentbook.json", "/srv/probot/.orchestration/agentbook.json", "/srv/kitap/.orchestration/agentbook.json"}
	if !reflect.DeepEqual(config.TokenAgentbooks, wantTokenBooks) {
		t.Fatalf("tokenAgentbooks=%q, want %q", config.TokenAgentbooks, wantTokenBooks)
	}
}

func TestConfigOverridesLegacyDefaults(t *testing.T) {
	data := []byte(`{
		"msgqRoot":"/q", "agentbooks":["/a.json"], "stateDir":"/state",
		"waOutbox":"", "waStore":"/wa.jsonl", "usageBin":"/bin",
		"usageHistory":"/history.jsonl", "clipboardDir":"/clips",
		"waBridge":false,
		"ntfy":{"url":"https://ntfy.example","topic":"alerts","token":"secret"},
		"codex":{"sockets":["/run/codex.sock"]},
		"fed":{"mode":"client","hub":"https://hub.example","peerName":"portable",
		       "token":"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"}
	}`)
	config, err := loadWith(
		func(key string) string {
			if key == "BP_HOME" {
				return LegacyHome
			}
			return ""
		},
		os.Stat,
		os.UserHomeDir,
		func(path string) ([]byte, error) {
			if path != LegacyHome+"/config.json" {
				t.Fatalf("read path=%q", path)
			}
			return data, nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if config.MsgqRoot != "/q" || config.StateDir != "/state" || config.WAOutbox != "" || config.WAStore != "/wa.jsonl" || config.UsageBin != "/bin" || config.UsageHistory != "/history.jsonl" || config.ClipboardDir != "/clips" || config.WABridge {
		t.Fatalf("overridden config = %#v", config)
	}
	if !reflect.DeepEqual(config.Agentbooks, []string{"/a.json"}) || config.Fed == nil || config.Fed.Mode != "client" || config.Fed.PeerName != "portable" {
		t.Fatalf("agentbooks/fed = %q / %#v", config.Agentbooks, config.Fed)
	}
	if config.Ntfy == nil || config.Ntfy.URL != "https://ntfy.example" || config.Ntfy.Topic != "alerts" || config.Ntfy.Token != "secret" {
		t.Fatalf("ntfy = %#v", config.Ntfy)
	}
	if config.Codex == nil || !reflect.DeepEqual(config.Codex.Sockets, []string{"/run/codex.sock"}) {
		t.Fatalf("codex = %#v", config.Codex)
	}
	if !reflect.DeepEqual(config.TokenAgentbooks, []string{"/srv/server-main/agentbook.json", "/srv/probot/.orchestration/agentbook.json", "/srv/kitap/.orchestration/agentbook.json"}) {
		t.Fatalf("legacy tokenAgentbooks=%q", config.TokenAgentbooks)
	}
}

func TestNonLegacyDefaults(t *testing.T) {
	config, err := loadWith(
		func(string) string { return "" },
		func(string) (os.FileInfo, error) { return nil, os.ErrNotExist },
		func() (string, error) { return "/Users/example", nil },
		func(string) ([]byte, error) { return nil, os.ErrNotExist },
	)
	if err != nil {
		t.Fatal(err)
	}
	if config.Legacy || config.MsgqRoot != "/Users/example/.blueprint/msgq" || config.StateDir != "/Users/example/.blueprint/state" {
		t.Fatalf("portable defaults = %#v", config)
	}
	if !reflect.DeepEqual(config.Agentbooks, []string{"/Users/example/.blueprint/agentbook.json"}) {
		t.Fatalf("agentbooks=%q", config.Agentbooks)
	}
	if !reflect.DeepEqual(config.TokenAgentbooks, config.Agentbooks) {
		t.Fatalf("tokenAgentbooks=%q, agentbooks=%q", config.TokenAgentbooks, config.Agentbooks)
	}
	if config.WAOutbox != "" || config.WAStore != "" || config.UsageBin != "" || config.UsageHistory != "" || config.ClipboardDir != "" || config.WABridge {
		t.Fatalf("optional integrations should be disabled: %#v", config)
	}
	if config.Fed != nil {
		t.Fatalf("federation should be disabled: %#v", config.Fed)
	}
	if config.Ntfy != nil {
		t.Fatalf("ntfy should be disabled: %#v", config.Ntfy)
	}
	if config.Codex != nil {
		t.Fatalf("codex should be disabled: %#v", config.Codex)
	}
}

func TestInvalidConfigFallsBackToDefaultsWithWarning(t *testing.T) {
	var warning bytes.Buffer
	config, err := loadWithWarning(
		func(key string) string {
			if key == "BP_HOME" {
				return "/tmp/bp-invalid-config-test"
			}
			return ""
		},
		os.Stat,
		os.UserHomeDir,
		func(string) ([]byte, error) { return []byte(`{"fed":`), nil },
		&warning,
	)
	if err != nil {
		t.Fatal(err)
	}
	if config.Home != "/tmp/bp-invalid-config-test" || config.StateDir != "/tmp/bp-invalid-config-test/state" {
		t.Fatalf("did not return portable defaults: %#v", config)
	}
	if config.InvalidConfig == "" {
		t.Fatal("invalid config marker is empty")
	}
	if !strings.Contains(warning.String(), "config.json is invalid, falling back to defaults:") {
		t.Fatalf("warning=%q", warning.String())
	}
}

func TestFederationConfigRequiresLoopbackHubListener(t *testing.T) {
	data := []byte(`{"fed":{"mode":"hub","listen":"0.0.0.0:7877","peerName":"tuna"}}`)
	_, err := loadWith(
		func(key string) string {
			if key == "BP_HOME" {
				return "/tmp/bp-test"
			}
			return ""
		},
		os.Stat,
		os.UserHomeDir,
		func(string) ([]byte, error) { return data, nil },
	)
	if err == nil || !strings.Contains(err.Error(), "loopback") {
		t.Fatalf("error=%v, want loopback validation error", err)
	}
}
