package bp

import "testing"

func TestIsSnapshotID(t *testing.T) {
	valid := []string{
		"add-feat-a1b2",
		"ss-a3f2b7c1",
		"20240102-0304-abcd-test",
	}
	for _, id := range valid {
		if !IsSnapshotID(id) {
			t.Fatalf("expected valid snapshot id: %s", id)
		}
	}
	invalid := []string{
		"",
		"add-feat-zzzz",           // invalid hex
		"ss-acdeff",               // too short
		"20240102-0304-aaaa-",     // empty slug
		"20240102-0304-aaaa-TOO_LONG_SLUG_EXCEEDS_LIMIT",
	}
	for _, id := range invalid {
		if IsSnapshotID(id) {
			t.Fatalf("expected invalid snapshot id: %s", id)
		}
	}
}
