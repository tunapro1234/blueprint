package main

import (
	"blueprint/internal/release"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

type stubUpdateChecker struct {
	manifest    release.Manifest
	downloadErr error
}

func nextUpdateTestVersion(t *testing.T) string {
	t.Helper()
	parts := strings.Split(release.Version(), ".")
	if len(parts) != 3 {
		t.Fatalf("unexpected release version %q", release.Version())
	}
	patch, err := strconv.Atoi(parts[2])
	if err != nil {
		t.Fatalf("unexpected release version %q: %v", release.Version(), err)
	}
	return fmt.Sprintf("%s.%s.%d", parts[0], parts[1], patch+1)
}

func (s stubUpdateChecker) Latest(context.Context) (release.Manifest, error) {
	return s.manifest, nil
}

func (s stubUpdateChecker) Download(context.Context, release.Manifest, string) ([]byte, error) {
	return nil, s.downloadErr
}

func TestNpmManagedPathRequiresPackageAndNativeLayout(t *testing.T) {
	for _, tc := range []struct {
		name, pkg, file string
		want            bool
	}{
		{"npm native", `{"name":"@tunapro/blueprint"}`, release.Platform(), true},
		{"other package", `{"name":"other"}`, release.Platform(), false},
		{"invalid metadata", `{`, release.Platform(), false},
		{"standalone binary", `{"name":"@tunapro/blueprint"}`, "bp", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.Mkdir(filepath.Join(root, "bin"), 0700); err != nil {
				t.Fatal(err)
			}
			target := filepath.Join(root, "bin", tc.file)
			if err := os.WriteFile(target, []byte("fixture"), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, "package.json"), []byte(tc.pkg), 0600); err != nil {
				t.Fatal(err)
			}
			link := filepath.Join(root, "linked-bp")
			if err := os.Symlink(target, link); err != nil {
				t.Fatal(err)
			}
			if got := npmManagedPath(link); got != tc.want {
				t.Fatalf("npm managed=%v, want %v", got, tc.want)
			}
		})
	}
}

func TestUpdateKeepsSetupContextAliveAfterSlowDownload(t *testing.T) {
	version := nextUpdateTestVersion(t)
	payload := bytes.Repeat([]byte{0x5a}, 35_000_000)
	manifest := release.Manifest{
		Version: version,
		SHA256:  map[string]string{release.Platform(): fmt.Sprintf("%x", sha256.Sum256(payload))},
	}
	manifestData, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signature := ed25519.Sign(privateKey, manifestData)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/latest.version":
			_, _ = fmt.Fprintln(w, manifest.Version)
		case "/releases/v" + version + "/manifest.json":
			_, _ = w.Write(manifestData)
		case "/releases/v" + version + "/manifest.sig":
			_, _ = w.Write(signature)
		case "/releases/v" + version + "/" + release.Platform():
			w.Header().Set("Content-Length", fmt.Sprint(len(payload)))
			flusher := w.(http.Flusher)
			const chunkSize = 250_000
			for offset := 0; offset < len(payload); offset += chunkSize {
				end := min(offset+chunkSize, len(payload))
				if _, err := w.Write(payload[offset:end]); err != nil {
					return
				}
				flusher.Flush()
				time.Sleep(90 * time.Millisecond)
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	checker := &release.Checker{Base: server.URL, HTTP: server.Client(), Key: publicKey, StallTimeout: 20 * time.Second}
	var commands []string
	a := &app{
		ctx:            context.Background(),
		out:            testOutput(t),
		err:            testOutput(t),
		releaseChecker: checker,
		releaseReplace: func(_ string, _ []byte, validate func(string) error, setup func() error) (string, error) {
			if err := validate("candidate"); err != nil {
				return "", err
			}
			if err := setup(); err != nil {
				return "", err
			}
			return "backup", nil
		},
		releaseCommand: func(ctx context.Context, _ string, args ...string) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			commands = append(commands, strings.Join(args, " "))
			return nil
		},
	}

	started := time.Now()
	if err := a.update(nil); err != nil {
		t.Fatalf("update failed after %s while the server kept streaming: %v", time.Since(started), err)
	}
	if elapsed := time.Since(started); elapsed < 10*time.Second {
		t.Fatalf("slow transfer took only %s; expected the regression to cover a body lasting over 10s", elapsed)
	}
	if !reflect.DeepEqual(commands, []string{"setup --check", "setup"}) {
		t.Fatalf("setup commands=%v", commands)
	}
}

func TestUpdateDownloadFailureNamesStepProgressAndInstallerFallback(t *testing.T) {
	a := &app{
		ctx: context.Background(),
		out: testOutput(t),
		err: testOutput(t),
		releaseChecker: stubUpdateChecker{
			manifest:    release.Manifest{Version: nextUpdateTestVersion(t)},
			downloadErr: fmt.Errorf("GET release after 22 MB of 35 MB: stalled after 20s"),
		},
	}
	got := ""
	if err := a.update(nil); err != nil {
		got = err.Error()
	}
	for _, want := range []string{
		"download " + release.Platform(),
		"22 MB of 35 MB",
		"stalled after 20s",
		"https://github.com/tunapro1234/blueprint/releases/latest/download/install.sh",
		"--local",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("update error %q does not include %q", got, want)
		}
	}
}
