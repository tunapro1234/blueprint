// Package buildinfo identifies an executable, not the source tree beside it.
package buildinfo

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"syscall"
)

type Identity struct {
	StartTicks     string          `json:"process_start_ticks,omitempty"`
	PID            int             `json:"pid"`
	Executable     string          `json:"executable,omitempty"`
	SHA256         string          `json:"sha256,omitempty"`
	ExecutableStat *ExecutableStat `json:"executable_stat,omitempty"`
	Revision       string          `json:"revision,omitempty"`
	Modified       bool            `json:"source_modified"`
}

// ExecutableStat is the filesystem identity used to validate a recorded hash.
// ctime changes on in-place writes even when a writer restores mtime.
type ExecutableStat struct {
	Device  uint64 `json:"device"`
	Inode   uint64 `json:"inode"`
	Size    int64  `json:"size"`
	MtimeNS int64  `json:"mtime_ns"`
	CtimeNS int64  `json:"ctime_ns"`
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
	if !validSHA256(i.SHA256) {
		return &i, "unverified: running executable differs"
	}
	if i.StartTicks == "" || processStart(fmt.Sprintf("/proc/%d/stat", i.PID)) != i.StartTicks {
		return &i, "unverified: daemon process identity not readable or changed"
	}
	exePath := fmt.Sprintf("/proc/%d/exe", i.PID)
	current, statOK := executableStat(exePath)
	if statOK && i.ExecutableStat != nil && *current == *i.ExecutableStat {
		return &i, "verified executable"
	}
	cachePath := path + ".verified-hash.json"
	if statOK && verifiedDaemonHashCache(cachePath, i, *current) {
		return &i, "verified executable"
	}
	hash, err := hashExecutable(exePath)
	if err != nil {
		return &i, "unverified: daemon PID not readable"
	}
	if hash != i.SHA256 {
		return &i, "unverified: running executable differs"
	}
	if statOK {
		data, err := json.Marshal(verifiedDaemonHash{PID: i.PID, StartTicks: i.StartTicks, Stat: *current, SHA256: i.SHA256})
		if err == nil {
			_ = writeAtomic(cachePath, data)
		}
	}
	return &i, "verified executable"
}

func Current() Identity {
	i := Identity{PID: os.Getpid(), StartTicks: processStart("/proc/self/stat")}
	i.Executable, _ = os.Executable()
	i.ExecutableStat, _ = executableStat("/proc/self/exe")
	i.SHA256, _ = cachedHash("/proc/self/exe", i.ExecutableStat, "")
	if i.SHA256 == "" {
		i.ExecutableStat, _ = executableStat(i.Executable)
		i.SHA256, _ = cachedHash(i.Executable, i.ExecutableStat, "")
	}
	return addBuildRevision(i)
}

// CurrentCached is Current with a persistent executable hash cache. Status uses
// this form because it starts a fresh CLI process for each poll; other callers
// keep the uncached Current behavior.
func CurrentCached(cachePath string) Identity {
	i := Identity{PID: os.Getpid(), StartTicks: processStart("/proc/self/stat")}
	i.Executable, _ = os.Executable()
	i.ExecutableStat, _ = executableStat("/proc/self/exe")
	i.SHA256, _ = cachedHash("/proc/self/exe", i.ExecutableStat, cachePath)
	if i.SHA256 == "" {
		i.ExecutableStat, _ = executableStat(i.Executable)
		i.SHA256, _ = cachedHash(i.Executable, i.ExecutableStat, cachePath)
	}
	return addBuildRevision(i)
}

func addBuildRevision(i Identity) Identity {
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

type hashCache struct {
	Stat   ExecutableStat `json:"stat"`
	SHA256 string         `json:"sha256"`
}

type verifiedDaemonHash struct {
	PID        int            `json:"pid"`
	StartTicks string         `json:"process_start_ticks"`
	Stat       ExecutableStat `json:"stat"`
	SHA256     string         `json:"sha256"`
}

func verifiedDaemonHashCache(path string, identity Identity, stat ExecutableStat) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var cached verifiedDaemonHash
	return json.Unmarshal(data, &cached) == nil && cached.PID == identity.PID && cached.StartTicks == identity.StartTicks && cached.Stat == stat && cached.SHA256 == identity.SHA256 && validSHA256(cached.SHA256)
}

var hashExecutable = hashFile

func cachedHash(path string, stat *ExecutableStat, cachePath string) (string, error) {
	if stat != nil && cachePath != "" {
		if data, err := os.ReadFile(cachePath); err == nil {
			var cached hashCache
			if json.Unmarshal(data, &cached) == nil && cached.Stat == *stat && validSHA256(cached.SHA256) {
				return cached.SHA256, nil
			}
		}
	}
	hash, err := hashExecutable(path)
	if err != nil || stat == nil || cachePath == "" {
		return hash, err
	}
	data, err := json.Marshal(hashCache{Stat: *stat, SHA256: hash})
	if err != nil {
		return hash, nil
	}
	if err := writeAtomic(cachePath, data); err != nil {
		return hash, nil
	}
	return hash, nil
}

func validSHA256(hash string) bool {
	if len(hash) != sha256.Size*2 {
		return false
	}
	for _, b := range hash {
		if !((b >= '0' && b <= '9') || (b >= 'a' && b <= 'f')) {
			return false
		}
	}
	return true
}

func executableStat(path string) (*ExecutableStat, bool) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, false
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return nil, false
	}
	return &ExecutableStat{
		Device:  uint64(st.Dev),
		Inode:   st.Ino,
		Size:    info.Size(),
		MtimeNS: st.Mtim.Sec*1e9 + st.Mtim.Nsec,
		CtimeNS: st.Ctim.Sec*1e9 + st.Ctim.Nsec,
	}, true
}

func hashFile(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", hash.Sum(nil)), nil
}

func writeAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".executable-hash-*")
	if err != nil {
		return err
	}
	tmp := file.Name()
	defer os.Remove(tmp)
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
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
