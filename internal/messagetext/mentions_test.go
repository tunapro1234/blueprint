package messagetext

import "testing"

func TestTrailingMentionMatchesOnlyAPickerToken(t *testing.T) {
	cases := map[string]string{
		"[WA:VBAI/PR 26-27|Utku#ACBD] @pr": "@pr",
		"@pr":                              "@pr",
		"ref @":                            "@",
		"line one\n@pr":                    "@pr",
		"tab\t@pr":                         "@pr",
		`ref @"pr`:                         `@"pr`,
		"ref @pr ":                         "",
		"ref @pr\n":                        "",
		"middle @pr token":                 "",
		"mail a@pr":                        "",
		"wrapped `@pr`":                    "",
		"paren (@pr)":                      "",
		"":                                 "",
	}
	for text, want := range cases {
		if got := TrailingMention(text); got != want {
			t.Errorf("TrailingMention(%q)=%q, want %q", text, got, want)
		}
	}
}

func TestNeutralizeTrailingMentionAppendsOneSpaceAndIsIdempotent(t *testing.T) {
	text := "[server-whatsapp] [kopru] GEC TESLIMAT: [WA:VBAI/PR 26-27|Utku#ACBD] @pr"
	once, changed := NeutralizeTrailingMention(text)
	if !changed || once != text+" " {
		t.Fatalf("got %q changed=%v", once, changed)
	}
	twice, changed := NeutralizeTrailingMention(once)
	if changed || twice != once {
		t.Fatalf("second pass changed the text: %q", twice)
	}
	if got, changed := NeutralizeTrailingMention("no mention here"); changed || got != "no mention here" {
		t.Fatalf("unrelated text changed: %q", got)
	}
}

func TestNeutralizeForClaudeAppliesBothRules(t *testing.T) {
	got := NeutralizeForClaude("look at /tmp/shot.png\nthen ping @pr")
	if got != "look at `/tmp/shot.png`\nthen ping @pr " {
		t.Fatalf("got %q", got)
	}
	if again := NeutralizeForClaude(got); again != got {
		t.Fatalf("not idempotent: %q", again)
	}
}
