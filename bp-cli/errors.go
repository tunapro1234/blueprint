package bp

import "errors"

var (
	ErrNoSnapshot     = errors.New("no snapshot")
	ErrNotBlueprint   = errors.New("not a blueprint package")
	ErrInvalidSection = errors.New("invalid section")
)
