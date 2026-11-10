package identity

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"blueprint/internal/messagetext"
)

func cgroupAt(path string) func() string { return func() string { return path } }

// knownHostsOnly is HostUnit without the live cgroup.procs scan.
func knownHostsOnly(_, unit string) bool { return hostUnits[unit] }

func TestResolveSystemdUnit(t *testing.T) {
	const inTmux = "/tmp/tmux-0/default,4242,0"
	script := fixedAncestors([]string{"/bin/bash", "/srv/jobs/check.sh"})
	tests := []struct {
		name        string
		env         map[string]string
		opts        Options
		session     string
		wantLabel   string
		wantCertain bool
		wantSource  string
	}{
		{
			name:        "a named system service signs as the unit",
			opts:        Options{Infer: true, Ancestors: fixedAncestors(), Cgroup: cgroupAt("/system.slice/cfv-gsync-check.service")},
			wantLabel:   "service:cfv-gsync-check",
			wantCertain: true,
			wantSource:  "systemd-unit",
		},
		{
			name:        "a named unit outranks the process-tree guess",
			opts:        Options{Infer: true, Ancestors: script, Cgroup: cgroupAt("/system.slice/report.service")},
			wantLabel:   "service:report",
			wantCertain: true,
			wantSource:  "systemd-unit",
		},
		{
			name:        "a templated unit in a sub-slice keeps its instance",
			opts:        Options{Cgroup: cgroupAt("/system.slice/system-backup.slice/backup@disk2.service")},
			wantLabel:   "service:backup-disk2",
			wantCertain: true,
			wantSource:  "systemd-unit",
		},
		{
			name:        "systemd escapes are decoded",
			opts:        Options{Cgroup: cgroupAt(`/system.slice/my\x2djob.service`)},
			wantLabel:   "service:my-job",
			wantCertain: true,
			wantSource:  "systemd-unit",
		},
		{
			name:       "a scope is not a service",
			opts:       Options{Infer: true, Ancestors: fixedAncestors(), Cgroup: cgroupAt("/system.slice/run-r1a2b.scope")},
			wantLabel:  Unknown,
			wantSource: "none",
		},
		{
			name:       "a login session scope is not a service",
			opts:       Options{Infer: true, Ancestors: fixedAncestors(), Cgroup: cgroupAt("/user.slice/user-0.slice/session-3.scope")},
			wantLabel:  Unknown,
			wantSource: "none",
		},
		{
			name:       "a user manager's subtree is not a system service",
			opts:       Options{Infer: true, Ancestors: fixedAncestors(), Cgroup: cgroupAt("/user.slice/user-1000.slice/user@1000.service/app.slice/sync.service")},
			wantLabel:  Unknown,
			wantSource: "none",
		},
		{
			name:       "a sub-cgroup delegated inside a service is not the service",
			opts:       Options{Infer: true, Ancestors: fixedAncestors(), Cgroup: cgroupAt("/system.slice/container.service/payload")},
			wantLabel:  Unknown,
			wantSource: "none",
		},
		{
			name:       "cron hosts other people's jobs and keeps the guess",
			opts:       Options{Infer: true, Ancestors: fixedAncestors([]string{"/bin/sh", "-c", "/srv/jobs/inbox.py"}, []string{"/usr/sbin/CRON", "-f"}), Cgroup: cgroupAt("/system.slice/cron.service")},
			wantLabel:  "cron?:inbox.py",
			wantSource: "process-tree",
		},
		{
			name:       "ssh hosts other people's shells",
			opts:       Options{Infer: true, Ancestors: fixedAncestors(), Cgroup: cgroupAt("/system.slice/ssh.service")},
			wantLabel:  Unknown,
			wantSource: "none",
		},
		{
			name:       "the bp daemon's conventional unit is a host by name",
			opts:       Options{Infer: true, Ancestors: script, Cgroup: cgroupAt("/system.slice/blueprint.service"), HostUnit: nil},
			wantLabel:  "bash?:check.sh",
			wantSource: "process-tree",
		},
		{
			name: "a unit that holds the tmux server is a host whatever its name",
			opts: Options{Infer: true, Ancestors: script, Cgroup: cgroupAt("/system.slice/agents.service"),
				HostUnit: func(cgroup, unit string) bool { return cgroup == "/system.slice/agents.service" && unit == "agents" }},
			wantLabel:  "bash?:check.sh",
			wantSource: "process-tree",
		},
		{
			name:       "a transient unit keeps the process-tree guess",
			opts:       Options{Infer: true, Ancestors: script, Cgroup: cgroupAt("/system.slice/run-u73908.service")},
			wantLabel:  "bash?:check.sh",
			wantSource: "process-tree",
		},
		{
			name:        "a transient unit names itself when there is no guess",
			opts:        Options{Infer: true, Ancestors: fixedAncestors(), Cgroup: cgroupAt("/system.slice/run-u73908.service")},
			wantLabel:   "service:run-u73908",
			wantCertain: true,
			wantSource:  "systemd-unit",
		},
		{
			name:        "a random-id transient unit names itself when there is no guess",
			opts:        Options{Infer: true, Ancestors: fixedAncestors(), Cgroup: cgroupAt("/system.slice/run-r0d1e2f3a4b5c.service")},
			wantLabel:   "service:run-r0d1e2f3a4b5c",
			wantCertain: true,
			wantSource:  "systemd-unit",
		},
		{
			name:        "a transient unit outranks a call site fallback",
			opts:        Options{Cgroup: cgroupAt("/system.slice/run-p4242-i4243.service"), Fallback: "server-main"},
			wantLabel:   "service:run-p4242-i4243",
			wantCertain: true,
			wantSource:  "systemd-unit",
		},
		{
			name:        "a tmux pane outranks the unit",
			env:         map[string]string{"TMUX": inTmux},
			session:     "writer",
			opts:        Options{Cgroup: cgroupAt("/system.slice/report.service")},
			wantLabel:   "writer",
			wantCertain: true,
			wantSource:  "tmux",
		},
		{
			name: "pane process context outranks the unit",
			opts: Options{Cgroup: cgroupAt("/system.slice/report.service"), Known: knownSet("writer"),
				Pane: func(context.Context) (string, error) { return "writer", nil }},
			wantLabel:  "writer?",
			wantSource: "pane-process-context",
		},
		{
			name:        "a verified daemon child outranks the unit",
			env:         map[string]string{"AGENT": "whatsapp"},
			opts:        Options{Cgroup: cgroupAt("/system.slice/report.service"), DaemonChild: func(int) bool { return true }},
			wantLabel:   "whatsapp",
			wantCertain: true,
			wantSource:  "bp-daemon-child",
		},
		{
			name:       "an AGENT guess outranks the unit",
			env:        map[string]string{"AGENT": "mailer"},
			opts:       Options{Cgroup: cgroupAt("/system.slice/report.service"), DaemonChild: func(int) bool { return false }},
			wantLabel:  "agent?:mailer",
			wantSource: "AGENT",
		},
		{
			name:        "a login name outranks the unit",
			env:         map[string]string{"USER": "deploy"},
			opts:        Options{Cgroup: cgroupAt("/system.slice/report.service")},
			wantLabel:   "deploy",
			wantCertain: true,
			wantSource:  "USER",
		},
		{
			name:        "marked --from inside a unit is scoped by the unit",
			opts:        Options{From: "nightly sync", FromMarked: true, Cgroup: cgroupAt("/system.slice/report.service")},
			wantLabel:   "service:report/nightly sync",
			wantCertain: true,
			wantSource:  "systemd-unit",
		},
		{
			name:        "marked --from inside a transient unit is scoped by the unit",
			opts:        Options{From: "probe", FromMarked: true, Infer: true, Ancestors: script, Cgroup: cgroupAt("/system.slice/run-u12.service")},
			wantLabel:   "service:run-u12/probe",
			wantCertain: true,
			wantSource:  "systemd-unit",
		},
		{
			name:       "marked --from elsewhere is a declaration",
			env:        map[string]string{"SUDO_USER": "tunapro"},
			opts:       Options{From: "tuna pro", FromMarked: true, Infer: true, Ancestors: script},
			wantLabel:  "declared?:tuna pro",
			wantSource: "--from",
		},
		{
			name:       "marked --from in a host unit is a declaration",
			opts:       Options{From: "cron:/srv/jobs/inbox.py", FromMarked: true, Cgroup: cgroupAt("/system.slice/cron.service")},
			wantLabel:  "declared?:cron:/srv/jobs/inbox.py",
			wantSource: "--from",
		},
		{
			name:        "marked --from is ignored by a verified daemon child",
			env:         map[string]string{"AGENT": "whatsapp"},
			opts:        Options{From: "someone", FromMarked: true, DaemonChild: func(int) bool { return true }},
			wantLabel:   "whatsapp",
			wantCertain: true,
			wantSource:  "bp-daemon-child",
		},
		{
			name:        "marked --from is ignored inside a pane",
			env:         map[string]string{"TMUX": inTmux},
			session:     "writer",
			opts:        Options{From: "someone", FromMarked: true},
			wantLabel:   "writer",
			wantCertain: true,
			wantSource:  "tmux",
		},
		{
			name:        "wa's unmarked --from keeps the bare stated label",
			opts:        Options{From: "cron:/srv/jobs/inbox.py", Cgroup: cgroupAt("/system.slice/report.service")},
			wantLabel:   "cron:/srv/jobs/inbox.py",
			wantCertain: true,
			wantSource:  "--from",
		},
		{
			name:       "wa's unmarked agent-shaped --from stays declared",
			opts:       Options{From: "writer"},
			wantLabel:  "declared?:writer",
			wantSource: "--from",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			setEnv(t, test.env)
			test.opts.Origin = noCodexOrigin
			if test.opts.Cgroup == nil {
				test.opts.Cgroup = cgroupAt("")
			}
			if test.opts.HostUnit == nil {
				test.opts.HostUnit = knownHostsOnly
			}
			client := &fakeSession{name: test.session}
			got := Resolve(context.Background(), client, test.opts)
			if got.Label != test.wantLabel || got.Certain != test.wantCertain || got.Source != test.wantSource {
				t.Fatalf("Resolve() = %+v, want label=%q certain=%v source=%q", got, test.wantLabel, test.wantCertain, test.wantSource)
			}
			if got.Authoritative() && got.Source != "tmux" {
				t.Fatalf("%s identity became authoritative: %+v", got.Source, got)
			}
			if strings.HasPrefix(got.Label, unitPrefix) {
				if got.Authoritative() || ValidName(got.Label) {
					t.Fatalf("unit label %q is agent-shaped or authoritative", got.Label)
				}
				if err := messagetext.Sender(got.Label); err != nil {
					t.Fatalf("unit label %q is not a usable sender: %v", got.Label, err)
				}
			}
			wantDeclared := ""
			if strings.Contains(got.Label, test.opts.From) && test.opts.From != "" {
				wantDeclared = test.opts.From
			}
			if got.Declared != wantDeclared {
				t.Fatalf("Declared=%q, want %q (%+v)", got.Declared, wantDeclared, got)
			}
		})
	}
}

func TestParseSystemUnitBoundsAndShape(t *testing.T) {
	long := "/system.slice/" + strings.Repeat("a", 200) + ".service"
	unit, ok := parseSystemUnit(long)
	if !ok || len(unit) > maxUnit {
		t.Fatalf("long unit = %q (%v), want at most %d bytes", unit, ok, maxUnit)
	}
	for _, path := range []string{
		"", "/", "/system.slice", "/system.slice/.service", "/system.slice/x.socket",
		"/system.slice/user@0.service", "/init.scope", "/system.slice/[x].service/..",
	} {
		if unit, ok := parseSystemUnit(path); ok {
			t.Errorf("parseSystemUnit(%q) = %q, want rejection", path, unit)
		}
	}
	unit, ok = parseSystemUnit("/system.slice/we[ir]d\nname.service")
	if !ok || strings.ContainsAny(unit, "[]\n") {
		t.Fatalf("unsafe unit name survived: %q (%v)", unit, ok)
	}
}

func TestParseCgroupFile(t *testing.T) {
	for data, want := range map[string]string{
		"0::/system.slice/report.service\n":                                    "/system.slice/report.service",
		"12:cpu:/x\n1:name=systemd:/system.slice/legacy.service\n":             "/system.slice/legacy.service",
		"1:name=systemd:/system.slice/a.service\n0::/system.slice/b.service\n": "/system.slice/b.service",
		"": "",
	} {
		if got := parseCgroupFile(data); got != want {
			t.Errorf("parseCgroupFile(%q) = %q, want %q", data, got, want)
		}
	}
}

// TestHostsOthersFindsTheTmuxServerOrDaemon builds a fake /sys/fs/cgroup and
// /proc: a unit that holds a tmux server or a bp daemon is a host under any
// name, one that holds only ordinary programs is not.
func TestHostsOthersFindsTheTmuxServerOrDaemon(t *testing.T) {
	root := t.TempDir()
	cgroupRoot, procRoot := filepath.Join(root, "cgroup"), filepath.Join(root, "proc")
	unitProcs := func(cgroup string, pids ...string) {
		dir := filepath.Join(cgroupRoot, cgroup)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "cgroup.procs"), []byte(strings.Join(pids, "\n")+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	process := func(pid, comm, exe string, args ...string) {
		dir := filepath.Join(procRoot, pid)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "comm"), []byte(comm+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "cmdline"), []byte(strings.Join(args, "\x00")+"\x00"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(exe, filepath.Join(dir, "exe")); err != nil {
			t.Fatal(err)
		}
	}
	process("100", "tmux: server", "/usr/bin/tmux", "tmux", "new-session", "-d")
	process("200", "bp", "/opt/bp/bp", "/opt/bp/bp", "daemon")
	process("300", "python3", "/usr/bin/python3", "python3", "/srv/jobs/report.py")
	process("400", "bp", "/opt/bp/bp", "bp", "msg", "x", "y")
	unitProcs("system.slice/agents.service", "100", "300")
	unitProcs("system.slice/supervisor.service", "200")
	unitProcs("system.slice/report.service", "300", "400")
	// The caller alone in its unit: its own pid is skipped, nothing hosts.
	unitProcs("system.slice/self.service", strconv.Itoa(os.Getpid()))
	// cgroup.procs that cannot be read (here a directory) proves nothing.
	if err := os.MkdirAll(filepath.Join(cgroupRoot, "system.slice/unreadable.service", "cgroup.procs"), 0o755); err != nil {
		t.Fatal(err)
	}

	for cgroup, want := range map[string]bool{
		"/system.slice/agents.service":     true,
		"/system.slice/supervisor.service": true,
		"/system.slice/report.service":     false,
		"/system.slice/missing.service":    false,
		"/system.slice/self.service":       false,
		"/system.slice/unreadable.service": false,
	} {
		unit, _ := parseSystemUnit(cgroup)
		if got := hostsOthersAt(cgroupRoot, procRoot, cgroup, unit); got != want {
			t.Errorf("hostsOthersAt(%s) = %v, want %v", cgroup, got, want)
		}
	}
	if !hostsOthersAt(cgroupRoot, procRoot, "/system.slice/sshd.service", "sshd") {
		t.Error("a named host unit must not need a cgroup scan")
	}
}
