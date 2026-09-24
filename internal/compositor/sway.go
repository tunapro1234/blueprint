package compositor

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

type Sway struct {
	Bin string
}

type swayNode struct {
	ID               int64      `json:"id"`
	Name             string     `json:"name"`
	Type             string     `json:"type"`
	PID              int        `json:"pid"`
	AppID            string     `json:"app_id"`
	Focused          bool       `json:"focused"`
	Nodes            []swayNode `json:"nodes"`
	FloatingNodes    []swayNode `json:"floating_nodes"`
	WindowProperties struct {
		Class string `json:"class"`
	} `json:"window_properties"`
}

func (s *Sway) command(ctx context.Context, args ...string) ([]byte, error) {
	bin := s.Bin
	if bin == "" {
		bin = "swaymsg"
	}
	out, err := exec.CommandContext(ctx, bin, args...).CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("swaymsg %s: %w: %s", args[0], err, strings.TrimSpace(string(out)))
	}
	return out, nil
}

func (s *Sway) ListWindows(ctx context.Context) ([]Window, error) {
	out, err := s.command(ctx, "-t", "get_tree", "-r")
	if err != nil {
		return nil, err
	}
	var root swayNode
	if err := json.Unmarshal(out, &root); err != nil {
		return nil, fmt.Errorf("decode sway tree: %w", err)
	}
	var windows []Window
	var walk func(swayNode, string)
	walk = func(node swayNode, workspace string) {
		if node.Type == "workspace" {
			workspace = node.Name
		}
		if node.PID > 0 && (node.AppID != "" || node.WindowProperties.Class != "") {
			class := node.AppID
			if class == "" {
				class = node.WindowProperties.Class
			}
			windows = append(windows, Window{ID: strconv.FormatInt(node.ID, 10), PID: node.PID, Class: class, Workspace: workspace, Focused: node.Focused})
		}
		for _, child := range node.Nodes {
			walk(child, workspace)
		}
		for _, child := range node.FloatingNodes {
			walk(child, workspace)
		}
	}
	walk(root, "")
	return windows, nil
}

func (s *Sway) FocusWindow(ctx context.Context, id string) error {
	windowID, err := strconv.ParseInt(id, 10, 64)
	if err != nil || windowID <= 0 {
		return fmt.Errorf("invalid sway window id %q", id)
	}
	_, err = s.command(ctx, "[con_id="+id+"]", "focus")
	return err
}

func (s *Sway) SetBorderColors(context.Context, string, string, string) error {
	return ErrBorderColorsUnsupported
}
