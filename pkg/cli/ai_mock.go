// ABOUTME: CLI entrypoint for `captain ai mock`.
// ABOUTME: Serves scripted OpenAI and/or Anthropic replies so agent runs spend no tokens.

package cli

import (
	"context"
	"fmt"
	"net"
	"os"
	"sort"
	"strings"

	"github.com/flanksource/captain/pkg/aimock"
	"github.com/flanksource/captain/pkg/aimock/anthropicmock"
	"github.com/flanksource/captain/pkg/aimock/openaimock"
)

type AIMockOptions struct {
	Scenario string `flag:"scenario" short:"s" help:"YAML scenario file of scripted replies" required:"true"`
	Only     string `flag:"only" help:"Serve a single protocol: anthropic or openai. Default serves both"`
	Addr     string `flag:"addr" help:"Listen address for the anthropic server (host:port); empty picks a free loopback port"`
	OpenAddr string `flag:"openai-addr" help:"Listen address for the openai server (host:port); empty picks a free loopback port"`
	Journal  string `flag:"journal" help:"Mirror every served request to this JSONL file"`
	Env      bool   `flag:"env" help:"Print export lines for --addr/--openai-addr and exit instead of serving"`
	Lenient  bool   `flag:"lenient" help:"Answer unmatched requests with a bland reply instead of failing"`
}

// AIMockResult reports where the servers listen and how to point a client at
// them. It is the JSON/pretty payload; --env prints the shell form instead.
type AIMockResult struct {
	Scenario  string   `json:"scenario" pretty:"label=Scenario"`
	Anthropic string   `json:"anthropic,omitempty" pretty:"label=Anthropic"`
	OpenAI    string   `json:"openai,omitempty" pretty:"label=OpenAI"`
	Env       []string `json:"env" pretty:"label=Environment"`
}

const (
	mockOnlyAnthropic = "anthropic"
	mockOnlyOpenAI    = "openai"
)

// RunAIMock serves the scenario until the context is cancelled. Both servers run
// by default so a scenario carrying both sections can drive a claude and a codex
// run from one process.
func RunAIMock(ctx context.Context, opts AIMockOptions) (any, error) {
	only := strings.ToLower(strings.TrimSpace(opts.Only))
	switch only {
	case "", mockOnlyAnthropic, mockOnlyOpenAI:
	default:
		return nil, fmt.Errorf("--only must be %q or %q, got %q", mockOnlyAnthropic, mockOnlyOpenAI, opts.Only)
	}

	scenario, err := aimock.Load(opts.Scenario)
	if err != nil {
		return nil, err
	}

	if opts.Env {
		env, err := mockEnv(opts, only)
		if err != nil {
			return nil, err
		}
		fmt.Fprint(os.Stdout, aimock.ExportLines(env))
		return nil, nil
	}

	result := AIMockResult{Scenario: scenario.Name}
	var servers []aimock.Server

	if only != mockOnlyOpenAI {
		srv, err := anthropicmock.Start(anthropicmock.Options{
			Scenario:    scenario,
			Addr:        opts.Addr,
			JournalPath: journalPath(opts.Journal, mockOnlyAnthropic, only),
			Lenient:     opts.Lenient,
		})
		if err != nil {
			return nil, err
		}
		defer srv.Close()
		servers = append(servers, srv)
		result.Anthropic = srv.APIURL()
	}

	if only != mockOnlyAnthropic {
		srv, err := openaimock.Start(openaimock.Options{
			Scenario:    scenario,
			Addr:        opts.OpenAddr,
			JournalPath: journalPath(opts.Journal, mockOnlyOpenAI, only),
			Lenient:     opts.Lenient,
		})
		if err != nil {
			return nil, err
		}
		defer srv.Close()
		servers = append(servers, srv)
		result.OpenAI = srv.APIURL()
	}

	result.Env = aimock.MergeEnv(servers...)
	fmt.Fprintf(os.Stderr, "captain ai mock: serving %q; Ctrl-C to stop\n", scenario.Name)
	fmt.Fprint(os.Stderr, aimock.ExportLines(result.Env))
	<-ctx.Done()

	return result, nil
}

// journalPath keeps the two servers from interleaving into one file when both
// are serving; a single-protocol run writes exactly the path the user asked for.
func journalPath(path, protocol, only string) string {
	if path == "" || only != "" {
		return path
	}
	return strings.TrimSuffix(path, ".jsonl") + "." + protocol + ".jsonl"
}

// mockEnv renders the client environment for the configured addresses without
// binding them, so `eval "$(captain ai mock --env …)"` describes the server the
// user starts separately. A default port would exit with the command and leave
// the exported URLs pointing at nothing, so --env insists on explicit addresses.
func mockEnv(opts AIMockOptions, only string) ([]string, error) {
	var env []string
	if only != mockOnlyOpenAI {
		rootURL, err := mockRootURL(opts.Addr, "--addr")
		if err != nil {
			return nil, err
		}
		env = append(env, anthropicmock.Env(rootURL)...)
	}
	if only != mockOnlyAnthropic {
		rootURL, err := mockRootURL(opts.OpenAddr, "--openai-addr")
		if err != nil {
			return nil, err
		}
		env = append(env, openaimock.Env(rootURL)...)
	}
	sort.Strings(env)
	return env, nil
}

// mockRootURL renders a listen address as the URL a client should call.
func mockRootURL(addr, flag string) (string, error) {
	if strings.TrimSpace(addr) == "" {
		return "", fmt.Errorf("--env needs %s: an ephemeral port would stop listening the moment this command exits", flag)
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "", fmt.Errorf("%s %q is not a host:port address: %w", flag, addr, err)
	}
	if host == "" {
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port), nil
}
