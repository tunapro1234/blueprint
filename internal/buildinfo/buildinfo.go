// Package buildinfo identifies an executable, not the source tree beside it.
package buildinfo

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
)

type Identity struct {
	StartTicks string `json:"process_start_ticks,omitempty"`
	PID        int    `json:"pid"`
	Executable string `json:"executable,omitempty"`
	SHA256     string `json:"sha256,omitempty"`
	Revision   string `json:"revision,omitempty"`
	Modified   bool   `json:"source_modified"`
}

// Record is written only when the daemon starts, never by status readers.
func Record(path string) error {
	data, err := json.Marshal(Current())
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".runtime-*")
	if err != nil {
		return err
	}
	name := f.Name()
	if _, err = f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

// Recorded returns the startup claim and whether the running executable agrees.
// A stale file or invisible host PID must not masquerade as daemon verification.
func Recorded(path string) (*Identity, string) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, "unknown: no readable daemon identity"
	}
	var i Identity
	if json.Unmarshal(data, &i) != nil || i.PID <= 0 || i.SHA256 == "" {
		return nil, "unknown: invalid daemon identity"
	}
	if i.StartTicks == "" || processStart(fmt.Sprintf("/proc/%d/stat", i.PID)) != i.StartTicks {
		return &i, "unverified: daemon process identity not readable or changed"
	}
	exe, err := os.ReadFile(fmt.Sprintf("/proc/%d/exe", i.PID))
	if err != nil {
		return &i, "unverified: daemon PID not readable"
	}
	if fmt.Sprintf("%x", sha256.Sum256(exe)) != i.SHA256 {
		return &i, "unverified: running executable differs"
	}
	return &i, "verified executable"
}

func Current() Identity {
	i := Identity{PID: os.Getpid(), StartTicks: processStart("/proc/self/stat")}
	i.Executable, _ = os.Executable()
	if data, err := os.ReadFile("/proc/self/exe"); err == nil {
		i.SHA256 = fmt.Sprintf("%x", sha256.Sum256(data))
	} else if data, err := os.ReadFile(i.Executable); err == nil {
		i.SHA256 = fmt.Sprintf("%x", sha256.Sum256(data))
	}
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, s := range info.Settings {
			switch s.Key {
			case "vcs.revision":
				i.Revision = s.Value
			case "vcs.modified":
				i.Modified = s.Value == "true"
			}
		}
	}
	return i
}

func processStart(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	end := strings.LastIndexByte(string(data), ')')
	if end < 0 {
		return ""
	}
	fields := strings.Fields(string(data[end+1:]))
	if len(fields) < 20 {
		return ""
	}
	return fields[19]
}
