package buildinfo

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDaemonRecordRejectsReusedPIDAndChangedExecutable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "daemon-runtime.json")
	if err := Record(path); err != nil {
		t.Fatal(err)
	}
	calls := countedHash(t)
	current, verified := Recorded(path)
	if current == nil || current.StartTicks == "" {
		t.Skip("process identity unavailable on this OS")
	}
	if current.ExecutableStat == nil || calls() != 0 {
		t.Fatalf("startup stat=%+v hash computations=%d; matching stat must skip hashing", current.ExecutableStat, calls())
	}
	if verified != "verified executable" {
		t.Fatal(verified)
	}
	for _, change := range []string{"pid-start", "sha"} {
		bad := *current
		if change == "pid-start" {
			bad.StartTicks += "0"
		} else {
			bad.SHA256 = "different"
		}
		data, _ := json.Marshal(bad)
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
		_, status := Recorded(path)
		if !strings.HasPrefix(status, "unverified:") {
			t.Fatalf("stale daemon identified as current: %s", status)
		}
	}
}

func TestCurrentHashCacheUsesUnchangedExecutableStat(t *testing.T) {
	dir := t.TempDir()
	executable := filepath.Join(dir, "bp")
	cache := filepath.Join(dir, "status-executable-hash.json")
	if err := os.WriteFile(executable, []byte("first executable image"), 0700); err != nil {
		t.Fatal(err)
	}
	stat, ok := executableStat(executable)
	if !ok {
		t.Fatal("executable stat unavailable")
	}
	calls := countedHash(t)
	want, err := cachedHash(executable, stat, cache)
	if err != nil {
		t.Fatal(err)
	}
	got, err := cachedHash(executable, stat, cache)
	if err != nil {
		t.Fatal(err)
	}
	if got != want || calls() != 1 {
		t.Fatalf("hash=%q want %q, hash computations=%d want 1", got, want, calls())
	}
}

func TestCurrentHashCacheRehashesReplacementAndInPlaceRewrite(t *testing.T) {
	dir := t.TempDir()
	executable := filepath.Join(dir, "bp")
	cache := filepath.Join(dir, "status-executable-hash.json")
	if err := os.WriteFile(executable, []byte("first image"), 0700); err != nil {
		t.Fatal(err)
	}
	firstStat, ok := executableStat(executable)
	if !ok {
		t.Fatal("executable stat unavailable")
	}
	calls := countedHash(t)
	if _, err := cachedHash(executable, firstStat, cache); err != nil {
		t.Fatal(err)
	}
	replacement := filepath.Join(dir, "replacement")
	if err := os.WriteFile(replacement, []byte("first image"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(replacement, executable); err != nil {
		t.Fatal(err)
	}
	replacedStat, ok := executableStat(executable)
	if !ok || replacedStat.Inode == firstStat.Inode {
		t.Fatalf("replacement inode did not change: before=%+v after=%+v", firstStat, replacedStat)
	}
	if _, err := cachedHash(executable, replacedStat, cache); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(executable, []byte("other image"), 0700); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * time.Millisecond)
	if err := os.Chtimes(executable, time.Now(), time.Now()); err != nil {
		t.Fatal(err)
	}
	rewrittenStat, ok := executableStat(executable)
	if !ok || rewrittenStat.Inode != replacedStat.Inode || *rewrittenStat == *replacedStat {
		t.Fatalf("in-place rewrite did not change tracked identity: before=%+v after=%+v", replacedStat, rewrittenStat)
	}
	if _, err := cachedHash(executable, rewrittenStat, cache); err != nil {
		t.Fatal(err)
	}
	if calls() != 3 {
		t.Fatalf("hash computations=%d want 3", calls())
	}
}

func TestCurrentHashCacheIgnoresCorruptCache(t *testing.T) {
	dir := t.TempDir()
	executable := filepath.Join(dir, "bp")
	cache := filepath.Join(dir, "status-executable-hash.json")
	if err := os.WriteFile(executable, []byte("executable"), 0700); err != nil {
		t.Fatal(err)
	}
	stat, ok := executableStat(executable)
	if !ok {
		t.Fatal("executable stat unavailable")
	}
	if err := os.WriteFile(cache, []byte("{broken"), 0600); err != nil {
		t.Fatal(err)
	}
	calls := countedHash(t)
	first, err := cachedHash(executable, stat, cache)
	if err != nil {
		t.Fatal(err)
	}
	second, err := cachedHash(executable, stat, cache)
	if err != nil {
		t.Fatal(err)
	}
	if first != second || calls() != 1 {
		t.Fatalf("first=%q second=%q hash computations=%d", first, second, calls())
	}
}

func TestRecordedOldIdentityFallsBackToHash(t *testing.T) {
	path := filepath.Join(t.TempDir(), "daemon-runtime.json")
	if err := Record(path); err != nil {
		t.Fatal(err)
	}
	current, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var identity Identity
	if err := json.Unmarshal(current, &identity); err != nil {
		t.Fatal(err)
	}
	identity.ExecutableStat = nil // Runtime records written before stat identities.
	current, err = json.Marshal(identity)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, current, 0600); err != nil {
		t.Fatal(err)
	}
	calls := countedHash(t)
	_, status := Recorded(path)
	if status != "verified executable" || calls() != 1 {
		t.Fatalf("status=%q hash computations=%d; old identity must hash", status, calls())
	}
	_, status = Recorded(path)
	if status != "verified executable" || calls() != 1 {
		t.Fatalf("cached old identity status=%q hash computations=%d", status, calls())
	}
	if err := os.WriteFile(path+".verified-hash.json", []byte("{broken"), 0600); err != nil {
		t.Fatal(err)
	}
	_, status = Recorded(path)
	if status != "verified executable" || calls() != 2 {
		t.Fatalf("corrupt proof cache status=%q hash computations=%d", status, calls())
	}
}

func countedHash(t *testing.T) func() int {
	t.Helper()
	original := hashExecutable
	calls := 0
	hashExecutable = func(path string) (string, error) {
		calls++
		return hashFile(path)
	}
	t.Cleanup(func() { hashExecutable = original })
	return func() int { return calls }
}

func TestExecutableStatFieldsAreNanosecondValues(t *testing.T) {
	path := filepath.Join(t.TempDir(), "executable")
	if err := os.WriteFile(path, []byte("x"), 0700); err != nil {
		t.Fatal(err)
	}
	stat, ok := executableStat(path)
	if !ok || stat.Size != 1 || stat.Device == 0 || stat.Inode == 0 || stat.MtimeNS == 0 || stat.CtimeNS == 0 {
		t.Fatal(fmt.Sprintf("unexpected stat identity: %+v", stat))
	}
}
