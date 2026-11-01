package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/flanksource/captain/pkg/api"
)

var ErrPromptVersion = errors.New("prompt version changed")

type promptDirectoryKey struct{}

type PreparePromptOptions struct {
	Reference string
	Version   string
	Directory string
	Request   PromptRenderRequest
	Tools     []api.ToolDefinition
}

type PreparedPrompt struct {
	rendered PromptRenderResult
	detail   PromptDetail
}

func (p *PreparedPrompt) Detail() PromptDetail { return p.detail }

func PromptContext(ctx context.Context, directory string) (context.Context, error) {
	directory, err := filepath.Abs(directory)
	if err != nil {
		return nil, err
	}
	stat, err := os.Stat(directory)
	if err != nil {
		return nil, err
	}
	if !stat.IsDir() {
		return nil, fmt.Errorf("prompt workspace %s is not a directory", directory)
	}
	return context.WithValue(ctx, promptDirectoryKey{}, directory), nil
}

func promptDirectory(ctx context.Context) (string, error) {
	if directory, ok := ctx.Value(promptDirectoryKey{}).(string); ok {
		return directory, nil
	}
	return os.Getwd()
}

func DiscoverPrompts(ctx context.Context, directory string, options PromptListOptions) ([]PromptSummary, error) {
	ctx, err := PromptContext(ctx, directory)
	if err != nil {
		return nil, err
	}
	return listPrompts(ctx, options)
}

func InspectPrompt(ctx context.Context, directory, reference string) (PromptDetail, error) {
	ctx, err := PromptContext(ctx, directory)
	if err != nil {
		return PromptDetail{}, err
	}
	return getPrompt(ctx, reference)
}

func PreparePrompt(ctx context.Context, options PreparePromptOptions) (*PreparedPrompt, error) {
	ctx, err := PromptContext(ctx, options.Directory)
	if err != nil {
		return nil, err
	}
	detail, err := getPrompt(ctx, options.Reference)
	if err != nil {
		return nil, err
	}
	if detail.Version != options.Version {
		return nil, fmt.Errorf("%w: %s", ErrPromptVersion, options.Reference)
	}
	if detail.ParseError != "" {
		return nil, errors.New(detail.ParseError)
	}
	request := options.Request
	request.Variables, err = validatePromptVariables(detail, request.Variables)
	if err != nil {
		return nil, err
	}
	request.Content, request.Directory = detail.Content, options.Directory
	rendered, err := renderPrompt(ctx, detail.ID, request)
	if err != nil {
		return nil, err
	}
	rendered, err = rendered.withCallerTools(options.Tools)
	if err != nil {
		return nil, err
	}
	if rendered.ValidationError != "" {
		return nil, errors.New(rendered.ValidationError)
	}
	return &PreparedPrompt{rendered: rendered, detail: detail}, nil
}
