package config

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestMonitorDefaults(t *testing.T) {
	config, err := loadWith(
		func(string) string { return "" },
		func(string) (os.FileInfo, error) { return fakeFileInfo{}, nil },
		func() (string, error) { return "/Users/example", nil },
		func(string) ([]byte, error) { return nil, os.ErrNotExist },
	)
	if err != nil {
		t.Fatal(err)
	}
	if config.Monitor.UsagePulseMinutes != 5 || config.Monitor.DashboardGenMinutes != 15 {
		t.Fatalf("default monitor = %#v", config.Monitor)
	}
	if got := config.Monitor.UsagePulseInterval(); got != 5*time.Minute {
		t.Fatalf("default usage-pulse interval = %s, want 5m", got)
	}
	if got := config.Monitor.DashboardGenInterval(); got != 15*time.Minute {
		t.Fatalf("default dashboard-gen interval = %s, want 15m", got)
	}
}

func TestMonitorOverrides(t *testing.T) {
	data := []byte(`{"monitor":{"usagePulseMinutes":3,"dashboardGenMinutes":30}}`)
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
			if !strings.HasSuffix(path, "config.json") {
				return nil, os.ErrNotExist
			}
			return data, nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if config.Monitor.UsagePulseMinutes != 3 || config.Monitor.DashboardGenMinutes != 30 {
		t.Fatalf("overridden monitor = %#v", config.Monitor)
	}
	if got := config.Monitor.UsagePulseInterval(); got != 3*time.Minute {
		t.Fatalf("usage-pulse interval = %s, want 3m", got)
	}
	if got := config.Monitor.DashboardGenInterval(); got != 30*time.Minute {
		t.Fatalf("dashboard-gen interval = %s, want 30m", got)
	}
}

func TestMonitorPartialOverrideKeepsOtherDefault(t *testing.T) {
	data := []byte(`{"monitor":{"dashboardGenMinutes":20}}`)
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
			if !strings.HasSuffix(path, "config.json") {
				return nil, os.ErrNotExist
			}
			return data, nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	// usage-pulse untouched -> default 5; dashboard-gen overridden -> 20.
	if config.Monitor.UsagePulseMinutes != 5 || config.Monitor.DashboardGenMinutes != 20 {
		t.Fatalf("partial-override monitor = %#v", config.Monitor)
	}
}

func TestMonitorIntervalClampsNonPositive(t *testing.T) {
	cases := []struct {
		name    string
		cfg     MonitorConfig
		pulse   time.Duration
		dashGen time.Duration
	}{
		{"zero falls back", MonitorConfig{UsagePulseMinutes: 0, DashboardGenMinutes: 0}, 5 * time.Minute, 15 * time.Minute},
		{"negative falls back", MonitorConfig{UsagePulseMinutes: -3, DashboardGenMinutes: -1}, 5 * time.Minute, 15 * time.Minute},
		{"one is honored", MonitorConfig{UsagePulseMinutes: 1, DashboardGenMinutes: 1}, 1 * time.Minute, 1 * time.Minute},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.cfg.UsagePulseInterval(); got != tc.pulse {
				t.Fatalf("usage-pulse interval = %s, want %s", got, tc.pulse)
			}
			if got := tc.cfg.DashboardGenInterval(); got != tc.dashGen {
				t.Fatalf("dashboard-gen interval = %s, want %s", got, tc.dashGen)
			}
		})
	}
}
