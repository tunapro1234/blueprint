package bp

import (
	"regexp"
	"strings"
)

var snapshotIDPattern = regexp.MustCompile(`^\d{8}-\d{4}-[a-f0-9]{4}-[a-z0-9-]{1,20}$`)

func IsSnapshotID(id string) bool {
	if id == "" {
		return false
	}
	return snapshotIDPattern.MatchString(strings.ToLower(id))
}
