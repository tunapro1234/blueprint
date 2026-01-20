package commands

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	bp "blueprint"
)

func MapCommand(ctx CommandContext) CommandResult {
	if langs, _ := ctx.Args["langs"].(bool); langs {
		return mapLangs(ctx)
	}
	path := ctx.Path
	if path == "" {
		path = "."
	}
	noRecursive, _ := ctx.Args["no_recursive"].(bool)
	var targets []string
	if !noRecursive {
		files, err := findBlueprintsRecursive(path)
		if err != nil {
			return CommandResult{ExitCode: 1, Output: err.Error(), Errors: []string{err.Error()}}
		}
		targets = files
	} else {
		info, err := os.Stat(path)
		if err != nil {
			msg := err.Error()
			if os.IsNotExist(err) {
				msg = "file not found"
			}
			out := fmt.Sprintf("✗ %s: %s", formatPath(path), msg)
			return CommandResult{ExitCode: 1, Output: out, Errors: []string{msg}}
		}
		if info.IsDir() {
			bpPath, err := bp.FindBlueprintFile(path)
			if err != nil {
				if errors.Is(err, bp.ErrNotBlueprint) {
					target := filepath.Join(path, "BLUEPRINT.yaml")
					out := fmt.Sprintf("✗ %s: file not found", formatPath(target))
					return CommandResult{ExitCode: 1, Output: out, Errors: []string{"file not found"}}
				}
				return CommandResult{ExitCode: 1, Output: err.Error(), Errors: []string{err.Error()}}
			}
			targets = []string{bpPath}
		} else {
			targets = []string{path}
		}
	}
	if len(targets) == 0 {
		out := fmt.Sprintf("✗ %s: file not found", formatPath(filepath.Join(path, "BLUEPRINT.yaml")))
		return CommandResult{ExitCode: 1, Output: out, Errors: []string{"file not found"}}
	}

	lines := []string{}
	exitCode := 0
	for idx, target := range targets {
		bpObj, err := bp.LoadBlueprint(target)
		if err != nil {
			msg := normalizeYAMLError(err)
			line := fmt.Sprintf("✗ %s: %s", formatPath(target), msg)
			lines = append(lines, line)
			exitCode = 1
			continue
		}
		warnRottenDependencies(bpObj)

		label := formatPath(bpObj.Dir)
		if !strings.HasSuffix(label, "/") {
			label += "/"
		}
		lines = append(lines, label)

		state, err := bp.LoadState(filepath.Join(bpObj.StateDir, "state.yaml"))
		if err != nil && !errors.Is(err, bp.ErrNoSnapshot) {
			lines = append(lines, fmt.Sprintf("  snapshot: (error: %s)", err.Error()))
			exitCode = 1
			continue
		}

		snapshotID := "(none)"
		apiHash := "(none)"
		rottenSnapshot := false
		if state != nil && state.SnapshotID != "" {
			snapshotID = state.SnapshotID
			if meta, err := bp.LoadSnapshotMeta(bpObj.StateDir, snapshotID); err == nil {
				if meta.APIHash != "" {
					apiHash = meta.APIHash
				}
				rottenSnapshot = meta.Rotten
			}
		}
		if apiHash == "(none)" {
			if hash, err := bpObj.APIHash(); err == nil && hash != "" {
				apiHash = hash
			}
		}
		if rottenSnapshot {
			lines = append(lines, fmt.Sprintf("  snapshot: %s [ROTTEN]", snapshotID))
		} else {
			lines = append(lines, fmt.Sprintf("  snapshot: %s", snapshotID))
		}
		lines = append(lines, fmt.Sprintf("  api: %s", apiHash))

		depsState, _, err := bpObj.DependencyState()
		if err != nil {
			lines = append(lines, fmt.Sprintf("  deps: (error: %s)", err.Error()))
			exitCode = 1
		} else if len(depsState) == 0 {
			lines = append(lines, "  deps: (none)")
		} else {
			lines = append(lines, "  deps:")
			keys := make([]string, 0, len(depsState))
			for k := range depsState {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, key := range keys {
				dep := depsState[key]
				id := dep.Pinned
				if id == "" {
					id = dep.Latest
				}
				api := dep.APIHash
				if api == "" {
					api = dep.LatestAPIHash
				}
				line := fmt.Sprintf("    %s @ %s (api: %s)", key, id, api)
				if dep.Rotten {
					line += " [ROTTEN]"
				}
				lines = append(lines, line)
			}
		}
		if idx < len(targets)-1 {
			lines = append(lines, "")
		}
	}

	return CommandResult{ExitCode: exitCode, Output: strings.Join(lines, "\n")}
}

func mapLangs(ctx CommandContext) CommandResult {
	rootDir, rootBP, err := resolveNewLangRoot(ctx.Path, "")
	if err != nil {
		return CommandResult{ExitCode: 1, Output: err.Error(), Errors: []string{err.Error()}}
	}
	roots, err := listLanguageRoots(rootDir)
	if err != nil {
		return CommandResult{ExitCode: 1, Output: err.Error(), Errors: []string{err.Error()}}
	}
	if len(roots) == 0 {
		msg := "No language roots found"
		return CommandResult{ExitCode: 1, Output: msg, Errors: []string{msg}}
	}
	patterns := loadBlueprintPatterns(rootBP)
	type langPkg struct {
		root     string
		packages map[string]struct{}
		hashes   map[string]string
		errors   map[string]string
	}
	langs := map[string]*langPkg{}
	langNames := []string{}
	allPkgs := map[string]struct{}{}

	for _, root := range roots {
		langName := strings.TrimPrefix(filepath.Base(root), "src-")
		pkgs, err := collectBlueprints(root, patterns)
		if err != nil {
			return CommandResult{ExitCode: 1, Output: err.Error(), Errors: []string{err.Error()}}
		}
		pkgSet := map[string]struct{}{}
		for relDir := range pkgs {
			pkgSet[relDir] = struct{}{}
			allPkgs[relDir] = struct{}{}
		}
		langs[langName] = &langPkg{
			root:     root,
			packages: pkgSet,
			hashes:   map[string]string{},
			errors:   map[string]string{},
		}
		langNames = append(langNames, langName)
	}
	sort.Strings(langNames)

	lines := []string{}
	lines = append(lines, "Languages:")
	for _, lang := range langNames {
		root := langs[lang].root
		rel, err := filepath.Rel(rootDir, root)
		if err != nil {
			rel = root
		}
		lines = append(lines, fmt.Sprintf("  %s: %s", lang, filepath.ToSlash(rel)))
	}

	pkgList := make([]string, 0, len(allPkgs))
	for pkg := range allPkgs {
		pkgList = append(pkgList, pkg)
	}
	sort.Strings(pkgList)

	if len(pkgList) > 0 {
		lines = append(lines, "")
	}

	for idx, pkg := range pkgList {
		lines = append(lines, fmt.Sprintf("Package: %s", formatRelPackage(pkg)))
		lines = append(lines, "  api_hash:")
		missing := false
		diff := false
		var firstHash string
		for _, lang := range langNames {
			lp := langs[lang]
			hash, ok := lp.hashes[pkg]
			if !ok {
				hash, err = loadPackageAPIHash(lp.root, pkg)
				if err != nil {
					lp.errors[pkg] = err.Error()
					hash = "(error)"
				}
				lp.hashes[pkg] = hash
			}
			if _, ok := lp.packages[pkg]; !ok {
				hash = "(missing)"
				missing = true
			} else if hash == "" {
				hash = "(none)"
			}
			lines = append(lines, fmt.Sprintf("    %s: %s", lang, hash))
			if firstHash == "" && hash != "(missing)" && hash != "(error)" && hash != "(none)" {
				firstHash = hash
			} else if hash != "(missing)" && hash != "(error)" && hash != "(none)" && firstHash != "" && hash != firstHash {
				diff = true
			}
		}
		status := "OK"
		if missing {
			status = "MISSING"
		} else if diff {
			status = "DIFF"
		}
		lines = append(lines, fmt.Sprintf("  status: %s", status))
		if idx < len(pkgList)-1 {
			lines = append(lines, "")
		}
	}
	return CommandResult{ExitCode: 0, Output: strings.Join(lines, "\n")}
}

func loadPackageAPIHash(root, relDir string) (string, error) {
	dir := root
	if relDir != "." {
		dir = filepath.Join(root, filepath.FromSlash(relDir))
	}
	bpPath, err := bp.FindBlueprintFile(dir)
	if err != nil {
		return "", err
	}
	bpObj, err := bp.LoadBlueprint(bpPath)
	if err != nil {
		return "", err
	}
	hash, err := bpObj.APIHash()
	if err != nil {
		return "", err
	}
	return hash, nil
}

func formatRelPackage(rel string) string {
	rel = filepath.ToSlash(rel)
	if rel == "." || rel == "" {
		return "."
	}
	if strings.HasPrefix(rel, ".") {
		return rel
	}
	return "./" + rel
}
