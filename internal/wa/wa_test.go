package wa

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"blueprint/internal/identity"
)

func TestSendPublishesOnlyCompleteVisibleJSON(t *testing.T) {
	outbox := t.TempDir()
	if err := Send(outbox, "agent", "hello", SendOptions{To: "target"}); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(outbox)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || strings.HasPrefix(entries[0].Name(), ".") || filepath.Ext(entries[0].Name()) != ".json" {
		t.Fatalf("published entries=%v", entries)
	}
	data, err := os.ReadFile(filepath.Join(outbox, entries[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	var record Outgoing
	if err := json.Unmarshal(data, &record); err != nil {
		t.Fatal(err)
	}
	if record.Agent != "agent" || record.Text != "[agent] hello" || record.To == nil || *record.To != "target" {
		t.Fatalf("record=%+v", record)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	if _, ok := raw["mentions"]; ok {
		t.Fatal("empty mentions key was serialized")
	}
	if _, ok := raw["mentionAll"]; ok {
		t.Fatal("false mentionAll key was serialized")
	}
}

func TestSendPublishesMentions(t *testing.T) {
	outbox := t.TempDir()
	wantMentions := []string{"905551234567@s.whatsapp.net", "123456789012345@lid"}
	if err := Send(outbox, "agent", "hello", SendOptions{Mentions: wantMentions, MentionAll: true}); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(outbox)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(outbox, entries[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	var mentions []string
	if err := json.Unmarshal(raw["mentions"], &mentions); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(mentions, wantMentions) {
		t.Fatalf("mentions=%v, want %v", mentions, wantMentions)
	}
	var mentionAll bool
	if err := json.Unmarshal(raw["mentionAll"], &mentionAll); err != nil {
		t.Fatal(err)
	}
	if !mentionAll {
		t.Fatal("mentionAll key was not serialized as true")
	}
}

func TestNormalizeMention(t *testing.T) {
	cases := []struct {
		name    string
		input   string
		want    string
		wantErr string
	}{
		{"formatted phone", "+90 555 123 45 67", "905551234567@s.whatsapp.net", ""},
		{"bare phone", "905551234567", "905551234567@s.whatsapp.net", ""},
		{"compact international phone", "+905551234567", "905551234567@s.whatsapp.net", ""},
		{"formatted punctuation", "+1 (234) 567-8901", "12345678901@s.whatsapp.net", ""},
		{"phone JID", "905551234567@s.whatsapp.net", "905551234567@s.whatsapp.net", ""},
		{"upper-case phone JID domain", "905551234567@S.WHATSAPP.NET", "905551234567@s.whatsapp.net", ""},
		{"LID", "123456789012345@lid", "123456789012345@lid", ""},
		{"upper-case LID domain", "123456789012345@LID", "123456789012345@lid", ""},
		{"legacy c.us JID", "905551234567@c.us", "905551234567@s.whatsapp.net", ""},
		{"leading at number", "@905551234567", "905551234567@s.whatsapp.net", ""},
		{"minimum digit count", "1234567", "1234567@s.whatsapp.net", ""},
		{"maximum digit count", "12345678901234567890", "12345678901234567890@s.whatsapp.net", ""},
		{"empty", "", "", "invalid mention \"\""},
		{"name", "Alice", "", "invalid mention \"Alice\""},
		{"letters in number", "90555abc4567", "", "invalid mention \"90555abc4567\""},
		{"group JID", "123456789012345@g.us", "", "invalid mention \"123456789012345@g.us\""},
		{"unsupported domain", "123456789012345@example.net", "", "invalid mention \"123456789012345@example.net\""},
		{"too few digits", "123456", "", "invalid mention \"123456\""},
		{"too many digits", "123456789012345678901", "", "invalid mention \"123456789012345678901\""},
		{"device suffix", "905551234567:4@s.whatsapp.net", "", "device suffixes are not supported"},
		{"malformed JID", "123456789012345@@lid", "", "invalid mention \"123456789012345@@lid\""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := NormalizeMention(tc.input)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("NormalizeMention(%q) error=%v, want error containing %q", tc.input, err, tc.wantErr)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("NormalizeMention(%q) = %q, %v; want %q", tc.input, got, err, tc.want)
			}
		})
	}
}

// spectator is the tmux client. Its name is the bug's shape: display-message
// with no target reports the ATTACHED session, so a cron job asking it gets
// whoever is watching. calls records whether it was asked at all.
type spectator struct {
	session string
	calls   int
}

func (s *spectator) DisplaySession(context.Context) (string, error) {
	s.calls++
	return s.session, nil
}

// TestAgentLabelReachingThePhone checks the six precedence cases through the
// label that actually leaves the machine: Format's output is the text a human
// reads on WhatsApp, and it is the only attribution there.
func TestAgentLabelReachingThePhone(t *testing.T) {
	const inTmux = "/tmp/tmux-0/default,4242,0"
	ancestors := func(frames ...[]string) func() [][]string {
		return func() [][]string { return frames }
	}

	tests := []struct {
		name      string
		env       map[string]string
		from      string
		known     func(string) bool
		ancestors func() [][]string
		wantText  string
		wantAsked bool
		wantSure  bool
	}{
		{
			name:      "inside a pane the session signs the message",
			env:       map[string]string{"TMUX": inTmux, "AGENT": "probot-fon", "SUDO_USER": "tunapro"},
			from:      "cron:/srv/kavram/x.py",
			wantText:  "[kavram-outreach] report ready",
			wantAsked: true,
			wantSure:  true,
		},
		{
			name:     "a cron script states its own origin",
			env:      map[string]string{"TMUX": ""},
			from:     "cron:/srv/kavram/outreach/workers/inbox_watcher.py",
			wantText: "[cron:/srv/kavram/outreach/workers/inbox_watcher.py] 4 new replies",
			wantSure: true,
		},
		{
			name:     "AGENT signs when there is no pane",
			env:      map[string]string{"TMUX": "", "AGENT": "compec-mail", "SUDO_USER": "tunapro"},
			wantText: "[agent?:compec-mail] sent",
			wantSure: false,
		},
		{
			name:     "a human on the box signs with their own name",
			env:      map[string]string{"TMUX": "", "SUDO_USER": "tunapro", "USER": "root"},
			wantText: "[tunapro] sent manually",
			wantSure: true,
		},
		{
			name:     "a login name that is also an agent is marked as a guess",
			env:      map[string]string{"TMUX": "", "SUDO_USER": "probot-fon"},
			known:    func(name string) bool { return name == "probot-fon" },
			wantText: "[user?:probot-fon] sent manually",
		},
		{
			// The incident itself: a cron script that says nothing. It used to
			// come out as an attached bystander's name.
			name:      "a silent cron job confesses a guess",
			env:       map[string]string{"TMUX": "", "USER": "root"},
			ancestors: ancestors([]string{"/bin/sh", "-c", "/srv/kavram/outreach/workers/inbox_watcher.py"}, []string{"/usr/sbin/CRON", "-f"}),
			wantText:  "[cron?:inbox_watcher.py] 4 new replies",
		},
		{
			name:      "nothing to go on says so instead of borrowing server-main",
			env:       map[string]string{"TMUX": "", "USER": "root"},
			ancestors: ancestors(),
			wantText:  "[" + identity.Unknown + "] ping",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			for _, key := range []string{"TMUX", "AGENT", "SUDO_USER", "USER", "LOGNAME"} {
				t.Setenv(key, test.env[key])
			}
			client := &spectator{session: "kavram-outreach"}
			who := Agent(context.Background(), client, identity.Options{
				From:      test.from,
				Known:     test.known,
				Ancestors: test.ancestors,
				Origin:    func(context.Context) identity.Origin { return identity.Origin{} },
			})
			text := strings.SplitN(test.wantText, "] ", 2)[1]
			if got := Format(who.Label, text); got != test.wantText {
				t.Fatalf("Format() = %q, want %q", got, test.wantText)
			}
			if who.Certain != test.wantSure {
				t.Fatalf("Certain = %v, want %v (source %s)", who.Certain, test.wantSure, who.Source)
			}
			if !test.wantSure && !who.Inferred() && who.Label != identity.Unknown {
				t.Fatalf("uncertain label %q neither confesses nor says unknown", who.Label)
			}
			if asked := client.calls > 0; asked != test.wantAsked {
				t.Fatalf("DisplaySession asked=%v, want %v", asked, test.wantAsked)
			}
			if who.Label == "server-main" {
				t.Fatalf("server-main signed a message it did not send: %+v", who)
			}
		})
	}
}
