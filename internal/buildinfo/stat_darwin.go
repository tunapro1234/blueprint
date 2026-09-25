package buildinfo

import (
	"os"
	"syscall"
)

func executableStat(path string) (*ExecutableStat, bool) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, false
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return nil, false
	}
	return &ExecutableStat{
		Device:  uint64(st.Dev),
		Inode:   st.Ino,
		Size:    info.Size(),
		MtimeNS: st.Mtimespec.Sec*1e9 + st.Mtimespec.Nsec,
		CtimeNS: st.Ctimespec.Sec*1e9 + st.Ctimespec.Nsec,
	}, true
}
