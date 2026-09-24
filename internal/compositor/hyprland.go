package compositor

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
)

type Hyprland struct {
	Bin string
}

var hyprAddress = regexp.MustCompile(`^0x[0-9a-fA-F]+$`)
var borderColor = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

func (h *Hyprland) command(ctx context.Context, args ...string) ([]byte, error) {
	bin := h.Bin
	if bin == "" {
		bin = "hyprctl"
	}
	out, err := exec.CommandContext(ctx, bin, args...).CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("hyprctl %s: %w: %s", args[0], err, strings.TrimSpace(string(out)))
	}
	return out, nil
}

func (h *Hyprland) ListWindows(ctx context.Context) ([]Window, error) {
	out, err := h.command(ctx, "-j", "clients")
	if err != nil {
		return nil, err
	}
	var clients []struct {
		Address        string `json:"address"`
		PID            int    `json:"pid"`
		Class          string `json:"class"`
		Focused        bool   `json:"focused"`
		FocusHistoryID *int   `json:"focusHistoryID"`
		Workspace      struct {
			ID   int    `json:"id"`
			Name string `json:"name"`
		} `json:"workspace"`
	}
	if err := json.Unmarshal(out, &clients); err != nil {
		return nil, fmt.Errorf("decode hyprctl clients: %w", err)
	}
	windows := make([]Window, 0, len(clients))
	for _, client := range clients {
		if client.PID <= 0 || !hyprAddress.MatchString(client.Address) {
			continue
		}
		workspace := client.Workspace.Name
		if workspace == "" {
			workspace = strconv.Itoa(client.Workspace.ID)
		}
		focused := client.Focused || client.FocusHistoryID != nil && *client.FocusHistoryID == 0
		windows = append(windows, Window{ID: client.Address, PID: client.PID, Class: client.Class, Workspace: workspace, Focused: focused})
	}
	return windows, nil
}

func (h *Hyprland) FocusWindow(ctx context.Context, id string) error {
	if !hyprAddress.MatchString(id) {
		return fmt.Errorf("invalid Hyprland window id %q", id)
	}
	_, err := h.command(ctx, "dispatch", "focuswindow", "address:"+id)
	return err
}

func (h *Hyprland) SetBorderColors(ctx context.Context, id, active, inactive string) error {
	if !hyprAddress.MatchString(id) {
		return fmt.Errorf("invalid Hyprland window id %q", id)
	}
	if !borderColor.MatchString(active) || !borderColor.MatchString(inactive) {
		return fmt.Errorf("border colors must be #rrggbb")
	}
	target := "address:" + id
	if _, err := h.command(ctx, "setprop", target, "activebordercolor", "rgb("+active[1:]+")"); err != nil {
		return err
	}
	_, err := h.command(ctx, "setprop", target, "inactivebordercolor", "rgb("+inactive[1:]+")")
	return err
}
