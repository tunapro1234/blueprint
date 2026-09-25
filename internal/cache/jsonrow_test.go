package cache

import "testing"

func TestMayHaveRecordTypeSkipsOnlyUnambiguousOtherTypes(t *testing.T) {
	cases := []struct {
		name string
		row  string
		want bool
	}{
		{"different type", `{"type":"session_meta","payload":{"type":"assistant"}}`, false},
		{"type after nested value", `{"payload":{"type":"session_meta"},"type":"assistant"}`, true},
		{"case folded field", `{"TYPE":"assistant"}`, true},
		{"escaped key", `{"ty\u0070e":"session_meta"}`, true},
		{"escaped value", `{"type":"session\u005fmeta"}`, true},
		{"duplicate field", `{"type":"session_meta","type":"assistant"}`, true},
		{"quoted nested field", `{"type":"session_meta","payload":"{\"type\":\"assistant\"}"}`, false},
		{"malformed object", `{"type":"session_meta","payload":`, true},
		{"trailing content", `{"type":"session_meta"} garbage`, true},
		{"leading whitespace", ` {"type":"session_meta"}`, false},
		{"relevant leading whitespace", ` {"type":"assistant"}`, true},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if got := MayHaveRecordType([]byte(test.row), "assistant"); got != test.want {
				t.Fatalf("MayHaveRecordType(%s)=%v want %v", test.row, got, test.want)
			}
		})
	}
}

func TestHasCompactRecordTypePrefixIsPositiveOnly(t *testing.T) {
	for _, test := range []struct {
		line string
		want bool
	}{
		{`{"type":"session_meta","payload":{}}`, true},
		{`{"type":"session_meta_extra"}`, false},
		{` {"type":"session_meta"}`, false},
		{`{"payload":{"type":"session_meta"}}`, false},
	} {
		if got := HasCompactRecordTypePrefix([]byte(test.line), "session_meta"); got != test.want {
			t.Errorf("HasCompactRecordTypePrefix(%s)=%v want %v", test.line, got, test.want)
		}
	}
}

func TestDefinitelyOtherCompactRecordTypeSkipsOnlyUnambiguousRows(t *testing.T) {
	for _, test := range []struct {
		line string
		want bool
	}{
		{`{"type":"queue-operation","operation":"enqueue"}`, true},
		{`{"type":"assistant","message":{}}`, false},
		{`{"type":"queue-operation","payload":{"type":"user"}}`, false},
		{`{"type":"queue-operation","Type":"user"}`, false},
		{`{"type":"queue-operation","content":"quoted \"type\""}`, false},
		{` {"type":"queue-operation"}`, false},
		{`{"type":"queue-operation","payload":`, true},
	} {
		if got := DefinitelyOtherCompactRecordType([]byte(test.line), "assistant", "user"); got != test.want {
			t.Errorf("DefinitelyOtherCompactRecordType(%s)=%v want %v", test.line, got, test.want)
		}
	}
}
