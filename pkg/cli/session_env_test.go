package cli

import "testing"

func TestParseProcEnviron(t *testing.T) {
	data := []byte("PATH=/usr/bin\x00CMUX_SURFACE_ID=SURFACE-1\x00CMUX_PORT=9150\x00EMPTY\x00")
	env := parseProcEnviron(data)
	if env["PATH"] != "/usr/bin" {
		t.Fatalf("PATH = %q", env["PATH"])
	}
	if env["CMUX_SURFACE_ID"] != "SURFACE-1" {
		t.Fatalf("CMUX_SURFACE_ID = %q", env["CMUX_SURFACE_ID"])
	}
	if _, ok := env["EMPTY"]; ok {
		t.Fatal("entry without '=' should be skipped")
	}
}

func TestExtractCmuxEnvIgnoresArgvDecoys(t *testing.T) {
	// Flattened `ps -Eww` output: the claude argv embeds ${CMUX_..:-cmux}
	// references (colon, not '='), which must NOT be captured as env vars.
	psOutput := `claude --settings {"hooks":[{"command":"${CMUX_CLAUDE_HOOK_CMUX_BIN:-cmux} hooks"}]} ` +
		`CMUX_SURFACE_ID=1D727ED7-0DCB CMUX_WORKSPACE_ID=4D2B7ACC CMUX_TAB_ID=4D2B7ACC ` +
		`CMUX_PANEL_ID=1D727ED7 CMUX_PORT=9150 CMUX_AGENT_LAUNCH_KIND=claude ` +
		`CMUX_SOCKET_PATH=/Users/moshe/.local/state/cmux/cmux.sock ` +
		`CMUX_AGENT_LAUNCH_ARGV_B64=L1VzZXJzL21vc2hlAA==`
	env := extractCmuxEnv(psOutput)

	if env["CMUX_SURFACE_ID"] != "1D727ED7-0DCB" {
		t.Fatalf("CMUX_SURFACE_ID = %q", env["CMUX_SURFACE_ID"])
	}
	if _, ok := env["CMUX_CLAUDE_HOOK_CMUX_BIN"]; ok {
		t.Fatal("decoy ${CMUX_...:-cmux} reference should not be captured")
	}
	if env["CMUX_AGENT_LAUNCH_ARGV_B64"] != "L1VzZXJzL21vc2hlAA==" {
		t.Fatalf("base64 value with '==' padding not captured: %q", env["CMUX_AGENT_LAUNCH_ARGV_B64"])
	}
}

func TestParseCmuxSurface(t *testing.T) {
	env := map[string]string{
		"CMUX_SURFACE_ID":        "SURFACE-1",
		"CMUX_WORKSPACE_ID":      "WS-1",
		"CMUX_TAB_ID":            "TAB-1",
		"CMUX_PANEL_ID":          "PANEL-1",
		"CMUX_PORT":              "9150",
		"CMUX_CLAUDE_PID":        "33088",
		"CMUX_AGENT_LAUNCH_KIND": "claude",
		"CMUX_SOCKET_PATH":       "/tmp/cmux.sock",
	}
	surface := parseCmuxSurface(env)
	if surface == nil {
		t.Fatal("expected surface")
	}
	if surface.SurfaceID != "SURFACE-1" || surface.WorkspaceID != "WS-1" ||
		surface.TabID != "TAB-1" || surface.PanelID != "PANEL-1" ||
		surface.AgentKind != "claude" || surface.SocketPath != "/tmp/cmux.sock" {
		t.Fatalf("surface = %+v", surface)
	}
	if surface.Port != 9150 {
		t.Fatalf("port = %d", surface.Port)
	}
	if surface.ClaudePID != 33088 {
		t.Fatalf("claude pid = %d", surface.ClaudePID)
	}
}

func TestParseCmuxSurfaceNilWhenAbsent(t *testing.T) {
	if surface := parseCmuxSurface(map[string]string{"PATH": "/usr/bin"}); surface != nil {
		t.Fatalf("expected nil surface, got %+v", surface)
	}
}
