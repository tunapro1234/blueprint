package bp

import (
	yamlparser "blueprint/deps/yamlparser"
)

// ParseResult is an alias for yamlparser.ParseResult.
type ParseResult = yamlparser.ParseResult

// ParseError is an alias for yamlparser.ParseError.
type ParseError = yamlparser.ParseError

// ParseYAML parses blueprint YAML content.
func ParseYAML(data []byte) (map[string]interface{}, error) {
	result := yamlparser.Parse(string(data))
	if result.Error != nil {
		return nil, result.Error
	}
	return result.Data, nil
}

// ParseFile reads and parses a YAML file.
func ParseFile(path string) ParseResult {
	return yamlparser.ParseFile(path)
}

// Parse parses blueprint YAML content.
func Parse(content string) ParseResult {
	return yamlparser.Parse(content)
}
