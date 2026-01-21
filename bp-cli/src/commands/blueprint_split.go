package commands

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	bp "blueprint"
	yaml "gopkg.in/yaml.v3"
)

type blueprintFileInfo struct {
	Path     string
	RelDir   string
	HasAPI   bool
	HasImpl  bool
	HasTests bool
}

func inspectBlueprintFile(path string) (blueprintFileInfo, error) {
	info := blueprintFileInfo{Path: path}
	bpObj, err := bp.LoadBlueprint(path)
	if err != nil {
		return info, err
	}
	info.HasAPI = hasSectionContent(bpObj.Data["api"])
	info.HasImpl = hasSectionContent(bpObj.Data["implementation"])
	info.HasTests = hasSectionContent(bpObj.Data["tests"])
	return info, nil
}

func hasSectionContent(val interface{}) bool {
	if val == nil {
		return false
	}
	switch val.(type) {
	case string:
		return false
	default:
		return true
	}
}

func needsSplit(file blueprintFileInfo, totalFiles int) bool {
	if totalFiles != 1 {
		return false
	}
	if !file.HasAPI {
		return false
	}
	return file.HasImpl || file.HasTests
}

func confirmSplit(prompt string) bool {
	stat, err := os.Stdin.Stat()
	if err == nil && stat.Mode()&os.ModeCharDevice == 0 {
		return false
	}
	fmt.Fprint(os.Stderr, prompt)
	reader := bufio.NewReader(os.Stdin)
	line, err := reader.ReadString('\n')
	if err != nil {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true
	default:
		return false
	}
}

func splitBlueprintFile(path string) error {
	bpObj, err := bp.LoadBlueprint(path)
	if err != nil {
		return err
	}
	apiVal := bpObj.Data["api"]
	implVal := bpObj.Data["implementation"]
	testsVal := bpObj.Data["tests"]
	if !hasSectionContent(apiVal) {
		return nil
	}
	if !(hasSectionContent(implVal) || hasSectionContent(testsVal)) {
		return nil
	}

	dir := filepath.Dir(path)
	apiPath := filepath.Join(dir, "BLUEPRINT.api.yaml")
	specPath := filepath.Join(dir, "BLUEPRINT.spec.yaml")
	if _, err := os.Stat(apiPath); err == nil {
		return fmt.Errorf("BLUEPRINT.api.yaml already exists in %s", dir)
	} else if !os.IsNotExist(err) {
		return err
	}
	if _, err := os.Stat(specPath); err == nil {
		return fmt.Errorf("BLUEPRINT.spec.yaml already exists in %s", dir)
	} else if !os.IsNotExist(err) {
		return err
	}

	apiData := map[string]interface{}{
		"api": apiVal,
	}
	if err := writeYAML(apiPath, apiData); err != nil {
		return err
	}

	specData := map[string]interface{}{}
	if hasSectionContent(implVal) {
		specData["implementation"] = implVal
	}
	if hasSectionContent(testsVal) {
		specData["tests"] = testsVal
	}
	if len(specData) > 0 {
		if err := writeYAML(specPath, specData); err != nil {
			return err
		}
	}

	newData := map[string]interface{}{}
	for key, val := range bpObj.Data {
		switch key {
		case "api", "implementation", "tests":
			continue
		default:
			newData[key] = val
		}
	}
	if hasSectionContent(apiVal) {
		newData["api"] = "./BLUEPRINT.api.yaml#api"
	}
	if hasSectionContent(implVal) {
		newData["implementation"] = "./BLUEPRINT.spec.yaml#implementation"
	}
	if hasSectionContent(testsVal) {
		newData["tests"] = "./BLUEPRINT.spec.yaml#tests"
	}
	return writeYAML(path, newData)
}

func writeYAML(path string, data map[string]interface{}) error {
	out, err := yaml.Marshal(data)
	if err != nil {
		return err
	}
	return os.WriteFile(path, out, 0o644)
}
