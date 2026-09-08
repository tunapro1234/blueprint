package config

import (
	"os"
	"strings"
	"testing"
)

func TestP2PYAMLAndValidation(t *testing.T) {
	for _, tc := range []struct {
		body    string
		wantErr bool
	}{
		{"p2p:\n  enabled: true\n  listen: [/ip4/127.0.0.1/tcp/0]\n  relay: true\n", false},
		{"p2p:\n  enabled: true\n  rendezvous: [https://example.com]\n", true},
		{"p2p:\n  enabled: true\n  peers:\n    laptop:\n      id: not-a-peer-id\n", true},
		{"p2p:\n  enabled: true\n  invented: yes\n", true},
	} {
		cfg, e := loadWith(func(k string) string {
			if k == "BP_HOME" {
				return "/test-bp"
			}
			return ""
		}, os.Stat, func() (string, error) { return "/home/test", nil }, func(p string) ([]byte, error) {
			if strings.HasSuffix(p, "config.yaml") {
				return []byte(tc.body), nil
			}
			return nil, os.ErrNotExist
		})
		if (e != nil) != tc.wantErr {
			t.Fatalf("%s: %v", tc.body, e)
		}
		if e == nil && (cfg.P2P == nil || !cfg.P2P.Enabled) {
			t.Fatal("p2p settings ignored")
		}
	}
}
