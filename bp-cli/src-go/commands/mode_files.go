package commands

import (
	"os"
)

func setTrackedFilesReadOnly(files map[string]string) error {
	for _, abs := range files {
		info, err := os.Stat(abs)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return err
		}
		if info.IsDir() {
			continue
		}
		if err := makeReadOnly(abs, info.Mode().Perm()); err != nil {
			return err
		}
	}
	return nil
}

func setTrackedFilesWritable(files map[string]string) error {
	for _, abs := range files {
		info, err := os.Stat(abs)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return err
		}
		if info.IsDir() {
			continue
		}
		perm := info.Mode().Perm() | 0o200
		if perm == 0 {
			perm = 0o600
		}
		if err := os.Chmod(abs, perm); err != nil {
			return err
		}
	}
	return nil
}
