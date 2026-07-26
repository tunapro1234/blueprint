package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type clipboardCommand struct {
	name string
	args []string
}

var clipboardReaders = []clipboardCommand{
	{name: "wl-paste", args: []string{"-t", "image/png"}},
	{name: "xclip", args: []string{"-selection", "clipboard", "-t", "image/png", "-o"}},
	{name: "pngpaste", args: []string{"-"}},
}

var clipboardWriters = []clipboardCommand{
	{name: "wl-copy"},
	{name: "xclip", args: []string{"-selection", "clipboard"}},
	{name: "pbcopy"},
}

func (a *app) image(args []string) error {
	switch {
	case len(args) == 0:
		return a.sendClipboardImage()
	case len(args) == 1 && args[0] == "recv":
		if a.config.ClipboardDir == "" {
			return fmt.Errorf("clipboard drop dir not configured")
		}
		path, err := receiveImage(os.Stdin, a.config.ClipboardDir, time.Now())
		if err != nil {
			return err
		}
		fmt.Fprintln(a.out, path)
		return nil
	default:
		return fmt.Errorf("usage: bp img [recv]")
	}
}

func (a *app) sendClipboardImage() error {
	configPath, err := connectConfigPath()
	if err != nil {
		return err
	}
	config, err := loadConnectConfig(configPath)
	if err != nil {
		return err
	}

	image, err := readClipboardImage(a.ctx)
	if err != nil {
		return err
	}
	sshPath, err := exec.LookPath("ssh")
	if err != nil {
		return fmt.Errorf("ssh is not installed")
	}
	cmd := exec.CommandContext(a.ctx, sshPath, config.Remote, "bp img recv")
	cmd.Stdin = bytes.NewReader(image)
	cmd.Stderr = a.err
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("send image to %s: %w", config.Remote, err)
	}
	serverPath := strings.TrimSpace(stdout.String())
	if serverPath == "" {
		return fmt.Errorf("remote receiver returned an empty path")
	}
	if strings.ContainsAny(serverPath, "\r\n") {
		return fmt.Errorf("remote receiver returned more than one line")
	}

	// Print the path even when the local clipboard command later fails, so the
	// upload remains useful from a terminal.
	fmt.Fprintln(a.out, serverPath)
	if err := writeClipboardText(a.ctx, serverPath); err != nil {
		return err
	}
	return nil
}

func readClipboardImage(ctx context.Context) ([]byte, error) {
	var attempts []error
	found := false
	for _, candidate := range clipboardReaders {
		path, err := exec.LookPath(candidate.name)
		if err != nil {
			continue
		}
		found = true
		cmd := exec.CommandContext(ctx, path, candidate.args...)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		data, err := cmd.Output()
		if err == nil && len(data) > 0 {
			return data, nil
		}
		if err == nil {
			err = errors.New("clipboard contained no image bytes")
		}
		if detail := strings.TrimSpace(stderr.String()); detail != "" {
			err = fmt.Errorf("%w: %s", err, detail)
		}
		attempts = append(attempts, fmt.Errorf("%s: %w", candidate.name, err))
	}
	if !found {
		return nil, fmt.Errorf("no image clipboard reader found; install wl-clipboard, xclip, or pngpaste")
	}
	return nil, fmt.Errorf("could not read a clipboard image: %w", errors.Join(attempts...))
}

func writeClipboardText(ctx context.Context, value string) error {
	var attempts []error
	found := false
	for _, candidate := range clipboardWriters {
		path, err := exec.LookPath(candidate.name)
		if err != nil {
			continue
		}
		found = true
		cmd := exec.CommandContext(ctx, path, candidate.args...)
		cmd.Stdin = strings.NewReader(value)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		if err := cmd.Run(); err == nil {
			return nil
		} else {
			if detail := strings.TrimSpace(stderr.String()); detail != "" {
				err = fmt.Errorf("%w: %s", err, detail)
			}
			attempts = append(attempts, fmt.Errorf("%s: %w", candidate.name, err))
		}
	}
	if !found {
		return fmt.Errorf("server path uploaded but no text clipboard writer found; install wl-clipboard, xclip, or use pbcopy")
	}
	return fmt.Errorf("server path uploaded but could not copy it to the clipboard: %w", errors.Join(attempts...))
}

func receiveImage(reader io.Reader, dir string, now time.Time) (path string, err error) {
	if err := os.MkdirAll(dir, 0o777); err != nil {
		return "", fmt.Errorf("create image drop directory: %w", err)
	}
	temp, err := os.CreateTemp(dir, ".incoming.*")
	if err != nil {
		return "", fmt.Errorf("create temporary image: %w", err)
	}
	tempPath := temp.Name()
	defer func() {
		temp.Close()
		os.Remove(tempPath)
	}()

	size, err := io.Copy(temp, reader)
	if err != nil {
		return "", fmt.Errorf("read image: %w", err)
	}
	if size == 0 {
		return "", fmt.Errorf("empty input (clipboard has no image)")
	}
	if _, err := temp.Seek(0, io.SeekStart); err != nil {
		return "", fmt.Errorf("inspect image: %w", err)
	}
	header := make([]byte, 512)
	n, err := temp.Read(header)
	if err != nil && !errors.Is(err, io.EOF) {
		return "", fmt.Errorf("inspect image: %w", err)
	}
	mimeType, extension, ok := detectImageType(header[:n])
	if !ok {
		return "", fmt.Errorf("input is not a supported image (mime=%s)", mimeType)
	}

	if _, err := temp.Seek(0, io.SeekStart); err != nil {
		return "", fmt.Errorf("store image: %w", err)
	}
	stamp := now.Format("20060102-150405")
	for suffix := 0; ; suffix++ {
		name := "clip-" + stamp
		if suffix > 0 {
			name += fmt.Sprintf("-%d", suffix)
		}
		path = filepath.Join(dir, name+"."+extension)
		output, openErr := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if errors.Is(openErr, os.ErrExist) {
			continue
		}
		if openErr != nil {
			return "", fmt.Errorf("create received image: %w", openErr)
		}
		removeOutput := true
		defer func() {
			output.Close()
			if removeOutput {
				os.Remove(path)
			}
		}()
		if _, err := io.Copy(output, temp); err != nil {
			return "", fmt.Errorf("store image: %w", err)
		}
		if err := output.Chmod(0o644); err != nil {
			return "", fmt.Errorf("set image permissions: %w", err)
		}
		if err := output.Close(); err != nil {
			return "", fmt.Errorf("close received image: %w", err)
		}
		removeOutput = false
		return path, nil
	}
}

func detectImageType(header []byte) (mimeType, extension string, ok bool) {
	switch {
	case bytes.HasPrefix(header, []byte("\x89PNG\r\n\x1a\n")):
		return "image/png", "png", true
	case len(header) >= 3 && header[0] == 0xff && header[1] == 0xd8 && header[2] == 0xff:
		return "image/jpeg", "jpg", true
	case bytes.HasPrefix(header, []byte("GIF87a")), bytes.HasPrefix(header, []byte("GIF89a")):
		return "image/gif", "gif", true
	case len(header) >= 12 && bytes.Equal(header[:4], []byte("RIFF")) && bytes.Equal(header[8:12], []byte("WEBP")):
		return "image/webp", "webp", true
	case bytes.HasPrefix(header, []byte("BM")):
		return "image/bmp", "bmp", true
	default:
		return "application/octet-stream", "", false
	}
}
