// Package compositor provides the small desktop-compositor surface bp needs.
package compositor

import (
	"context"
	"errors"
	"fmt"
	"os"
)

// Window is the compositor-owned identity and process metadata for one window.
type Window struct {
	ID        string `json:"id"`
	PID       int    `json:"pid"`
	Class     string `json:"class,omitempty"`
	Workspace string `json:"workspace,omitempty"`
	Focused   bool   `json:"focused"`
}

// Adapter is the compositor-specific part of agent window discovery.
type Adapter interface {
	ListWindows(context.Context) ([]Window, error)
	FocusWindow(context.Context, string) error
	SetBorderColors(context.Context, string, string, string) error
}

// ErrBorderColorsUnsupported is returned by compositors without per-window
// active and inactive border color controls.
var ErrBorderColorsUnsupported = errors.New("per-window border colors are not supported by this compositor")

// Detect selects only explicitly identified, supported Wayland compositors.
func Detect() (Adapter, error) {
	return DetectEnvironment(os.Getenv)
}

// DetectEnvironment is split out so detection does not require mutating the
// process environment in tests.
func DetectEnvironment(getenv func(string) string) (Adapter, error) {
	if getenv("HYPRLAND_INSTANCE_SIGNATURE") != "" {
		return &Hyprland{}, nil
	}
	if getenv("SWAYSOCK") != "" {
		return &Sway{}, nil
	}
	return nil, fmt.Errorf("no supported compositor detected (expected HYPRLAND_INSTANCE_SIGNATURE or SWAYSOCK)")
}
