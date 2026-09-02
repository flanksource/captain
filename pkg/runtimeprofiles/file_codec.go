package runtimeprofiles

import (
	"bytes"
	"errors"
	"fmt"
	"io"

	"gopkg.in/yaml.v3"
)

// decodeFile reads one record file strictly: an unknown key is an error, and an
// id key is refused because the id is derived from the file name.
func decodeFile[I any](path string, data []byte) (I, error) {
	var in I
	var probe struct {
		ID any `yaml:"id"`
	}
	if err := yaml.Unmarshal(data, &probe); err != nil {
		return in, fmt.Errorf("%w: %s: %v", ErrInvalid, path, err)
	}
	if probe.ID != nil {
		return in, fmt.Errorf("%w: %s declares an id, but the id is derived from the file name", ErrInvalid, path)
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&in); err != nil {
		if errors.Is(err, io.EOF) {
			return in, fmt.Errorf("%w: %s is empty", ErrInvalid, path)
		}
		return in, fmt.Errorf("%w: %s: %v", ErrInvalid, path, err)
	}
	return in, nil
}

func encodeFile(in any) ([]byte, error) {
	var buffer bytes.Buffer
	encoder := yaml.NewEncoder(&buffer)
	encoder.SetIndent(2)
	if err := encoder.Encode(in); err != nil {
		return nil, err
	}
	if err := encoder.Close(); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}
