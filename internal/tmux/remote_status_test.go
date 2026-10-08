package tmux

import "testing"

func TestRemoteControlStatus(t *testing.T) {
	const url = "https://claude.ai/code/session_01ABC"
	active := "❯ /remote-control\n  ⎿  /remote-control is active · Continue here, on your phone, or at " + url + "\n"
	disconnected := "● Remote Control disconnected — signed-in claude.ai account or organization changed on this machine — run /remote-control to start a session for the current account\n"
	cases := []struct {
		name, pane string
		state      RemoteControl
		url        string
	}{
		{"never", "❯ hello\n", RemoteUnknown, ""},
		{"active", active, RemoteActive, url},
		{"wrapped url", "  /remote-control is active · Continue here, on your phone, or at\n  " + url + ".\n", RemoteActive, url},
		{"dropped", active + disconnected, RemoteDisconnected, ""},
		{"reconnected", active + disconnected + "❯ /remote-control\n  /remote-control is active · Continue here, on your phone, or at https://claude.ai/code/session_02NEW\n", RemoteActive, "https://claude.ai/code/session_02NEW"},
		// Text quoted in conversation or tool output is not Claude Code's own line.
		{"quoted disconnect", active + "     compec-main: ● Remote Control disconnected — signed-in\n", RemoteActive, url},
		{"quoted active", "> the line reads /remote-control is active · see " + url + "\n", RemoteUnknown, url},
	}
	for _, tc := range cases {
		state, got, reason := RemoteControlStatus(tc.pane)
		if state != tc.state || got != tc.url {
			t.Errorf("%s: state=%v url=%q, want %v %q", tc.name, state, got, tc.state, tc.url)
		}
		if (state == RemoteDisconnected) != (reason != "") {
			t.Errorf("%s: reason=%q for state %v", tc.name, reason, state)
		}
	}
	if _, _, reason := RemoteControlStatus(active + disconnected); reason[:27] != "Remote Control disconnected" {
		t.Errorf("reason = %q", reason)
	}
}
