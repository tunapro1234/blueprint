package bp

import "testing"

func TestCanonicalizeValueStableOrder(t *testing.T) {
	val := map[string]interface{}{"b": 1, "a": 2}
	got := canonicalizeValue(val)
	if got != "{\"a\":2,\"b\":1}" {
		t.Fatalf("unexpected canonical value: %s", got)
	}
	val2 := map[interface{}]interface{}{1: "x", "b": "y"}
	got2 := canonicalizeValue(val2)
	if got2 == "" {
		t.Fatalf("expected canonical value for interface map")
	}
}

func TestAPIHashStable(t *testing.T) {
	bp1 := &Blueprint{Data: map[string]interface{}{"api": map[string]interface{}{"b": "2", "a": "1"}}}
	bp2 := &Blueprint{Data: map[string]interface{}{"api": map[string]interface{}{"a": "1", "b": "2"}}}
	h1, err := bp1.APIHash()
	if err != nil {
		t.Fatalf("APIHash: %v", err)
	}
	h2, err := bp2.APIHash()
	if err != nil {
		t.Fatalf("APIHash: %v", err)
	}
	if h1 != h2 {
		t.Fatalf("expected same API hash, got %s vs %s", h1, h2)
	}
}
