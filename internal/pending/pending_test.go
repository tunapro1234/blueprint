package pending

import (
	"fmt"
	"testing"
	"time"
)

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
