package tokens

import (
	"errors"
	"os"
	"path/filepath"
	"time"
)

const footprintTTL = 6 * time.Hour

func Footprint(config Config) (FootprintStats, error) {
	config = config.normalized()
	cachePath := filepath.Join(config.StoreDir, "footprint.json")
	var cached FootprintStats
	if err := readJSON(cachePath, &cached); err == nil {
		if checked, parseErr := time.Parse(time.RFC3339Nano, cached.CheckedAt); parseErr == nil {
			age := config.Now().Sub(checked)
			if age >= 0 && age < footprintTTL {
				return cached, nil
			}
		}
	}

	claude, err := directorySize(config.ClaudeRoot)
	if err != nil {
		return FootprintStats{}, err
	}
	codex, err := directorySize(config.CodexRoot)
	if err != nil {
		return FootprintStats{}, err
	}
	store, err := directorySize(config.StoreDir)
	if err != nil {
		return FootprintStats{}, err
	}
	result := FootprintStats{
		CheckedAt: config.Now().Format(time.RFC3339Nano),
		Claude:    claude, Codex: codex, Store: store,
		Total: claude + codex + store,
	}
	if err := writeJSON(cachePath, result); err != nil {
		return FootprintStats{}, err
	}
	return result, nil
}

func directorySize(root string) (int64, error) {
	if root == "" {
		return 0, nil
	}
	var total int64
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil
			}
			return err
		}
		if entry.IsDir() || filepath.Base(path) == ".lock" {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		total += info.Size()
		return nil
	})
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	return total, err
}

func HumanBytes(value int64) string {
	const gib = int64(1 << 30)
	return formatOneDecimal(value, gib) + "G"
}
