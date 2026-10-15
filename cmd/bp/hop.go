package main

import (
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"blueprint/internal/book"
	"blueprint/internal/hop"
	"blueprint/internal/p2p"
	bptmux "blueprint/internal/tmux"
)

const hopUsage = "usage: bp hop <agent> --to <peer> [--recursive] [--no-files] [--copy] [--delete-source] [--raw] [--dry-run] | bp hop list [--json] | bp hop accept <id> [--dir <path>] [--parent <name>] [--resume] [--yes] | bp hop reject <id>"

func (a *app) hopCommand(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("%s", hopUsage)
	}
	switch args[0] {
	case "list":
		return a.hopList(args[1:])
	case "accept":
		return a.hopAccept(args[1:])
	case "reject":
		return a.hopReject(args[1:])
	default:
		return a.hopPush(args)
	}
}

// hopPush moves (or copies) an agent to a peer. The default is a move: the
// bundle is delivered for the peer owner's review, then the source agent is
// stopped and archived with its files kept (never deleted unless asked).
func (a *app) hopPush(args []string) error {
	name := args[0]
	if err := rejectFlag("hop", name); err != nil {
		return err
	}
	peer, includeFiles, move, deleteSource, raw, dryRun, recursive := "", true, true, false, false, false, false
	for i := 1; i < len(args); i++ {
		switch args[i] {
		case "--to":
			if i+1 >= len(args) {
				return fmt.Errorf("--to requires a peer")
			}
			i++
			peer = args[i]
		case "--no-files":
			includeFiles = false
		case "--copy":
			move = false
		case "--delete-source":
			deleteSource = true
		case "--raw":
			raw = true
		case "--dry-run":
			dryRun = true
		case "--recursive":
			recursive = true
		default:
			return fmt.Errorf("unknown hop option: %s", args[i])
		}
	}
	if peer == "" {
		return fmt.Errorf("%s", hopUsage)
	}
	if deleteSource && !move {
		return fmt.Errorf("--delete-source applies to a move, not --copy")
	}
	if a.config.P2P == nil {
		return fmt.Errorf("p2p is not configured")
	}
	if _, ok := a.config.P2P.Peers[peer]; !ok {
		return fmt.Errorf("unknown p2p peer: %s", peer)
	}

	fleet, err := book.LoadFleet(book.Paths(a.config.Agentbooks))
	if err != nil {
		return err
	}
	if _, ok := fleet.Agents[name]; !ok {
		return fmt.Errorf("unknown agent: %s", name)
	}
	if recursive {
		return a.hopPushTree(fleet, name, peer, includeFiles, raw, move, deleteSource, dryRun)
	}
	agent := fleet.Agents[name]
	if agent.Folder == "" {
		return fmt.Errorf("agent %s has no recorded folder", name)
	}
	// Never move an agent in the middle of a turn: its transcript and files would
	// be captured mid-write.
	if a.hasSession(name) && a.paneBusy(name) {
		return fmt.Errorf("%s is busy; wait for it to go idle before hopping it", name)
	}

	plan, err := a.resolveHopAgent(name, agent, includeFiles)
	if err != nil {
		return err
	}
	spec, transcripts, harness := plan.spec, plan.transcripts, plan.harness
	if len(transcripts) == 0 && !includeFiles {
		return fmt.Errorf("nothing to hop: no transcript resolved and --no-files given")
	}

	tmp, err := os.CreateTemp("", "bp-hop-*.tar.gz")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	source := hop.Origin{Host: hopHostname(), Agent: name}
	manifest, werr := hop.Write(tmp, hop.WriteOptions{Agent: spec, Folder: agent.Folder, IncludeFiles: includeFiles, Raw: raw, Transcripts: transcripts, Source: source})
	cerr := tmp.Close()
	if werr != nil {
		return fmt.Errorf("build bundle: %w", werr)
	}
	if cerr != nil {
		return cerr
	}

	if dryRun {
		fmt.Fprintf(a.out, "dry run: would hop %s to %s (%s)\n", name, peer, hopMode(move, deleteSource, includeFiles))
		fmt.Fprintf(a.out, "  %d file(s), %d bytes\n", len(manifest.Files), manifest.TotalBytes)
		for _, f := range manifest.Files {
			fmt.Fprintf(a.out, "    %s (%d)\n", f.Path, f.Size)
		}
		return nil
	}

	channel, err := p2p.NewHopID()
	if err != nil {
		return err
	}
	header := p2p.HopHeader{
		Channel: channel, Agent: name, Harness: harness,
		Thread: spec.ThreadID, Session: spec.SessionID, FolderBase: spec.FolderBase,
		IncludeFiles: includeFiles, TotalBytes: manifest.TotalBytes, FileCount: len(manifest.Files),
		Format: hop.Format, SourceHost: source.Host, SourceAgent: name,
	}
	f, err := os.Open(tmpPath)
	if err != nil {
		return err
	}
	res, serr := p2p.SendHopControl(a.ctx, a.config.StateDir, peer, header, f)
	_ = f.Close()
	if serr != nil {
		return fmt.Errorf("hop transfer failed (is the p2p service running? bp p2p start): %w", serr)
	}
	if !res.Accepted {
		return fmt.Errorf("peer %s refused the hop: %s", peer, res.Error)
	}
	fmt.Fprintf(a.out, "%s: bundle delivered to %s for review (id %s)\n", name, peer, res.ID)
	fmt.Fprintf(a.out, "  on %s the owner runs: bp hop accept %s\n", peer, res.ID)

	if !move {
		fmt.Fprintf(a.out, "  copy: %s left running here\n", name)
		return nil
	}
	// Move: stop and archive the source, but keep its files unless asked to
	// delete. A delivery that succeeded must not leave a second live copy.
	if err := a.close([]string{name}); err != nil {
		fmt.Fprintf(a.err, "WARNING: delivered but could not stop %s here: %v\n", name, err)
		return nil
	}
	if err := a.archive([]string{name}, true); err != nil {
		fmt.Fprintf(a.err, "WARNING: delivered and stopped, but could not archive %s: %v\n", name, err)
	}
	if deleteSource {
		if err := removeSourceFolder(agent.Folder); err != nil {
			fmt.Fprintf(a.err, "WARNING: could not delete source files: %v\n", err)
		} else {
			fmt.Fprintf(a.out, "  source files deleted: %s\n", agent.Folder)
		}
	} else {
		fmt.Fprintf(a.out, "  source stopped and archived; files kept at %s\n", agent.Folder)
	}
	return nil
}

// hopAgentPlan is one agent resolved for packing: its portable spec, the
// transcript files to carry, its source folder and harness.
type hopAgentPlan struct {
	spec        hop.AgentSpec
	transcripts []hop.TranscriptFile
	folder      string
	harness     string
}

// resolveHopAgent turns an agentbook record into a packable plan: the portable
// spec plus, for Claude, the resolved conversation transcript and its subagent
// files. A non-Claude harness carries its thread id but not a transcript. It
// does not enforce "something to carry"; the caller decides, since a recursive
// hop keeps an empty agent to preserve the hierarchy.
func (a *app) resolveHopAgent(name string, agent book.Agent, includeFiles bool) (hopAgentPlan, error) {
	if agent.Folder == "" {
		return hopAgentPlan{}, fmt.Errorf("agent %s has no recorded folder", name)
	}
	harness := hopHarness(agent.Launch)
	spec := hop.AgentSpec{
		Name: name, Harness: harness,
		ThreadID: agent.IdentityThreadID,
		Folder:   agent.Folder, FolderBase: filepath.Base(agent.Folder),
		ClaudeAccount: agent.ClaudeAccount,
		Nickname:      agent.Nickname, Role: agent.Role, Class: agent.Class, Color: agent.Color,
	}
	var transcripts []hop.TranscriptFile
	if harness == "claude" {
		resumeID := ""
		if agent.Launch != nil {
			resumeID = agent.Launch.ResumeID
		}
		path, err := bptmux.ResolveSessionPath(bptmux.ClaudeProjectsRoot(), agent.Folder, name, resumeID)
		if err != nil {
			fmt.Fprintf(a.err, "WARNING: could not resolve %s transcript: %v; hopping without the conversation\n", name, err)
		} else {
			spec.SessionID = strings.TrimSuffix(filepath.Base(path), ".jsonl")
			transcripts = append(transcripts, hop.TranscriptFile{AbsPath: path, Name: spec.SessionID + ".jsonl"})
			transcripts = appendSubagentTranscripts(transcripts, filepath.Join(filepath.Dir(path), spec.SessionID, "subagents"), spec.SessionID)
		}
	} else {
		fmt.Fprintf(a.err, "NOTE: conversation-file carry is implemented for Claude; %s thread id is carried but its transcript is not\n", harness)
	}
	return hopAgentPlan{spec: spec, transcripts: transcripts, folder: agent.Folder, harness: harness}, nil
}

// subtreeOrder returns the root and all its descendants in parent-first order,
// so a receiver can register each agent after its parent already exists. Only
// agents present in the loaded fleet are included (archived agents are not).
func subtreeOrder(fleet book.Fleet, root string) []string {
	children := map[string][]string{}
	for child, parent := range fleet.Parents {
		if child == parent || child == "" {
			continue
		}
		children[parent] = append(children[parent], child)
	}
	for parent := range children {
		sort.Strings(children[parent])
	}
	var order []string
	seen := map[string]bool{}
	queue := []string{root}
	for len(queue) > 0 {
		name := queue[0]
		queue = queue[1:]
		if seen[name] {
			continue
		}
		if _, ok := fleet.Agents[name]; !ok {
			continue
		}
		seen[name] = true
		order = append(order, name)
		queue = append(queue, children[name]...)
	}
	return order
}

// hopPushTree moves (or copies) a whole agent subtree to a peer as one atomic
// recursive bundle. The hierarchy is carried in the manifest; the receiver
// rebuilds it on accept. A move archives every agent here (children first) once
// the bundle is delivered.
func (a *app) hopPushTree(fleet book.Fleet, root, peer string, includeFiles, raw, move, deleteSource, dryRun bool) error {
	order := subtreeOrder(fleet, root)
	if len(order) == 0 {
		return fmt.Errorf("unknown agent: %s", root)
	}
	// Never move any agent mid-turn.
	for _, name := range order {
		if a.hasSession(name) && a.paneBusy(name) {
			return fmt.Errorf("%s is busy; wait for the whole subtree to be idle before hopping it", name)
		}
	}
	var writeAgents []hop.TreeWriteAgent
	for _, name := range order {
		plan, err := a.resolveHopAgent(name, fleet.Agents[name], includeFiles)
		if err != nil {
			return err
		}
		parent := fleet.Parents[name]
		if name == root {
			parent = "" // the subtree root has no parent inside the bundle
		}
		writeAgents = append(writeAgents, hop.TreeWriteAgent{
			Spec: plan.spec, Parent: parent, Folder: plan.folder,
			IncludeFiles: includeFiles, Raw: raw, Transcripts: plan.transcripts,
		})
	}

	tmp, err := os.CreateTemp("", "bp-hop-tree-*.tar.gz")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	source := hop.Origin{Host: hopHostname(), Agent: root}
	manifest, werr := hop.WriteTree(tmp, hop.TreeWriteOptions{Root: root, Agents: writeAgents, Source: source})
	cerr := tmp.Close()
	if werr != nil {
		return fmt.Errorf("build subtree bundle: %w", werr)
	}
	if cerr != nil {
		return cerr
	}

	if dryRun {
		fmt.Fprintf(a.out, "dry run: would hop subtree %s (%d agents) to %s (%s)\n", root, len(order), peer, hopMode(move, deleteSource, includeFiles))
		fmt.Fprintf(a.out, "  %d file(s), %d bytes\n", len(manifest.Files), manifest.TotalBytes)
		for _, name := range order {
			parent := fleet.Parents[name]
			if name == root {
				parent = "(root)"
			}
			fmt.Fprintf(a.out, "    %s  (parent: %s)\n", name, parent)
		}
		return nil
	}

	channel, err := p2p.NewHopID()
	if err != nil {
		return err
	}
	rootHarness := hopHarness(fleet.Agents[root].Launch)
	header := p2p.HopHeader{
		Channel: channel, Agent: root, Harness: rootHarness,
		FolderBase: filepath.Base(fleet.Agents[root].Folder),
		IncludeFiles: manifest.IncludeFiles, TotalBytes: manifest.TotalBytes, FileCount: len(manifest.Files),
		Format: hop.TreeFormat, SourceHost: source.Host, SourceAgent: root, Agents: len(order),
	}
	f, err := os.Open(tmpPath)
	if err != nil {
		return err
	}
	res, serr := p2p.SendHopControl(a.ctx, a.config.StateDir, peer, header, f)
	_ = f.Close()
	if serr != nil {
		return fmt.Errorf("hop transfer failed (is the p2p service running? bp p2p start): %w", serr)
	}
	if !res.Accepted {
		return fmt.Errorf("peer %s refused the hop: %s", peer, res.Error)
	}
	fmt.Fprintf(a.out, "%s: subtree of %d agents delivered to %s for review (id %s)\n", root, len(order), peer, res.ID)
	fmt.Fprintf(a.out, "  on %s the owner runs: bp hop accept %s\n", peer, res.ID)

	if !move {
		fmt.Fprintf(a.out, "  copy: subtree left in place here\n")
		return nil
	}
	// Move: archive each agent (children first) so a parent is never archived
	// while a live child still points at it. Files are kept unless asked.
	for i := len(order) - 1; i >= 0; i-- {
		name := order[i]
		if err := a.close([]string{name}); err != nil {
			fmt.Fprintf(a.err, "WARNING: delivered but could not stop %s here: %v\n", name, err)
		}
		if err := a.archive([]string{name}, true); err != nil {
			fmt.Fprintf(a.err, "WARNING: delivered but could not archive %s: %v\n", name, err)
		}
		if deleteSource {
			if err := removeSourceFolder(fleet.Agents[name].Folder); err != nil {
				fmt.Fprintf(a.err, "WARNING: could not delete source files for %s: %v\n", name, err)
			}
		}
	}
	if deleteSource {
		fmt.Fprintf(a.out, "  source subtree stopped, archived and files deleted\n")
	} else {
		fmt.Fprintf(a.out, "  source subtree stopped and archived; files kept\n")
	}
	return nil
}

func (a *app) hopList(args []string) error {
	jsonOut := false
	if len(args) == 1 && args[0] == "--json" {
		jsonOut = true
	} else if len(args) > 0 {
		return fmt.Errorf("usage: bp hop list [--json]")
	}
	pending, err := p2p.ListHopPending(a.config.StateDir)
	if err != nil {
		return err
	}
	if jsonOut {
		return json.NewEncoder(a.out).Encode(pending)
	}
	if len(pending) == 0 {
		fmt.Fprintln(a.out, "no pending hops")
		return nil
	}
	for _, m := range pending {
		scope := m.Header.Agent + " (" + m.Header.Harness + ")"
		if m.Header.Agents > 0 {
			scope = fmt.Sprintf("subtree %s +%d agents", m.Header.Agent, m.Header.Agents-1)
		}
		fmt.Fprintf(a.out, "%s  %s from %s  %d file(s) %d bytes  %s\n",
			m.ID, scope, m.PeerAlias, m.Header.FileCount, m.Header.TotalBytes, m.ReceivedAt)
	}
	return nil
}

func (a *app) hopReject(args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("usage: bp hop reject <id>")
	}
	id := args[0]
	if _, err := p2p.ReadHopMeta(a.config.StateDir, id); err != nil {
		return fmt.Errorf("no staged hop %s: %w", id, err)
	}
	if err := p2p.RemoveHopStaging(a.config.StateDir, id); err != nil {
		return err
	}
	fmt.Fprintf(a.out, "hop %s rejected and removed\n", id)
	return nil
}

// hopAccept materializes a staged bundle into a target directory, places the
// conversation transcript, and resumes the agent. Nothing here trusts the
// bundle: hop.Read re-validates every entry and caps the extracted size.
func (a *app) hopAccept(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: bp hop accept <id> [--dir <path>] [--parent <name>] [--resume] [--yes]")
	}
	id := args[0]
	targetDir, parent, yes, resume := "", "", false, false
	for i := 1; i < len(args); i++ {
		switch args[i] {
		case "--dir":
			if i+1 >= len(args) {
				return fmt.Errorf("--dir requires a directory")
			}
			i++
			targetDir = args[i]
		case "--parent":
			if i+1 >= len(args) {
				return fmt.Errorf("--parent requires an agent name")
			}
			i++
			parent = args[i]
		case "--resume":
			resume = true
		case "--yes":
			yes = true
		default:
			return fmt.Errorf("unknown accept option: %s", args[i])
		}
	}
	meta, err := p2p.ReadHopMeta(a.config.StateDir, id)
	if err != nil {
		return fmt.Errorf("no staged hop %s: %w", id, err)
	}
	// Dispatch on the bundle layout: a recursive bundle rebuilds a whole subtree
	// and registers agents without launching unless --resume is given.
	bundlePeek, err := os.Open(p2p.HopBundlePath(a.config.StateDir, meta))
	if err != nil {
		return err
	}
	format, perr := hop.PeekFormat(bundlePeek)
	_ = bundlePeek.Close()
	if perr != nil {
		return fmt.Errorf("inspect bundle: %w", perr)
	}
	if format == hop.TreeFormat {
		return a.hopAcceptTree(id, meta, targetDir, parent, resume, yes)
	}
	name := meta.Header.Agent
	if a.hasSession(name) {
		return fmt.Errorf("an agent named %s is already running here; rename or close it first", name)
	}
	if targetDir == "" {
		base := meta.Header.FolderBase
		if base == "" {
			base = name
		}
		cwd, err := os.Getwd()
		if err != nil {
			return err
		}
		targetDir = filepath.Join(cwd, base)
	}
	absDir, err := filepath.Abs(targetDir)
	if err != nil {
		return err
	}
	if entries, err := os.ReadDir(absDir); err == nil && len(entries) > 0 && !yes {
		return fmt.Errorf("target %s is not empty; pass --dir <empty path> or --yes to merge into it", absDir)
	}
	if err := os.MkdirAll(absDir, 0o755); err != nil {
		return err
	}

	bundle, err := os.Open(p2p.HopBundlePath(a.config.StateDir, meta))
	if err != nil {
		return err
	}
	defer bundle.Close()
	extractDir, err := os.MkdirTemp("", "bp-hop-accept-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(extractDir)
	manifest, err := hop.Read(bundle, extractDir, 0)
	if err != nil {
		return fmt.Errorf("extract bundle: %w", err)
	}

	if err := mergeTree(filepath.Join(extractDir, "work"), absDir); err != nil {
		return fmt.Errorf("place files: %w", err)
	}
	if manifest.Agent.Harness == "claude" {
		projDir := bptmux.ClaudeProjectDir(absDir)
		if err := os.MkdirAll(projDir, 0o755); err != nil {
			return err
		}
		if err := mergeTree(filepath.Join(extractDir, "transcript"), projDir); err != nil {
			return fmt.Errorf("place transcript: %w", err)
		}
	}
	fmt.Fprintf(a.out, "hop %s: materialized %s into %s\n", id, name, absDir)

	openArgs := []string{name, absDir, "--resume"}
	if manifest.Agent.Harness == "claude" && manifest.Agent.SessionID != "" {
		openArgs = append(openArgs, "--thread", manifest.Agent.SessionID)
	}
	openArgs = append(openArgs, harnessFlag(manifest.Agent.Harness)...)
	if err := a.open(openArgs); err != nil {
		fmt.Fprintf(a.err, "materialized but could not resume automatically: %v\n", err)
		fmt.Fprintf(a.err, "  files are at %s; resume manually with: bp open %s %s --resume\n", absDir, name, absDir)
		return nil
	}
	if err := p2p.RemoveHopStaging(a.config.StateDir, id); err != nil {
		fmt.Fprintf(a.err, "resumed %s; could not clean staging %s: %v\n", name, id, err)
	}
	return nil
}

// hopAcceptTree materializes a recursive bundle: it rebuilds every agent's
// files and transcript under a base directory and registers each in the
// agentbook (parent-first) as a closed, revivable agent. Nothing is launched
// unless --resume is given, so accepting a whole hierarchy never starts dozens
// of sessions at once. As in a single accept, hop.ReadTree re-validates the
// bundle and caps its size before anything is written.
func (a *app) hopAcceptTree(id string, meta p2p.HopMeta, targetBase, rootParent string, resume, yes bool) error {
	base := targetBase
	if base == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return err
		}
		base = cwd
	}
	absBase, err := filepath.Abs(base)
	if err != nil {
		return err
	}
	bundle, err := os.Open(p2p.HopBundlePath(a.config.StateDir, meta))
	if err != nil {
		return err
	}
	defer bundle.Close()
	extractDir, err := os.MkdirTemp("", "bp-hop-tree-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(extractDir)
	manifest, err := hop.ReadTree(bundle, extractDir, 0)
	if err != nil {
		return fmt.Errorf("extract subtree bundle: %w", err)
	}
	for _, ta := range manifest.Agents {
		if a.hasSession(ta.Agent.Name) {
			return fmt.Errorf("an agent named %s is already running here; rename or close it first", ta.Agent.Name)
		}
	}

	paths := book.Paths(a.config.Agentbooks)
	usedDirs := map[string]bool{}
	dirFor := map[string]string{}
	// manifest.Agents is parent-first, so a parent is always registered before
	// its children.
	for _, ta := range manifest.Agents {
		name := ta.Agent.Name
		folderBase := ta.Agent.FolderBase
		if folderBase == "" {
			folderBase = name
		}
		absDir := filepath.Join(absBase, folderBase)
		if usedDirs[absDir] {
			// Two agents sharing a folder base would collide; fall back to the
			// agent name so each lands in its own directory.
			absDir = filepath.Join(absBase, name)
		}
		usedDirs[absDir] = true
		dirFor[name] = absDir
		if entries, err := os.ReadDir(absDir); err == nil && len(entries) > 0 && !yes {
			return fmt.Errorf("target %s (for %s) is not empty; pass --yes to merge or --dir an empty base", absDir, name)
		}
		if err := os.MkdirAll(absDir, 0o755); err != nil {
			return err
		}
		if err := mergeTree(filepath.Join(extractDir, "agents", name, "work"), absDir); err != nil {
			return fmt.Errorf("place files for %s: %w", name, err)
		}
		if ta.Agent.Harness == "claude" {
			projDir := bptmux.ClaudeProjectDir(absDir)
			if err := os.MkdirAll(projDir, 0o755); err != nil {
				return err
			}
			if err := mergeTree(filepath.Join(extractDir, "agents", name, "transcript"), projDir); err != nil {
				return fmt.Errorf("place transcript for %s: %w", name, err)
			}
		}
		parent := ta.Parent
		if name == manifest.Root {
			parent = rootParent // the subtree root attaches under the chosen local parent
		}
		reg := book.Registration{Parent: parent, Role: ta.Agent.Role, Launch: hopLaunch(ta.Agent.Harness, ta.Agent.SessionID)}
		if err := book.SetStatus(paths, name, "closed", absDir, reg); err != nil {
			return fmt.Errorf("register %s: %w", name, err)
		}
		if ta.Agent.Color != "" {
			_ = book.SetColor(paths, name, ta.Agent.Color)
		}
	}
	fmt.Fprintf(a.out, "hop %s: materialized subtree %s (%d agents) under %s\n", id, manifest.Root, len(manifest.Agents), absBase)
	if rootParent != "" {
		fmt.Fprintf(a.out, "  root %s attached under %s\n", manifest.Root, rootParent)
	}

	if !resume {
		fmt.Fprintln(a.out, "  agents registered as closed; revive one with: bp open <name> <dir> --resume")
		return p2p.RemoveHopStaging(a.config.StateDir, id)
	}
	// Opt-in resume: launch parent-first.
	for _, ta := range manifest.Agents {
		name := ta.Agent.Name
		openArgs := []string{name, dirFor[name], "--resume"}
		if ta.Agent.Harness == "claude" && ta.Agent.SessionID != "" {
			openArgs = append(openArgs, "--thread", ta.Agent.SessionID)
		}
		openArgs = append(openArgs, harnessFlag(ta.Agent.Harness)...)
		if err := a.open(openArgs); err != nil {
			fmt.Fprintf(a.err, "materialized but could not resume %s: %v (files at %s)\n", name, err, dirFor[name])
		}
	}
	return p2p.RemoveHopStaging(a.config.StateDir, id)
}

// hopLaunch reconstructs a minimal launch record so a hopped agent, registered
// closed, revives in the right harness. Claude resumes by session; the other
// harnesses carry only their mode (matching single-agent accept, which does not
// restore a non-Claude transcript).
func hopLaunch(harness, sessionID string) *bptmux.OpenOptions {
	switch harness {
	case "codex":
		return &bptmux.OpenOptions{Codex: true}
	case "hermes":
		return &bptmux.OpenOptions{Hermes: true}
	case "opencode":
		return &bptmux.OpenOptions{OpenCode: true}
	default:
		o := &bptmux.OpenOptions{Resume: true}
		if sessionID != "" {
			o.ResumeID = sessionID
		}
		return o
	}
}

func hopHarness(o *bptmux.OpenOptions) string {
	switch {
	case o == nil:
		return "claude"
	case o.Codex:
		return "codex"
	case o.Hermes:
		return "hermes"
	case o.OpenCode:
		return "opencode"
	default:
		return "claude"
	}
}

func harnessFlag(h string) []string {
	switch h {
	case "codex":
		return []string{"--codex"}
	case "hermes":
		return []string{"--hermes"}
	case "opencode":
		return []string{"--opencode"}
	default:
		return nil
	}
}

func hopHostname() string {
	h, _ := os.Hostname()
	return h
}

func hopMode(move, del, files bool) string {
	parts := []string{}
	if move {
		parts = append(parts, "move")
	} else {
		parts = append(parts, "copy")
	}
	if del {
		parts = append(parts, "delete-source")
	}
	if files {
		parts = append(parts, "with-files")
	} else {
		parts = append(parts, "no-files")
	}
	return strings.Join(parts, ",")
}

func appendSubagentTranscripts(list []hop.TranscriptFile, subDir, sessionID string) []hop.TranscriptFile {
	entries, err := os.ReadDir(subDir)
	if err != nil {
		return list
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		list = append(list, hop.TranscriptFile{
			AbsPath: filepath.Join(subDir, e.Name()),
			Name:    filepath.ToSlash(filepath.Join(sessionID, "subagents", e.Name())),
		})
	}
	return list
}

// removeSourceFolder deletes a hopped agent's working directory, only when the
// operator passed --delete-source. It refuses obviously unsafe targets.
func removeSourceFolder(folder string) error {
	clean := filepath.Clean(folder)
	if clean == "" || clean == "/" || clean == "." || len(clean) < 2 {
		return fmt.Errorf("refusing to delete %q", folder)
	}
	if home, _ := os.UserHomeDir(); home != "" && clean == filepath.Clean(home) {
		return fmt.Errorf("refusing to delete the home directory")
	}
	return os.RemoveAll(clean)
}

// mergeTree copies the regular files under src into dst, preserving structure.
// A missing src is a no-op (a files-only or conversation-only bundle).
func mergeTree(src, dst string) error {
	info, err := os.Stat(src)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("%s is not a directory", src)
	}
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if !d.Type().IsRegular() {
			return nil
		}
		return copyFile(p, target)
	})
}

func copyFile(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	mode := os.FileMode(0o644)
	if info, err := in.Stat(); err == nil {
		mode = info.Mode().Perm()
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}
