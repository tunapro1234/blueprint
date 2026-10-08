package main

import (
	"strings"
	"testing"
)

func TestDoctorRemoteControlCheck(t *testing.T) {
	active := "  /remote-control is active · Continue here, on your phone, or at https://claude.ai/code/session_01ABC\n"
	dropped := active + "● Remote Control disconnected — signed-in claude.ai account or organization changed on this machine\n"

	check, ok := doctorRemoteControlCheck("alpha", dropped, false)
	if !ok || check.OK || !check.Warning || check.Agent != "alpha" ||
		!strings.Contains(check.Detail, "organization changed") || !strings.HasPrefix(check.Next, "bp remote alpha") {
		t.Fatalf("dropped = %+v %v", check, ok)
	}
	for _, pane := range []string{active, "❯ hi\n"} {
		if check, ok := doctorRemoteControlCheck("alpha", pane, false); ok {
			t.Fatalf("fleet-wide doctor reported a healthy session: %+v", check)
		}
		if check, ok := doctorRemoteControlCheck("alpha", pane, true); !ok || !check.OK || check.Warning {
			t.Fatalf("--agent doctor = %+v %v", check, ok)
		}
	}
}
