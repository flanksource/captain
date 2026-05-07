package cmux

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func CmuxBin() string {
	if p := os.Getenv("CMUX_BIN"); p != "" {
		return p
	}
	if p, err := exec.LookPath("cmux"); err == nil {
		return p
	}
	return "/Applications/cmux.app/Contents/Resources/bin/cmux"
}

func run(args ...string) (string, error) {
	cmd := exec.Command(CmuxBin(), args...)
	out, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return "", fmt.Errorf("cmux %s: %s", strings.Join(args, " "), strings.TrimSpace(string(ee.Stderr)))
		}
		return "", fmt.Errorf("cmux %s: %w", strings.Join(args, " "), err)
	}
	return strings.TrimSpace(string(out)), nil
}

func ActiveBrowserSurface() (string, error) {
	panels, err := run("list-panels")
	if err != nil {
		return "", err
	}

	var browserIDs []string
	for _, line := range strings.Split(panels, "\n") {
		if strings.Contains(line, "browser") {
			if fields := strings.Fields(line); len(fields) > 0 {
				browserIDs = append(browserIDs, fields[0])
			}
		}
	}

	if len(browserIDs) == 0 {
		return "", fmt.Errorf("no browser surfaces found")
	}
	if len(browserIDs) == 1 {
		return browserIDs[0], nil
	}

	panes, err := run("list-panes")
	if err != nil {
		return browserIDs[0], nil
	}

	browserSet := make(map[string]bool, len(browserIDs))
	for _, id := range browserIDs {
		browserSet[id] = true
	}

	for _, line := range strings.Split(panes, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 || !strings.HasPrefix(fields[0], "pane:") {
			continue
		}
		surfaces, err := run("list-pane-surfaces", "--pane", fields[0])
		if err != nil {
			continue
		}
		for _, sline := range strings.Split(surfaces, "\n") {
			if !strings.Contains(sline, "[selected]") {
				continue
			}
			sfields := strings.Fields(sline)
			if len(sfields) >= 2 && browserSet[sfields[1]] {
				return sfields[1], nil
			}
		}
	}

	return browserIDs[0], nil
}

type ScreenshotResult struct {
	Path string `json:"path"`
}

func BrowserScreenshot(surface string) (string, error) {
	out, err := run("browser", surface, "screenshot", "--json")
	if err != nil {
		return "", err
	}

	var result ScreenshotResult
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		// Fallback: parse "screenshot <path>" format
		if fields := strings.Fields(out); len(fields) >= 2 {
			return fields[1], nil
		}
		return out, nil
	}
	return result.Path, nil
}

func Screenshot() (string, error) {
	surface, err := ActiveBrowserSurface()
	if err != nil {
		return "", err
	}
	return BrowserScreenshot(surface)
}

func CopyToClipboard(text string) error {
	cmd := exec.Command("pbcopy")
	cmd.Stdin = strings.NewReader(text)
	return cmd.Run()
}

func DefaultSocketPath() string {
	if p := os.Getenv("CMUX_SOCKET_PATH"); p != "" {
		return p
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "Application Support", "cmux", "cmux.sock")
}
