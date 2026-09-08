// Package release verifies immutable, signed native releases.
package release

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/x509"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const BaseURL = "https://bp.tunapro.xyz"

//go:embed version.txt
var versionText string

//go:embed release.pub
var PublicKeyPEM []byte

func Version() string { return strings.TrimSpace(versionText) }

var validVersion = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`)

type Manifest struct {
	Version   string            `json:"version"`
	Revision  string            `json:"revision"`
	Published string            `json:"published"`
	SHA256    map[string]string `json:"sha256"`
}
type Checker struct {
	Base string
	HTTP *http.Client
	Key  ed25519.PublicKey
}

func Default() *Checker {
	block, _ := pem.Decode(PublicKeyPEM)
	value, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		panic(err)
	}
	return &Checker{Base: BaseURL, HTTP: &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(r *http.Request, via []*http.Request) error {
		if r.URL.Scheme != "https" {
			return fmt.Errorf("non-HTTPS redirect refused")
		}
		if len(via) > 5 {
			return fmt.Errorf("too many redirects")
		}
		return nil
	}}, Key: value.(ed25519.PublicKey)}
}
func (c *Checker) get(ctx context.Context, path string, limit int64) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(c.Base, "/")+"/"+path, nil)
	if err != nil {
		return nil, err
	}
	response, err := c.HTTP.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return nil, fmt.Errorf("release download: HTTP %d", response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("release response too large")
	}
	return data, nil
}
func (c *Checker) Latest(ctx context.Context) (Manifest, error) {
	data, err := c.get(ctx, "latest.version", 128)
	if err != nil {
		return Manifest{}, err
	}
	version := strings.TrimSpace(string(data))
	return c.Manifest(ctx, version)
}
func (c *Checker) Manifest(ctx context.Context, version string) (Manifest, error) {
	var m Manifest
	if !validVersion.MatchString(version) {
		return m, fmt.Errorf("invalid release version")
	}
	base := "releases/v" + version + "/"
	data, err := c.get(ctx, base+"manifest.json", 64<<10)
	if err != nil {
		return m, err
	}
	signature, err := c.get(ctx, base+"manifest.sig", 128)
	if err != nil {
		return m, err
	}
	if !ed25519.Verify(c.Key, data, signature) {
		return m, fmt.Errorf("release signature verification failed")
	}
	if err = json.Unmarshal(data, &m); err != nil {
		return m, err
	}
	if m.Version != version {
		return m, fmt.Errorf("release version mismatch")
	}
	for name, sum := range m.SHA256 {
		if filepath.Base(name) != name || len(sum) != 64 {
			return m, fmt.Errorf("invalid release checksum entry")
		}
		if _, err := hex.DecodeString(sum); err != nil {
			return m, err
		}
	}
	return m, nil
}
func Platform() string { return "bp-" + runtime.GOOS + "-" + runtime.GOARCH }
func Newer(candidate, current string) bool {
	if !validVersion.MatchString(candidate) || !validVersion.MatchString(current) {
		return false
	}
	a, b := strings.Split(candidate, "."), strings.Split(current, ".")
	for i := range a {
		x, _ := strconv.ParseUint(a[i], 10, 64)
		y, _ := strconv.ParseUint(b[i], 10, 64)
		if x != y {
			return x > y
		}
	}
	return false
}
func (c *Checker) Download(ctx context.Context, m Manifest, name string) ([]byte, error) {
	sum, ok := m.SHA256[name]
	if !ok {
		return nil, fmt.Errorf("release does not contain %s", name)
	}
	data, err := c.get(ctx, "releases/v"+m.Version+"/"+name, 64<<20)
	if err != nil {
		return nil, err
	}
	if fmt.Sprintf("%x", sha256.Sum256(data)) != sum {
		return nil, fmt.Errorf("SHA-256 mismatch for %s", name)
	}
	return data, nil
}

// Replace retains the previous executable. Failed setup restores it atomically.
func Replace(target string, data []byte, validate func(string) error, setup func() error) (string, error) {
	target, err := filepath.EvalSymlinks(target)
	if err != nil {
		return "", err
	}
	lock, err := os.OpenFile(filepath.Join(filepath.Dir(target), ".bp-update.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return "", err
	}
	defer lock.Close()
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return "", err
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)

	info, err := os.Stat(target)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("target is not a regular file")
	}
	candidate, err := os.CreateTemp(filepath.Dir(target), ".bp-update-")
	if err != nil {
		return "", err
	}
	name := candidate.Name()
	defer os.Remove(name)
	if _, err = candidate.Write(data); err == nil {
		err = candidate.Chmod(info.Mode().Perm())
	}
	if err == nil {
		err = candidate.Sync()
	}
	closeErr := candidate.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return "", err
	}
	if err = validate(name); err != nil {
		return "", err
	}
	old, err := os.ReadFile(target)
	if err != nil {
		return "", err
	}
	backup, err := os.CreateTemp(filepath.Dir(target), filepath.Base(target)+".before-update-")
	if err != nil {
		return "", err
	}
	backupName := backup.Name()
	if _, err = backup.Write(old); err == nil {
		err = backup.Chmod(info.Mode().Perm())
	}
	if err == nil {
		err = backup.Sync()
	}
	closeErr = backup.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return backupName, err
	}
	if err = os.Rename(name, target); err != nil {
		return backupName, err
	}
	if err = setup(); err != nil {
		rollback, restoreErr := os.CreateTemp(filepath.Dir(target), ".bp-restore-")
		if restoreErr == nil {
			_, restoreErr = rollback.Write(old)
			if restoreErr == nil {
				restoreErr = rollback.Chmod(info.Mode().Perm())
			}
			rollback.Close()
			if restoreErr == nil {
				restoreErr = os.Rename(rollback.Name(), target)
			}
		}
		if restoreErr != nil {
			return backupName, fmt.Errorf("setup failed: %v; restore failed: %v; backup: %s", err, restoreErr, backupName)
		}
		return backupName, fmt.Errorf("setup failed; previous binary restored: %w", err)
	}
	return backupName, nil
}
