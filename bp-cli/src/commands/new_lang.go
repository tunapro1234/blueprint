package commands

import (
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	bp "blueprint"
)

var defaultBlueprintPatterns = []string{
	"BLUEPRINT.yaml",
	".BLUEPRINT.yaml",
	"BLUEPRINT.*.yaml",
	".BLUEPRINT.*.yaml",
	"*.BP.yaml",
	"*.bp.yaml",
}

func NewLangCommand(ctx CommandContext) CommandResult {
	lang, _ := ctx.Args["lang"].(string)
	lang = strings.TrimSpace(lang)
	if lang == "" {
		return CommandResult{ExitCode: 1, Output: "Language is required", Errors: []string{"language is required"}}
	}
	rootArg, _ := ctx.Args["root"].(string)
	fromArg, _ := ctx.Args["from"].(string)
	targetArg, _ := ctx.Args["target"].(string)

	rootDir, rootBP, err := resolveNewLangRoot(ctx.Path, rootArg)
	if err != nil {
		return CommandResult{ExitCode: 1, Output: err.Error(), Errors: []string{err.Error()}}
	}
	patterns := loadBlueprintPatterns(rootBP)

	sourceRoot, err := resolveSourceLangRoot(rootDir, fromArg)
	if err != nil {
		return CommandResult{ExitCode: 1, Output: err.Error(), Errors: []string{err.Error()}}
	}
	sourceInfo, err := os.Stat(sourceRoot)
	if err != nil {
		msg := "Source language root not found"
		if !os.IsNotExist(err) {
			msg = err.Error()
		}
		return CommandResult{ExitCode: 1, Output: msg, Errors: []string{msg}}
	}
	if !sourceInfo.IsDir() {
		msg := "Source language root not found"
		return CommandResult{ExitCode: 1, Output: msg, Errors: []string{msg}}
	}
	targetRoot, err := resolveTargetLangRoot(rootDir, lang, targetArg)
	if err != nil {
		return CommandResult{ExitCode: 1, Output: err.Error(), Errors: []string{err.Error()}}
	}
	if _, err := os.Stat(targetRoot); err == nil {
		msg := "Target exists"
		return CommandResult{ExitCode: 1, Output: msg, Errors: []string{msg}}
	} else if !os.IsNotExist(err) {
		return CommandResult{ExitCode: 1, Output: err.Error(), Errors: []string{err.Error()}}
	}
	if err := os.MkdirAll(targetRoot, 0o755); err != nil {
		return CommandResult{ExitCode: 1, Output: err.Error(), Errors: []string{err.Error()}}
	}

	packages, err := collectBlueprints(sourceRoot, patterns)
	if err != nil {
		return CommandResult{ExitCode: 1, Output: err.Error(), Errors: []string{err.Error()}}
	}
	blueprintCount := 0
	for relDir, files := range packages {
		targetDir := targetRoot
		if relDir != "." {
			targetDir = filepath.Join(targetRoot, filepath.FromSlash(relDir))
		}
		if err := os.MkdirAll(targetDir, 0o755); err != nil {
			return CommandResult{ExitCode: 1, Output: err.Error(), Errors: []string{err.Error()}}
		}
		for _, srcPath := range files {
			dstPath := filepath.Join(targetDir, filepath.Base(srcPath))
			if isAPIBlueprintFile(filepath.Base(srcPath)) {
				relLink, err := filepath.Rel(targetDir, srcPath)
				if err != nil {
					relLink = srcPath
				}
				if err := os.Symlink(relLink, dstPath); err != nil {
					return CommandResult{ExitCode: 1, Output: err.Error(), Errors: []string{err.Error()}}
				}
				blueprintCount++
				continue
			}
			if err := copyFilePreservePerm(srcPath, dstPath); err != nil {
				return CommandResult{ExitCode: 1, Output: err.Error(), Errors: []string{err.Error()}}
			}
		}
	}
	sharedCopied, err := copySharedDirs(sourceRoot, targetRoot, packages)
	if err != nil {
		return CommandResult{ExitCode: 1, Output: err.Error(), Errors: []string{err.Error()}}
	}

	result := NewLangResult{
		Source:       sourceRoot,
		Target:       targetRoot,
		Packages:     len(packages),
		Blueprints:   blueprintCount,
		SharedCopied: sharedCopied,
	}
	output := fmt.Sprintf("Language root created: %s", formatPath(targetRoot))
	return CommandResult{ExitCode: 0, Output: output, Data: result}
}

func resolveNewLangRoot(pathArg, rootArg string) (string, *bp.Blueprint, error) {
	root := strings.TrimSpace(rootArg)
	if root == "" {
		rootPath, err := bp.ResolveRootDir(pathArg)
		if err != nil {
			return "", nil, err
		}
		root = rootPath
	}
	info, err := os.Stat(root)
	if err != nil {
		return "", nil, err
	}
	if !info.IsDir() {
		root = filepath.Dir(root)
	}
	rootBP, err := bp.LoadBlueprint(root)
	if err != nil {
		return "", nil, err
	}
	return rootBP.Dir, rootBP, nil
}

func resolveSourceLangRoot(rootDir, fromArg string) (string, error) {
	from := strings.TrimSpace(fromArg)
	if from != "" {
		if filepath.IsAbs(from) || strings.ContainsAny(from, `/\`) {
			if !filepath.IsAbs(from) {
				from = filepath.Join(rootDir, from)
			}
			return from, nil
		}
		if strings.HasPrefix(from, "src-") {
			return filepath.Join(rootDir, from), nil
		}
		return filepath.Join(rootDir, "src-"+from), nil
	}
	roots, err := listLanguageRoots(rootDir)
	if err != nil {
		return "", err
	}
	if len(roots) == 0 {
		return "", fmt.Errorf("Source language root not found")
	}
	sort.Strings(roots)
	return roots[0], nil
}

func resolveTargetLangRoot(rootDir, lang, targetArg string) (string, error) {
	target := strings.TrimSpace(targetArg)
	if target == "" {
		target = filepath.Join(rootDir, "src-"+lang)
	} else if !filepath.IsAbs(target) {
		target = filepath.Join(rootDir, target)
	}
	return target, nil
}

func listLanguageRoots(rootDir string) ([]string, error) {
	entries, err := os.ReadDir(rootDir)
	if err != nil {
		return nil, err
	}
	roots := []string{}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		if strings.HasPrefix(name, "src-") {
			roots = append(roots, filepath.Join(rootDir, name))
		}
	}
	return roots, nil
}

func loadBlueprintPatterns(root *bp.Blueprint) []string {
	if root == nil {
		return append([]string{}, defaultBlueprintPatterns...)
	}
	section, err := root.GetSection("language_policy")
	if err != nil || section == nil {
		return append([]string{}, defaultBlueprintPatterns...)
	}
	raw, ok := section["blueprint_patterns"]
	if !ok || raw == nil {
		return append([]string{}, defaultBlueprintPatterns...)
	}
	patterns := parsePatternList(raw)
	if len(patterns) == 0 {
		return append([]string{}, defaultBlueprintPatterns...)
	}
	return patterns
}

func parsePatternList(raw interface{}) []string {
	switch v := raw.(type) {
	case []string:
		out := make([]string, 0, len(v))
		for _, item := range v {
			item = strings.TrimSpace(item)
			if item != "" {
				out = append(out, item)
			}
		}
		return out
	case []interface{}:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if s, ok := item.(string); ok {
				s = strings.TrimSpace(s)
				if s != "" {
					out = append(out, s)
				}
			}
		}
		return out
	default:
		return nil
	}
}

func matchesBlueprint(name string, patterns []string) bool {
	lowerName := strings.ToLower(name)
	for _, pattern := range patterns {
		p := strings.ToLower(strings.TrimSpace(pattern))
		if p == "" {
			continue
		}
		if ok, err := path.Match(p, lowerName); err == nil && ok {
			return true
		}
	}
	return bp.IsBlueprintFile(name)
}

func isAPIBlueprintFile(name string) bool {
	lower := strings.ToLower(name)
	if !strings.HasSuffix(lower, ".yaml") {
		return false
	}
	return strings.Contains(lower, ".api.")
}

func collectBlueprints(root string, patterns []string) (map[string][]string, error) {
	packages := map[string][]string{}
	stateDirs := map[string]string{}
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !d.IsDir() {
			return nil
		}
		parent := filepath.Dir(path)
		if stateDirRel, ok := stateDirs[parent]; ok {
			if isStateDirName(d.Name(), stateDirRel) {
				return filepath.SkipDir
			}
		} else if d.Name() == ".blueprint" {
			return filepath.SkipDir
		} else if d.Name() == "deps" {
			return filepath.SkipDir
		}

		entries, err := os.ReadDir(path)
		if err != nil {
			return err
		}
		var files []string
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			if matchesBlueprint(entry.Name(), patterns) {
				files = append(files, filepath.Join(path, entry.Name()))
			}
		}
		if len(files) > 0 {
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			rel = filepath.ToSlash(rel)
			if rel == "" {
				rel = "."
			}
			sort.Strings(files)
			packages[rel] = files
			stateDirs[path] = bp.StateDirRelFromFile(files[0])
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return packages, nil
}

func copyFilePreservePerm(src, dst string) error {
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		link, err := os.Readlink(src)
		if err != nil {
			return err
		}
		return os.Symlink(link, dst)
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, info.Mode().Perm())
	if err != nil {
		in.Close()
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		in.Close()
		out.Close()
		return err
	}
	if err := in.Close(); err != nil {
		out.Close()
		return err
	}
	if err := out.Sync(); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Chmod(dst, info.Mode().Perm())
}

func copySharedDirs(sourceRoot, targetRoot string, packages map[string][]string) (int, error) {
	packageTop := map[string]struct{}{}
	for relDir := range packages {
		if relDir == "." {
			continue
		}
		parts := strings.Split(filepath.ToSlash(relDir), "/")
		if len(parts) > 0 && parts[0] != "" {
			packageTop[parts[0]] = struct{}{}
		}
	}
	entries, err := os.ReadDir(sourceRoot)
	if err != nil {
		return 0, err
	}
	shared := 0
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		if name == ".blueprint" || name == "deps" {
			continue
		}
		if _, ok := packageTop[name]; ok {
			continue
		}
		src := filepath.Join(sourceRoot, name)
		dst := filepath.Join(targetRoot, name)
		if err := copyDirContents(src, dst); err != nil {
			return shared, err
		}
		shared++
	}
	return shared, nil
}

func copyDirContents(src, dst string) error {
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("not a directory: %s", src)
	}
	return filepath.WalkDir(src, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			if rel == "." {
				return os.MkdirAll(dst, 0o755)
			}
			return os.MkdirAll(target, 0o755)
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			return os.Symlink(link, target)
		}
		in, err := os.Open(path)
		if err != nil {
			return err
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, info.Mode().Perm())
		if err != nil {
			in.Close()
			return err
		}
		if _, err := io.Copy(out, in); err != nil {
			in.Close()
			out.Close()
			return err
		}
		if err := in.Close(); err != nil {
			out.Close()
			return err
		}
		if err := out.Sync(); err != nil {
			out.Close()
			return err
		}
		if err := out.Close(); err != nil {
			return err
		}
		return os.Chmod(target, info.Mode().Perm())
	})
}
