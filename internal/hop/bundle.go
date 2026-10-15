package hop

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// TranscriptFile names one transcript artifact to carry: its absolute source
// path and the name it takes under the bundle's transcript/ root.
type TranscriptFile struct {
	AbsPath string
	Name    string // relative, e.g. "<sessionId>.jsonl" or "<sessionId>/subagents/a.jsonl"
}

// WriteOptions drives Write. The caller (cmd/bp/hop) resolves the agent, its
// folder and its transcript files; this package only reads and packs them.
type WriteOptions struct {
	Agent        AgentSpec
	Folder       string // source working directory; packed when IncludeFiles
	IncludeFiles bool
	Raw          bool // include .git and gitignored files too
	Transcripts  []TranscriptFile
	Source       Origin
	Clock        func() time.Time
}

// Write packs a bundle into w and returns the manifest it wrote. File sizes are
// stat'd up front so the manifest (the first tar entry) is complete before any
// payload streams — the receiver authorizes against it before accepting bytes.
func Write(w io.Writer, opt WriteOptions) (Manifest, error) {
	clock := opt.Clock
	if clock == nil {
		clock = time.Now
	}
	if !ValidName(opt.Agent.Name) {
		return Manifest{}, fmt.Errorf("invalid agent name %q", opt.Agent.Name)
	}
	if opt.Agent.Harness == "" {
		return Manifest{}, fmt.Errorf("agent harness is required")
	}

	type packItem struct {
		abs  string
		name string // path inside the tar (begins with a payload root)
		size int64
		mode uint32
	}
	var items []packItem
	var files []FileEntry
	var total int64

	add := func(abs, name string) error {
		info, err := os.Lstat(abs)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			// Symlinks are not carried: a link's target is outside the bundle's
			// control and is a classic extraction-escape vector.
			return nil
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		if err := safePayloadPath(name); err != nil {
			return err
		}
		items = append(items, packItem{abs: abs, name: name, size: info.Size(), mode: uint32(info.Mode().Perm())})
		files = append(files, FileEntry{Path: name, Size: info.Size(), Mode: uint32(info.Mode().Perm())})
		total += info.Size()
		return nil
	}

	for _, t := range opt.Transcripts {
		if t.AbsPath == "" || t.Name == "" {
			continue
		}
		if err := add(t.AbsPath, path(transcriptRoot, t.Name)); err != nil {
			return Manifest{}, fmt.Errorf("add transcript %s: %w", t.Name, err)
		}
	}

	if opt.IncludeFiles && opt.Folder != "" {
		rels, err := enumerate(opt.Folder, opt.Raw)
		if err != nil {
			return Manifest{}, err
		}
		for _, rel := range rels {
			if err := add(filepath.Join(opt.Folder, rel), path(workRoot, rel)); err != nil {
				return Manifest{}, fmt.Errorf("add file %s: %w", rel, err)
			}
		}
	}

	// Stable order makes the bundle deterministic for a given input.
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	sort.Slice(items, func(i, j int) bool { return items[i].name < items[j].name })

	manifest := Manifest{
		Format:       Format,
		CreatedAt:    clock().UTC().Format(time.RFC3339),
		Agent:        opt.Agent,
		IncludeFiles: opt.IncludeFiles,
		TotalBytes:   total,
		Files:        files,
		Source:       opt.Source,
	}

	gz := gzip.NewWriter(w)
	tw := tar.NewWriter(gz)

	manifestBytes, err := json.Marshal(manifest)
	if err != nil {
		return Manifest{}, err
	}
	if err := tw.WriteHeader(&tar.Header{Name: manifestName, Mode: 0o600, Size: int64(len(manifestBytes)), Typeflag: tar.TypeReg, ModTime: clock().UTC()}); err != nil {
		return Manifest{}, err
	}
	if _, err := tw.Write(manifestBytes); err != nil {
		return Manifest{}, err
	}

	for _, it := range items {
		if err := writeFile(tw, it.abs, it.name, it.size, it.mode, clock); err != nil {
			return Manifest{}, err
		}
	}
	if err := tw.Close(); err != nil {
		return Manifest{}, err
	}
	if err := gz.Close(); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

func writeFile(tw *tar.Writer, abs, name string, size int64, mode uint32, clock func() time.Time) error {
	f, err := os.Open(abs)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: int64(mode), Size: size, Typeflag: tar.TypeReg, ModTime: clock().UTC()}); err != nil {
		return err
	}
	// Copy at most `size` bytes: a file growing mid-pack must not desync the tar.
	n, err := io.CopyN(tw, f, size)
	if err == io.EOF && n == size {
		return nil
	}
	if err != nil && !(err == io.EOF && n < size) {
		return err
	}
	if n < size {
		// File shrank mid-pack; pad so the header's Size still holds.
		if _, err := tw.Write(make([]byte, size-n)); err != nil {
			return err
		}
	}
	return nil
}

// Read extracts a bundle from r into destDir, returning the manifest. It is the
// trust gate for an incoming bundle: the manifest is validated first, each
// entry's name is checked against the payload roots and the extraction root,
// symlinks and non-regular entries are refused, and the cumulative size is
// capped at maxBytes (0 means use the manifest total). destDir must exist.
func Read(r io.Reader, destDir string, maxBytes int64) (Manifest, error) {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return Manifest{}, fmt.Errorf("open bundle: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)

	hdr, err := tr.Next()
	if err != nil {
		return Manifest{}, fmt.Errorf("read bundle header: %w", err)
	}
	if hdr.Name != manifestName {
		return Manifest{}, fmt.Errorf("bundle does not begin with %s", manifestName)
	}
	manifestBytes, err := io.ReadAll(io.LimitReader(tr, maxFrameManifest))
	if err != nil {
		return Manifest{}, err
	}
	var manifest Manifest
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		return Manifest{}, fmt.Errorf("decode manifest: %w", err)
	}
	if err := manifest.Validate(); err != nil {
		return Manifest{}, err
	}
	cap := maxBytes
	if cap <= 0 {
		cap = manifest.TotalBytes
	}

	root, err := filepath.Abs(destDir)
	if err != nil {
		return Manifest{}, err
	}
	declared := make(map[string]FileEntry, len(manifest.Files))
	for _, f := range manifest.Files {
		declared[f.Path] = f
	}

	var written int64
	seen := 0
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return Manifest{}, err
		}
		if hdr.Typeflag != tar.TypeReg {
			// Directories are implied by file paths; links and devices are refused.
			return Manifest{}, fmt.Errorf("bundle entry %q is not a regular file", hdr.Name)
		}
		if err := safePayloadPath(hdr.Name); err != nil {
			return Manifest{}, err
		}
		entry, ok := declared[hdr.Name]
		if !ok {
			return Manifest{}, fmt.Errorf("bundle entry %q not in manifest", hdr.Name)
		}
		target := filepath.Join(root, filepath.FromSlash(hdr.Name))
		if !withinRoot(root, target) {
			return Manifest{}, fmt.Errorf("bundle entry %q escapes extraction root", hdr.Name)
		}
		if written+entry.Size > cap {
			return Manifest{}, fmt.Errorf("bundle exceeds size cap of %d bytes", cap)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return Manifest{}, err
		}
		n, err := writeOut(target, tr, entry.Size, os.FileMode(entry.Mode))
		written += n
		if err != nil {
			return Manifest{}, err
		}
		seen++
	}
	if seen != len(manifest.Files) {
		return Manifest{}, fmt.Errorf("bundle carried %d of %d declared files", seen, len(manifest.Files))
	}
	return manifest, nil
}

func writeOut(target string, r io.Reader, size int64, mode os.FileMode) (int64, error) {
	if mode == 0 {
		mode = 0o600
	}
	f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode.Perm())
	if err != nil {
		return 0, err
	}
	n, err := io.CopyN(f, r, size)
	if cerr := f.Close(); err == nil && cerr != nil {
		err = cerr
	}
	if err == io.EOF && n == size {
		return n, nil
	}
	if err != nil {
		return n, err
	}
	if n != size {
		return n, fmt.Errorf("short write for %s: %d of %d", filepath.Base(target), n, size)
	}
	return n, nil
}

// maxFrameManifest bounds the in-bundle manifest.json so a hostile bundle
// cannot force an unbounded allocation before validation. 32 MiB is far above
// any real file list (hundreds of thousands of entries).
const maxFrameManifest = 32 << 20

func withinRoot(root, target string) bool {
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// enumerate lists the working-directory files to pack, relative to folder. In
// git repositories (and not raw) it defers to git for gitignore semantics,
// listing tracked and untracked-but-not-ignored files and excluding .git. Raw,
// or outside a repository, it walks the tree; raw keeps everything including
// .git, non-raw still skips .git.
func enumerate(folder string, raw bool) ([]string, error) {
	info, err := os.Stat(folder)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("folder %q is not a directory", folder)
	}
	if !raw {
		if rels, ok := gitFiles(folder); ok {
			return rels, nil
		}
	}
	var rels []string
	err = filepath.Walk(folder, func(p string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, rerr := filepath.Rel(folder, p)
		if rerr != nil {
			return rerr
		}
		if rel == "." {
			return nil
		}
		top := strings.SplitN(filepath.ToSlash(rel), "/", 2)[0]
		if top == ".git" && !raw {
			if fi.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if fi.IsDir() || !fi.Mode().IsRegular() {
			return nil
		}
		rels = append(rels, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		return nil, err
	}
	return rels, nil
}

// gitFiles returns tracked + untracked-not-ignored files via git, or ok=false
// when folder is not a git work tree or git is unavailable.
func gitFiles(folder string) ([]string, bool) {
	if _, err := exec.LookPath("git"); err != nil {
		return nil, false
	}
	cmd := exec.Command("git", "-C", folder, "rev-parse", "--is-inside-work-tree")
	if out, err := cmd.Output(); err != nil || strings.TrimSpace(string(out)) != "true" {
		return nil, false
	}
	cmd = exec.Command("git", "-C", folder, "ls-files", "-z", "--cached", "--others", "--exclude-standard")
	out, err := cmd.Output()
	if err != nil {
		return nil, false
	}
	var rels []string
	for _, b := range bytes.Split(out, []byte{0}) {
		rel := string(b)
		if rel == "" {
			continue
		}
		rels = append(rels, filepath.ToSlash(rel))
	}
	return rels, true
}

// path joins a payload root with a slash-separated relative name.
func path(root, name string) string {
	return root + strings.TrimPrefix(filepath.ToSlash(name), "/")
}
