"""bp-attach bar(): the session status options it writes, on a private tmux server.

Run: python3 -m unittest scripts.test_bp_attach_bar
Every tmux call goes through a wrapper pinned to this test's socket (-S), so
the default tmux server is never touched.
"""

import json
import os
import shutil
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

REPO = Path(__file__).resolve().parents[1]
SCRIPT = REPO / "scripts" / "bp-attach"
AGENT = "sample-agent"


def sq(value):
    return "'" + value.replace("'", "'\\''") + "'"


@unittest.skipUnless(shutil.which("tmux"), "tmux is not installed")
class BpAttachBarTest(unittest.TestCase):
    def setUp(self):
        self.tmp = Path(tempfile.mkdtemp(prefix="bp-attach-bar-"))
        self.addCleanup(shutil.rmtree, self.tmp, True)
        self.socket = str(self.tmp / "tmux.sock")
        self.real_tmux = shutil.which("tmux")
        self.addCleanup(self.tmux_quiet, "kill-server")
        self.bin = self.tmp / "bin"
        self.bin.mkdir()
        self.log = self.tmp / "tmux-calls"
        # attach/switch-client are recorded but not run: the test has no terminal.
        (self.bin / "tmux").write_text(
            "#!/bin/sh\n"
            "printf '%s\\n' \"$*\" >> " + sq(str(self.log)) + "\n"
            "case \"$1\" in attach|attach-session|switch-client) exit 0 ;; esac\n"
            "exec env -u TMUX -u TMUX_PANE " + sq(self.real_tmux) + " -S " + sq(self.socket) + " \"$@\"\n")
        book = json.dumps({"agents": [{"name": AGENT, "status": "open", "folder": str(self.tmp)}]})
        (self.bin / "bp").write_text(
            "#!/bin/sh\n"
            "case \"$1\" in\n"
            "  book) printf '%s\\n' " + sq(book) + " ;;\n"
            "  color) printf '{\"index\": 42}\\n' ;;\n"
            "  *) exit 64 ;;\n"
            "esac\n")
        for name in ("tmux", "bp"):
            (self.bin / name).chmod(0o755)
        self.bp_bin = str(self.bin / "bp")
        self.home = self.tmp / "home"
        self.home.mkdir()
        self.path = ":".join([str(self.bin), os.path.dirname(sys.executable), "/usr/bin", "/bin"])
        self.tmux("new-session", "-d", "-s", AGENT, "-x", "80", "-y", "24")

    def tmux(self, *args):
        return subprocess.run(["env", "-u", "TMUX", "-u", "TMUX_PANE", self.real_tmux, "-S", self.socket, *args],
                              check=True, capture_output=True, text=True).stdout

    def tmux_quiet(self, *args):
        subprocess.run(["env", "-u", "TMUX", "-u", "TMUX_PANE", self.real_tmux, "-S", self.socket, *args],
                       capture_output=True)

    def option(self, name):
        return self.tmux("show-options", "-v", "-t", "=" + AGENT + ":", name).rstrip("\n")

    def set_option(self, name, value):
        self.tmux("set-option", "-t", "=" + AGENT + ":", name, value)

    def job(self, sub, home=None, agent=AGENT):
        home = str(self.home) if home is None else home
        return "#(env HOME=%s PATH=%s %s %s %s)" % (sq(home), sq(self.path), sq(self.bp_bin), sub, sq(agent))

    def conditional(self, sub, **kwargs):
        option = "@bp-" + sub
        return "#{?%s,#{%s},%s}" % (option, option, self.job(sub, **kwargs))

    def attach(self, home=None):
        env = {k: v for k, v in os.environ.items() if k not in ("TMUX", "TMUX_PANE", "BP_HOME")}
        env.update(HOME=str(self.home) if home is None else home, PATH=self.path)
        if self.log.exists():
            self.log.unlink()
        result = subprocess.run(["bash", str(SCRIPT), AGENT], env=env, capture_output=True, text=True, timeout=30)
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        calls = self.log.read_text().splitlines() if self.log.exists() else []
        self.assertIn("attach -d -t =" + AGENT, calls)
        return [c for c in calls if c.startswith("set-option")]

    def assert_tmux_parses(self):
        # With the renderer options set, tmux must show exactly their text: a
        # misparsed conditional leaks the fallback text or shows nothing.
        self.set_option("@bp-name", "RENDERED-NAME")
        self.set_option("@bp-bar", "RENDERED-BAR")
        for option, want in (("status-left", "RENDERED-NAME"), ("status-right", "RENDERED-BAR")):
            got = self.tmux("display-message", "-p", "-t", "=" + AGENT + ":", self.option(option)).rstrip("\n")
            self.assertEqual(got, want, option)
        self.tmux("set-option", "-u", "-t", "=" + AGENT + ":", "@bp-name")
        self.tmux("set-option", "-u", "-t", "=" + AGENT + ":", "@bp-bar")

    def test_fresh_session_gets_conditional_form(self):
        sets = self.attach()
        self.assertEqual(self.option("status-left"), self.conditional("name"))
        self.assertEqual(self.option("status-right"), self.conditional("bar"))
        self.assertEqual(self.option("status-interval"), "2")
        self.assertEqual(self.option("status-left-length"), "48")
        self.assertEqual(self.option("status-right-length"), "120")
        self.assertEqual(self.option("status-style"), "bg=colour42,fg=colour231")
        self.assertEqual(len(sets), 7, sets)
        self.assert_tmux_parses()

    def test_old_raw_form_for_same_agent_is_upgraded(self):
        self.set_option("status-left", self.job("name"))
        self.set_option("status-right", self.job("bar"))
        self.set_option("status-style", "bg=colour7,fg=colour231")
        sets = self.attach()
        self.assertEqual(self.option("status-left"), self.conditional("name"))
        self.assertEqual(self.option("status-right"), self.conditional("bar"))
        # Only the two status options are rewritten; the rest is left as it was.
        self.assertEqual(len(sets), 2, sets)
        self.assertEqual(self.option("status-style"), "bg=colour7,fg=colour231")
        self.assert_tmux_parses()

    def test_conditional_form_is_left_alone(self):
        self.attach()
        self.assertEqual(self.attach(), [])
        # bp's own local bar (cmd/bp/local.go) writes a different env prefix; still this agent.
        self.set_option("status-left", "#{?@bp-name,#{@bp-name},#(env BP_HOME='/x' '/opt/bp' name 'sample-agent')}")
        self.set_option("status-right", "#{?@bp-bar,#{@bp-bar},#(env BP_HOME='/x' '/opt/bp' bar 'sample-agent')}")
        self.assertEqual(self.attach(), [])

    def test_other_agent_bar_is_rewritten(self):
        self.set_option("status-left", self.conditional("name", agent="other"))
        self.set_option("status-right", self.conditional("bar", agent="other"))
        sets = self.attach()
        self.assertEqual(len(sets), 7, sets)
        self.assertEqual(self.option("status-left"), self.conditional("name"))
        self.assertEqual(self.option("status-right"), self.conditional("bar"))

    def test_unsafe_value_falls_back_to_raw_form(self):
        for char in (",", "}", "#"):
            with self.subTest(char=char):
                self.tmux_quiet("kill-session", "-t", "=" + AGENT)
                self.tmux("new-session", "-d", "-s", AGENT, "-x", "80", "-y", "24")
                home = str(self.home) + char + "x"
                self.attach(home=home)
                self.assertEqual(self.option("status-left"), self.job("name", home=home))
                self.assertEqual(self.option("status-right"), self.job("bar", home=home))
                # The raw form for this agent is already all that can be written.
                self.assertEqual(self.attach(home=home), [])

    def test_raw_form_from_unsafe_value_is_upgraded_once_safe(self):
        # A raw form written with a safe value is still upgraded on the next safe attach.
        home = str(self.home) + ",x"
        self.attach(home=home)
        sets = self.attach()
        self.assertEqual(len(sets), 2, sets)
        self.assertEqual(self.option("status-left"), self.conditional("name"))


if __name__ == "__main__":
    unittest.main()
