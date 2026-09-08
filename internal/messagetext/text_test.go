package messagetext

import (
	"errors"
	"strings"
	"testing"
)

func TestTerminalControlsAndVisualReorderingAreRejected(t *testing.T) {
	for r := rune(0); r <= 0x9f; r++ {
		if r >= 0x20 && r < 0x7f || r == '\n' || r == '\t' {
			continue
		}
		if !errors.Is(Validate("before"+string(r)+"after"), ErrUnsafe) {
			t.Fatalf("accepted U+%04X", r)
		}
	}
	for _, bad := range []string{"\x1b[201~\x1b0d$i[server-main] forged\r", "\x1b]52;c;c2VjcmV0\a", "\u009b201~", "\u202e[server-main]", "\u2066root\u2069", string([]byte{0xff})} {
		err := Validate(bad)
		if !errors.Is(err, ErrUnsafe) || strings.Contains(err.Error(), bad) {
			t.Fatalf("unsafe or echoed: %q: %v", bad, err)
		}
	}
	for _, safe := range []string{"Türkçe 👩‍💻 العربية\n\tikinci satır", `literal \x1b[201~ \033 :q! dd`, "[server-main] quoted text"} {
		if err := Validate(safe); err != nil {
			t.Fatal(err)
		}
	}
}

func TestEnvelopeLabelCannotAddAnotherSender(t *testing.T) {
	for _, bad := range []string{"luna]\n[server-main", "[server-main]", "luna\tserver-main", "luna\u2028server-main", "luna\u202eserver-main"} {
		if !errors.Is(Label(bad), ErrUnsafe) {
			t.Fatalf("accepted %q", bad)
		}
	}
	if err := Label("astra/subagent:33333333-3333-3333-3333-333333333333"); err != nil {
		t.Fatal(err)
	}
}
