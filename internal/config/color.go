package config

import (
	"fmt"
	"strconv"
	"strings"
)

// ColorIndex normalizes a named accent or xterm palette index.
func ColorIndex(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if named, ok := map[string]string{"red": "160", "orange": "208", "yellow": "178", "green": "70", "cyan": "44", "blue": "33", "purple": "135", "pink": "205", "gray": "245", "white": "255"}[value]; ok {
		return named, nil
	}
	n, err := strconv.Atoi(value)
	if err != nil || n < 0 || n > 255 {
		return "", fmt.Errorf("invalid color %q: use a named color or 0–255", value)
	}
	return strconv.Itoa(n), nil
}
