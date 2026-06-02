//go:build darwin

// Package screenshot exposes screenshot.capture — grab the Mac's screen
// (or a region/window the user picks) and return it as a data:image/png
// URI, which the server turns into a vision attachment (see
// core/registry.go's dataURIImage convention). macOS-only.
package screenshot

import (
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/cmcoffee/gohort/gohort-desktop/core"
)

// maxEdge caps the long edge of the returned image. ~1568px is the
// effective resolution Claude's vision uses; larger just wastes tokens
// and gets downscaled anyway. Region/window grabs smaller than this are
// left untouched (no upscaling), so they stay crisp.
const maxEdge = 1568

func init() { core.RegisterTool(new(captureTool)) }

type captureTool struct{}

func (captureTool) Name() string { return "screenshot.capture" }
func (captureTool) Desc() string {
	return "Capture the Mac's screen and return it as an image you can see. By default captures the whole main display. Set interactive=true to let the USER drag-select a region or click a specific window to capture — use that when the user says \"let me show you\" or you want them to choose exactly what to share. Set display to capture a specific monitor (1=main). The image is downscaled and returned as a vision attachment, so just call it and look."
}
func (captureTool) Params() map[string]core.ToolParam {
	return map[string]core.ToolParam{
		"interactive": {Type: "boolean", Description: "If true, the user drags to select a region or presses Space + clicks a window to choose what's captured. Use when the user should pick the target."},
		"display":     {Type: "number", Description: "Which display to capture (1 = main). Omit for the main display. Ignored when interactive is true."},
	}
}
func (captureTool) Required() []string { return nil }
func (captureTool) Enabled() bool      { return true }

func (captureTool) Handler() core.ToolHandler { return capture }

func capture(args map[string]any) (string, error) {
	f, err := os.CreateTemp("", "gohort-screen-*.png")
	if err != nil {
		return "", err
	}
	path := f.Name()
	f.Close()
	defer os.Remove(path)

	capArgs := []string{"-x", "-t", "png"}
	if b, _ := args["interactive"].(bool); b {
		// Interactive: the user drag-selects a region or Space+clicks a
		// window. No -x so the normal capture UI/feedback shows.
		capArgs = []string{"-i", "-t", "png"}
	} else if d, ok := args["display"].(float64); ok && d >= 1 {
		capArgs = append(capArgs, "-D", strconv.Itoa(int(d)))
	}
	capArgs = append(capArgs, path)

	if out, err := exec.Command("/usr/sbin/screencapture", capArgs...).CombinedOutput(); err != nil {
		return "", fmt.Errorf("screencapture failed: %v (%s)", err, strings.TrimSpace(string(out)))
	}
	// Interactive cancel (Esc) leaves no/empty file; so does a missing
	// Screen Recording grant for a non-interactive capture.
	if info, err := os.Stat(path); err != nil || info.Size() == 0 {
		return "", fmt.Errorf("no screenshot captured — either you cancelled the selection, or Gohort-Bridge needs Screen Recording access (System Settings → Privacy & Security → Screen Recording)")
	}

	downscale(path, maxEdge) // best-effort; only shrinks oversized images

	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(data), nil
}

// downscale shrinks the image in place so its longest edge is at most
// maxEdge px. Never upscales (region grabs below maxEdge are left as-is).
// Best-effort via sips; leaves the file untouched on any error.
func downscale(path string, maxEdge int) {
	w, h := imageSize(path)
	if w <= maxEdge && h <= maxEdge {
		return
	}
	exec.Command("/usr/bin/sips", "-Z", strconv.Itoa(maxEdge), path).Run()
}

// imageSize returns the pixel dimensions of an image via sips, or 0,0 on
// error.
func imageSize(path string) (w, h int) {
	out, err := exec.Command("/usr/bin/sips", "-g", "pixelWidth", "-g", "pixelHeight", path).Output()
	if err != nil {
		return 0, 0
	}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "pixelWidth:") {
			fmt.Sscanf(line, "pixelWidth: %d", &w)
		} else if strings.HasPrefix(line, "pixelHeight:") {
			fmt.Sscanf(line, "pixelHeight: %d", &h)
		}
	}
	return w, h
}
