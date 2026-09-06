package cmux

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

func run(args ...string) (string, error) {
	binary, err := CmuxBin()
	if err != nil {
		return "", err
	}
	cmd := exec.Command(binary, args...)
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

// Surface is a cmux terminal surface hosting an agent, keyed by its UUID (which
// matches the CMUX_SURFACE_ID env var of the process running inside it).
type Surface struct {
	ID        string
	Ref       string
	Title     string
	Workspace string
	Tty       string
	Type      string
}

// Surfaces returns the current cmux surfaces keyed by surface UUID, sourced from
// `cmux --json --id-format both tree --all`. Returns an error when cmux is not
// running (callers should treat enrichment as best-effort).
func Surfaces() (map[string]Surface, error) {
	out, err := run("--json", "--id-format", "both", "tree", "--all")
	if err != nil {
		return nil, err
	}
	return parseSurfaces([]byte(out))
}

// cmuxTree mirrors the subset of `cmux --json tree` we consume.
type cmuxTree struct {
	Windows []struct {
		Workspaces []struct {
			Title string `json:"title"`
			Panes []struct {
				Surfaces []struct {
					ID    string `json:"id"`
					Ref   string `json:"ref"`
					Title string `json:"title"`
					Tty   string `json:"tty"`
					Type  string `json:"type"`
				} `json:"surfaces"`
			} `json:"panes"`
		} `json:"workspaces"`
	} `json:"windows"`
}

func parseSurfaces(data []byte) (map[string]Surface, error) {
	var tree cmuxTree
	if err := json.Unmarshal(data, &tree); err != nil {
		return nil, err
	}
	surfaces := make(map[string]Surface)
	for _, win := range tree.Windows {
		for _, ws := range win.Workspaces {
			for _, pane := range ws.Panes {
				for _, s := range pane.Surfaces {
					if s.ID == "" {
						continue
					}
					surfaces[s.ID] = Surface{
						ID:        s.ID,
						Ref:       s.Ref,
						Title:     stripStatusGlyph(s.Title),
						Workspace: stripStatusGlyph(ws.Title),
						Tty:       s.Tty,
						Type:      s.Type,
					}
				}
			}
		}
	}
	return surfaces, nil
}

// stripStatusGlyph removes cmux's leading status/spinner glyph (the "✳" idle
// marker or a braille spinner rune, U+2800–U+28FF) from a title, keeping the
// human-readable text. Other leading runes (e.g. "…/path") are preserved.
func stripStatusGlyph(title string) string {
	title = strings.TrimSpace(title)
	r, size := utf8.DecodeRuneInString(title)
	if r == '✳' || (r >= 0x2800 && r <= 0x28FF) {
		return strings.TrimSpace(title[size:])
	}
	return title
}
