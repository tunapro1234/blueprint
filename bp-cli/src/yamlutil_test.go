package bp

import "testing"

func TestConvertYAML(t *testing.T) {
	in := map[interface{}]interface{}{
		"a": map[interface{}]interface{}{
			"b": 1,
		},
		2: []interface{}{map[interface{}]interface{}{"c": "d"}},
	}
	out := convertYAML(in).(map[string]interface{})
	if out["a"] == nil {
		t.Fatalf("expected nested map")
	}
	if _, ok := out["2"]; !ok {
		t.Fatalf("expected numeric key converted to string")
	}
}
