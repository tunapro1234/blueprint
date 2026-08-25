package pending

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// A message with embedded line breaks must stay ONE record: the spool is
// line-delimited, so an unescaped newline would be read back as extra records
// with empty fields instead of as text.
func TestSpoolRecordSurvivesEmbeddedNewlines(t *testing.T) {
	dir := t.TempDir()
	text := "first line\nsecond line\r\nthird line\n"
	entry := Entry{TS: time.Now().Unix(), From: "ada\nbda", Kind: "msg", Text: text}
	if err := Append(dir, "alp", entry); err != nil {
		t.Fatal(err)
	}
	entries, dropped, err := Load(dir, "alp")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || dropped != 0 {
		t.Fatalf("len=%d dropped=%d, want 1/0 — the newlines split the record", len(entries), dropped)
	}
	if entries[0].Text != text || entries[0].From != "ada\nbda" {
		t.Fatalf("round trip lost content: %+v", entries[0])
	}
}

// A single spooled message larger than bufio.Scanner's 64 KiB default used to
// make the whole file unreadable ("token too long"), so every later Load failed
// and the queued messages were stranded for good.
func TestLoadReadsRecordLargerThanDefaultScannerBuffer(t *testing.T) {
	dir := t.TempDir()
	big := strings.Repeat("x", 200*1024)
	now := time.Now()
	if err := Append(dir, "alp", Entry{TS: now.Add(-time.Minute).Unix(), From: "ada", Kind: "msg", Text: big}); err != nil {
		t.Fatal(err)
	}
	if err := Append(dir, "alp", Entry{TS: now.Unix(), From: "ada", Kind: "msg", Text: "after"}); err != nil {
		t.Fatal(err)
	}
	entries, dropped, err := Load(dir, "alp")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(entries) != 2 || dropped != 0 {
		t.Fatalf("len=%d dropped=%d, want 2/0", len(entries), dropped)
	}
	if entries[0].Text != big || entries[1].Text != "after" {
		t.Fatalf("oversized record did not round trip (len=%d)", len(entries[0].Text))
	}
}

// Whatever Append accepts, Load must be able to read back: a record over the
// store's own limit is refused at write time rather than jamming the spool.
func TestAppendRefusesRecordOverStoreLimit(t *testing.T) {
	dir := t.TempDir()
	entry := Entry{TS: time.Now().Unix(), From: "ada", Kind: "msg", Text: strings.Repeat("x", maxRecordBytes)}
	if err := Append(dir, "alp", entry); err == nil {
		t.Fatal("append accepted a record the reader cannot read back")
	}
	entries, _, err := Load(dir, "alp")
	if err != nil {
		t.Fatalf("load after refused append: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("len=%d, want 0 — nothing should have been written", len(entries))
	}
}

// Peek is the read-only twin of Load. A read that prunes is how spooled
// messages disappeared with nobody told: bp q counted every agent's queue with
// Load and threw the drop count away.
func TestPeekLeavesSpoolUntouchedAndAgreesWithLoad(t *testing.T) {
	dir := t.TempDir()
	crowdedSpool(t, dir, "alp")
	file := path(dir, "alp")

	before, err := os.Stat(file)
	if err != nil {
		t.Fatal(err)
	}
	bytesBefore, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}

	peeked, over, err := Peek(dir, "alp")
	if err != nil {
		t.Fatal(err)
	}
	if len(peeked) != maxItems || over != 6 {
		t.Fatalf("peek len=%d over=%d, want %d/6", len(peeked), over, maxItems)
	}
	items, alsoOver, err := Stat(dir, "alp")
	if err != nil {
		t.Fatal(err)
	}
	if items != len(peeked) || alsoOver != over {
		t.Fatalf("stat=%d/%d, want %d/%d", items, alsoOver, len(peeked), over)
	}

	after, err := os.Stat(file)
	if err != nil {
		t.Fatal(err)
	}
	bytesAfter, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if after.Size() != before.Size() || !after.ModTime().Equal(before.ModTime()) {
		t.Fatalf("peek rewrote the spool: %d/%v -> %d/%v",
			before.Size(), before.ModTime(), after.Size(), after.ModTime())
	}
	if string(bytesAfter) != string(bytesBefore) {
		t.Fatal("peek changed the spool contents")
	}

	// Same input, same view: whatever Peek reported is what a delivery hands over.
	loaded, dropped, err := Load(dir, "alp")
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded) != len(peeked) || dropped != over {
		t.Fatalf("load=%d/%d, peek=%d/%d — the two views disagree", len(loaded), dropped, len(peeked), over)
	}
	for i := range loaded {
		if loaded[i] != peeked[i] {
			t.Fatalf("entry %d differs: %+v vs %+v", i, loaded[i], peeked[i])
		}
	}
}

// bp q counts every spool in the directory. Counting must not be a delivery:
// the over-cap records stay on disk until an agent actually opens and is told
// how many were dropped.
func TestCountsDoesNotPruneSpools(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	for i := 0; i < 25; i++ {
		entry := Entry{TS: now.Add(time.Duration(i-25) * time.Minute).Unix(), From: "ada", Kind: "announce", Text: fmt.Sprint(i)}
		if err := Append(dir, "alp", entry); err != nil {
			t.Fatal(err)
		}
	}
	agents, items, err := Counts(dir)
	if err != nil {
		t.Fatal(err)
	}
	if agents != 1 || items != maxItems {
		t.Fatalf("counts=%d agents/%d items, want 1/%d", agents, items, maxItems)
	}
	records, err := os.ReadFile(path(dir, "alp"))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(strings.TrimSpace(string(records)), "\n") + 1; got != 25 {
		t.Fatalf("spool holds %d records after Counts, want 25 — counting deleted messages", got)
	}
}

// crowdedSpool writes one aged-out record and 25 live ones: the view keeps the
// newest 20 and reports 6 dropped (1 old + 5 over the cap).
func crowdedSpool(t *testing.T, dir, agent string) {
	t.Helper()
	now := time.Now()
	old := Entry{TS: now.Add(-maxAge - time.Minute).Unix(), From: "ada", Kind: "announce", Text: "old"}
	if err := Append(dir, agent, old); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 25; i++ {
		entry := Entry{TS: now.Add(time.Duration(i-25) * time.Minute).Unix(), From: "ada", Kind: "announce", Text: fmt.Sprint(i)}
		if err := Append(dir, agent, entry); err != nil {
			t.Fatal(err)
		}
	}
}

func TestLoadLimitsAgeAndItems(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	old := Entry{TS: now.Add(-7*time.Hour*24 - time.Minute).Unix(), From: "ada", Kind: "announce", Text: "old"}
	if err := Append(dir, "alp", old); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 22; i++ {
		entry := Entry{TS: now.Add(time.Duration(i-22) * time.Minute).Unix(), From: "ada", Kind: "announce", Text: fmt.Sprint(i)}
		if err := Append(dir, "alp", entry); err != nil {
			t.Fatal(err)
		}
	}
	entries, dropped, err := Load(dir, "alp")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 20 {
		t.Fatalf("len=%d, want 20", len(entries))
	}
	if dropped != 3 {
		t.Fatalf("dropped=%d, want 3", dropped)
	}
	if entries[0].Text != "2" || entries[19].Text != "21" {
		t.Fatalf("kept range=%q..%q, want 2..21", entries[0].Text, entries[19].Text)
	}
	entries, dropped, err = Load(dir, "alp")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 20 || dropped != 0 {
		t.Fatalf("second load len=%d dropped=%d, want 20/0", len(entries), dropped)
	}
}

// A sandboxed agent (Codex under bubblewrap, measured 2026-08-25) has the state
// tree mounted READ-ONLY. Load opens the spool O_RDWR because it prunes, so
// every bp msg from that agent failed at the spool rather than at the delivery
// — and the failure looked like "bp cannot send" rather than "bp cannot write
// here". The error must name that difference, because the caller's correct
// response differs: skip the digest, do not fail the message.
func TestLoadReportsAReadOnlySpoolDistinctly(t *testing.T) {
	dir := t.TempDir()
	if err := Append(dir, "kavram-main", Entry{TS: time.Now().Unix(), From: "bp", Kind: "msg", Text: "bekleyen mesaj"}); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(dir, "pending", "kavram-main.jsonl"), 0o444); err != nil {
		t.Skipf("cannot drop write permission here: %v", err)
	}
	// The classifier is checked directly, because root ignores the permission
	// bit and the real case (a bubblewrap read-only mount) cannot be staged in a
	// unit test. These are the two errno values such a mount produces.
	for _, err := range []error{syscall.EROFS, os.ErrPermission, fmt.Errorf("open: %w", syscall.EROFS)} {
		if !readOnly(err) {
			t.Fatalf("readOnly(%v) = false, want true", err)
		}
	}
	if readOnly(os.ErrNotExist) || readOnly(errors.New("bozuk dosya")) {
		t.Fatal("an ordinary failure was classified as read-only")
	}
	if os.Geteuid() != 0 {
		if _, _, err := Load(dir, "kavram-main"); !errors.Is(err, ErrReadOnly) {
			t.Fatalf("Load err=%v, want ErrReadOnly", err)
		}
	}
	// Reading still works, so a sandboxed client can still SEE what is waiting.
	if entries, _, err := Peek(dir, "kavram-main"); err != nil || len(entries) != 1 {
		t.Fatalf("Peek entries=%v err=%v — a read-only spool must still be readable", entries, err)
	}
}
