// Package bash is the provider of the bash model, which is no model: it runs the prompt as a bash
// command in the run's directory, streams its output as the answer, and reports its exit code.
package bash

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"github.com/flanksource/captain/pkg/ai"
)

// Provider runs prompts as bash commands.
type Provider struct {
	model string
}

// New is the bash provider for cfg's model.
func New(cfg ai.Config) (*Provider, error) {
	return &Provider{model: cfg.Model.Name}, nil
}

// Outcome is a command's structured output.
type Outcome struct {
	ExitCode int `json:"exitCode"`
}

func (p *Provider) GetModel() string { return p.model }

func (p *Provider) GetRuntime() ai.Runtime { return ai.RuntimeOf(ai.Bash, ai.ModeAPI) }

// Execute runs req's prompt and answers with its output, stdout and stderr interleaved as written,
// and its Outcome as the structured output.
func (p *Provider) Execute(ctx context.Context, req ai.Request) (*ai.Response, error) {
	events, err := p.ExecuteStream(ctx, req)
	if err != nil {
		return nil, err
	}
	response := &ai.Response{Model: p.model, Runtime: p.GetRuntime()}
	var text strings.Builder
	for event := range events {
		switch event.Kind {
		case ai.EventText:
			text.WriteString(event.Text)
		case ai.EventResult:
			response.StructuredData = event.StructuredData
		case ai.EventError:
			return nil, errors.New(event.Error)
		}
	}
	response.Text = text.String()
	return response, nil
}

// killWait is how long a cancelled command's output may stay open once its process group is
// killed.
const killWait = 10 * time.Second

// ExecuteStream runs req's prompt in req's directory, sending each line of its output as it is
// written, then a result that succeeds when the command exits 0 and carries its Outcome. A run
// that is cancelled kills the command's process group and ends with an error.
func (p *Provider) ExecuteStream(ctx context.Context, req ai.Request) (<-chan ai.Event, error) {
	command := req.Prompt.User
	if strings.TrimSpace(command) == "" {
		return nil, errors.New("bash runs the prompt as a command, and the prompt is empty")
	}
	reader, writer, err := os.Pipe()
	if err != nil {
		return nil, fmt.Errorf("bash: %w", err)
	}
	cmd := exec.CommandContext(ctx, "bash", "-c", command)
	cmd.Dir = req.Cwd()
	cmd.Stdout, cmd.Stderr = writer, writer
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = killWait
	err = cmd.Start()
	_ = writer.Close()
	if err != nil {
		_ = reader.Close()
		return nil, fmt.Errorf("bash: %w", err)
	}
	events := make(chan ai.Event)
	go func() {
		defer close(events)
		defer func() { _ = reader.Close() }()
		lines := bufio.NewReader(reader)
		for {
			line, readErr := lines.ReadString('\n')
			if line != "" {
				events <- ai.Event{Kind: ai.EventText, Text: line, Model: p.model}
			}
			if readErr != nil {
				if !errors.Is(readErr, io.EOF) {
					events <- ai.Event{Kind: ai.EventError, Error: fmt.Sprintf("bash: read the command's output: %v", readErr), Model: p.model}
				}
				break
			}
		}
		events <- p.result(ctx, cmd.Wait())
	}()
	return events, nil
}

// result is the event a command that ended with waitErr ends its stream with.
func (p *Provider) result(ctx context.Context, waitErr error) ai.Event {
	if ctx.Err() != nil {
		return ai.Event{Kind: ai.EventError, Error: fmt.Sprintf("bash: the command stopped: %v", context.Cause(ctx)), Model: p.model}
	}
	code := 0
	var exitErr *exec.ExitError
	if errors.As(waitErr, &exitErr) {
		code = exitErr.ExitCode()
	} else if waitErr != nil {
		return ai.Event{Kind: ai.EventError, Error: fmt.Sprintf("bash: %v", waitErr), Model: p.model}
	}
	outcome, err := json.Marshal(Outcome{ExitCode: code})
	if err != nil {
		return ai.Event{Kind: ai.EventError, Error: fmt.Sprintf("bash: encode the command's outcome: %v", err), Model: p.model}
	}
	return ai.Event{Kind: ai.EventResult, Success: code == 0, StructuredData: outcome, Model: p.model}
}
