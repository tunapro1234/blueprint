package hop

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// TreeFormat is the layout version of a recursive bundle: a whole agent subtree
// (a root and its descendants) packed together so it can be moved atomically.
// It is distinct from the single-agent Format so a reader dispatches on the
// number it finds rather than guessing.
const TreeFormat = 2

// agentsRoot is the payload prefix for a recursive bundle. Every file lives
// under agents/<name>/work/ or agents/<name>/transcript/, where <name> is one
// of the bundle's declared agents. This namespacing is what lets one tar carry
// many agents without their files colliding.
const agentsRoot = "agents/"

// TreeAgent is one agent inside a recursive bundle: its portable spec and the
// name of its parent WITHIN the bundle (empty for the subtree root). The files
// carried for it are the manifest Files whose path is under agents/<name>/.
type TreeAgent struct {
	Agent  AgentSpec `json:"agent"`
	Parent string    `json:"parent,omitempty"`
}

// TreeManifest is the table of contents of a recursive bundle, stored as the
// first tar entry exactly like the single-agent Manifest.
type TreeManifest struct {
	Format       int         `json:"format"`
	CreatedAt    string      `json:"createdAt"`
	Root         string      `json:"root"`
	Agents       []TreeAgent `json:"agents"`
	IncludeFiles bool        `json:"includeFiles"`
	TotalBytes   int64       `json:"totalBytes"`
	Files        []FileEntry `json:"files"`
	Source       Origin      `json:"source,omitempty"`
}

// TreeWriteAgent is the caller's description of one agent to pack. The caller
// (cmd/bp/hop) resolves each agent's folder and transcript files; this package
// only reads and packs them, namespacing every path under the agent's name.
type TreeWriteAgent struct {
	Spec         AgentSpec
	Parent       string
	Folder       string
	IncludeFiles bool
	Raw          bool
	Transcripts  []TranscriptFile
}

// TreeWriteOptions drives WriteTree. Agents must be ordered parent-first and
// form a single tree rooted at Root.
type TreeWriteOptions struct {
	Root   string
	Agents []TreeWriteAgent
	Source Origin
	Clock  func() time.Time
}

// WriteTree packs a recursive bundle into w and returns the manifest it wrote.
// Like Write, every file is stat'd up front so the manifest (the first tar
// entry) is complete before any payload streams.
func WriteTree(w io.Writer, opt TreeWriteOptions) (TreeManifest, error) {
	clock := opt.Clock
	if clock == nil {
		clock = time.Now
	}
	if !ValidName(opt.Root) {
		return TreeManifest{}, fmt.Errorf("invalid root agent name %q", opt.Root)
	}
	if len(opt.Agents) == 0 {
		return TreeManifest{}, fmt.Errorf("no agents to pack")
	}

	type packItem struct {
		abs  string
		name string
		size int64
		mode uint32
	}
	var items []packItem
	var files []FileEntry
	var treeAgents []TreeAgent
	var total int64
	includeAny := false

	add := func(abs, name string) error {
		info, err := os.Lstat(abs)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			// Symlinks and non-regular files are never carried (extraction-escape
			// vector), matching the single-agent packer.
			return nil
		}
		if err := safeTreePath(name); err != nil {
			return err
		}
		items = append(items, packItem{abs: abs, name: name, size: info.Size(), mode: uint32(info.Mode().Perm())})
		files = append(files, FileEntry{Path: name, Size: info.Size(), Mode: uint32(info.Mode().Perm())})
		total += info.Size()
		return nil
	}

	for _, a := range opt.Agents {
		if !ValidName(a.Spec.Name) {
			return TreeManifest{}, fmt.Errorf("invalid agent name %q", a.Spec.Name)
		}
		if a.Spec.Harness == "" {
			return TreeManifest{}, fmt.Errorf("agent %s is missing harness", a.Spec.Name)
		}
		base := agentsRoot + a.Spec.Name + "/"
		for _, t := range a.Transcripts {
			if t.AbsPath == "" || t.Name == "" {
				continue
			}
			if err := add(t.AbsPath, path(base+transcriptRoot, t.Name)); err != nil {
				return TreeManifest{}, fmt.Errorf("add transcript %s for %s: %w", t.Name, a.Spec.Name, err)
			}
		}
		if a.IncludeFiles && a.Folder != "" {
			includeAny = true
			rels, err := enumerate(a.Folder, a.Raw)
			if err != nil {
				return TreeManifest{}, fmt.Errorf("enumerate %s: %w", a.Spec.Name, err)
			}
			for _, rel := range rels {
				if err := add(filepath.Join(a.Folder, rel), path(base+workRoot, rel)); err != nil {
					return TreeManifest{}, fmt.Errorf("add file %s for %s: %w", rel, a.Spec.Name, err)
				}
			}
		}
		treeAgents = append(treeAgents, TreeAgent{Agent: a.Spec, Parent: a.Parent})
	}

	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	sort.Slice(items, func(i, j int) bool { return items[i].name < items[j].name })

	manifest := TreeManifest{
		Format:       TreeFormat,
		CreatedAt:    clock().UTC().Format(time.RFC3339),
		Root:         opt.Root,
		Agents:       treeAgents,
		IncludeFiles: includeAny,
		TotalBytes:   total,
		Files:        files,
		Source:       opt.Source,
	}
	if err := manifest.Validate(); err != nil {
		return TreeManifest{}, err
	}

	gz := gzip.NewWriter(w)
	tw := tar.NewWriter(gz)
	manifestBytes, err := json.Marshal(manifest)
	if err != nil {
		return TreeManifest{}, err
	}
	if err := tw.WriteHeader(&tar.Header{Name: manifestName, Mode: 0o600, Size: int64(len(manifestBytes)), Typeflag: tar.TypeReg, ModTime: clock().UTC()}); err != nil {
		return TreeManifest{}, err
	}
	if _, err := tw.Write(manifestBytes); err != nil {
		return TreeManifest{}, err
	}
	for _, it := range items {
		if err := writeFile(tw, it.abs, it.name, it.size, it.mode, clock); err != nil {
			return TreeManifest{}, err
		}
	}
	if err := tw.Close(); err != nil {
		return TreeManifest{}, err
	}
	if err := gz.Close(); err != nil {
		return TreeManifest{}, err
	}
	return manifest, nil
}

// ReadTree extracts a recursive bundle from r into destDir, returning the
// manifest. Like Read it is the trust gate: the manifest is validated first
// (format, the agent set and its hierarchy, every file path), then each entry
// is checked against the payload namespace and the extraction root, symlinks
// and non-regular entries are refused, and the cumulative size is capped.
func ReadTree(r io.Reader, destDir string, maxBytes int64) (TreeManifest, error) {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return TreeManifest{}, fmt.Errorf("open bundle: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)

	hdr, err := tr.Next()
	if err != nil {
		return TreeManifest{}, fmt.Errorf("read bundle header: %w", err)
	}
	if hdr.Name != manifestName {
		return TreeManifest{}, fmt.Errorf("bundle does not begin with %s", manifestName)
	}
	manifestBytes, err := io.ReadAll(io.LimitReader(tr, maxFrameManifest))
	if err != nil {
		return TreeManifest{}, err
	}
	var manifest TreeManifest
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		return TreeManifest{}, fmt.Errorf("decode manifest: %w", err)
	}
	if err := manifest.Validate(); err != nil {
		return TreeManifest{}, err
	}
	declaredAgent := map[string]bool{}
	for _, a := range manifest.Agents {
		declaredAgent[a.Agent.Name] = true
	}
	cap := maxBytes
	if cap <= 0 {
		cap = manifest.TotalBytes
	}
	root, err := filepath.Abs(destDir)
	if err != nil {
		return TreeManifest{}, err
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
			return TreeManifest{}, err
		}
		if hdr.Typeflag != tar.TypeReg {
			return TreeManifest{}, fmt.Errorf("bundle entry %q is not a regular file", hdr.Name)
		}
		if err := safeTreePath(hdr.Name); err != nil {
			return TreeManifest{}, err
		}
		if !declaredAgent[treePathAgent(hdr.Name)] {
			return TreeManifest{}, fmt.Errorf("bundle entry %q is for an undeclared agent", hdr.Name)
		}
		entry, ok := declared[hdr.Name]
		if !ok {
			return TreeManifest{}, fmt.Errorf("bundle entry %q not in manifest", hdr.Name)
		}
		target := filepath.Join(root, filepath.FromSlash(hdr.Name))
		if !withinRoot(root, target) {
			return TreeManifest{}, fmt.Errorf("bundle entry %q escapes extraction root", hdr.Name)
		}
		if written+entry.Size > cap {
			return TreeManifest{}, fmt.Errorf("bundle exceeds size cap of %d bytes", cap)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return TreeManifest{}, err
		}
		n, err := writeOut(target, tr, entry.Size, os.FileMode(entry.Mode))
		written += n
		if err != nil {
			return TreeManifest{}, err
		}
		seen++
	}
	if seen != len(manifest.Files) {
		return TreeManifest{}, fmt.Errorf("bundle carried %d of %d declared files", seen, len(manifest.Files))
	}
	return manifest, nil
}

// Validate checks a tree manifest before any file is written: the format, a
// well-formed single hierarchy rooted at Root, unique valid names, and files
// that sum to TotalBytes and sit under a declared agent's namespace.
func (m TreeManifest) Validate() error {
	if m.Format != TreeFormat {
		return fmt.Errorf("unsupported tree bundle format %d (need %d)", m.Format, TreeFormat)
	}
	if !ValidName(m.Root) {
		return fmt.Errorf("invalid root agent name %q", m.Root)
	}
	if len(m.Agents) == 0 {
		return fmt.Errorf("tree manifest has no agents")
	}
	parents := make(map[string]string, len(m.Agents))
	seenRoot := false
	for _, a := range m.Agents {
		if !ValidName(a.Agent.Name) {
			return fmt.Errorf("invalid agent name %q", a.Agent.Name)
		}
		if a.Agent.Harness == "" {
			return fmt.Errorf("agent %s missing harness", a.Agent.Name)
		}
		if _, dup := parents[a.Agent.Name]; dup {
			return fmt.Errorf("duplicate agent %q in bundle", a.Agent.Name)
		}
		parents[a.Agent.Name] = a.Parent
		if a.Agent.Name == a.Parent {
			return fmt.Errorf("agent %q is its own parent", a.Agent.Name)
		}
		if a.Agent.Name == m.Root {
			if a.Parent != "" {
				return fmt.Errorf("root %q must have no parent", m.Root)
			}
			seenRoot = true
		}
	}
	if !seenRoot {
		return fmt.Errorf("root %q is not among the bundle agents", m.Root)
	}
	// Every non-root parent must reference a bundle agent, and walking parents
	// from any agent must reach the root without a cycle.
	for name, parent := range parents {
		if name == m.Root {
			continue
		}
		if parent == "" {
			return fmt.Errorf("agent %q has no parent but is not the root", name)
		}
		steps := 0
		for at := name; at != m.Root; {
			next, ok := parents[at]
			if !ok {
				return fmt.Errorf("agent %q has parent %q outside the bundle", at, parents[at])
			}
			if next == "" && at != m.Root {
				return fmt.Errorf("agent %q does not descend from root %q", name, m.Root)
			}
			at = next
			if steps++; steps > len(parents) {
				return fmt.Errorf("parent cycle involving %q", name)
			}
		}
	}
	var sum int64
	for _, f := range m.Files {
		if err := safeTreePath(f.Path); err != nil {
			return err
		}
		if _, ok := parents[treePathAgent(f.Path)]; !ok {
			return fmt.Errorf("file %q is for an undeclared agent", f.Path)
		}
		if f.Size < 0 {
			return fmt.Errorf("file %q has negative size", f.Path)
		}
		sum += f.Size
	}
	if m.TotalBytes < 0 {
		return fmt.Errorf("negative total bytes")
	}
	if sum != m.TotalBytes {
		return fmt.Errorf("manifest total %d does not match file sum %d", m.TotalBytes, sum)
	}
	return nil
}

// AgentFiles returns the manifest file entries that belong to one agent, with
// their agents/<name>/ prefix removed so the caller can place them relative to
// that agent's target directory. kind is "work" or "transcript".
func (m TreeManifest) AgentFiles(name, kind string) []FileEntry {
	prefix := agentsRoot + name + "/" + kind + "/"
	var out []FileEntry
	for _, f := range m.Files {
		if strings.HasPrefix(f.Path, prefix) {
			out = append(out, FileEntry{Path: strings.TrimPrefix(f.Path, prefix), Size: f.Size, Mode: f.Mode})
		}
	}
	return out
}

// PeekFormat reads only a bundle's manifest header and returns its Format, so
// an accepter can dispatch between a single-agent (Format) and a recursive
// (TreeFormat) bundle without extracting anything. It reads at most the capped
// manifest bytes and never writes to disk.
func PeekFormat(r io.Reader) (int, error) {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return 0, fmt.Errorf("open bundle: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	hdr, err := tr.Next()
	if err != nil {
		return 0, fmt.Errorf("read bundle header: %w", err)
	}
	if hdr.Name != manifestName {
		return 0, fmt.Errorf("bundle does not begin with %s", manifestName)
	}
	manifestBytes, err := io.ReadAll(io.LimitReader(tr, maxFrameManifest))
	if err != nil {
		return 0, err
	}
	var head struct {
		Format int `json:"format"`
	}
	if err := json.Unmarshal(manifestBytes, &head); err != nil {
		return 0, fmt.Errorf("decode manifest: %w", err)
	}
	return head.Format, nil
}

// safeTreePath enforces the recursive-bundle namespace: agents/<validname>/work/...
// or agents/<validname>/transcript/..., on top of the generic no-escape checks.
func safeTreePath(p string) error {
	if p == "" || strings.HasPrefix(p, "/") || strings.Contains(p, `\`) {
		return fmt.Errorf("unsafe bundle path %q", p)
	}
	if strings.Contains(p, "..") {
		return fmt.Errorf("bundle path %q contains ..", p)
	}
	for _, r := range p {
		if r == 0 {
			return fmt.Errorf("bundle path contains NUL")
		}
	}
	if !strings.HasPrefix(p, agentsRoot) {
		return fmt.Errorf("bundle path %q outside %s", p, agentsRoot)
	}
	rest := strings.TrimPrefix(p, agentsRoot)
	slash := strings.IndexByte(rest, '/')
	if slash <= 0 {
		return fmt.Errorf("bundle path %q missing agent segment", p)
	}
	name := rest[:slash]
	if !ValidName(name) {
		return fmt.Errorf("bundle path %q has invalid agent segment", p)
	}
	after := rest[slash+1:]
	if !strings.HasPrefix(after, workRoot) && !strings.HasPrefix(after, transcriptRoot) {
		return fmt.Errorf("bundle path %q outside known roots", p)
	}
	return nil
}

// treePathAgent returns the agent name segment of a validated tree path. It
// assumes safeTreePath has already accepted p.
func treePathAgent(p string) string {
	rest := strings.TrimPrefix(p, agentsRoot)
	if i := strings.IndexByte(rest, '/'); i > 0 {
		return rest[:i]
	}
	return ""
}
