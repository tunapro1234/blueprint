package ledger

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Artifact is a content-addressed reference to a local file: where it is,
// its sha256 digest and size when recorded, and an optional revision the
// citer names (a git commit, a build id). The ledger never copies file
// contents; it stores the reference and re-hashes the file to check that
// what was verified is still what is there.
//
// A digest proves identity of bytes, not truth of a claim: a finding is
// verified only by an explicit verification with evidence.
type Artifact struct {
	// Path is the path as cited (absolute, cleaned); Target is what it
	// resolved to. A later check resolves Path again: a retargeted symlink
	// is drift even if the old target's bytes did not change.
	Path     string `json:"path"`
	Target   string `json:"target"`
	Digest   string `json:"digest"`
	Size     int64  `json:"size"`
	Revision string `json:"revision,omitempty"`
}

// ArtifactRef is what a caller cites: a path and an optional revision and
// expected digest. When Digest is given it must match the file.
type ArtifactRef struct {
	Path     string `json:"path"`
	Revision string `json:"revision,omitempty"`
	Digest   string `json:"digest,omitempty"`
}

const (
	maxArtifactBytes = 4 << 30
	maxArtifacts     = 32
)

// containedPath resolves path and checks it is a regular file inside one of
// roots. Symlinks are followed before the check, so a link inside a root
// that points outside it is refused.
//
// Residual risk, by design: the check and the later hash are separate
// steps, so a process running as the same user can swap the file in
// between. The ledger records what it hashed; it does not defend against
// the owner's own processes racing it.
func containedPath(roots []string, path string) (string, os.FileInfo, error) {
	if !filepath.IsAbs(path) {
		return "", nil, invalid("artifact path %q must be absolute", path)
	}
	if len(roots) == 0 {
		return "", nil, fmt.Errorf("%w: the project has no roots, so no file may be cited; the lead adds one with bp work project root", ErrForbidden)
	}
	real, err := filepath.EvalSymlinks(filepath.Clean(path))
	if err != nil {
		return "", nil, invalid("artifact %q: %v", path, err)
	}
	inside := false
	for _, root := range roots {
		if rel, err := filepath.Rel(root, real); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel) {
			inside = true
			break
		}
	}
	if !inside {
		return "", nil, fmt.Errorf("%w: %s is outside the project roots", ErrForbidden, path)
	}
	info, err := os.Stat(real)
	if err != nil {
		return "", nil, invalid("artifact %q: %v", path, err)
	}
	if !info.Mode().IsRegular() {
		return "", nil, invalid("artifact %q is not a regular file", path)
	}
	if info.Size() > maxArtifactBytes {
		return "", nil, invalid("artifact %q is over %d bytes", path, int64(maxArtifactBytes))
	}
	return real, info, nil
}

func hashFile(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(f, maxArtifactBytes+1))
	if err != nil {
		return "", 0, err
	}
	if n > maxArtifactBytes {
		return "", 0, invalid("artifact %q is over %d bytes", path, int64(maxArtifactBytes))
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), n, nil
}

// resolveArtifacts hashes every cited file.
func resolveArtifacts(roots []string, refs []ArtifactRef) ([]Artifact, error) {
	if len(refs) > maxArtifacts {
		return nil, invalid("at most %d artifacts", maxArtifacts)
	}
	out := make([]Artifact, 0, len(refs))
	for _, ref := range refs {
		if err := textField("revision", ref.Revision, 200, false); err != nil {
			return nil, err
		}
		real, _, err := containedPath(roots, ref.Path)
		if err != nil {
			return nil, err
		}
		digest, size, err := hashFile(real)
		if err != nil {
			return nil, err
		}
		if ref.Digest != "" && ref.Digest != digest && "sha256:"+ref.Digest != digest {
			return nil, fmt.Errorf("%w: %s has digest %s, not the claimed %s", ErrStaleEvidence, ref.Path, digest, ref.Digest)
		}
		out = append(out, Artifact{Path: filepath.Clean(ref.Path), Target: real, Digest: digest, Size: size, Revision: ref.Revision})
	}
	return out, nil
}

// Drift is one artifact whose current bytes differ from the recorded ones.
type Drift struct {
	Path    string `json:"path"`
	Want    string `json:"want"`
	Got     string `json:"got,omitempty"`
	Problem string `json:"problem,omitempty"`
}

// checkArtifacts re-hashes recorded artifacts. A file that is gone,
// unreadable or no longer inside the roots counts as drift.
func checkArtifacts(roots []string, recorded []Artifact) []Drift {
	var drift []Drift
	for _, a := range recorded {
		real, _, err := containedPath(roots, a.Path)
		if err != nil {
			drift = append(drift, Drift{Path: a.Path, Want: a.Digest, Problem: err.Error()})
			continue
		}
		if target := a.Target; target != "" && real != target {
			drift = append(drift, Drift{Path: a.Path, Want: a.Digest, Problem: "now resolves to " + real + ", was " + target})
			continue
		}
		digest, _, err := hashFile(real)
		if err != nil {
			drift = append(drift, Drift{Path: a.Path, Want: a.Digest, Problem: err.Error()})
			continue
		}
		if digest != a.Digest {
			drift = append(drift, Drift{Path: a.Path, Want: a.Digest, Got: digest})
		}
	}
	return drift
}

func driftText(drift []Drift) string {
	parts := make([]string, 0, len(drift))
	for _, d := range drift {
		if d.Problem != "" {
			parts = append(parts, d.Path+": "+d.Problem)
		} else {
			parts = append(parts, fmt.Sprintf("%s: %s -> %s", d.Path, short(d.Want), short(d.Got)))
		}
	}
	return strings.Join(parts, "; ")
}

func short(digest string) string {
	d := strings.TrimPrefix(digest, "sha256:")
	if len(d) > 12 {
		d = d[:12]
	}
	return d
}
