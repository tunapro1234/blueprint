package commands

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	bp "blueprint"
	yaml "gopkg.in/yaml.v3"
)

type archiveMeta struct {
	ArchivedAt   string `yaml:"archived_at"`
	LastSnapshot string `yaml:"last_snapshot"`
	Reason       string `yaml:"reason"`
}

func writeArchiveMeta(path string, meta archiveMeta) error {
	if meta.ArchivedAt == "" {
		meta.ArchivedAt = time.Now().Format("2006-01-02T15:04:05")
	}
	out, err := yaml.Marshal(meta)
	if err != nil {
		return err
	}
	return os.WriteFile(path, out, 0o644)
}

func readArchiveMeta(path string) (archiveMeta, error) {
	var meta archiveMeta
	data, err := os.ReadFile(path)
	if err != nil {
		return meta, err
	}
	if err := yaml.Unmarshal(data, &meta); err != nil {
		return meta, err
	}
	return meta, nil
}

func resolveExistingLangRoot(rootDir string, rootBP *bp.Blueprint, lang string) (languageRoot, error) {
	lang = strings.TrimSpace(lang)
	if lang == "" {
		return languageRoot{}, fmt.Errorf("Language root not found: %s", lang)
	}
	roots, err := loadLanguageRoots(rootDir, rootBP)
	if err != nil {
		return languageRoot{}, err
	}
	for _, root := range roots {
		if strings.EqualFold(root.Name, lang) {
			return root, nil
		}
	}
	if looksLikePath(lang) {
		candidate := lang
		if !filepath.IsAbs(candidate) {
			candidate = filepath.Join(rootDir, candidate)
		}
		if info, err := os.Stat(candidate); err == nil && info.IsDir() {
			return languageRoot{Name: deriveLanguageName(filepath.Base(candidate), "src-*"), Path: candidate}, nil
		}
	}
	candidate := filepath.Join(rootDir, lang)
	if info, err := os.Stat(candidate); err == nil && info.IsDir() {
		return languageRoot{Name: deriveLanguageName(filepath.Base(candidate), "src-*"), Path: candidate}, nil
	}
	if rootBP != nil {
		if target, _ := resolveTargetLangRoot(rootDir, rootBP, lang, ""); target != "" {
			if info, err := os.Stat(target); err == nil && info.IsDir() {
				return languageRoot{Name: deriveLanguageName(filepath.Base(target), "src-*"), Path: target}, nil
			}
		}
	}
	return languageRoot{}, fmt.Errorf("Language root not found: %s", lang)
}

func looksLikePath(input string) bool {
	return strings.ContainsAny(input, `/\`) || filepath.IsAbs(input)
}

func ensureLangName(root languageRoot) string {
	if root.Name != "" {
		return root.Name
	}
	name := deriveLanguageName(filepath.Base(root.Path), "src-*")
	if name != "" {
		return name
	}
	return filepath.Base(root.Path)
}

func updateLanguageLists(rootBP *bp.Blueprint, rootDir, langName, langRootPath string, add bool) error {
	if rootBP == nil || rootBP.Path == "" {
		return nil
	}
	data, err := os.ReadFile(rootBP.Path)
	if err != nil {
		return err
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return err
	}
	if len(doc.Content) == 0 {
		return nil
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return nil
	}
	langPolicy := findMapValue(root, "language_policy")
	if langPolicy == nil || langPolicy.Kind != yaml.MappingNode {
		return nil
	}
	updated := false
	if langName != "" {
		if node := findMapValue(langPolicy, "languages"); node != nil {
			if updateStringSeq(node, func(list []string) []string {
				return updateNameList(list, langName, add)
			}) {
				updated = true
			}
		}
	}
	if node := findMapValue(langPolicy, "language_roots"); node != nil {
		if updateStringSeq(node, func(list []string) []string {
			return updateRootList(list, rootDir, langRootPath, add)
		}) {
			updated = true
		}
	}
	if !updated {
		return nil
	}
	out, err := yaml.Marshal(&doc)
	if err != nil {
		return err
	}
	return os.WriteFile(rootBP.Path, out, 0o644)
}

func findMapValue(node *yaml.Node, key string) *yaml.Node {
	if node == nil || node.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i < len(node.Content)-1; i += 2 {
		if node.Content[i].Value == key {
			return node.Content[i+1]
		}
	}
	return nil
}

func updateStringSeq(node *yaml.Node, update func([]string) []string) bool {
	if node == nil || node.Kind != yaml.SequenceNode {
		return false
	}
	list := make([]string, 0, len(node.Content))
	for _, item := range node.Content {
		if item.Kind != yaml.ScalarNode {
			return false
		}
		list = append(list, item.Value)
	}
	next := update(list)
	if reflect.DeepEqual(list, next) {
		return false
	}
	node.Content = node.Content[:0]
	for _, val := range next {
		node.Content = append(node.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: val})
	}
	return true
}

func updateNameList(list []string, name string, add bool) []string {
	out := make([]string, 0, len(list)+1)
	found := false
	for _, item := range list {
		if strings.EqualFold(item, name) {
			found = true
			if add {
				out = append(out, item)
			}
			continue
		}
		out = append(out, item)
	}
	if add && !found {
		out = append(out, name)
	}
	return out
}

func updateRootList(list []string, rootDir, langRootPath string, add bool) []string {
	absTarget := langRootPath
	if !filepath.IsAbs(absTarget) {
		absTarget = filepath.Join(rootDir, absTarget)
	}
	absTarget = filepath.Clean(absTarget)
	out := make([]string, 0, len(list)+1)
	found := false
	for _, item := range list {
		absItem := item
		if !filepath.IsAbs(absItem) {
			absItem = filepath.Join(rootDir, absItem)
		}
		absItem = filepath.Clean(absItem)
		if absItem == absTarget {
			found = true
			if add {
				out = append(out, item)
			}
			continue
		}
		out = append(out, item)
	}
	if add && !found {
		rel := langRootPath
		if filepath.IsAbs(rel) {
			if r, err := filepath.Rel(rootDir, rel); err == nil {
				rel = r
			}
		}
		rel = filepath.ToSlash(rel)
		out = append(out, rel)
	}
	return out
}

func formatRelPath(rootDir, target string) string {
	rel, err := filepath.Rel(rootDir, target)
	if err != nil {
		return filepath.ToSlash(target)
	}
	rel = filepath.ToSlash(rel)
	if rel == "." {
		return "."
	}
	if !strings.HasSuffix(rel, "/") {
		rel += "/"
	}
	return rel
}
