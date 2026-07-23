//go:build e2e

// Package mcp2cli_test owns the process-level contract between Captain and a
// remote MCP server. It deliberately observes only binaries, files, and wire
// behavior so changes to Captain's internal command construction remain visible.
package mcp2cli_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	fixtureUsername = "captain-e2e"
	fixturePassword = "captain-e2e-password"
	commandTimeout  = 15 * time.Second
	buildTimeout    = 2 * time.Minute
)

type testHarness struct {
	captainBin string
	workspace  string
	ctx        context.Context
	env        []string
	secrets    []string
}

type commandResult struct {
	path    string
	args    []string
	stdout  string
	stderr  string
	err     error
	secrets []string
}

type fixtureProcess struct {
	cmd      *exec.Cmd
	url      string
	stderr   *lockedBuffer
	done     chan error
	stopOnce sync.Once
	stopErr  error
}

type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

type listRecord struct {
	Name      string       `json:"name"`
	Transport string       `json:"transport"`
	Endpoint  string       `json:"endpoint"`
	ToolCount int          `json:"toolCount"`
	Tools     []listedTool `json:"tools"`
}

type listedTool struct {
	Name        string          `json:"name"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

type echoResult struct {
	Username  string `json:"username"`
	Message   string `json:"message"`
	Count     int    `json:"count"`
	Uppercase bool   `json:"uppercase"`
	Format    string `json:"format"`
	Output    string `json:"output"`
}

type callToolResult struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	StructuredContent echoResult `json:"structuredContent"`
	IsError           bool       `json:"isError"`
}

type auditRecord struct {
	Auth       string `json:"auth"`
	HTTPMethod string `json:"http_method"`
	MCPMethod  string `json:"mcp_method"`
	Path       string `json:"path"`
	Username   string `json:"username"`
}

func TestMCP2CLIBlackBox(t *testing.T) {
	root := repositoryRoot(t)
	tempDir := t.TempDir()
	binDir := filepath.Join(tempDir, "bin")
	workspace := filepath.Join(tempDir, "workspace")
	home := filepath.Join(tempDir, "home")
	configHome := filepath.Join(tempDir, "config")
	cacheHome := filepath.Join(tempDir, "cache")
	for _, dir := range []string{binDir, workspace, home, configHome, cacheHome} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatalf("create test directory %s: %v", dir, err)
		}
	}

	harness := &testHarness{
		captainBin: filepath.Join(binDir, "captain"),
		workspace:  workspace,
		ctx:        t.Context(),
		env: replaceEnvironment(os.Environ(), map[string]string{
			"HOME":            home,
			"XDG_CONFIG_HOME": configHome,
			"XDG_CACHE_HOME":  cacheHome,
		}),
	}
	fixtureBin := filepath.Join(binDir, "mcp-fixture")
	buildBinary(t, root, harness.captainBin, "./cmd/captain")
	buildBinary(t, root, fixtureBin, "./tests/e2e/mcp2cli/fixture")

	auditPath := filepath.Join(tempDir, "mcp-audit.jsonl")
	fixture := startFixture(t, fixtureBin, auditPath)
	encodedCredentials := base64.StdEncoding.EncodeToString([]byte(fixtureUsername + ":" + fixturePassword))
	authorization := "Basic " + encodedCredentials
	harness.secrets = []string{fixturePassword, fixtureUsername + ":" + fixturePassword, encodedCredentials, authorization}

	if !t.Run("rejects unauthenticated registration without persistence", func(t *testing.T) {
		result := harness.runCaptain(
			"mcp", "add", "rejected", fixture.url,
			"--transport", "http",
			"--timeout", "5s",
		)
		if result.err == nil {
			t.Fatalf("unauthenticated registration succeeded\n%s", result.diagnostics(fixture.stderr.String()))
		}
		combined := strings.ToLower(result.stdout + "\n" + result.stderr)
		if !strings.Contains(combined, "401") &&
			!strings.Contains(combined, "unauthorized") &&
			!strings.Contains(combined, "authorization required") {
			t.Fatalf("unauthenticated registration did not report HTTP 401\n%s", result.diagnostics(fixture.stderr.String()))
		}

		list := harness.runCaptain("mcp", "list", "--format", "json")
		requireSuccess(t, list, fixture.stderr.String())
		var records []listRecord
		if err := json.Unmarshal([]byte(list.stdout), &records); err != nil {
			t.Fatalf("decode registry listing: %v\n%s", err, list.diagnostics(fixture.stderr.String()))
		}
		for _, record := range records {
			if record.Name == "rejected" {
				t.Fatalf("failed registration was persisted\n%s", list.diagnostics(fixture.stderr.String()))
			}
		}
	}) {
		return
	}

	if !t.Run("registers authenticated HTTP server", func(t *testing.T) {
		result := harness.runCaptain(
			"mcp", "add", "secure", fixture.url,
			"--transport", "http",
			"--header", "Authorization: "+authorization,
			"--timeout", "5s",
		)
		requireSuccess(t, result, fixture.stderr.String())
		if !strings.Contains(result.stdout, `Added MCP server "secure"`) {
			t.Fatalf("registration confirmation is missing\n%s", result.diagnostics(fixture.stderr.String()))
		}
		requireNoSecrets(t, result)
	}) {
		return
	}

	if !t.Run("lists the cached HTTP tool catalog", func(t *testing.T) {
		result := harness.runCaptain("mcp", "list", "secure", "--tools", "--format", "json")
		requireSuccess(t, result, fixture.stderr.String())

		var records []listRecord
		if err := json.Unmarshal([]byte(result.stdout), &records); err != nil {
			t.Fatalf("decode catalog listing: %v\n%s", err, result.diagnostics(fixture.stderr.String()))
		}
		if len(records) != 1 {
			t.Fatalf("catalog record count = %d, want 1\n%s", len(records), result.diagnostics(fixture.stderr.String()))
		}
		record := records[0]
		if record.Name != "secure" || record.Transport != "http" || record.Endpoint != fixture.url {
			t.Fatalf("catalog server = %+v, want secure HTTP endpoint %s\n%s", record, fixture.url, result.diagnostics(fixture.stderr.String()))
		}
		if record.ToolCount != 1 || len(record.Tools) != 1 || record.Tools[0].Name != "echo" {
			t.Fatalf("catalog tools = %+v, want one echo tool\n%s", record.Tools, result.diagnostics(fixture.stderr.String()))
		}
		if len(record.Tools[0].InputSchema) == 0 {
			t.Fatalf("echo input schema is empty\n%s", result.diagnostics(fixture.stderr.String()))
		}
	}) {
		return
	}

	var onlineHelp string
	if !t.Run("generates typed flags from the cached schema", func(t *testing.T) {
		result := harness.runCaptain("mcp", "run", "secure", "echo", "--help")
		requireSuccess(t, result, fixture.stderr.String())
		onlineHelp = result.stdout
		for _, flag := range []string{"--message", "--count", "--uppercase", "--format"} {
			if !strings.Contains(result.stdout, flag) {
				t.Fatalf("generated help is missing %s\n%s", flag, result.diagnostics(fixture.stderr.String()))
			}
		}
	}) {
		return
	}

	if !t.Run("invokes echo with typed arguments", func(t *testing.T) {
		result := harness.runCaptain(
			"mcp", "run", "secure", "echo",
			"--message", "captain",
			"--count", "2",
			"--uppercase",
			"--format", "compact",
			"--json",
		)
		requireSuccess(t, result, fixture.stderr.String())
		requireNoSecrets(t, result)

		var call callToolResult
		if err := json.Unmarshal([]byte(result.stdout), &call); err != nil {
			t.Fatalf("decode MCP call result: %v\n%s", err, result.diagnostics(fixture.stderr.String()))
		}
		if call.IsError {
			t.Fatalf("MCP call returned isError=true\n%s", result.diagnostics(fixture.stderr.String()))
		}
		want := echoResult{
			Username:  fixtureUsername,
			Message:   "CAPTAIN",
			Count:     2,
			Uppercase: true,
			Format:    "compact",
			Output:    "CAPTAINCAPTAIN",
		}
		if call.StructuredContent != want {
			t.Fatalf("structured echo result = %+v, want %+v\n%s", call.StructuredContent, want, result.diagnostics(fixture.stderr.String()))
		}
		if len(call.Content) != 1 || call.Content[0].Type != "text" {
			t.Fatalf("MCP content = %+v, want one text item\n%s", call.Content, result.diagnostics(fixture.stderr.String()))
		}
		var text echoResult
		if err := json.Unmarshal([]byte(call.Content[0].Text), &text); err != nil {
			t.Fatalf("decode text echo result: %v\n%s", err, result.diagnostics(fixture.stderr.String()))
		}
		if text != want {
			t.Fatalf("text echo result = %+v, want %+v\n%s", text, want, result.diagnostics(fixture.stderr.String()))
		}
	}) {
		return
	}

	if err := fixture.stop(); err != nil {
		t.Fatalf("stop MCP fixture: %v\nfixture stderr:\n%s", err, fixture.stderr.String())
	}

	if !t.Run("records authentication and MCP methods", func(t *testing.T) {
		records := readAuditRecords(t, auditPath)
		rejected := false
		acceptedMethods := map[string]bool{}
		for _, record := range records {
			if record.Path != "/mcp" {
				t.Fatalf("audit path = %q, want /mcp", record.Path)
			}
			if record.Auth == "rejected" {
				rejected = true
				if record.Username != "" {
					t.Fatalf("rejected audit record contains username: %+v", record)
				}
				continue
			}
			if record.Auth != "accepted" {
				t.Fatalf("audit auth = %q, want accepted or rejected", record.Auth)
			}
			if record.HTTPMethod == "POST" {
				acceptedMethods[record.MCPMethod] = true
			}
			if record.Username != fixtureUsername {
				t.Fatalf("accepted audit username = %q, want %q", record.Username, fixtureUsername)
			}
		}
		if !rejected {
			t.Fatal("audit log has no rejected authentication request")
		}
		for _, method := range []string{"initialize", "tools/list", "tools/call"} {
			if !acceptedMethods[method] {
				t.Fatalf("audit log has no accepted %s request; accepted methods: %v", method, acceptedMethods)
			}
		}
	}) {
		return
	}

	t.Run("uses cached schema while the server is offline", func(t *testing.T) {
		result := harness.runCaptain("mcp", "run", "secure", "echo", "--help")
		requireSuccess(t, result, fixture.stderr.String())
		for _, flag := range []string{"--message", "--count", "--uppercase", "--format"} {
			if !strings.Contains(result.stdout, flag) {
				t.Fatalf("offline help is missing %s\n%s", flag, result.diagnostics(fixture.stderr.String()))
			}
		}
		if result.stdout != onlineHelp {
			t.Fatalf("offline help differs from online cached help\n%s", result.diagnostics(fixture.stderr.String()))
		}
	})
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("get working directory: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found above test working directory")
		}
		dir = parent
	}
}

func buildBinary(t *testing.T, root, destination, source string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), buildTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "go", "build", "-trimpath", "-o", destination, source)
	cmd.Dir = root
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if ctx.Err() != nil {
		err = ctx.Err()
	}
	if err != nil {
		t.Fatalf(
			"build %s\ncommand: go build -trimpath -o %s %s\nexit: %v\nstdout:\n%s\nstderr:\n%s",
			source, destination, source, err, stdout.String(), stderr.String(),
		)
	}
}

// startFixture owns the readiness handshake and guarantees process cleanup.
func startFixture(t *testing.T, fixtureBin, auditPath string) *fixtureProcess {
	t.Helper()
	stderr := &lockedBuffer{}
	cmd := exec.Command(fixtureBin)
	cmd.Env = replaceEnvironment(os.Environ(), map[string]string{
		"MCP_FIXTURE_USERNAME":  fixtureUsername,
		"MCP_FIXTURE_PASSWORD":  fixturePassword,
		"MCP_FIXTURE_AUDIT_LOG": auditPath,
	})
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("open fixture stdout: %v", err)
	}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start fixture: %v", err)
	}

	fixture := &fixtureProcess{
		cmd:    cmd,
		stderr: stderr,
		done:   make(chan error, 1),
	}
	go func() {
		fixture.done <- cmd.Wait()
	}()
	t.Cleanup(func() {
		if err := fixture.stop(); err != nil {
			t.Errorf("clean up MCP fixture: %v\nfixture stderr:\n%s", err, stderr.String())
		}
	})

	type readinessResult struct {
		url string
		err error
	}
	ready := make(chan readinessResult, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		if !scanner.Scan() {
			err := scanner.Err()
			if err == nil {
				err = errors.New("fixture closed stdout before readiness")
			}
			ready <- readinessResult{err: err}
			return
		}
		var message struct {
			URL string `json:"url"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &message); err != nil {
			ready <- readinessResult{err: fmt.Errorf("decode readiness JSON: %w", err)}
			return
		}
		parsed, err := url.Parse(message.URL)
		if err != nil || parsed.Scheme != "http" || parsed.Hostname() != "127.0.0.1" || parsed.Path != "/mcp" {
			ready <- readinessResult{err: fmt.Errorf("invalid fixture URL %q", message.URL)}
			return
		}
		ready <- readinessResult{url: message.URL}
	}()

	select {
	case result := <-ready:
		if result.err != nil {
			t.Fatalf("wait for fixture readiness: %v\nfixture stderr:\n%s", result.err, stderr.String())
		}
		fixture.url = result.url
	case err := <-fixture.done:
		t.Fatalf("fixture exited before readiness: %v\nfixture stderr:\n%s", err, stderr.String())
	case <-time.After(10 * time.Second):
		t.Fatalf("fixture readiness timed out\nfixture stderr:\n%s", stderr.String())
	}
	return fixture
}

func (f *fixtureProcess) stop() error {
	f.stopOnce.Do(func() {
		select {
		case err := <-f.done:
			f.stopErr = err
			return
		default:
		}

		if err := f.cmd.Process.Signal(os.Interrupt); err != nil && !errors.Is(err, os.ErrProcessDone) {
			f.stopErr = fmt.Errorf("signal fixture: %w", err)
			return
		}
		select {
		case err := <-f.done:
			f.stopErr = err
		case <-time.After(5 * time.Second):
			_ = f.cmd.Process.Kill()
			<-f.done
			f.stopErr = errors.New("fixture did not stop within 5s")
		}
	})
	return f.stopErr
}

func (h *testHarness) runCaptain(args ...string) commandResult {
	ctx, cancel := context.WithTimeout(h.ctx, commandTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, h.captainBin, args...)
	cmd.Dir = h.workspace
	cmd.Env = h.env
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if ctx.Err() != nil {
		err = ctx.Err()
	}
	return commandResult{
		path:    h.captainBin,
		args:    append([]string(nil), args...),
		stdout:  stdout.String(),
		stderr:  stderr.String(),
		err:     err,
		secrets: h.secrets,
	}
}

func requireSuccess(t *testing.T, result commandResult, fixtureStderr string) {
	t.Helper()
	if result.err != nil {
		t.Fatalf("Captain command failed\n%s", result.diagnostics(fixtureStderr))
	}
}

func requireNoSecrets(t *testing.T, result commandResult) {
	t.Helper()
	output := result.stdout + "\n" + result.stderr
	for _, secret := range result.secrets {
		if secret != "" && strings.Contains(output, secret) {
			t.Fatalf("Captain output contains Basic Auth credential material\n%s", result.diagnostics(""))
		}
	}
}

// diagnostics redacts credentials before rendering subprocess failures.
func (r commandResult) diagnostics(fixtureStderr string) string {
	args := make([]string, 0, len(r.args)+1)
	args = append(args, strconv.Quote(r.path))
	for _, arg := range r.args {
		args = append(args, strconv.Quote(redact(arg, r.secrets)))
	}
	exit := "0"
	if r.err != nil {
		exit = r.err.Error()
		var exitErr *exec.ExitError
		if errors.As(r.err, &exitErr) {
			exit = strconv.Itoa(exitErr.ExitCode())
		}
	}
	diagnostic := fmt.Sprintf(
		"command: %s\nexit: %s\nstdout:\n%s\nstderr:\n%s",
		strings.Join(args, " "),
		exit,
		redact(r.stdout, r.secrets),
		redact(r.stderr, r.secrets),
	)
	if fixtureStderr != "" {
		diagnostic += "\nfixture stderr:\n" + redact(fixtureStderr, r.secrets)
	}
	return diagnostic
}

func redact(value string, secrets []string) string {
	for _, secret := range secrets {
		if secret != "" {
			value = strings.ReplaceAll(value, secret, "[REDACTED]")
		}
	}
	return value
}

func replaceEnvironment(base []string, replacements map[string]string) []string {
	env := make([]string, 0, len(base)+len(replacements))
	for _, entry := range base {
		name, _, found := strings.Cut(entry, "=")
		if _, replace := replacements[name]; found && replace {
			continue
		}
		env = append(env, entry)
	}
	for name, value := range replacements {
		env = append(env, name+"="+value)
	}
	return env
}

func readAuditRecords(t *testing.T, path string) []auditRecord {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("open MCP audit log: %v", err)
	}
	defer file.Close()

	var records []auditRecord
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		var record auditRecord
		if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
			t.Fatalf("decode MCP audit record %d: %v", len(records)+1, err)
		}
		records = append(records, record)
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("read MCP audit log: %v", err)
	}
	if len(records) == 0 {
		t.Fatal("MCP audit log is empty")
	}
	return records
}

func (b *lockedBuffer) Write(data []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(data)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
