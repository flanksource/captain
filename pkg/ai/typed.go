package ai

import (
	"context"
	"encoding/json"
	"fmt"
)

func ExecuteTyped[T any](ctx context.Context, p Provider, req Request) (*T, *Response, error) {
	var zero T
	req.StructuredOutput = &zero

	resp, err := p.Execute(ctx, req)
	if err != nil {
		return nil, resp, err
	}

	if resp.StructuredData != nil {
		if typed, ok := resp.StructuredData.(*T); ok {
			return typed, resp, nil
		}
	}

	var result T
	if err := json.Unmarshal([]byte(resp.Text), &result); err != nil {
		return nil, resp, fmt.Errorf("failed to unmarshal response into %T: %w", zero, err)
	}

	return &result, resp, nil
}
