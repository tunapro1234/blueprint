package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"blueprint/internal/cache"
	bpconfig "blueprint/internal/config"
	bptmux "blueprint/internal/tmux"
)

func TestBarOutputCacheHitsAvoidRuntimeReads(t *testing.T) {
	stateDir := t.TempDir()
	callLog := filepath.Join(t.TempDir(), "tmux.log")
	t.Setenv("BAR_TEST_TMUX_LOG", callLog)
	tmux := barCacheTestTmux(t)
	cacheReads := 0
	a := &app{
		ctx:    context.Background(),
		tmux:   tmux,
		out:    testOutput(t),
		config: bpconfig.Config{StateDir: stateDir},
		loadCache: func(map[string]string) map[string]cache.State {
			cacheReads++
			return nil
		},
	}

	for _, tc := range []struct {
		name   string
		suffix string
		want   string
		run    func() error
	}{
		{name: "bar", suffix: ".txt", want: "cached bar", run: func() error { return a.bar([]string{"agent"}) }},
		{name: "name", suffix: ".name", want: "cached name", run: func() error {
			return a.run([]string{"name", "agent"})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a.out = testOutput(t)
			writeBarTestFile(t, filepath.Join(stateDir, "bar", "agent"+tc.suffix), tc.want+"\n")
			if err := tc.run(); err != nil {
				t.Fatal(err)
			}
			if got := readTestOutput(t, a.out); got != tc.want+"\n" {
				t.Fatalf("output=%q, want %q", got, tc.want+"\n")
			}
			if data, err := os.ReadFile(callLog); err == nil && len(data) != 0 {
				t.Fatalf("cache hit called tmux: %s", data)
			} else if err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			}
			if cacheReads != 0 {
				t.Fatalf("cache hit performed %d runtime reads, want zero", cacheReads)
			}
			if err := os.Remove(callLog); err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			}
		})
	}
}

func TestBarOutputCacheExpiryRecomputes(t *testing.T) {
	for _, suffix := range []string{".txt", ".name"} {
		t.Run(suffix, func(t *testing.T) {
			stateDir := t.TempDir()
			a := &app{config: bpconfig.Config{StateDir: stateDir}}
			path := a.barCachePath("agent", suffix)
			writeBarTestFile(t, path, "expired\n")
			old := time.Now().Add(-barCacheTTL - time.Second)
			if err := os.Chtimes(path, old, old); err != nil {
				t.Fatal(err)
			}

			calls := 0
			got := a.cachedBarOutput("agent", suffix, func() string {
				calls++
				return "fresh"
			})
			if got != "fresh" || calls != 1 {
				t.Fatalf("output=%q compute calls=%d, want fresh/1", got, calls)
			}
			if got, _, ok := readBarCache(path); !ok || got != "fresh" {
				t.Fatalf("cache=%q, present=%v; want fresh cache", got, ok)
			}
		})
	}
}

func TestBarOutputCacheHeldLockUsesStaleOrComputes(t *testing.T) {
	for _, suffix := range []string{".txt", ".name"} {
		for _, stale := range []bool{true, false} {
			name := "cold lock computes"
			if stale {
				name = "stale cache survives held lock"
			}
			t.Run(suffix+"/"+name, func(t *testing.T) {
				stateDir := t.TempDir()
				a := &app{config: bpconfig.Config{StateDir: stateDir}}
				path := a.barCachePath("agent", suffix)
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if stale {
					writeBarTestFile(t, path, "stale line\n")
					old := time.Now().Add(-barCacheTTL - time.Second)
					if err := os.Chtimes(path, old, old); err != nil {
						t.Fatal(err)
					}
				}

				lock, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0o644)
				if err != nil {
					t.Fatal(err)
				}
				defer lock.Close()
				if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
					t.Fatal(err)
				}
				defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN) //nolint:errcheck

				calls := 0
				got := a.cachedBarOutput("agent", suffix, func() string {
					calls++
					return "computed line"
				})
				if stale {
					if got != "stale line" || calls != 0 {
						t.Fatalf("output=%q compute calls=%d, want stale line/0", got, calls)
					}
					return
				}
				if got != "computed line" || calls != 1 {
					t.Fatalf("output=%q compute calls=%d, want computed line/1", got, calls)
				}
				if data, err := os.ReadFile(path); err != nil || string(data) != "computed line\n" {
					t.Fatalf("cold held-lock cache=%q, err=%v", data, err)
				}
				info, err := os.Stat(path)
				if err != nil {
					t.Fatal(err)
				}
				if got := info.Mode().Perm(); got != 0o644 {
					t.Fatalf("cache mode=%#o, want 0644", got)
				}
			})
		}
	}
}

func TestBarNameMissReadsRuntimeStateOnce(t *testing.T) {
	dir := t.TempDir()
	bookPath := filepath.Join(dir, "agentbook.json")
	writeBarTestFile(t, bookPath, `{"agents":[{"name":"agent","folder":"/missing-agent-folder"}]}`)
	callLog := filepath.Join(dir, "tmux.log")
	t.Setenv("BAR_TEST_TMUX_LOG", callLog)
	a := &app{
		ctx:    context.Background(),
		tmux:   barCacheTestTmux(t),
		out:    testOutput(t),
		config: bpconfig.Config{Agentbooks: []string{bookPath}, StateDir: filepath.Join(dir, "state")},
	}

	line := a.barName("agent")
	if !strings.Contains(line, "fg=colour178") || !strings.Contains(line, " agent ") {
		t.Fatalf("name plate=%q, want accent 178 and agent label", line)
	}
	data, err := os.ReadFile(callLog)
	if err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	for _, command := range strings.Fields(string(data)) {
		counts[command]++
	}
	if counts["list-panes"] != 2 {
		t.Fatalf("list-panes calls=%d (%q), want two: one harness check plus one RuntimeState", counts["list-panes"], data)
	}
	if counts["capture-pane"] != 2 {
		t.Fatalf("capture-pane calls=%d (%q), want one RuntimeState capture plus one accent read", counts["capture-pane"], data)
	}
	if counts["show-options"] != 1 {
		t.Fatalf("show-options calls=%d, want one style check", counts["show-options"])
	}
}

func barCacheTestTmux(t *testing.T) *bptmux.Client {
	t.Helper()
	path := filepath.Join(t.TempDir(), "tmux")
	script := `#!/bin/sh
printf '%s\n' "$1" >> "$BAR_TEST_TMUX_LOG"
case "$1" in
  list-panes) printf '1\tclaude\t9999999\n' ;;
  capture-pane) printf '\033[48;5;178m agent \033[0m\n' ;;
  show-options) printf 'bg=colour178,fg=colour16\n' ;;
esac
`
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return &bptmux.Client{Bin: path, Sleep: func(time.Duration) {}, Now: time.Now}
}
