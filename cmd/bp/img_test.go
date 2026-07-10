package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	bptmux "blueprint/internal/tmux"
)

func TestReceiveImageDetectsSupportedFormats(t *testing.T) {
	tests := []struct {
		name string
		data []byte
		ext  string
	}{
		{name: "png", data: append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 24)...), ext: ".png"},
		{name: "jpeg", data: []byte("\xff\xd8\xff\xe0\x00\x10JFIF\x00"), ext: ".jpg"},
		{name: "gif", data: []byte("GIF89a\x01\x00\x01\x00"), ext: ".gif"},
		{name: "webp", data: []byte("RIFF\x04\x00\x00\x00WEBPVP8 "), ext: ".webp"},
		{name: "bmp", data: append([]byte("BM\x1a\x00\x00\x00\x00\x00\x00\x00\x1a\x00\x00\x00"), make([]byte, 20)...), ext: ".bmp"},
	}
	now := time.Date(2026, 7, 10, 12, 34, 56, 0, time.UTC)
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			path, err := receiveImage(bytes.NewReader(test.data), dir, now)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.HasSuffix(path, test.ext) {
				t.Fatalf("path=%q, want extension %s", path, test.ext)
			}
			info, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode().Perm() != 0o644 {
				t.Fatalf("mode=%o, want 644", info.Mode().Perm())
			}
		})
	}
}

func TestReceiveImageRejectsEmptyAndNonImageInput(t *testing.T) {
	for _, data := range [][]byte{nil, []byte("plain text")} {
		if _, err := receiveImage(bytes.NewReader(data), t.TempDir(), time.Now()); err == nil {
			t.Fatalf("receiveImage(%q) succeeded, want error", data)
		}
	}
}

func TestReceiveImageUsesCollisionSafeName(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 7, 10, 12, 34, 56, 0, time.UTC)
	data := append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 24)...)
	first, err := receiveImage(bytes.NewReader(data), dir, now)
	if err != nil {
		t.Fatal(err)
	}
	second, err := receiveImage(bytes.NewReader(data), dir, now)
	if err != nil {
		t.Fatal(err)
	}
	if first == second || !strings.Contains(second, "-1.png") {
		t.Fatalf("paths %q and %q are not collision safe", first, second)
	}
}

func TestClipboardCommandsUseRequestedOrderAndArguments(t *testing.T) {
	binDir := t.TempDir()
	logPath := filepath.Join(t.TempDir(), "calls")
	writeScript := func(name, body string) {
		t.Helper()
		path := filepath.Join(binDir, name)
		if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeScript("wl-paste", "printf '%s\\n' \"wl-paste:$*\" >> \"$CALL_LOG\"\nexit 1\n")
	writeScript("xclip", "printf '%s\\n' \"xclip:$*\" >> \"$CALL_LOG\"\ncase \" $* \" in *' -o '*) printf '\\211PNG\\r\\n\\032\\n000000000000000000000000' ;; *) /bin/cat >> \"$CLIP_OUT\" ;; esac\n")
	t.Setenv("PATH", binDir)
	t.Setenv("CALL_LOG", logPath)
	clipOut := filepath.Join(t.TempDir(), "clipboard")
	t.Setenv("CLIP_OUT", clipOut)

	data, err := readClipboardImage(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(data, []byte("\x89PNG")) {
		t.Fatalf("unexpected clipboard data %q", data)
	}
	if err := writeClipboardText(context.Background(), "/srv/server-main/clipboard/clip.png"); err != nil {
		t.Fatal(err)
	}
	calls, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	want := "wl-paste:-t image/png\nxclip:-selection clipboard -t image/png -o\nxclip:-selection clipboard\n"
	if string(calls) != want {
		t.Fatalf("calls=%q, want %q", calls, want)
	}
	copied, err := os.ReadFile(clipOut)
	if err != nil {
		t.Fatal(err)
	}
	if string(copied) != "/srv/server-main/clipboard/clip.png" {
		t.Fatalf("copied=%q", copied)
	}
}

func TestSendClipboardImageUsesConfiguredRemote(t *testing.T) {
	binDir := t.TempDir()
	logPath := filepath.Join(t.TempDir(), "ssh-call")
	imagePath := filepath.Join(t.TempDir(), "sent-image")
	clipboardPath := filepath.Join(t.TempDir(), "clipboard")
	writeTestExecutableBody := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(binDir, name), []byte("#!/bin/sh\n"+body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeTestExecutableBody("wl-paste", "printf '\\211PNG\\r\\n\\032\\n000000000000000000000000'\n")
	writeTestExecutableBody("ssh", "printf '%s\\n' \"$*\" > \"$SSH_LOG\"\n/bin/cat > \"$IMAGE_OUT\"\nprintf '%s\\n' /srv/server-main/clipboard/clip-20260710-123456.png\n")
	writeTestExecutableBody("wl-copy", "/bin/cat > \"$CLIP_OUT\"\n")
	t.Setenv("PATH", binDir)
	t.Setenv("SSH_LOG", logPath)
	t.Setenv("IMAGE_OUT", imagePath)
	t.Setenv("CLIP_OUT", clipboardPath)
	home := t.TempDir()
	t.Setenv("HOME", home)
	configDir := filepath.Join(home, ".config", "bp")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "config"), []byte("REMOTE=ops@example.com\nREMOTE_METHOD=mosh\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := os.CreateTemp(t.TempDir(), "stdout")
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	errOut, err := os.CreateTemp(t.TempDir(), "stderr")
	if err != nil {
		t.Fatal(err)
	}
	defer errOut.Close()
	a := &app{ctx: context.Background(), tmux: bptmux.New(), out: out, err: errOut}
	if err := a.sendClipboardImage(); err != nil {
		t.Fatal(err)
	}

	assertFileContents := func(path, want string) {
		t.Helper()
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != want {
			t.Fatalf("%s=%q, want %q", path, got, want)
		}
	}
	assertFileContents(logPath, "ops@example.com bp img recv\n")
	assertFileContents(clipboardPath, "/srv/server-main/clipboard/clip-20260710-123456.png")
	image, err := os.ReadFile(imagePath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(image, []byte("\x89PNG")) {
		t.Fatalf("uploaded data=%q, want PNG", image)
	}
}
