package yamlparser

import (
	"reflect"
	"strings"
	"testing"
)

func TestParseSimple(t *testing.T) {
	content := "_meta:\n  version: \"1\"\nlist:\n  - one\n  - two\nflag: yes\ncount: 12\n"
	res := Parse(content)
	if res.Error != nil {
		t.Fatalf("Parse error: %v", res.Error)
	}
	if res.Data["flag"].(bool) != true {
		t.Fatalf("expected flag true")
	}
	if res.Data["count"].(string) != "12" {
		t.Fatalf("expected count as string")
	}
	list := res.Data["list"].([]interface{})
	if !reflect.DeepEqual(list, []interface{}{"one", "two"}) {
		t.Fatalf("unexpected list: %v", list)
	}
	meta := res.Data["_meta"].(map[string]interface{})
	if meta["version"] != "1" {
		t.Fatalf("unexpected meta: %v", meta)
	}
}

func TestParseInlineMapAndNested(t *testing.T) {
	content := "items:\n  - name: test\n    value: 123\n  - key: val\n"
	res := Parse(content)
	if res.Error != nil {
		t.Fatalf("Parse error: %v", res.Error)
	}
	items := res.Data["items"].([]interface{})
	if len(items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(items))
	}
	item0 := items[0].(map[string]interface{})
	if item0["name"] != "test" || item0["value"].(string) != "123" {
		t.Fatalf("unexpected item0: %v", item0)
	}
}

func TestParseMultiline(t *testing.T) {
	content := "text: |\n  line1\n  line2\nfolded: >\n  line1\n  line2\n"
	res := Parse(content)
	if res.Error != nil {
		t.Fatalf("Parse error: %v", res.Error)
	}
	if res.Data["text"].(string) != "line1\nline2\n" {
		t.Fatalf("unexpected literal block: %q", res.Data["text"].(string))
	}
	if res.Data["folded"].(string) != "line1 line2\n\n" {
		t.Fatalf("unexpected folded block: %q", res.Data["folded"].(string))
	}
}

func TestParseQuotesAndComments(t *testing.T) {
	content := "value: \"a: b # not comment\"\nplain: hello # comment\n"
	res := Parse(content)
	if res.Error != nil {
		t.Fatalf("Parse error: %v", res.Error)
	}
	if res.Data["value"].(string) != "a: b # not comment" {
		t.Fatalf("unexpected quoted value: %v", res.Data["value"])
	}
	if res.Data["plain"].(string) != "hello" {
		t.Fatalf("unexpected plain value: %v", res.Data["plain"])
	}
}

func TestParseWarnings(t *testing.T) {
	content := "a:\n\tb: 1\na: 2\n"
	res := Parse(content)
	if res.Error != nil {
		t.Fatalf("Parse error: %v", res.Error)
	}
	if len(res.Warnings) < 2 {
		t.Fatalf("expected warnings, got %v", res.Warnings)
	}
	foundDup := false
	foundTab := false
	for _, w := range res.Warnings {
		if strings.Contains(w, "Duplicate key") {
			foundDup = true
		}
		if strings.Contains(w, "Tab") {
			foundTab = true
		}
	}
	if !foundDup || !foundTab {
		t.Fatalf("missing warnings: %v", res.Warnings)
	}
}

func TestParseErrors(t *testing.T) {
	cases := []string{
		"a 1\n",
		"a: 1\n  b: 2\n",
		"a: \"test\n",
		"- item\n",
	}
	for _, content := range cases {
		res := Parse(content)
		if res.Error == nil {
			t.Fatalf("expected error for content: %q", content)
		}
	}
}

func TestParseFileMissing(t *testing.T) {
	res := ParseFile("nonexistent.yaml")
	if res.Error == nil {
		t.Fatalf("expected error for missing file")
	}
	if !strings.Contains(res.Error.Message, "no such file") {
		t.Fatalf("unexpected error message: %s", res.Error.Message)
	}
}
