package api

import (
	"encoding/json"
	"fmt"

	"gopkg.in/yaml.v3"
)

// Batch splits a run's input files into runs of Size files each, Concurrency
// of them at a time (zero means one). It is written `batch: 1` for the size
// alone or `batch: {size: 1, concurrency: 4}`.
type Batch struct {
	Size        int `json:"size,omitempty" yaml:"size,omitempty"`
	Concurrency int `json:"concurrency,omitempty" yaml:"concurrency,omitempty"`
}

type batchFields Batch

func (b *Batch) UnmarshalJSON(data []byte) error {
	var size int
	if err := json.Unmarshal(data, &size); err == nil {
		*b = Batch{Size: size}
		return nil
	}
	return json.Unmarshal(data, (*batchFields)(b))
}

func (b *Batch) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind == yaml.ScalarNode {
		*b = Batch{}
		return node.Decode(&b.Size)
	}
	return node.Decode((*batchFields)(b))
}

// Validate refuses a batch that cannot split anything.
func (b Batch) Validate() error {
	if b.Size < 1 {
		return fmt.Errorf("batch.size must be at least 1, got %d", b.Size)
	}
	if b.Concurrency < 0 {
		return fmt.Errorf("batch.concurrency must not be negative, got %d", b.Concurrency)
	}
	return nil
}

// Workers is how many batched runs execute at once.
func (b Batch) Workers() int { return max(b.Concurrency, 1) }
