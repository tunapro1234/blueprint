package pending

import (
	"fmt"
	"strings"
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
