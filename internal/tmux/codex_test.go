package tmux

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// codexPaneWith renders a Codex screen the way the live pane does: transcript,
// a rule line, the composer (first row carries the prompt marker, the rest are
// wrap continuations), a blank row, then the footer.
func codexPaneWith(rows []string) string {
	lines := []string{
		"• Ran 6 commands · ctrl + t to view transcript",
		"",
		strings.Repeat("─", 70),
		"",
	}
	for i, row := range rows {
		if i == 0 {
			lines = append(lines, "› "+row)
			continue
		}
		lines = append(lines, "  "+row)
	}
	lines = append(lines, "", "  gpt-5.6-sol medium fast · /srv/probot/out-codex", "")
	return strings.Join(lines, "\n")
}

// wrapText splits a message the way a narrow pane renders it.
func wrapText(text string, width int) []string {
	var rows []string
	for len(text) > width {
		rows = append(rows, text[:width])
		text = text[width:]
	}
	return append(rows, text)
}

// The q218783035 case (probot-outreach, 2026-08-25): a ~520 character
// plain-ASCII message pasted into probot-out-codex, too long for one rendered
// row and too short for Codex's paste chip. Until Codex got a composer reader,
// bp saw one row of eight — so it could neither verify the paste nor recognise
// the composer as still holding its own text, and told the operator the message
// was gone while it sat on screen.
func TestCodexComposerReadsWrappedPaste(t *testing.T) {
	message := "[probot-outreach] blueprint bp'deki gonderme arizani duzeltti (16ed445): sorun teslimat degil spool'du — bp duyuru eklemek icin spool'u yazma modunda aciyordu, senin sandbox'inda /srv/blueprint salt-okunur oldugu icin daha ilk adimda patliyordu. Artik spool salt-okunursa mesaj duyurusuz TEK BASINA gidiyor. DOGRULAMA RICASI: musait oldugunda bana kisa bir test mesaji at."
	pane := codexPaneWith(wrapText(message, 68))

	if _, ok := composerBoxText(pane); !ok {
		t.Fatal("codex composer unreadable: the pane has no box reader")
	}
	text, ours := StuckPaste(pane, []string{message})
	if !ours || text != message {
		t.Fatalf("StuckPaste ours=%v text=%q, want our own message back", ours, text)
	}
	if got := classifyComposer(pane, stripSpace(message)); got != composerMine {
		t.Fatalf("classifyComposer=%v, want composerMine", got)
	}
}

// An idle Codex composer must read as EMPTY: its placeholder is not a human
// typing, and a pane that reads as occupied blocks its own queue.
func TestCodexIdleComposerReadsEmpty(t *testing.T) {
	pane := codexPaneWith([]string{"Ask Codex to do anything"})
	if Typing(pane) {
		t.Fatal("idle codex composer read as typing")
	}
	if box, ok := composerBoxText(pane); ok && box != "" {
		t.Fatalf("idle codex box=%q, want empty", box)
	}
}

// Someone else's half-written line in a Codex composer must stay FOREIGN: the
// reader exists to let bp finish its OWN paste, never to press Enter on a
// human's unfinished text.
func TestCodexForeignComposerStaysForeign(t *testing.T) {
	pane := codexPaneWith(wrapText("abi sunu bir kontrol eder misin, listedeki numaralarin nitelik alanini yeniden gozden gecirmemiz lazim", 68))
	if _, ours := StuckPaste(pane, []string{"[bp] bambaska bir mesaj, yeterince uzun olsun diye uzatiyorum."}); ours {
		t.Fatal("a stranger's text in a codex composer was claimed as ours")
	}
}

func modernCodexPane(text string) string {
	return "• Previous answer\n\n› " + text + "\n  gpt-6-astra high · /srv/blueprint\n"
}

func TestCodexCurrentFooterAndLongRunningTurn(t *testing.T) {
	for _, timer := range []string{"0s", "1m 11s", "2h 4m 9s"} {
		pane := "◦ Working (" + timer + " • esc to interrupt)\n" + modernCodexPane("Ask Codex to do anything")
		if !CodexPane(pane) || !Busy(pane) || Typing(pane) {
			t.Fatalf("incorrect current Codex state: %q", pane)
		}
	}
	text := "[server-main] ilk satır\n  ikinci satır ve Türkçe içerik"
	pane := modernCodexPane(text)
	if _, ok := StuckPaste(pane, []string{text}); !ok {
		t.Fatal("current wrapped composer not recognized")
	}
	if !Typing(modernCodexPane("kullanıcının yarım metni")) {
		t.Fatal("user input ignored")
	}
}

// This fake terminal consumes the actual buffer and submit calls. It generates
// screens from its input state instead of scripting the implementation's reads.
type codexTerminal struct {
	harness, mode, search                                  string
	chip, expandFirst, expanded, searchAfterPaste          bool
	draft, buffer                                          string
	submitted                                              []string
	keys                                                   []string
	pasted                                                 int
	active, working, afterPasteBusy, editAfterPaste, modal bool
	menu, menuAfterPaste, menuAfterEnter                   bool
}

func (h *codexTerminal) client() *Client {
	c := New()
	c.Sleep = func(time.Duration) {}
	c.Now = func() time.Time { return time.Unix(1000, 0) }
	c.exec = func(_ context.Context, data []byte, args ...string) ([]byte, error) {
		switch args[0] {
		case "display-message":
			if h.harness != "" {
				return []byte(h.harness), nil
			}
			return []byte("codex"), nil
		case "list-clients":
			if h.active {
				return []byte("agent\t1000\n"), nil
			}
			return nil, nil
		case "capture-pane":
			draft := h.draft
			if draft == "" {
				draft = "Ask Codex to do anything"
			}
			if h.chip && h.draft != "" && !h.expanded {
				if h.harness == "claude" {
					draft = "[Pasted text #1 +2 lines]"
				} else {
					draft = "[Pasted Content 2048 chars]"
				}
			}
			pane := modernCodexPane(draft)
			if h.harness == "claude" {
				if h.draft == "" {
					draft = ""
				}
				pane = claudePane("❯ " + draft)
				if h.mode == "normal" {
					pane = strings.ReplaceAll(pane, "-- INSERT --", "-- NORMAL --")
				}
			} else if h.mode != "" {
				pane = strings.Replace(pane, " · /srv/blueprint", " · /srv/blueprint     Vim: "+strings.Title(h.mode), 1)
			}
			if h.search != "" {
				pane = modernCodexPane(draft)
				pane = strings.Replace(pane, "  gpt-6-astra high · /srv/blueprint", "  \x1b[36m"+h.search+"\x1b[0m", 1)
			}
			if h.working {
				pane = "◦ Working (1m 11s • esc to interrupt)\n" + pane
			}
			if h.modal {
				pane += "Press enter to confirm or esc to cancel\n"
			}
			if h.menu {
				pane += codexNavigationFixture
			}
			return []byte(pane), nil
		case "load-buffer":
			h.buffer = string(data)
		case "paste-buffer":
			h.pasted++
			if h.menuAfterPaste {
				h.menu = true
			}
			if h.mode == "normal" && !strings.Contains(" "+strings.Join(args, " ")+" ", " -p ") {
				h.keys = append(h.keys, "Vim interpreted raw paste as commands")
			} else {
				h.draft += h.buffer
			}
			if h.searchAfterPaste {
				h.search = "/"
			}
			if h.afterPasteBusy {
				h.working = true
			}
			if h.editAfterPaste {
				h.draft += " user's edit"
				h.active = true
			}
		case "send-keys":
			key := args[len(args)-1]
			h.keys = append(h.keys, key)
			if key == "Enter" {
				if h.menuAfterEnter {
					h.menu, h.draft = true, ""
					break
				}
				if h.expandFirst && !h.expanded {
					h.expanded = true
					break
				}
				h.submitted = append(h.submitted, h.draft)
				h.draft = ""
			}
		default:
			return nil, fmt.Errorf("unexpected tmux call: %v", args)
		}
		return nil, nil
	}
	return c
}

func TestCodexDeliveryHarness(t *testing.T) {
	for _, text := range []string{"[bp] kısa mesaj", "[bp] Türkçe satır\n  ikinci satır", "[bp] " + strings.Repeat("wrapped text ", 50)} {
		h := &codexTerminal{}
		if err := h.client().Send(context.Background(), "agent", text); err != nil {
			t.Fatal(err)
		}
		if h.pasted != 1 || len(h.submitted) != 1 || h.submitted[0] != text {
			t.Fatalf("delivery changed or repeated: %+v", h)
		}
	}
	for _, tc := range []struct {
		name     string
		terminal codexTerminal
		want     error
	}{
		{"busy", codexTerminal{working: true}, ErrBusy},
		{"typing", codexTerminal{draft: "user draft"}, ErrTyping},
		{"multiline typing", codexTerminal{draft: "\n  user's second line"}, ErrTyping},
		{"human at keyboard", codexTerminal{active: true}, ErrTyping},
		{"approval", codexTerminal{modal: true}, ErrDialog},
		{"edit after paste", codexTerminal{editAfterPaste: true}, ErrUnverified},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := tc.terminal
			err := h.client().Send(context.Background(), "agent", "[bp] requested message")
			if !errors.Is(err, tc.want) {
				t.Fatalf("err=%v want %v", err, tc.want)
			}
			if len(h.keys) > 0 || len(h.submitted) > 0 {
				t.Fatalf("pressed keys into a protected pane: %+v", h)
			}
		})
	}
}

func TestCodexHistoricalWorkingLineDoesNotBlockFreshComposer(t *testing.T) {
	pane := "◦ Working (23s • esc to interrupt)\n" + strings.Repeat("old transcript output\n", 40) + modernCodexPane("Ask Codex to do anything")
	if Busy(pane) {
		t.Fatal("historical Working text treated as current turn")
	}
	live := strings.Repeat("old transcript output\n", 40) + "◦ Working (23s • esc to interrupt)\n" + modernCodexPane("Ask Codex to do anything")
	if !Busy(live) {
		t.Fatal("live Working row lost")
	}
}
