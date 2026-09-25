//go:build !linux && !darwin

package buildinfo

// executableStat has no portable inode/ctime identity here, so every caller
// falls back to hashing the executable.
func executableStat(string) (*ExecutableStat, bool) { return nil, false }
