package prompt

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// FanOut is the command and concurrency for a prompt's session-per-item run.
type FanOut struct {
	Exec        string `json:"exec" yaml:"exec"`
	Concurrency int    `json:"concurrency,omitempty" yaml:"concurrency,omitempty"`
}

func decodePromptFanOut(raw map[string]any) (*FanOut, error) {
	value, ok := raw["fanout"]
	if !ok {
		return nil, nil
	}
	if _, ok := value.(map[string]any); !ok {
		return nil, fmt.Errorf("fanout must be a mapping")
	}
	data, err := yaml.Marshal(value)
	if err != nil {
		return nil, err
	}
	var fanOut FanOut
	if err := validateDeclarationFields(data, fanOut); err != nil {
		return nil, fmt.Errorf("decode fanout: %w", err)
	}
	if err := yaml.Unmarshal(data, &fanOut); err != nil {
		return nil, fmt.Errorf("decode fanout: %w", err)
	}
	if strings.TrimSpace(fanOut.Exec) == "" {
		return nil, fmt.Errorf("fanout.exec must be a non-empty script")
	}
	if fanOut.Concurrency < 0 {
		return nil, fmt.Errorf("fanout.concurrency must not be negative")
	}
	return &fanOut, nil
}
