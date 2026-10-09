package config

import (
	"os"
	"path/filepath"
	"strings"
)

// LoadLegacyFixture loads root+"/srv/blueprint" as if it were the server
// installation at /srv/blueprint: every absolute /srv path the legacy loader
// reads is served from under root, and every path in the result is rebased
// under root, so a test exercises the server's code paths without reading or
// writing the live machine.
func LoadLegacyFixture(root string) (Config, error) {
	mapped := func(path string) string {
		if strings.HasPrefix(path, "/srv/") || path == "/srv" {
			return filepath.Join(root, path)
		}
		return path
	}
	cfg, err := loadWithWarning(func(key string) string {
		if key == "BP_HOME" {
			return LegacyHome
		}
		return ""
	}, func(path string) (os.FileInfo, error) { return os.Stat(mapped(path)) },
		func() (string, error) { return filepath.Join(root, "root"), nil },
		func(path string) ([]byte, error) { return os.ReadFile(mapped(path)) }, nil)
	if err != nil {
		return cfg, err
	}
	for _, path := range []*string{&cfg.Path, &cfg.Home, &cfg.MsgqRoot, &cfg.StateDir, &cfg.WAOutbox, &cfg.WAStore, &cfg.UsageBin, &cfg.UsageHistory, &cfg.ClipboardDir} {
		*path = mapped(*path)
	}
	for _, list := range [][]string{cfg.Agentbooks, cfg.TokenAgentbooks} {
		for index := range list {
			list[index] = mapped(list[index])
		}
	}
	return cfg, nil
}
