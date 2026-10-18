package jev

import (
	"fmt"
	"io"
	"time"

	"github.com/flanksource/captain/pkg/ai"
	native "github.com/flanksource/captain/pkg/ai/jev"
)

type jevResult struct {
	raw      []byte
	decoded  native.Response
	usage    ai.Usage
	duration time.Duration
}

func decodeResponse(body io.Reader, request native.Request) (*jevResult, error) {
	raw, err := io.ReadAll(io.LimitReader(body, 1<<20+1))
	if err != nil {
		return nil, fmt.Errorf("jev read response: %w", err)
	}
	if len(raw) > 1<<20 {
		return nil, fmt.Errorf("jev response exceeds 1 MiB")
	}
	decoded, _, err := native.ParseResponse(raw, request)
	if err != nil {
		return nil, err
	}
	return &jevResult{raw: raw, decoded: decoded, usage: ai.Usage{InputTokens: *decoded.Usage.InputTokens, OutputTokens: *decoded.Usage.OutputTokens}}, nil
}
