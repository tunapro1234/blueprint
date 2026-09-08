"""Quota-free real-tmux tests. Every process uses a private socket and fake CLIs."""
import json
import re
import datetime as dt
import os
from pathlib import Path
import pty
import select
import shutil
import signal
import subprocess
import tempfile
import time
import unittest

REPO = Path(__file__).resolve().parents[1]
FAKE = '''#!/usr/bin/env python3
import json, os, signal, sys
if os.environ.get("BP_FAKE_ARGS"):
 open(os.environ["BP_FAKE_ARGS"],"w").write(json.dumps(sys.argv[1:]))
if os.environ.get("BP_FAKE_BATCH"):
 print(json.dumps(sys.argv[1:]))
 sys.exit(37)
# The real CLIs consume settings/config switches before receiving user input.
args = sys.argv[1:]
if len(args) >= 2 and (args[0] == "--settings" or (args[0] == "-c" and args[1].startswith("hooks.SessionStart="))):
 sys.argv = [sys.argv[0]] + args[2:]
count = 0
def interrupt(*_):
 global count
 count += 1
 if count > 1: sys.exit(0)
 print("CANCELLED_ONCE", flush=True)
signal.signal(signal.SIGINT, interrupt)
print("FAKE_READY", flush=True)
for line in sys.stdin:
 if line.strip() == "quit": break
'''


FAKE_TUI = r'''package main
import("os"; "os/exec"; "fmt"; "encoding/json"; "time"; "strings"; "syscall"; "path/filepath")
func main() {
 if observer:=os.Getenv("BP_FAKE_OBSERVER"); observer!="" {
  c:=exec.Command("python3",append([]string{observer,filepath.Base(os.Args[0])},os.Args[1:]...)...); c.Stderr=os.Stderr
  out,e:=c.Output();if e!=nil{panic(e)}
  if path:=strings.TrimSpace(string(out));path!="" {
   os.Setenv("CODEX_THREAD_ID",strings.TrimSuffix(filepath.Base(path),".lock"))
   f,e:=os.OpenFile(path,os.O_CREATE|os.O_RDWR,0600);if e!=nil{panic(e)};defer f.Close()
   if e=syscall.Flock(int(f.Fd()),syscall.LOCK_EX);e!=nil{panic(e)}
  }
 }
 raw:=exec.Command("stty","raw","-echo"); raw.Stdin=os.Stdin; if raw.Run()!=nil { os.Exit(2) }
 defer func(){ sane:=exec.Command("stty","sane"); sane.Stdin=os.Stdin; _=sane.Run() }()
 fmt.Print("\x1b[?2004h")
 defer fmt.Print("\x1b[?2004l")
 mode:=os.Getenv("BP_FAKE_VIM")
 escape,paste,inPaste:="","",false
 input:=make(chan byte)
 go func(){ b:=make([]byte,1); for { if _,e:=os.Stdin.Read(b); e!=nil{return}; input<-b[0] } }()
 tick:=time.NewTicker(50*time.Millisecond); defer tick.Stop()
 text,last,previous,title:="","FAKE_READY","",""
 for {
  _,err:=os.Stat(os.Getenv("BP_FAKE_BUSY")); busy:=err==nil
  frame:=strings.ReplaceAll(last,"\n","\r\n")+"\r\n\r\n"
  if busy { frame+="◦ Working (1m 11s • esc to interrupt)\r\n" }
  prompt:=text; if prompt=="" {prompt="Ask Codex to do anything"}
  if filepath.Base(os.Args[0])=="claude" {
   frame+="────────────────────────────────────────\r\n❯ "+strings.ReplaceAll(text,"\n","\r\n")+"\r\n────────────────────────────────────────\r\n  -- "+strings.ToUpper(mode)+" -- ⏵⏵ bypass permissions on"
  } else {
   frame+="› "+strings.ReplaceAll(prompt,"\n","\r\n")+"\r\n  gpt-6-astra high · /work     Vim: "+mode
  }
  if title!="" { frame+=fmt.Sprintf("\x1b[48;5;135m %s \x1b[0m",title) }
  if frame!=previous {fmt.Print("\x1b[2J\x1b[H"+frame); previous=frame}
  select {
  case <-tick.C:
  case c:=<-input:
   if c==27 || escape!="" {
    escape+=string([]byte{c})
    if escape=="\x1b[200~" {inPaste=true; paste=""; escape=""} else if escape=="\x1b[201~" {text+=strings.ReplaceAll(strings.ReplaceAll(paste,"\r\n","\n"),"\r","\n"); paste=""; inPaste=false; escape=""} else if len(escape)>=6 {escape=""}
    continue
   }
   if inPaste {paste+=string([]byte{c}); continue}
   if mode=="normal" && c!=3 && c!='\r' && c!='\n' {if c=='i' {mode="insert"}; continue}
   switch c {
   case 3:return
   case 21:text=""
   case '\r','\n':
    if text=="quit" {return}
    if strings.HasPrefix(text,"__bp_whoami") {
     c:=exec.Command("bp","whoami");c.Env=os.Environ()
     if fields:=strings.Fields(text);len(fields)>1 {c.Env=append(c.Env,"CODEX_THREAD_ID="+fields[1])}
     out,e:=c.CombinedOutput();if e!=nil {panic(string(out))}
     if e=os.WriteFile(os.Getenv("BP_FAKE_IDENTITY_RESULT"),out,0600);e!=nil {panic(e)}
     text="";continue
    }
    f,e:=os.OpenFile(os.Getenv("BP_FAKE_RECEIVED"),os.O_CREATE|os.O_APPEND|os.O_WRONLY,0600)
    if e!=nil {panic(e)}; _=json.NewEncoder(f).Encode(text); _=f.Close()
    if path,e:=os.ReadFile(filepath.Join(os.Getenv("HOME"),fmt.Sprintf("native-%d-transcript",os.Getpid())));e==nil {
     log,e:=os.OpenFile(string(path),os.O_APPEND|os.O_WRONLY,0600);if e!=nil{panic(e)}
     stamp:=time.Now().UTC().Format(time.RFC3339Nano)
     var row,done map[string]any
     if filepath.Base(os.Args[0])=="claude" {
      row=map[string]any{"type":"user","timestamp":stamp,"message":map[string]any{"role":"user","content":text}}
      done=map[string]any{"type":"system","subtype":"turn_duration","timestamp":stamp}
     }else{
      row=map[string]any{"type":"response_item","timestamp":stamp,"payload":map[string]any{"type":"message","role":"user","content":[]any{map[string]any{"type":"input_text","text":text}}}}
      done=map[string]any{"type":"event_msg","timestamp":stamp,"payload":map[string]any{"type":"task_complete"}}
     }
     enc:=json.NewEncoder(log);_=enc.Encode(row);_=enc.Encode(done)
     if strings.HasPrefix(text,"/rename ") && filepath.Base(os.Args[0])=="claude" {title=strings.TrimPrefix(text,"/rename ");_=enc.Encode(map[string]any{"type":"custom-title","customTitle":title})}
     _=log.Close()
    }
    last,text=text,""
   default:text+=string([]byte{c})
   }
  }
 }
}
'''


OBSERVER = r"""import datetime, json, os, re, subprocess, sys, tomllib, uuid
from pathlib import Path
cli = sys.argv[1]
args = sys.argv[2:]
if cli == "codex":
 settings = {}
else:
 if len(args) < 2: sys.exit(0)
 if args[0] != "--settings": sys.exit(0)
 settings = json.loads(Path(args[1]).read_text())
thread = str(uuid.uuid4())
if cli == "claude" and "--resume" in args:
 thread = args[args.index("--resume") + 1]
if cli == "codex" and "resume" in args:
 thread = args[args.index("resume") + 1]
cwd = str(Path.cwd())
stamp = datetime.datetime.now(datetime.timezone.utc).isoformat()
home = Path.home()
if cli == "codex":
 path = home / ".codex/sessions/2026/09/05" / ("rollout-" + thread + ".jsonl")
 model = "gpt-6-astra"
 records = [dict(type="session_meta", payload=dict(id=thread, cwd=cwd, source="cli")),
            dict(type="turn_context", payload=dict(model=model, effort="high")),
            dict(type="event_msg", timestamp=stamp, payload=dict(type="token_count", info=dict(last_token_usage=dict(total_tokens=30000), model_context_window=200000))),
            dict(type="event_msg", timestamp=stamp, payload=dict(type="task_complete"))]
else:
 path = home / ".claude/projects" / re.sub(r"[^A-Za-z0-9]", "-", cwd) / (thread + ".jsonl")
 model = "claude-fable-5-1"
 records = [dict(type="assistant", effort="medium", timestamp=stamp, sessionId=thread,
                 message=dict(model=model, role="assistant", usage=dict(input_tokens=30000), stop_reason="end_turn")),
            dict(type="system", subtype="turn_duration", timestamp=stamp)]
path.parent.mkdir(parents=True, exist_ok=True)
if cli != "codex" or not path.exists():
 path.write_text("".join(json.dumps(r) + "\n" for r in records))
(home / ("native-" + str(os.getppid()) + "-transcript")).write_text(str(path))
payload = dict(session_id=thread, transcript_path=str(path), cwd=cwd, hook_event_name="SessionStart", source="startup", model=model)
for group in settings.get("hooks", {}).get("SessionStart", []):
 for handler in group["hooks"]:
  subprocess.run(handler["command"], shell=True, input=json.dumps(payload), text=True, check=True)
if cli == "codex":
 locks = home / ".codex/thread-writer-locks"
 locks.mkdir(parents=True, exist_ok=True)
 print(str(locks / (thread + ".lock")))
if cli == "claude":
 payload["model"] = dict(id=model)
 payload["context_window"] = dict(context_window_size=200000, current_usage=dict(input_tokens=30000, cache_read_input_tokens=0, cache_creation_input_tokens=0))
 subprocess.run(settings["statusLine"]["command"], shell=True, input=json.dumps(payload), text=True, check=True)
"""


@unittest.skipUnless(shutil.which("tmux") and shutil.which("go"), "tmux and Go required")
class LocalCLITest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.build = tempfile.TemporaryDirectory(prefix="bp-test-build-")
        cls.binary = str(Path(cls.build.name) / "bp")
        subprocess.run(["go", "build", "-o", cls.binary, "./cmd/bp"], cwd=REPO, check=True)
        source = Path(cls.build.name) / "fake.go"
        source.write_text(FAKE_TUI)
        cls.fake_tui = str(Path(cls.build.name) / "codex")
        subprocess.run(["go", "build", "-o", cls.fake_tui, str(source)], check=True)

    @classmethod
    def tearDownClass(cls):
        cls.build.cleanup()

    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix="bp-local-test-")
        self.root = Path(self.temp.name)
        self.bin = self.root / "bin"
        self.bin.mkdir()
        (self.bin / "bp").symlink_to(self.binary)
        self.socket = str(self.root / "tmux.sock")
        self.tmux = shutil.which("tmux")
        config = self.root / "tmux.conf"
        config.write_text("set -g remain-on-exit on\n")
        (self.bin / "tmux").write_text(f'#!/bin/sh\nexec "{self.tmux}" -S "{self.socket}" -f "{config}" "$@"\n')
        (self.bin / "tmux").chmod(0o755)
        for cli in ["codex", "claude", "opencode", "hermes"]:
            (self.bin / cli).write_text(FAKE)
            (self.bin / cli).chmod(0o755)
        self.env = dict(os.environ, BP_NO_UPDATE_CHECK="1", HOME=str(self.root), BP_HOME=str(self.root / ".blueprint"),
                        PATH=str(self.bin) + os.pathsep + os.environ["PATH"], TERM="xterm-256color", SHELL="/bin/bash")
        for key in ["TMUX", "TMUX_PANE", "BP_SESSION", "AGENT", "AGENTBOOK", "ZDOTDIR", "CODEX_HOME", "CLAUDE_CONFIG_DIR", "CODEX_THREAD_ID"]:
            self.env.pop(key, None)
        observer = self.root / "native-observer.py"
        observer.write_text(OBSERVER)
        self.env["BP_FAKE_OBSERVER"] = str(observer)
        self.children = []

    def tearDown(self):
        subprocess.run([self.tmux, "-S", self.socket, "kill-server"], capture_output=True)
        for pid, fd in self.children:
            os.close(fd)
            try:
                os.kill(pid, signal.SIGTERM)
            except ProcessLookupError:
                pass
            os.waitpid(pid, 0)
        self.temp.cleanup()

    def start(self, cli, name=None):
        pid, fd = pty.fork()
        if pid == 0:
            os.chdir(self.root)
            os.execve(self.binary, [self.binary, "run", "--name", name or cli + "-test", cli], self.env)
        self.children.append((pid, fd))
        self.read_until(fd, b"FAKE_READY")
        return fd

    def read_until(self, fd, needle):
        data = b""
        deadline = time.monotonic() + 8
        while time.monotonic() < deadline:
            if select.select([fd], [], [], 0.1)[0]:
                try:
                    data += os.read(fd, 65536)
                except OSError:
                    break
                if needle in data:
                    return
        self.fail(f"missing {needle!r}: {data[-1500:]!r}")

    def alive(self, name):
        return subprocess.run([self.tmux, "-S", self.socket, "has-session", "-t", "=" + name], capture_output=True).returncode == 0

    def wait_closed(self, name):
        deadline = time.monotonic() + 5
        while self.alive(name) and time.monotonic() < deadline:
            time.sleep(0.05)
        self.assertFalse(self.alive(name), "CLI left a shell/dead pane behind")
        while time.monotonic() < deadline:
            book = json.loads((self.root / ".blueprint/agentbook.json").read_text())
            status = next(a["status"] for a in book["agents"] if a["name"] == name)
            if status == "closed": break
            time.sleep(0.05)
        self.assertEqual(status, "closed")

    def resume_client(self, args, cwd=None, cli="claude"):
        pid, fd = pty.fork()
        if pid == 0:
            os.chdir(cwd or self.root)
            os.execve(self.binary, [self.binary, "run", cli] + args, self.env)
        self.children.append((pid, fd))
        return fd

    def test_codex_resume_reuses_native_writer_with_and_without_picker(self):
        shutil.copyfile(self.fake_tui, self.bin / "codex")
        original = self.start("codex", "hypr-codex")
        status = json.loads(subprocess.check_output([self.binary, "status", "--json"], env=self.env))
        thread = next(a["thread_id"] for a in status["agents"] if a["name"] == "hypr-codex")
        transcript = next((self.root / ".codex/sessions").glob("**/*-" + thread + ".jsonl"))
        content = transcript.read_bytes()
        before = subprocess.check_output([self.tmux, "-S", self.socket, "list-panes", "-a", "-F", "#{pane_pid}"], text=True)
        for args in [["resume", thread, "--yolo"], ["resume", "--last", "--yolo"], ["resume", "--yolo"]]:
            fd = self.resume_client(args, cli="codex")
            if args == ["resume", "--yolo"]:
                self.read_until(fd, b"Session number")
                os.write(fd, b"1\n")
            self.read_until(fd, b"FAKE_READY")
        after = subprocess.check_output([self.tmux, "-S", self.socket, "list-panes", "-a", "-F", "#{pane_pid}"], text=True)
        self.assertEqual(before, after, "resume started a second native writer")
        self.assertEqual(transcript.read_bytes(), content, "resume mutated the existing transcript")
        entries = json.loads((self.root / ".blueprint/agentbook.json").read_text())["agents"]
        self.assertEqual([a["name"] for a in entries], ["hypr-codex"])
        os.write(original, b"\x03")
        self.wait_closed("hypr-codex")

    def test_codex_resume_foreign_writer_fails_before_creating_pane(self):
        import fcntl
        thread = "01a08084-80c4-75a3-bbfc-3b7ee1645d2a"
        locks = self.root / ".codex/thread-writer-locks"
        locks.mkdir(parents=True)
        path = locks / (thread + ".lock")
        with path.open("w+") as writer:
            writer.write("preserve writer evidence")
            writer.flush()
            fcntl.flock(writer, fcntl.LOCK_EX | fcntl.LOCK_NB)
            fd = self.resume_client(["resume", thread, "--yolo"], cli="codex")
            self.read_until(fd, b"active writer outside a matched tmux pane")
            book = self.root / ".blueprint/agentbook.json"
            self.assertEqual(json.loads(book.read_text()).get("agents", []), [])
            self.assertEqual(path.read_text(), "preserve writer evidence")

    def test_codex_resume_after_exit_keeps_thread_and_owner_name(self):
        shutil.copyfile(self.fake_tui, self.bin / "codex")
        original = self.start("codex", "hypr-codex")
        status = json.loads(subprocess.check_output([self.binary, "status", "--json"], env=self.env))
        thread = next(a["thread_id"] for a in status["agents"] if a["name"] == "hypr-codex")
        attach = self.resume_client(["resume", thread], cli="codex")
        self.read_until(attach, b"FAKE_READY")
        os.write(original, b"\x03")
        self.wait_closed("hypr-codex")
        reopened = self.resume_client(["resume", thread, "--yolo"], cli="codex")
        self.read_until(reopened, b"FAKE_READY")
        status = json.loads(subprocess.check_output([self.binary, "status", "--json"], env=self.env))
        row = next(a for a in status["agents"] if a["name"] == "hypr-codex")
        self.assertEqual(row["thread_id"], thread, row)
        entries = json.loads((self.root / ".blueprint/agentbook.json").read_text())["agents"]
        self.assertEqual([a["name"] for a in entries], ["hypr-codex"])
        os.write(reopened, b"\x03")
        self.wait_closed("hypr-codex")


    def test_local_codex_identity_is_readable_without_granting_authority(self):
        shutil.copyfile(self.fake_tui, self.bin / "codex")
        result = self.root / "identity-result.json"
        self.env["BP_FAKE_IDENTITY_RESULT"] = str(result)
        fd = self.start("codex", "my-codex")
        status = json.loads(subprocess.check_output([self.binary,"status","--json"],env=self.env))
        root = next(a["thread_id"] for a in status["agents"] if a["name"] == "my-codex")
        child = "22222222-2222-2222-2222-222222222222"
        parent_path = next((self.root / ".codex/sessions").glob("**/*-"+root+".jsonl"))
        child_path = parent_path.parent / ("rollout-child-"+child+".jsonl")
        child_path.write_text(json.dumps({"type":"session_meta","payload":{"id":child,
            "source":{"subagent":{"thread_spawn":{"parent_thread_id":root}}}}})+"\n")
        for thread, label in [(root,"my-codex?"),(child,"my-codex/subagent:"+child+"?")]:
            os.write(fd,("__bp_whoami "+thread+"\r").encode())
            deadline = time.monotonic()+5
            observed = {}
            while time.monotonic()<deadline:
                if result.exists():
                    observed = json.loads(result.read_text())
                    if observed.get("ThreadID")==thread: break
                time.sleep(.05)
            self.assertEqual(observed.get("Label"),label,observed)
            self.assertFalse(observed.get("Certain"),observed)
            self.assertFalse(observed.get("authority"),observed)
        os.write(fd,b"quit\r")
        self.wait_closed("my-codex")

    def test_continue_reuses_live_owner_and_symlink(self):
        shutil.copyfile(self.fake_tui, self.bin / "claude")
        self.start("claude", "advice")
        observation = next((self.root / ".blueprint/state/local").glob("run-*/observation.json"))
        thread = json.loads(observation.read_text())["session_id"]
        before = subprocess.check_output([self.tmux, "-S", self.socket, "list-panes", "-a", "-F", "#{pane_pid}"], text=True)
        alias = self.root.parent / (self.root.name + "-alias")
        alias.symlink_to(self.root)
        try:
            for args, cwd in [(["-c"], alias), (["--resume", thread], self.root), (["--continue"], alias)]:
                fd = self.resume_client(args, cwd)
                self.read_until(fd, b"FAKE_READY")
            after = subprocess.check_output([self.tmux, "-S", self.socket, "list-panes", "-a", "-F", "#{pane_pid}"], text=True)
            self.assertEqual(before, after)
            entries = json.loads((self.root / ".blueprint/agentbook.json").read_text())["agents"]
            self.assertEqual([a["name"] for a in entries], ["advice"])
            self.assertEqual(entries[0]["folder"], str(self.root.resolve()))
        finally:
            alias.unlink()

    def test_resume_after_exit_reuses_record_and_thread(self):
        shutil.copyfile(self.fake_tui, self.bin / "claude")
        fd = self.start("claude", "advice")
        observation = next((self.root / ".blueprint/state/local").glob("run-*/observation.json"))
        thread = json.loads(observation.read_text())["session_id"]
        os.write(fd, b"quit\r")
        self.wait_closed("advice")
        fd = self.resume_client(["-c"])
        self.read_until(fd, b"FAKE_READY")
        entries = json.loads((self.root / ".blueprint/agentbook.json").read_text())["agents"]
        self.assertEqual([a["name"] for a in entries], ["advice"])
        current = json.loads(Path(entries[0]["localRuntime"]["path"]).read_text())
        self.assertEqual(current["session_id"], thread)

    def test_multiple_legacy_owners_block_resume_without_killing(self):
        shutil.copyfile(self.fake_tui, self.bin / "claude")
        self.start("claude", "advice-one")
        self.start("claude", "advice-two")
        entries = json.loads((self.root / ".blueprint/agentbook.json").read_text())["agents"]
        first = json.loads(Path(entries[0]["localRuntime"]["path"]).read_text())
        path = Path(entries[1]["localRuntime"]["path"])
        path.write_text(json.dumps(first))  # reproduce old launchers sharing one native UUID
        before = subprocess.check_output([self.tmux, "-S", self.socket, "list-panes", "-a", "-F", "#{pane_pid}"], text=True)
        fd = self.resume_client(["--resume", first["session_id"]])
        self.read_until(fd, b"multiple live bp owners")
        after = subprocess.check_output([self.tmux, "-S", self.socket, "list-panes", "-a", "-F", "#{pane_pid}"], text=True)
        self.assertEqual(before, after)

    def test_codex_native_name_index_updates_bar_and_alias(self):
        shutil.copyfile(self.fake_tui, self.bin / "codex")
        self.start("codex", "codex-original")
        entries = json.loads((self.root / ".blueprint/agentbook.json").read_text())["agents"]
        # The fake Codex holds the same kernel writer lock used by the runtime probe.
        transcript = next((self.root / ".codex/sessions").glob("*/*/*/*.jsonl"))
        thread = json.loads(transcript.read_text().splitlines()[0])["payload"]["id"]
        index = self.root / ".codex/session_index.jsonl"
        def rename(title):
            with index.open("a") as f:
                f.write(json.dumps(dict(id=thread, thread_name=title, updated_at="2026-09-08T12:00:00Z")) + "\n")
        rename("hypr-codex")
        result = subprocess.run([self.binary, "name", "codex-original"], env=self.env, capture_output=True, text=True, check=True)
        self.assertIn("hypr-codex", result.stdout)
        result = subprocess.run([self.binary, "color", "hypr-codex", "red"], env=self.env, capture_output=True, text=True)
        self.assertEqual(result.returncode, 0, result.stderr)
        rename("server-main")
        result = subprocess.run([self.binary, "name", "codex-original"], env=self.env, capture_output=True, text=True, check=True)
        self.assertNotIn("server-main", result.stdout)
        self.assertIn("codex-original", result.stdout)

    def test_concurrent_resume_creates_only_one_pane(self):
        thread = "2832a3a6-1234-1234-1234-123456789012"
        # No observer is needed: the durable claim must close the startup gap.
        first = self.resume_client(["--resume", thread])
        second = self.resume_client(["--resume", thread])
        self.read_until(first, b"FAKE_READY")
        self.read_until(second, b"FAKE_READY")
        panes = subprocess.check_output([self.tmux, "-S", self.socket, "list-panes", "-a", "-F", "#{pane_pid}"], text=True).splitlines()
        self.assertEqual(len(panes), 1)

    def test_onboard_starts_one_main_and_repeated_call_attaches(self):
        capture = self.root / "launch-args.json"
        self.env["BP_FAKE_ARGS"] = str(capture)
        def onboard():
            pid, fd = pty.fork()
            if pid == 0:
                os.chdir(self.root)
                os.execve(self.binary, [self.binary, "onboard", "--cli", "codex"], self.env)
            self.children.append((pid, fd))
            self.read_until(fd, b"FAKE_READY")
            return fd
        fd = onboard()
        self.assertTrue(self.alive("main"))
        before = subprocess.check_output([self.tmux, "-S", self.socket, "list-panes", "-a", "-F", "#{pane_pid}"], text=True)
        args = json.loads(capture.read_text())
        self.assertIn("Read ONBOARDING.md", args[-1])
        self.assertNotIn("--yolo", args)
        self.assertNotIn("--dangerously-skip-permissions", args)
        onboard()
        after = subprocess.check_output([self.tmux, "-S", self.socket, "list-panes", "-a", "-F", "#{pane_pid}"], text=True)
        self.assertEqual(before, after)
        agents = json.loads((self.root / ".blueprint/agentbook.json").read_text())
        self.assertEqual(agents["orchestrator"], "main")
        self.assertEqual(len(agents["agents"]), 1)
        os.write(fd, b"quit\r")
        self.wait_closed("main")

    def test_version_and_doctor_with_broken_configuration(self):
        config = self.root / ".blueprint/config.yaml"
        config.parent.mkdir()
        config.write_text("bar: [broken\n")
        result = subprocess.run([self.binary, "version", "--json"], env=self.env, capture_output=True, text=True)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(json.loads(result.stdout)["version"], (REPO / "internal/release/version.txt").read_text().strip())
        result = subprocess.run([self.binary, "doctor", "--json"], env=self.env, capture_output=True, text=True)
        self.assertNotEqual(result.returncode, 0)
        report = json.loads(result.stdout)
        self.assertFalse(report["ok"])
        self.assertFalse(next(c for c in report["checks"] if c["name"] == "config")["ok"])

    def test_disable_preserves_aliases_and_records(self):
        rc = self.root / ".bashrc"
        rc.write_text('alias claude="claude --dangerously-skip-permissions"\n')
        subprocess.run([self.binary, "setup"], env=self.env, check=True, capture_output=True)
        book = self.root / ".blueprint/agentbook.json"
        before = book.read_bytes()
        subprocess.run([self.binary, "setup", "--disable"], env=self.env, check=True, capture_output=True)
        self.assertEqual(before, book.read_bytes())
        result = subprocess.run(["bash", "-ic", "type claude"], env=self.env, capture_output=True, text=True)
        self.assertIn("dangerously-skip-permissions", result.stdout)
        self.assertNotIn("_bp_agent", (self.root / ".config/bp/shell.sh").read_text())

    @unittest.skipUnless(shutil.which("openssl"), "OpenSSL required")
    def test_signed_installer_rejects_tampering_before_replacement(self):
        import hashlib
        fixtures = self.root / "release-fixtures"
        fixtures.mkdir()
        private = fixtures / "private.pem"
        public = fixtures / "public.pem"
        subprocess.run(["openssl", "genpkey", "-algorithm", "ED25519", "-out", str(private)], check=True, capture_output=True)
        subprocess.run(["openssl", "pkey", "-in", str(private), "-pubout", "-out", str(public)], check=True, capture_output=True)
        candidate = fixtures / "bp-linux-amd64"
        shutil.copyfile(self.binary, candidate)
        (fixtures / "latest.version").write_text("1.6.0\n")
        checksums = fixtures / "checksums.txt"
        checksums.write_text("# bp-release 1.6.0\n" + hashlib.sha256(candidate.read_bytes()).hexdigest() + "  bp-linux-amd64\n")
        subprocess.run(["openssl", "pkeyutl", "-sign", "-inkey", str(private), "-rawin", "-in", str(checksums), "-out", str(fixtures / "checksums.sig")], check=True, capture_output=True)
        installer = self.root / "install-test.sh"
        installer.write_text((REPO / "install.sh").read_text().replace((REPO / "internal/release/release.pub").read_text().strip(), public.read_text().strip()))
        curl = self.bin / "curl"
        curl.write_text('#!/usr/bin/env python3\nimport os,sys,pathlib,urllib.parse\na=sys.argv[1:]\nu=next(x for x in a if x.startswith("https://"))\np=pathlib.Path(os.environ["BP_TEST_RELEASE"]) / pathlib.Path(urllib.parse.urlparse(u).path).name\ndata=p.read_bytes()\nif "-o" in a: pathlib.Path(a[a.index("-o")+1]).write_bytes(data)\nelse: sys.stdout.buffer.write(data)\n')
        curl.chmod(0o755)
        env = dict(self.env, BP_TEST_RELEASE=str(fixtures), BP_ONBOARD="skip")
        target = self.root / ".local/bin/bp"
        result = subprocess.run(["sh", str(installer), "--local"], env=env, capture_output=True, text=True)
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        original = target.read_bytes()
        candidate.write_bytes(b"tampered executable")
        result = subprocess.run(["sh", str(installer), "--local"], env=env, capture_output=True, text=True)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("SHA-256 mismatch", result.stdout)
        self.assertEqual(target.read_bytes(), original)
        checksums.write_text("# bp-release 1.6.0\n" + "0"*64 + "  bp-linux-amd64\n")
        result = subprocess.run(["sh", str(installer), "--local"], env=env, capture_output=True, text=True)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("signature", result.stdout)
        self.assertEqual(target.read_bytes(), original)

    def test_installer_setup_failure_restores_existing_binary(self):
        target = self.root / ".local/bin/bp"
        target.parent.mkdir(parents=True)
        target.write_text("original-binary")
        candidate = self.root / "candidate"
        candidate.write_text("#!/bin/sh\ncase \"$1\" in\nhelp) echo 'bp setup bp config path|check bp run '; exit 0;;\nsetup) [ \"${2:-}\" = --check ]; exit $?;;\nesac\nexit 1\n")
        candidate.chmod(0o755)
        result = subprocess.run(["sh", str(REPO / "install.sh"), "--local"], env=dict(self.env, BP_LOCAL_BINARY=str(candidate), BP_ONBOARD="skip"), capture_output=True, text=True)
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(target.read_text(), "original-binary")
        self.assertIn("previous binary restored", result.stdout)

    def test_installer_does_not_install_dependencies_without_opt_in(self):
        dependency_bin = self.root / "dependency-bin"
        dependency_bin.mkdir()
        (dependency_bin / "uname").write_text('#!/bin/sh\ncase "$1" in -s) echo Linux;; -m) echo x86_64;; esac\n')
        (dependency_bin / "uname").chmod(0o755)
        marker = self.root / "package-manager-was-called"
        for name in ["apt-get", "brew", "sudo"]:
            command = dependency_bin / name
            command.write_text('#!/bin/sh\n/bin/touch "' + str(marker) + '"\n')
            command.chmod(0o755)
        result = subprocess.run(["/bin/sh", str(REPO / "install.sh")],
            env=dict(self.env, PATH=str(dependency_bin), BP_LOCAL_BINARY=self.binary), capture_output=True, text=True)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("tmux is required", result.stdout)
        self.assertFalse(marker.exists())
        self.assertFalse((self.root / ".local/bin/bp").exists())

    def test_installer_preflight_keeps_existing_binary(self):
        target = self.root / ".local/bin/bp"
        target.parent.mkdir(parents=True)
        target.write_text("original-binary")
        env = dict(self.env, BP_LOCAL_BINARY=self.binary, BP_ONBOARD="skip", SHELL="/bin/fish")
        result = subprocess.run(["sh", str(REPO / "install.sh"), "--local"], env=env, capture_output=True, text=True)
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(target.read_text(), "original-binary")

    def test_quit_closes_only_own_session_for_each_cli(self):
        for cli in ["codex", "claude", "opencode", "hermes"]:
            fd = self.start(cli)
            os.write(fd, b"quit\r")
            self.wait_closed(cli + "-test")

    def test_ctrl_c_cancellation_keeps_session_until_cli_exits(self):
        fd = self.start("codex")
        os.write(fd, b"\x03")
        self.read_until(fd, b"CANCELLED_ONCE")
        self.assertTrue(self.alive("codex-test"))
        os.write(fd, b"\x03")
        self.wait_closed("codex-test")

    def test_local_bar_is_session_scoped_and_setup_repairs_open_session(self):
        def tmux(*args):
            return subprocess.check_output([self.tmux, "-S", self.socket, *args], text=True).strip()
        # A normal user session must retain its theme and key/scroll settings.
        subprocess.run([str(self.bin / "tmux"), "new-session", "-d", "-s", "other", "sleep 60"],
                       env=self.env, check=True)
        tmux("set-option", "-t", "=other:", "status-right", "USER_BAR")
        global_before = {key: tmux("show-options", "-g", "-v", key)
                         for key in ["status-style", "status-left", "status-right", "mouse", "prefix"]}
        fd = self.start("codex")
        before_pid = tmux("display-message", "-p", "-t", "=codex-test:", "#{pane_pid}")
        self.assertEqual(tmux("show-options", "-t", "=codex-test:", "-v", "mouse"), "on")
        # A real terminal wheel event must enter tmux copy-mode, not the CLI.
        os.write(fd, b"\x1b[<64;5;5M")
        deadline = time.monotonic() + 3
        while tmux("display-message", "-p", "-t", "=codex-test:", "#{pane_in_mode}") != "1":
            if time.monotonic() > deadline: self.fail("wheel did not enter copy-mode")
            time.sleep(0.05)
        tmux("send-keys", "-t", "=codex-test:", "-X", "begin-selection")
        self.assertEqual(tmux("display-message", "-p", "-t", "=codex-test:", "#{selection_present}"), "1")
        tmux("send-keys", "-t", "=codex-test:", "-X", "cancel")

        for key, subcommand in [("status-left", " name "), ("status-right", " bar ")]:
            value = tmux("show-options", "-t", "=codex-test:", "-v", key)
            self.assertIn(subcommand, value)
            self.assertIn(str(self.root / ".blueprint"), value)
            self.assertIn(self.binary, value)
        self.assertNotIn("green", tmux("show-options", "-t", "=codex-test:", "-v", "status-style"))
        # Reproduce the old installer: an already-open bp session has no bp bar.
        tmux("set-option", "-t", "=codex-test:", "status-right", "OLD_BAR")
        subprocess.run([self.binary, "setup", "--shell", "bash"], env=self.env,
                       check=True, capture_output=True)
        self.assertIn(" bar ", tmux("show-options", "-t", "=codex-test:", "-v", "status-right"))
        self.assertEqual(before_pid, tmux("display-message", "-p", "-t", "=codex-test:", "#{pane_pid}"))
        self.assertEqual(tmux("show-options", "-t", "=other:", "-v", "status-right"), "USER_BAR")
        for key, value in global_before.items():
            self.assertEqual(tmux("show-options", "-g", "-v", key), value, key)
        settings = self.root / ".blueprint" / "config.yaml"
        settings.write_text(settings.read_text() + "\nlocalMouse: false\n" if "localMouse:" not in settings.read_text() else settings.read_text().replace("localMouse: true", "localMouse: false"))
        subprocess.run([self.binary, "setup", "--shell", "bash"], env=self.env, check=True, capture_output=True)
        self.assertEqual(tmux("show-options", "-t", "=codex-test:", "-v", "mouse"), "off")
        os.write(fd, b"quit\r")
        self.wait_closed("codex-test")
        self.assertTrue(self.alive("other"))

    def test_noninteractive_arguments_and_exit_status_are_unchanged(self):
        args = ["exec", "a b", "$(touch SHOULD_NOT_EXIST)", "quote'", "--help"]
        result = subprocess.run([self.binary, "run", "codex", *args], env=dict(self.env, BP_FAKE_BATCH="1"), capture_output=True, text=True)
        self.assertEqual(result.returncode, 37)
        self.assertEqual(json.loads(result.stdout), args)
        self.assertFalse((self.root / ".blueprint").exists())

    def test_one_command_install_and_repeated_shell_setup(self):
        (self.root / ".bashrc").write_text("export KEEP_ME=yes\n")
        env = dict(self.env, BP_LOCAL_BINARY=self.binary)
        for _ in range(2):
            subprocess.run(["sh", str(REPO / "install.sh"), "--local"], env=env, check=True, capture_output=True)
        rc = (self.root / ".bashrc").read_text()
        self.assertIn("export KEEP_ME=yes", rc)
        self.assertEqual(rc.count("# bp local agents"), 1)
        self.assertFalse((self.root / ".tmux.conf").exists())
        settings = self.root / ".blueprint/config.yaml"
        self.assertTrue(settings.exists())
        self.assertEqual(settings.stat().st_mode & 0o777, 0o600)
        settings.write_text("bar:\n  widgets: [model, quota]\n")
        subprocess.run(["sh", str(REPO / "install.sh"), "--local"], env=env, check=True, capture_output=True)
        self.assertEqual(settings.read_text(), "bar:\n  widgets: [model, quota]\n")
        checked = subprocess.run([self.binary, "config", "check"], env=env, capture_output=True, text=True)
        self.assertEqual(checked.returncode, 0, checked.stderr)
        self.assertIn(str(settings), checked.stdout)
        # Noninteractive shell wrappers preserve output and do not recurse.
        result = subprocess.run(["bash", "-c", '. "$HOME/.config/bp/shell.sh"; codex --version'],
                                env=dict(env, BP_FAKE_BATCH="1"), capture_output=True, text=True)
        self.assertEqual(result.returncode, 37)
        self.assertEqual(json.loads(result.stdout), ["--version"])

    def test_shell_wrappers_fall_back_after_bp_uninstall(self):
        subprocess.run([self.binary, "setup", "--shell", "bash"], env=self.env,
                       capture_output=True, check=True)
        native = self.root / "native-only"
        native.mkdir()
        cli = native / "claude"
        cli.write_text('#!/bin/sh\nprintf "NATIVE_AFTER_UNINSTALL:%s\\n" "$*"\n')
        cli.chmod(0o755)
        pid, fd = pty.fork()
        if pid == 0:
            env = dict(self.env, PATH=str(native))
            os.execve("/bin/bash", ["bash", "--noprofile", "--norc", "-c",
                '. "$HOME/.config/bp/shell.sh"; claude --continue'], env)
        self.children.append((pid, fd))
        self.read_until(fd, b"NATIVE_AFTER_UNINSTALL:--continue")

    def test_existing_aliases_keep_options_and_wrap_in_bash_and_zsh(self):
        for shell in ["bash", "zsh"]:
            if not shutil.which(shell):
                self.skipTest(shell + " is not installed")
            with self.subTest(shell=shell):
                rc = self.root / (".bashrc" if shell == "bash" else ".zshrc")
                aliases = ('alias claude="claude --dangerously-skip-permissions"\n'
                           'alias codex=\'codex --search --yolo --model "example[1m]"\'\n'
                           '# alias gcodex="do-not-run-this"\n')
                rc.write_text(aliases)
                subprocess.run([self.binary, "setup", "--shell", shell], env=self.env,
                               capture_output=True, check=True)
                self.assertTrue(rc.read_text().startswith(aliases))
                for cli, expected in [("claude", ["--dangerously-skip-permissions"]),
                                      ("codex", ["--search", "--yolo", "--model", "example[1m]"])]:
                    record = self.root / (shell + "-" + cli + "-args.json")
                    script = FAKE.replace('count = 0',
                        'open(' + repr(str(record)) + ', "w").write(json.dumps(sys.argv[1:]))\ncount = 0')
                    (self.bin / cli).write_text(script)
                    (self.bin / cli).chmod(0o755)
                    pid, fd = pty.fork()
                    if pid == 0:
                        os.chdir(self.root)
                        env = dict(self.env, SHELL=shutil.which(shell))
                        # Re-source twice: aliases must neither disappear nor gain duplicate flags.
                        command = ('. "$HOME/.config/bp/shell.sh"; . "$HOME/.config/bp/shell.sh"; '
                                   + cli + ' "extra arg" \'$(touch SHOULD_NOT_EXIST)\'; '
                                   'printf "__BP_OUTER_SHELL__\\n"')
                        # CI runners may have global interactive startup hooks (notably
                        # zsh compinit security prompts). Skip all automatic rc loading,
                        # then source the test user's real rc as a script so aliases are
                        # defined before the command's next line is parsed.
                        runner = self.root / (shell + "-" + cli + "-alias-runner")
                        runner.write_text('. "$HOME/.' + shell + 'rc"\n' + command + '\n')
                        shell_args = ([shell, "-f", "-i", str(runner)] if shell == "zsh" else
                                      [shell, "--noprofile", "--norc", "-i", str(runner)])
                        os.execve(shutil.which(shell), shell_args, env)
                    self.children.append((pid, fd))
                    self.read_until(fd, b"FAKE_READY")
                    self.assertEqual(json.loads(record.read_text()),
                                     expected + ["extra arg", "$(touch SHOULD_NOT_EXIST)"])
                    sessions = subprocess.check_output([self.tmux, "-S", self.socket,
                                                        "list-sessions", "-F", "#{session_name}"], text=True).splitlines()
                    own = [name for name in sessions if name.startswith(cli + "-")]
                    self.assertEqual(len(own), 1)
                    os.write(fd, b"quit\r")
                    self.read_until(fd, b"__BP_OUTER_SHELL__")
                    self.wait_closed(own[0])
                    self.assertFalse((self.root / "SHOULD_NOT_EXIST").exists())

    def test_shell_integration_preserves_custom_functions_and_batch_aliases(self):
        subprocess.run([self.binary, "setup", "--shell", "bash"], env=self.env,
                       capture_output=True, check=True)
        for shell in ["bash", "zsh"]:
            if not shutil.which(shell):
                self.skipTest(shell + " is not installed")
            # Functions can be custom launchers. Never replace or activate them during setup.
            script = ('function claude { printf "USER_FUNCTION:%s\\n" "$*"; }\n'
                      'alias codex=\'codex --search --yolo\'\n'
                      '. "$HOME/.config/bp/shell.sh"\n'
                      '. "$HOME/.config/bp/shell.sh"\n'
                      'claude "a b"\n'
                      "eval 'codex --version'\n")
            args = [shell, "-c", ("shopt -s expand_aliases\n" if shell == "bash" else "") + script]
            result = subprocess.run(args, env=dict(self.env, BP_FAKE_BATCH="1"),
                                    capture_output=True, text=True)
            self.assertEqual(result.returncode, 37, result.stderr)
            lines = result.stdout.splitlines()
            self.assertEqual(lines[0], "USER_FUNCTION:a b")
            self.assertEqual(json.loads(lines[1]), ["--search", "--yolo", "--version"])

    def test_fresh_local_sessions_discover_their_own_metrics_without_book_edits(self):
        fds = []
        for cli in ["codex", "claude"]:
            shutil.copyfile(self.fake_tui, self.bin / cli)
            (self.bin / cli).chmod(0o755)
            self.env.update(BP_FAKE_HARNESS=cli, BP_FAKE_VIM="insert",
                            BP_FAKE_BUSY=str(self.root / "absent"), BP_FAKE_RECEIVED=str(self.root / (cli + "-received")))
            fds.append((cli, self.start(cli)))
        status = subprocess.run([self.binary, "status", "--json"], env=self.env,
                                capture_output=True, text=True, check=True)
        agents = {a["name"]: a for a in json.loads(status.stdout)["agents"]}
        ids = set()
        for cli, fd in fds:
            row = agents[cli + "-test"]
            self.assertEqual(row["activity"].get("binding"), "local-launch-observer", row)
            self.assertEqual(row["activity"]["state"], "idle", row)
            self.assertEqual(row["context_window"], 200000)
            self.assertEqual(row["ctx_tokens"], 30000)
            ids.add(row["thread_id"])
            deadline = time.monotonic() + 5
            while True:
                bar = subprocess.check_output([self.binary, "bar", cli + "-test"], env=self.env, text=True)
                if "30k" in bar or time.monotonic() >= deadline: break
                time.sleep(0.2)
            self.assertIn("30k", bar)
            self.assertNotIn("boş", bar)
            self.assertIn("astra high" if cli == "codex" else "fable med", bar)
            # A sibling process must not impersonate the CLI's observation callback.
            book = json.loads((self.root / ".blueprint/agentbook.json").read_text())
            binding = next(a["localRuntime"] for a in book["agents"] if a["name"] == cli + "-test")
            if cli == "claude":
                original = Path(binding["path"]).read_bytes()
                attack = subprocess.run([self.binary, "_observe", binding["path"], cli], input=original,
                                    env=self.env, capture_output=True)
                self.assertNotEqual(attack.returncode, 0)
                self.assertEqual(Path(binding["path"]).read_bytes(), original)
            os.write(fd, b"\x03")
            self.wait_closed(cli + "-test")
        self.assertEqual(len(ids), 2, "same cwd merged different conversations")

    def test_native_rename_updates_name_color_and_message_target(self):
        received=self.root/"received.jsonl"
        shutil.copyfile(self.fake_tui,self.bin/"claude")
        (self.bin/"claude").chmod(0o755)
        self.env.update(BP_FAKE_HARNESS="claude",BP_FAKE_VIM="insert",BP_FAKE_BUSY=str(self.root/"absent"),BP_FAKE_RECEIVED=str(received))
        fd=self.start("claude")
        os.write(fd,b"/rename orch\r")
        deadline=time.monotonic()+8
        while True:
            plate=subprocess.check_output([self.binary,"name","claude-test"],env=self.env,text=True)
            if " orch " in plate:break
            if time.monotonic()>deadline:self.fail(plate)
            time.sleep(0.1)
        self.assertIn("colour135",plate)
        row=next(a for a in json.loads(subprocess.check_output([self.binary,"status","--json"],env=self.env,text=True))["agents"] if a["name"]=="claude-test")
        self.assertEqual(row["display_name"],"orch")
        self.assertEqual(row["activity"]["state"],"idle")
        color=json.loads(subprocess.check_output([self.binary,"color","orch","--json"],env=self.env,text=True))
        self.assertEqual(color["index"],135)
        result=subprocess.run([self.binary,"msg","orch","message to renamed agent fixture"],env=self.env,capture_output=True,text=True,timeout=15)
        channel=re.search(r"CHANNEL=(q[0-9]+)",result.stdout)
        self.assertIsNotNone(channel,result.stdout+result.stderr)
        done=self.root/".blueprint/msgq/done"/(channel.group(1)+".json")
        deadline=time.monotonic()+12
        while not done.exists() and time.monotonic()<deadline:time.sleep(0.1)
        self.assertTrue(done.exists())
        self.assertNotIn("unverified",done.read_text().lower())
        self.assertEqual(len(received.read_text().splitlines()),2)
        second=self.start("claude", "claude-second")
        os.write(second,b"/rename orch\r")
        deadline=time.monotonic()+8
        while " orch " not in subprocess.check_output([self.binary,"name","claude-second"],env=self.env,text=True):
            if time.monotonic()>deadline:self.fail("second rename not observed")
            time.sleep(0.1)
        ambiguous=subprocess.run([self.binary,"msg","orch","must not route ambiguously"],env=self.env,capture_output=True,text=True)
        self.assertNotEqual(ambiguous.returncode,0)
        self.assertIn("ambiguous agent title",ambiguous.stderr)
        os.write(second,b"/rename server-main\r")
        deadline=time.monotonic()+8
        while " claude-second " not in subprocess.check_output([self.binary,"name","claude-second"],env=self.env,text=True):
            if time.monotonic()>deadline:self.fail("reserved title was displayed as identity")
            time.sleep(0.1)
        os.write(second,b"\x03")
        self.wait_closed("claude-second")
        # Native rename never changes the canonical session or hierarchy.
        self.assertTrue(self.alive("claude-test"))
        self.assertFalse(self.alive("orch"))
        os.write(fd,b"\x03")
        self.wait_closed("claude-test")

    def test_unpinned_server_codex_binds_writer_and_delivers(self):
        received = self.root / "received.jsonl"
        shutil.copyfile(self.fake_tui, self.bin / "codex")
        (self.bin / "codex").chmod(0o755)
        self.env.update(BP_FAKE_HARNESS="codex", BP_FAKE_VIM="insert",
                        BP_FAKE_BUSY=str(self.root / "absent"), BP_FAKE_RECEIVED=str(received))
        fd=self.start("codex")
        path=self.root/".blueprint/agentbook.json"
        book=json.loads(path.read_text())
        entry=next(a for a in book["agents"] if a["name"]=="codex-test")
        entry.pop("localRuntime",None)
        entry["launch"]={"codex":True,"resumeId":"aaaaaaaa-1111-1111-1111-111111111111"}
        path.write_text(json.dumps(book))
        status=json.loads(subprocess.check_output([self.binary,"status","--json"],env=self.env,text=True))
        row=next(a for a in status["agents"] if a["name"]=="codex-test")
        self.assertEqual(row["activity"].get("binding"),"pane-writer-lock",row)
        self.assertEqual(row["activity"]["state"],"idle",row)
        self.assertFalse(row["activity"]["delivery_blocked"],row)
        result=subprocess.run([self.binary,"msg","codex-test","unpinned writer fixture"],env=self.env,capture_output=True,text=True,timeout=15)
        channel=re.search(r"CHANNEL=(q[0-9]+)",result.stdout)
        self.assertIsNotNone(channel,result.stdout+result.stderr)
        done=self.root/".blueprint/msgq/done"/(channel.group(1)+".json")
        deadline=time.monotonic()+12
        while not done.exists() and time.monotonic()<deadline: time.sleep(0.1)
        self.assertTrue(done.exists(),"unpinned writer did not deliver")
        self.assertNotIn("unverified",done.read_text().lower())
        self.assertEqual(len(received.read_text().splitlines()),1)
        os.write(fd,b"\x03")
        self.wait_closed("codex-test")

    def test_local_worker_delivers_queued_message_after_busy_turn(self):
        busy = self.root / "busy"
        received = self.root / "received.jsonl"
        busy.touch()
        shutil.copyfile(self.fake_tui, self.bin / "codex")
        (self.bin / "codex").chmod(0o755)
        self.env.update(BP_FAKE_BUSY=str(busy), BP_FAKE_RECEIVED=str(received))
        fd = self.start("codex")
        result = subprocess.run([self.binary, "msg", "codex-test", "delivery fixture message"],
                                env=self.env, capture_output=True, text=True, timeout=10)
        self.assertIn("RESULT=queued", result.stdout, result.stdout + result.stderr)
        self.assertFalse(received.exists(), "message interrupted busy target")
        busy.unlink()
        deadline = time.monotonic() + 12
        while not received.exists() and time.monotonic() < deadline:
            time.sleep(0.1)
        if not received.exists():
            pane = subprocess.run([self.tmux, "-S", self.socket, "capture-pane", "-pt", "codex-test"], capture_output=True, text=True).stdout
            commands = subprocess.run([self.tmux, "-S", self.socket, "list-panes", "-a", "-F", "#{pane_current_command}"], capture_output=True, text=True).stdout
            logs = (self.root / ".blueprint/state/local-delivery.log").read_text()
            self.fail(f"local dispatcher did not deliver; commands={commands!r} pane={pane!r} log={logs!r}")
        messages = [json.loads(line) for line in received.read_text().splitlines()]
        self.assertEqual(len(messages), 1)
        self.assertIn("delivery fixture message", messages[0])
        os.write(fd, b"\x03")
        self.wait_closed("codex-test")

    def test_manual_compact_delivers_without_another_user_turn(self):
        import datetime
        received = self.root / "received.jsonl"
        shutil.copyfile(self.fake_tui, self.bin / "claude")
        (self.bin / "claude").chmod(0o755)
        self.env.update(BP_FAKE_VIM="insert", BP_FAKE_HARNESS="claude",
                        BP_FAKE_BUSY=str(self.root / "absent"), BP_FAKE_RECEIVED=str(received))
        fd = self.start("claude")
        path = Path(next(self.root.glob("native-*-transcript")).read_text())
        stamp = datetime.datetime.now(datetime.timezone.utc)
        def append(row):
            with path.open("a") as f: f.write(json.dumps(row) + "\n")
        append({"type":"user", "timestamp":(stamp-datetime.timedelta(hours=1)).isoformat(), "message":{"content":"old task"}})
        result = subprocess.run([self.binary,"msg","claude-test","after compact fixture"],env=self.env,capture_output=True,text=True,timeout=10)
        self.assertIn("RESULT=queued",result.stdout,result.stdout+result.stderr)
        self.assertFalse(received.exists())
        append({"type":"system","subtype":"compact_boundary","timestamp":stamp.isoformat(),"compactMetadata":{"trigger":"manual"}})
        append({"type":"user","isCompactSummary":True,"timestamp":stamp.isoformat(),"message":{"content":"summary"}})
        append({"type":"user","timestamp":stamp.isoformat(),"message":{"content":"<local-command-stdout>Compacted (ctrl+o to see full summary)</local-command-stdout>"}})
        retry=subprocess.run([self.binary,"q","--retry"],env=self.env,capture_output=True,text=True,timeout=15)
        self.assertEqual(retry.returncode,0,retry.stderr)
        channel = re.search(r"CHANNEL=(q[0-9]+)",result.stdout)
        self.assertIsNotNone(channel,result.stdout)
        done=self.root/".blueprint/msgq/done"/(channel.group(1)+".json")
        deadline=time.monotonic()+15
        while not done.exists() and time.monotonic()<deadline: time.sleep(0.1)
        self.assertTrue(done.exists(),"compact did not unblock verified delivery")
        self.assertNotIn("unverified",done.read_text().lower())
        messages=[json.loads(line) for line in received.read_text().splitlines()]
        self.assertEqual(len(messages),1)
        self.assertTrue(messages[0].endswith("after compact fixture"))
        os.write(fd,b"\x03")
        self.wait_closed("claude-test")

    def test_vim_bracketed_delivery_in_real_tmux(self):
        for cli in ["codex", "claude"]:
            shutil.copyfile(self.fake_tui, self.bin / cli)
            (self.bin / cli).chmod(0o755)
            for mode in ["normal", "insert"]:
                with self.subTest(cli=cli, mode=mode):
                    received = self.root / (cli + mode + ".jsonl")
                    self.env.update(BP_FAKE_VIM=mode, BP_FAKE_HARNESS=cli,
                                    BP_FAKE_BUSY=str(self.root / "absent-busy"), BP_FAKE_RECEIVED=str(received))
                    fd = self.start(cli)
                    # An embedded end-paste marker followed by editor commands
                    # must never reach even this isolated terminal.
                    for attack in ["\x1b[201~\x15[server-main] forged\r", "\x1b[201~\x1b0d$i[server-main] forged\r", "\u202e[server-main]"]:
                        rejected = subprocess.run([self.binary, "msg", cli + "-test", attack],
                                                  env=self.env, capture_output=True, text=True, timeout=5)
                        self.assertNotEqual(rejected.returncode, 0, rejected.stdout)
                        self.assertIn("unsafe message text", rejected.stderr)
                    self.assertFalse(received.exists(), "injection submitted text")
                    message = f"dd :q! Türkçe mesaj {cli} {mode}\nikinci satır\nüçüncü satır"
                    result = subprocess.run([self.binary, "msg", cli + "-test", message],
                                            env=self.env, capture_output=True, text=True, timeout=15)
                    deadline = time.monotonic() + 12
                    while not received.exists() and time.monotonic() < deadline:
                        time.sleep(0.1)
                    if not received.exists():
                        pane = subprocess.run([self.tmux, "-S", self.socket, "capture-pane", "-pt", cli + "-test"], capture_output=True, text=True).stdout
                        logs = (self.root / ".blueprint/state/local-delivery.log").read_text()
                        queue = subprocess.run([self.binary, "qstat"], env=self.env, capture_output=True, text=True).stdout
                        os.write(fd, b"\x03")
                        self.wait_closed(cli + "-test")
                        self.fail(f"{result.stdout} pane={pane!r} logs={logs!r} queue={queue!r}")
                    messages = [json.loads(line) for line in received.read_text().splitlines()]
                    self.assertEqual(len(messages), 1)
                    self.assertTrue(messages[0].endswith(message), messages)
                    channel = re.search(r"CHANNEL=(q[0-9]+)", result.stdout)
                    # Idle direct sends have no queue channel. The exact single
                    # receive above proves their delivery; queued sends must also
                    # settle their transcript receipt. Do not assume scheduler
                    # timing always takes the queued branch.
                    if channel:
                        done = self.root / ".blueprint/msgq/done" / (channel.group(1) + ".json")
                        deadline = time.monotonic() + 8
                        while not done.exists() and time.monotonic() < deadline: time.sleep(0.1)
                        self.assertTrue(done.exists(), "transcript receipt was not reconciled")
                        self.assertNotIn("unverified", done.read_text().lower())
                    else:
                        self.assertEqual(result.returncode, 0, result.stderr)
                        self.assertIn("RESULT=delivered", result.stdout)
                    time.sleep(0.8)  # Let the dispatcher observe the cleared composer before exit.
                    os.write(fd, b"\x03")
                    self.wait_closed(cli + "-test")

    def test_exiting_one_session_leaves_another_running(self):
        first = self.start("codex")
        second = self.start("claude")
        os.write(first, b"quit\r")
        self.wait_closed("codex-test")
        self.assertTrue(self.alive("claude-test"))
        os.write(second, b"quit\r")
        self.wait_closed("claude-test")

    def test_interactive_shell_wrapper_returns_to_outer_shell(self):
        subprocess.run(["sh", str(REPO / "install.sh"), "--local"],
                       env=dict(self.env, BP_LOCAL_BINARY=self.binary), check=True, capture_output=True)
        pid, fd = pty.fork()
        if pid == 0:
            os.chdir(self.root)
            os.execve("/bin/bash", ["bash", "-c", '. "$HOME/.config/bp/shell.sh"; codex; echo OUTER_SHELL; read -r done'], self.env)
        self.children.append((pid, fd))
        self.read_until(fd, b"FAKE_READY")
        os.write(fd, b"quit\r")
        self.read_until(fd, b"OUTER_SHELL")
        result = subprocess.run([self.tmux, "-S", self.socket, "list-sessions"], capture_output=True)
        self.assertNotEqual(result.returncode, 0)
        os.write(fd, b"done\r")


if __name__ == "__main__":
    unittest.main()
