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
	Path        string
	RelDir      string
	HasAPI      bool
	APIInline   bool
	HasImpl     bool
	ImplInline  bool
	HasTests    bool
	TestsInline bool
}

func inspectBlueprintFile(path string) (blueprintFileInfo, error) {
	info := blueprintFileInfo{Path: path}
	bpObj, err := bp.LoadBlueprint(path)
	if err != nil {
		return info, err
	}
	apiInfo := sectionInfo(bpObj.Data["api"])
	implInfo := sectionInfo(bpObj.Data["implementation"])
	testsInfo := sectionInfo(bpObj.Data["tests"])
	info.HasAPI = apiInfo.Present
	info.APIInline = apiInfo.Inline
	info.HasImpl = implInfo.Present
	info.ImplInline = implInfo.Inline
	info.HasTests = testsInfo.Present
	info.TestsInline = testsInfo.Inline
	return info, nil
}

type sectionPresence struct {
	Present bool
	Inline  bool
}

func sectionInfo(val interface{}) sectionPresence {
	if val == nil {
		return sectionPresence{}
	}
	switch val.(type) {
	case string:
		return sectionPresence{Present: true, Inline: false}
	default:
		return sectionPresence{Present: true, Inline: true}
	}
}

func needsSplit(file blueprintFileInfo, totalFiles int) bool {
	if totalFiles != 1 {
		return false
	}
	if !file.HasAPI {
		return false
	}
	if !file.APIInline {
		return false
	}
	return file.ImplInline || file.TestsInline
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
	apiInfo := sectionInfo(apiVal)
	implInfo := sectionInfo(implVal)
	testsInfo := sectionInfo(testsVal)
	if !apiInfo.Present {
		return nil
	}
	if !(implInfo.Inline || testsInfo.Inline) {
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
	if implInfo.Inline {
		specData["implementation"] = implVal
	}
	if testsInfo.Inline {
		specData["tests"] = testsVal
	}
	if len(specData) > 0 {
		specData["_meta"] = map[string]interface{}{
			"type": "spec",
		}
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
	if apiInfo.Present {
		newData["api"] = "./BLUEPRINT.api.yaml#api"
	}
	if implInfo.Present {
		newData["implementation"] = "./BLUEPRINT.spec.yaml#implementation"
	}
	if testsInfo.Present {
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
