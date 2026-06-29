package cmux

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/flanksource/clicky"
)

const defaultCommandTimeout = 30 * time.Second

// Runner executes the cmux CLI. Tests inject this to assert command arguments
// without requiring a live cmux.app instance.
type Runner func(ctx context.Context, cwd, binary string, timeout time.Duration, args ...string) (stdout string, err error)

type Client struct {
	Binary  string
	Timeout time.Duration
	Runner  Runner
}

type NewWorkspaceOpts struct {
	Cwd            string
	Name           string
	Description    string
	Command        string
	Focus          bool
	ResolveSurface bool
}

type EnsureWorkspaceOpts struct {
	Cwd         string
	Name        string
	Description string
	Focus       bool
}

type NewSurfaceOpts struct {
	WorkspaceRef string
	Cwd          string
	SurfaceType  string
	Focus        bool
}

type ReadScreenOpts struct {
	WorkspaceRef string
	SurfaceRef   string
	Lines        int
	Scrollback   bool
}

type WorkspaceRef struct {
	WorkspaceID string
	SurfaceID   string
	Raw         string
}

func NewClient(binary string) *Client {
	return &Client{Binary: binary}
}

func (c *Client) Available(ctx context.Context) error {
	if _, err := c.run(ctx, "", "ping"); err != nil {
		return fmt.Errorf("cmux is not available or not running: %w", err)
	}
	return nil
}

func (c *Client) NewWorkspace(ctx context.Context, opts NewWorkspaceOpts) (WorkspaceRef, error) {
	name := strings.TrimSpace(opts.Name)
	if name == "" {
		name = filepath.Base(filepath.Clean(opts.Cwd))
	}
	if name == "." || name == string(filepath.Separator) {
		name = "gavel-todos"
	}

	args := []string{
		"new-workspace",
		"--cwd", opts.Cwd,
		"--name", name,
		"--focus", strconv.FormatBool(opts.Focus),
	}
	if opts.Description != "" {
		args = append(args, "--description", opts.Description)
	}
	if opts.Command != "" {
		args = append(args, "--command", opts.Command)
	}
	args = append(args, "--id-format", "both")

	stdout, err := c.run(ctx, opts.Cwd, args...)
	if err != nil {
		return WorkspaceRef{}, err
	}
	ref := ParseWorkspaceRef(stdout)
	if ref.String() == "" {
		return WorkspaceRef{}, fmt.Errorf("cmux new-workspace did not return a workspace reference: %q", strings.TrimSpace(stdout))
	}
	if opts.ResolveSurface && ref.SurfaceID == "" {
		surfaceID, err := c.ResolveSurface(ctx, ref.String())
		if err != nil {
			return WorkspaceRef{}, err
		}
		ref.SurfaceID = surfaceID
	}
	return ref, nil
}

func (c *Client) EnsureWorkspace(ctx context.Context, opts EnsureWorkspaceOpts) (WorkspaceRef, bool, error) {
	name := strings.TrimSpace(opts.Name)
	if name == "" {
		name = filepath.Base(filepath.Clean(opts.Cwd))
	}
	if name == "." || name == string(filepath.Separator) {
		name = "gavel-todos"
	}

	ref, found, err := c.FindWorkspace(ctx, name, opts.Cwd)
	if err != nil {
		return WorkspaceRef{}, false, err
	}
	if found {
		return ref, true, nil
	}

	created, err := c.NewWorkspace(ctx, NewWorkspaceOpts{
		Cwd:         opts.Cwd,
		Name:        name,
		Description: opts.Description,
		Focus:       opts.Focus,
	})
	if err != nil {
		return WorkspaceRef{}, false, err
	}
	return created, false, nil
}

// SelectWorkspace switches cmux to the given workspace (the documented
// `select-workspace` command), bringing its terminal to the front so the user
// can watch or take over the agent session running there.
func (c *Client) SelectWorkspace(ctx context.Context, workspaceRef string) error {
	if strings.TrimSpace(workspaceRef) == "" {
		return fmt.Errorf("workspace reference is required")
	}
	_, err := c.run(ctx, "", "select-workspace", "--workspace", workspaceRef)
	return err
}

func (c *Client) FindWorkspace(ctx context.Context, name, cwd string) (WorkspaceRef, bool, error) {
	workspaces, err := c.ListWorkspaces(ctx)
	if err != nil {
		return WorkspaceRef{}, false, err
	}
	wantName := strings.TrimSpace(name)
	wantCwd := cleanPath(cwd)
	for _, workspace := range workspaces {
		if strings.TrimSpace(workspace.Ref) == "" {
			continue
		}
		if workspace.DisplayTitle() != wantName {
			continue
		}
		if wantCwd != "" && cleanPath(workspace.CurrentDirectory) != wantCwd {
			continue
		}
		return WorkspaceRef{WorkspaceID: strings.TrimSpace(workspace.Ref), Raw: strings.TrimSpace(workspace.Ref)}, true, nil
	}
	return WorkspaceRef{}, false, nil
}

func (c *Client) ListWorkspaces(ctx context.Context) ([]cmuxListedWorkspace, error) {
	raw, err := c.run(ctx, "", "list-workspaces", "--json")
	if err != nil {
		return nil, err
	}
	var list cmuxWorkspaceList
	if err := json.Unmarshal([]byte(jsonPayload(raw)), &list); err != nil {
		return nil, fmt.Errorf("parse cmux list-workspaces output: %w", err)
	}
	return list.Workspaces, nil
}

func (c *Client) NewSurface(ctx context.Context, opts NewSurfaceOpts) (WorkspaceRef, error) {
	workspaceRef := strings.TrimSpace(opts.WorkspaceRef)
	if workspaceRef == "" {
		return WorkspaceRef{}, fmt.Errorf("workspace reference is required")
	}
	surfaceType := strings.TrimSpace(opts.SurfaceType)
	if surfaceType == "" {
		surfaceType = "terminal"
	}
	args := []string{
		"new-surface",
		"--type", surfaceType,
		"--workspace", workspaceRef,
	}
	if opts.Cwd != "" {
		args = append(args, "--working-directory", opts.Cwd)
	}
	args = append(args, "--focus", strconv.FormatBool(opts.Focus))

	stdout, err := c.run(ctx, opts.Cwd, args...)
	if err != nil {
		return WorkspaceRef{}, err
	}
	surfaceID := ParseSurfaceRef(stdout)
	if surfaceID == "" {
		surfaceID, err = c.ResolveSurface(ctx, workspaceRef)
		if err != nil {
			return WorkspaceRef{}, err
		}
	}
	return WorkspaceRef{WorkspaceID: workspaceRef, SurfaceID: surfaceID, Raw: strings.TrimSpace(stdout)}, nil
}

func (c *Client) Send(ctx context.Context, workspaceRef, text string) error {
	if strings.TrimSpace(workspaceRef) == "" {
		return fmt.Errorf("workspace reference is required")
	}
	_, err := c.run(ctx, "", "send", "--workspace", workspaceRef, "--", text)
	return err
}

func (c *Client) SendSurface(ctx context.Context, workspaceRef, surfaceRef, text string) error {
	if strings.TrimSpace(workspaceRef) == "" {
		return fmt.Errorf("workspace reference is required")
	}
	if strings.TrimSpace(surfaceRef) == "" {
		return fmt.Errorf("surface reference is required")
	}
	_, err := c.run(ctx, "", "send", "--workspace", workspaceRef, "--surface", surfaceRef, "--", text)
	return err
}

func (c *Client) SendKeySurface(ctx context.Context, workspaceRef, surfaceRef, key string) error {
	if strings.TrimSpace(workspaceRef) == "" {
		return fmt.Errorf("workspace reference is required")
	}
	if strings.TrimSpace(surfaceRef) == "" {
		return fmt.Errorf("surface reference is required")
	}
	if strings.TrimSpace(key) == "" {
		return fmt.Errorf("key is required")
	}
	_, err := c.run(ctx, "", "send-key", "--workspace", workspaceRef, "--surface", surfaceRef, strings.TrimSpace(key))
	return err
}

func (c *Client) ReadScreen(ctx context.Context, opts ReadScreenOpts) (string, error) {
	if strings.TrimSpace(opts.SurfaceRef) != "" && strings.TrimSpace(opts.WorkspaceRef) == "" {
		return "", fmt.Errorf("workspace reference is required when surface reference is set")
	}
	args := []string{"read-screen"}
	if strings.TrimSpace(opts.WorkspaceRef) != "" {
		args = append(args, "--workspace", opts.WorkspaceRef)
	}
	if strings.TrimSpace(opts.SurfaceRef) != "" {
		args = append(args, "--surface", opts.SurfaceRef)
	}
	if opts.Lines > 0 {
		args = append(args, "--lines", strconv.Itoa(opts.Lines))
	} else if opts.Scrollback {
		args = append(args, "--scrollback")
	}
	return c.run(ctx, "", args...)
}

func (c *Client) ResolveSurface(ctx context.Context, workspaceRef string) (string, error) {
	if strings.TrimSpace(workspaceRef) == "" {
		return "", fmt.Errorf("workspace reference is required")
	}
	raw, err := c.run(ctx, "", "tree", "--json", "--workspace", workspaceRef)
	if err != nil {
		return "", err
	}
	surfaceID := surfaceRefFromTree(raw)
	if surfaceID == "" {
		return "", fmt.Errorf("cmux tree did not return a surface id for workspace %s", workspaceRef)
	}
	return surfaceID, nil
}

func (r WorkspaceRef) String() string {
	if r.WorkspaceID != "" {
		return r.WorkspaceID
	}
	if r.Raw != "" {
		return r.Raw
	}
	return r.SurfaceID
}

func ParseWorkspaceRef(stdout string) WorkspaceRef {
	raw := strings.TrimSpace(stdout)
	if raw == "" {
		return WorkspaceRef{}
	}
	if isWorkspaceRef(raw) {
		return WorkspaceRef{WorkspaceID: raw, Raw: raw}
	}
	if ref := regexp.MustCompile(`workspace:[A-Za-z0-9._:/-]+`).FindString(raw); ref != "" {
		return WorkspaceRef{WorkspaceID: ref, Raw: raw}
	}

	var m map[string]any
	if err := json.Unmarshal([]byte(jsonPayload(raw)), &m); err == nil {
		ref := WorkspaceRef{
			WorkspaceID: firstString(m, "workspace_id", "workspaceId", "workspace", "id"),
			SurfaceID:   firstString(m, "surface_id", "surfaceId", "surface_ref", "surfaceRef", "surface"),
			Raw:         raw,
		}
		if ref.String() != "" {
			return ref
		}
	}

	ref := WorkspaceRef{
		WorkspaceID: matchRef(raw, `(?i)workspace(?:[_ -]?id)?["'=:\s]+([A-Za-z0-9._:/-]+)`),
		SurfaceID:   matchRef(raw, `(?i)surface(?:[_ -]?id)?["'=:\s]+([A-Za-z0-9._:/-]+)`),
		Raw:         raw,
	}
	if ref.String() != "" && ref.WorkspaceID != raw {
		return ref
	}

	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			ref.Raw = line
			return ref
		}
	}
	return ref
}

func ParseSurfaceRef(stdout string) string {
	raw := strings.TrimSpace(stdout)
	if raw == "" {
		return ""
	}

	var m map[string]any
	if err := json.Unmarshal([]byte(jsonPayload(raw)), &m); err == nil {
		for _, candidate := range []string{
			firstString(m, "surface_id", "surfaceId", "surface_ref", "surfaceRef", "surface", "ref", "id"),
		} {
			if isSurfaceRef(candidate) {
				return candidate
			}
		}
	}

	if ref := regexp.MustCompile(`surface:[A-Za-z0-9._:/-]+`).FindString(raw); ref != "" {
		return ref
	}
	if isSurfaceRef(raw) {
		return raw
	}
	return ""
}

func (c *Client) run(ctx context.Context, cwd string, args ...string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	binary := c.Binary
	if binary == "" {
		binary = "cmux"
	}
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = defaultCommandTimeout
	}
	runner := c.Runner
	if runner == nil {
		runner = defaultRunner
	}
	return runner(ctx, cwd, binary, timeout, args...)
}

func defaultRunner(_ context.Context, cwd, binary string, timeout time.Duration, args ...string) (string, error) {
	proc := clicky.Exec(binary, args...).WithTimeout(timeout)
	if cwd != "" {
		proc = proc.WithCwd(cwd)
	}
	result := proc.Run().Result()
	if result.Error != nil || result.ExitCode != 0 {
		msg := strings.TrimSpace(result.Stderr)
		if msg == "" {
			msg = strings.TrimSpace(result.Stdout)
		}
		if msg == "" && result.Error != nil {
			msg = result.Error.Error()
		}
		return result.Stdout, fmt.Errorf("%s %s failed (exit %d): %s", binary, strings.Join(args, " "), result.ExitCode, msg)
	}
	return result.Stdout, nil
}

func matchRef(s, pattern string) string {
	m := regexp.MustCompile(pattern).FindStringSubmatch(s)
	if len(m) < 2 {
		return ""
	}
	return strings.Trim(m[1], `"'`)
}

func firstString(m map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := m[key]; ok {
			switch typed := value.(type) {
			case string:
				if strings.TrimSpace(typed) != "" {
					return strings.TrimSpace(typed)
				}
			case fmt.Stringer:
				if strings.TrimSpace(typed.String()) != "" {
					return strings.TrimSpace(typed.String())
				}
			case float64:
				return strconv.FormatInt(int64(typed), 10)
			}
		}
	}
	return ""
}

func surfaceRefFromTree(raw string) string {
	var tree cmuxTree
	if err := json.Unmarshal([]byte(raw), &tree); err != nil {
		return ""
	}
	if isSurfaceRef(tree.Active.SurfaceRef) {
		return tree.Active.SurfaceRef
	}
	for _, window := range tree.Windows {
		for _, workspace := range window.Workspaces {
			if surfaceID := selectedSurfaceFromWorkspace(workspace); surfaceID != "" {
				return surfaceID
			}
		}
	}
	return ""
}

func selectedSurfaceFromWorkspace(workspace cmuxTreeWorkspace) string {
	for _, pane := range workspace.Panes {
		if pane.Active || pane.Focused {
			if isSurfaceRef(pane.SelectedSurfaceRef) {
				return pane.SelectedSurfaceRef
			}
			if surfaceID := selectedSurfaceFromPane(pane); surfaceID != "" {
				return surfaceID
			}
		}
	}
	for _, pane := range workspace.Panes {
		if isSurfaceRef(pane.SelectedSurfaceRef) {
			return pane.SelectedSurfaceRef
		}
		if surfaceID := selectedSurfaceFromPane(pane); surfaceID != "" {
			return surfaceID
		}
		for _, surfaceID := range pane.SurfaceRefs {
			if isSurfaceRef(surfaceID) {
				return surfaceID
			}
		}
	}
	return ""
}

func selectedSurfaceFromPane(pane cmuxTreePane) string {
	for _, surface := range pane.Surfaces {
		if (surface.Active || surface.Focused || surface.Selected || surface.SelectedInPane) && isSurfaceRef(surface.Ref) {
			return surface.Ref
		}
	}
	for _, surface := range pane.Surfaces {
		if isSurfaceRef(surface.Ref) {
			return surface.Ref
		}
	}
	return ""
}

func isSurfaceRef(ref string) bool {
	return strings.HasPrefix(strings.TrimSpace(ref), "surface:")
}

func isWorkspaceRef(ref string) bool {
	return strings.HasPrefix(strings.TrimSpace(ref), "workspace:")
}

func cleanPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	return filepath.Clean(path)
}

func jsonPayload(raw string) string {
	trimmed := strings.TrimSpace(raw)
	object := strings.Index(trimmed, "{")
	array := strings.Index(trimmed, "[")
	switch {
	case object == -1:
		if array >= 0 {
			return trimmed[array:]
		}
	case array == -1:
		return trimmed[object:]
	case object < array:
		return trimmed[object:]
	default:
		return trimmed[array:]
	}
	return trimmed
}

type cmuxWorkspaceList struct {
	Workspaces []cmuxListedWorkspace `json:"workspaces"`
}

type cmuxListedWorkspace struct {
	CurrentDirectory string `json:"current_directory"`
	CustomTitle      string `json:"custom_title"`
	Ref              string `json:"ref"`
	Title            string `json:"title"`
}

func (w cmuxListedWorkspace) DisplayTitle() string {
	if strings.TrimSpace(w.CustomTitle) != "" {
		return strings.TrimSpace(w.CustomTitle)
	}
	return strings.TrimSpace(w.Title)
}

type cmuxTree struct {
	Active  cmuxTreeActive   `json:"active"`
	Windows []cmuxTreeWindow `json:"windows"`
}

type cmuxTreeActive struct {
	SurfaceRef string `json:"surface_ref"`
}

type cmuxTreeWindow struct {
	Workspaces []cmuxTreeWorkspace `json:"workspaces"`
}

type cmuxTreeWorkspace struct {
	Panes []cmuxTreePane `json:"panes"`
}

type cmuxTreePane struct {
	Active             bool              `json:"active"`
	Focused            bool              `json:"focused"`
	SelectedSurfaceRef string            `json:"selected_surface_ref"`
	SurfaceRefs        []string          `json:"surface_refs"`
	Surfaces           []cmuxTreeSurface `json:"surfaces"`
}

type cmuxTreeSurface struct {
	Active         bool   `json:"active"`
	Focused        bool   `json:"focused"`
	Ref            string `json:"ref"`
	Selected       bool   `json:"selected"`
	SelectedInPane bool   `json:"selected_in_pane"`
}
