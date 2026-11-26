package p2p

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"slices"
	"sort"
	"time"

	"blueprint/internal/buildinfo"
)

// The P2P node reads the p2p config section once, when `bp p2p serve` starts.
// It records a fingerprint of that section so a CLI can tell the operator that
// an edited config.json (for example a changed expose list) is not active yet.
// The record holds a hash only, never config text, so it cannot leak anything.

// runningConfigScheme names the canonical serialization hashed below. Change it
// whenever that serialization changes: a record with another scheme is then
// "unknown" instead of a false stale warning after an upgrade.
const runningConfigScheme = "bp-p2p-config-v1"

// RunningConfig is what a P2P node records about itself when it starts.
type RunningConfig struct {
	Scheme      string    `json:"scheme"`
	Fingerprint string    `json:"fingerprint"`
	PID         int       `json:"pid"`
	StartTicks  string    `json:"process_start_ticks,omitempty"`
	Started     time.Time `json:"started"`
}

// RunningConfigPath is the node's record, next to its service lock.
func RunningConfigPath(root string) string { return statePath(root, "running-config.json") }

// ConfigFingerprint is a SHA-256 over a canonical form of the p2p section. It
// depends only on the parsed values: encoding/json writes struct fields in
// declaration order and map keys sorted, so file format, key order and
// whitespace do not matter. Expose lists are sets and are sorted and
// de-duplicated; every other list keeps its order. A nil section (p2p removed
// from the config) has its own fingerprint.
//
// New Config or Peer fields must be omitempty (or the scheme must change), so
// a node started by the previous release still compares equal while its
// config is unchanged.
func ConfigFingerprint(cfg *Config) string {
	var canonical any
	if cfg != nil {
		c := *cfg
		if len(c.Peers) > 0 {
			peers := make(map[string]Peer, len(c.Peers))
			for name, p := range c.Peers {
				expose := append([]string(nil), p.Expose...)
				sort.Strings(expose)
				p.Expose = slices.Compact(expose)
				peers[name] = p
			}
			c.Peers = peers
		}
		canonical = c
	}
	data, err := json.Marshal(canonical)
	if err != nil {
		// Config holds only strings, numbers, bools, slices and maps.
		panic(fmt.Sprintf("p2p config fingerprint: %v", err))
	}
	sum := sha256.Sum256(append([]byte(runningConfigScheme+"\n"), data...))
	return hex.EncodeToString(sum[:])
}

// WriteRunningConfig records the calling process as the node running cfg. The
// file is replaced atomically with mode 0600 and is never removed: a record
// left by a node that has exited fails the liveness check instead.
func WriteRunningConfig(root string, cfg Config) error {
	pid := os.Getpid()
	return atomicJSON(RunningConfigPath(root), RunningConfig{
		Scheme:      runningConfigScheme,
		Fingerprint: ConfigFingerprint(&cfg),
		PID:         pid,
		StartTicks:  buildinfo.ProcessStartTicks(pid),
		Started:     time.Now().UTC(),
	})
}

// ReadRunningConfig reads and checks the record's shape.
func ReadRunningConfig(root string) (RunningConfig, error) {
	var r RunningConfig
	if err := readJSON(RunningConfigPath(root), &r); err != nil {
		return RunningConfig{}, err
	}
	if r.Scheme == "" || r.PID <= 0 || len(r.Fingerprint) != sha256.Size*2 {
		return RunningConfig{}, fmt.Errorf("invalid p2p running-config record")
	}
	if _, err := hex.DecodeString(r.Fingerprint); err != nil {
		return RunningConfig{}, fmt.Errorf("invalid p2p running-config record")
	}
	return r, nil
}

// DriftState says whether the live node runs the config on disk.
type DriftState int

const (
	// DriftUnknown: no live node, no usable record, or the record is not the
	// live node's (an older binary writes none). Callers stay silent.
	DriftUnknown DriftState = iota
	// DriftCurrent: the live node loaded the config that is on disk now.
	DriftCurrent
	// DriftStale: the live node loaded a different p2p section.
	DriftStale
)

// ConfigDrift is the answer for one state dir.
type ConfigDrift struct {
	State   DriftState
	PID     int
	Started time.Time
}

// CompareRunningConfig compares the record with current, the p2p section on
// disk now. livePID is the pid the node's control socket just reported; the
// record counts only if it was written by that very process (same pid and,
// where /proc can say, the same process start time).
func CompareRunningConfig(root string, livePID int, current *Config) ConfigDrift {
	if root == "" || livePID <= 0 {
		return ConfigDrift{}
	}
	r, err := ReadRunningConfig(root)
	if err != nil || r.Scheme != runningConfigScheme || r.PID != livePID {
		return ConfigDrift{}
	}
	if r.StartTicks != "" && buildinfo.ProcessStartTicks(r.PID) != r.StartTicks {
		return ConfigDrift{}
	}
	d := ConfigDrift{State: DriftCurrent, PID: r.PID, Started: r.Started}
	if r.Fingerprint != ConfigFingerprint(current) {
		d.State = DriftStale
	}
	return d
}

// CheckRunningConfig proves a node is live through its control socket (the
// same probe `bp p2p start` and `bp p2p status` use; it is bound only while
// the node holds its service lock) and then compares its record. Without a
// record it does not touch the socket. It never takes the service lock, so it
// cannot make a node that is starting at the same moment fail.
func CheckRunningConfig(ctx context.Context, root string, current *Config) ConfigDrift {
	if root == "" {
		return ConfigDrift{}
	}
	if _, err := ReadRunningConfig(root); err != nil {
		return ConfigDrift{}
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	var info Info
	if err := Control(ctx, root, http.MethodGet, "/status", &info); err != nil {
		return ConfigDrift{}
	}
	return CompareRunningConfig(root, info.PID, current)
}

// Warning is the one-line operator warning for a stale node, or "".
func (d ConfigDrift) Warning() string {
	if d.State != DriftStale {
		return ""
	}
	return fmt.Sprintf("warning: the running P2P node (pid %d, started %s) loaded an older p2p config; "+
		"changes to p2p.* (such as peers.<name>.expose) are not active until the node is restarted "+
		"(restart the service unit that runs bp p2p serve, or run bp p2p stop, wait for it to exit, then bp p2p start)",
		d.PID, d.Started.Local().Format("2006-01-02 15:04:05 MST"))
}
