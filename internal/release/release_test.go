package release

import (
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
	"strings"
	"testing"
)

func TestSignedReleaseRejectsTampering(t *testing.T) {
	pub, key, _ := ed25519.GenerateKey(rand.Reader)
	payload := []byte("executable")
	m := Manifest{Version: "1.6.0", SHA256: map[string]string{Platform(): fmt.Sprintf("%x", sha256.Sum256(payload))}}
	data, _ := json.Marshal(m)
	signature := ed25519.Sign(key, data)
	corruptManifest, corruptBinary, corruptSignature := false, false, false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/latest.version":
			fmt.Fprint(w, "1.6.0\n")
		case "/releases/v1.6.0/manifest.json":
			if corruptManifest {
				fmt.Fprint(w, `{"version":"1.6.0","sha256":{}}`)
			} else {
				w.Write(data)
			}
		case "/releases/v1.6.0/manifest.sig":
			if corruptSignature {
				w.Write(make([]byte, 64))
			} else {
				w.Write(signature)
			}
		default:
			if corruptBinary {
				w.Write([]byte("tampered"))
			} else {
				w.Write(payload)
			}
		}
	}))
	defer server.Close()
	checker := &Checker{Base: server.URL, HTTP: server.Client(), Key: pub}
	verified, err := checker.Latest(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	got, err := checker.Download(context.Background(), verified, Platform())
	if err != nil || string(got) != string(payload) {
		t.Fatal(err, string(got))
	}
	corruptManifest = true
	if _, err = checker.Latest(context.Background()); err == nil {
		t.Fatal("unverified manifest accepted")
	}
	corruptManifest = false
	corruptSignature = true
	if _, err = checker.Latest(context.Background()); err == nil {
		t.Fatal("bad signature accepted")
	}
	corruptSignature = false
	corruptBinary = true
	if _, err = checker.Download(context.Background(), verified, Platform()); err == nil {
		t.Fatal("bad executable accepted")
	}
	if _, err = checker.Manifest(context.Background(), "../../escape"); err == nil {
		t.Fatal("path traversal accepted")
	}
	if Newer("1.5.9", "1.6.0") || Newer("1.6.0", "1.6.0") || !Newer("1.10.0", "1.6.0") {
		t.Fatal("version comparison")
	}
}
func TestUpdatePreservesExecutableOnValidationAndSetupFailure(t *testing.T) {
	for _, stage := range []string{"validate", "setup", "success"} {
		t.Run(stage, func(t *testing.T) {
			target := filepath.Join(t.TempDir(), "bp")
			os.WriteFile(target, []byte("old"), 0755)
			validated, setup := false, false
			backup, err := Replace(target, []byte("new"), func(path string) error {
				validated = true
				data, _ := os.ReadFile(target)
				if string(data) != "old" {
					t.Fatal("replaced before validation")
				}
				if stage == "validate" {
					return fmt.Errorf("invalid candidate")
				}
				return nil
			}, func() error {
				setup = true
				if stage == "setup" {
					return fmt.Errorf("setup failed")
				}
				return nil
			})
			got, _ := os.ReadFile(target)
			if stage == "success" {
				if err != nil || string(got) != "new" {
					t.Fatal(err, string(got))
				}
			} else {
				if err == nil || string(got) != "old" {
					t.Fatal(err, string(got))
				}
			}
			if !validated || setup != (stage != "validate") {
				t.Fatal("wrong order")
			}
			if backup != "" {
				old, _ := os.ReadFile(backup)
				if string(old) != "old" {
					t.Fatal("backup lost")
				}
			}
			if stage == "setup" && !strings.Contains(err.Error(), "restored") {
				t.Fatal(err)
			}
		})
	}
}
