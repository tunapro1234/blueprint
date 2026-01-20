package bp

import (
	"regexp"
	"strings"
)

var (
	legacySnapshotIDPattern = regexp.MustCompile(`^\d{8}-\d{4}-[a-f0-9]{4}-[a-z0-9-]{1,20}$`)
	slugSnapshotIDPattern   = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,19}-[a-f0-9]{4}$`)
	ssSnapshotIDPattern     = regexp.MustCompile(`^ss-[a-f0-9]{8}$`)
)

func IsSnapshotID(id string) bool {
	if id == "" {
		return false
	}
	lower := strings.ToLower(id)
	return legacySnapshotIDPattern.MatchString(lower) || slugSnapshotIDPattern.MatchString(lower) || ssSnapshotIDPattern.MatchString(lower)
}
