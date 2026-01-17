package bp

import "testing"

func TestIsSnapshotID(t *testing.T) {
	valid := []string{
		"20240102-0304-abcd-test",
		"20240102-0304-ABCD-test",
		"20240102-0304-1234-a",
	}
	for _, id := range valid {
		if !IsSnapshotID(id) {
			t.Fatalf("expected valid snapshot id: %s", id)
		}
	}
	invalid := []string{
		"",
		"20240102-0304-acde",       // missing slug
		"20240102-030-aaaa-test",   // invalid time
		"20240102-0304-zzzz-test",  // invalid hex
		"20240102-0304-aaaa-",      // empty slug
		"20240102-0304-aaaa-TOO_LONG_SLUG_EXCEEDS_LIMIT",
	}
	for _, id := range invalid {
		if IsSnapshotID(id) {
			t.Fatalf("expected invalid snapshot id: %s", id)
		}
	}
}
