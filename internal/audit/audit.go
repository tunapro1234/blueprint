// Package audit keeps the owner-readable log of security-relevant decisions.
//
// Each event is one JSON line in <state>/audit.jsonl. The log is append-only:
// when it grows past MaxBytes it is renamed with a timestamp and a new file
// starts; old files are never removed by bp.
package audit

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// MaxBytes is the size at which the active log is rotated.
const MaxBytes = 32 << 20

// maxField bounds every free-text field so one event cannot flood the log.
const maxField = 512

// Severities.
const (
	Info  = "info"
	Warn  = "warn"
	Alert = "alert"
)

// Event is one audit record. Kind is a dotted name such as
// "p2p.msg.accepted"; Actor is who caused it and Target what it touched.
type Event struct {
	Time     time.Time         `json:"time"`
	Kind     string            `json:"kind"`
	Severity string            `json:"severity"`
	Actor    string            `json:"actor,omitempty"`
	Target   string            `json:"target,omitempty"`
	Peer     string            `json:"peer,omitempty"`
	PeerID   string            `json:"peerId,omitempty"`
	ID       string            `json:"id,omitempty"`
	Reason   string            `json:"reason,omitempty"`
	Fields   map[string]string `json:"fields,omitempty"`
}

// Path is the active log file under a state directory.
func Path(stateDir string) string { return filepath.Join(stateDir, "audit.jsonl") }

// Append writes one event. It never fails the caller's operation for a
// logging problem silently: the error is returned so callers can report it.
func Append(stateDir string, ev Event) error {
	if stateDir == "" {
		return errors.New("audit: no state directory")
	}
	if ev.Time.IsZero() {
		ev.Time = time.Now().UTC()
	}
	if ev.Severity == "" {
		ev.Severity = Info
	}
	ev.Kind = clip(ev.Kind)
	ev.Actor, ev.Target, ev.Peer, ev.PeerID, ev.ID, ev.Reason = clip(ev.Actor), clip(ev.Target), clip(ev.Peer), clip(ev.PeerID), clip(ev.ID), clip(ev.Reason)
	for k, v := range ev.Fields {
		ev.Fields[k] = clip(v)
	}
	line, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	line = append(line, '\n')
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return err
	}
	lock, err := os.OpenFile(Path(stateDir)+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	path := Path(stateDir)
	if st, err := os.Stat(path); err == nil && st.Size()+int64(len(line)) > MaxBytes {
		rotated := filepath.Join(stateDir, "audit-"+time.Now().UTC().Format("20060102T150405Z")+".jsonl")
		if err := os.Rename(path, rotated); err != nil {
			return fmt.Errorf("audit: rotate: %w", err)
		}
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(line)
	return err
}

// Filter selects events for Read. Zero fields match everything.
type Filter struct {
	Since    time.Time
	Kind     string // prefix match on Kind
	Severity string // minimum severity
	Peer     string
	Limit    int // newest N; 0 means all
}

func rank(s string) int {
	switch s {
	case Alert:
		return 2
	case Warn:
		return 1
	}
	return 0
}

// Read returns matching events from the active log, oldest first. Lines that
// do not parse are skipped: the log is evidence, not configuration.
func Read(stateDir string, f Filter) ([]Event, error) {
	file, err := os.Open(Path(stateDir))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()
	var out []Event
	sc := bufio.NewScanner(file)
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	for sc.Scan() {
		var ev Event
		if json.Unmarshal(sc.Bytes(), &ev) != nil {
			continue
		}
		if !f.Since.IsZero() && ev.Time.Before(f.Since) {
			continue
		}
		if f.Kind != "" && !strings.HasPrefix(ev.Kind, f.Kind) {
			continue
		}
		if f.Severity != "" && rank(ev.Severity) < rank(f.Severity) {
			continue
		}
		if f.Peer != "" && ev.Peer != f.Peer {
			continue
		}
		out = append(out, ev)
	}
	if f.Limit > 0 && len(out) > f.Limit {
		out = out[len(out)-f.Limit:]
	}
	return out, sc.Err()
}

func clip(s string) string {
	if len(s) <= maxField {
		return s
	}
	cut := maxField
	for cut > 0 && !utf8Start(s[cut]) {
		cut--
	}
	return s[:cut] + "..."
}

func utf8Start(b byte) bool { return b&0xC0 != 0x80 }
