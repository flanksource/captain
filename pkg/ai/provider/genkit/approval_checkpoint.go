package genkit

import (
	"encoding/base64"
	"encoding/json"
	"fmt"

	"github.com/flanksource/captain/pkg/api"

	gkai "github.com/firebase/genkit/go/ai"
)

const (
	genkitApprovalCheckpointCodec   = "genkit-messages-json"
	genkitApprovalCheckpointVersion = 1
	checkpointBytesKey              = "$captainBytes"
)

func encodeToolApprovalCheckpoint(response *gkai.ModelResponse) (*api.ProviderCheckpoint, error) {
	if response == nil || response.Request == nil || response.Message == nil {
		return nil, fmt.Errorf("genkit approval checkpoint requires the model request and response message")
	}
	messages := cloneCheckpointMessages(response.Request.Messages)
	messages = append(messages, response.Message.Clone())
	encodeCheckpointMetadata(messages)
	payload, err := json.Marshal(messages)
	if err != nil {
		return nil, fmt.Errorf("encode genkit approval checkpoint: %w", err)
	}
	return &api.ProviderCheckpoint{
		Codec: genkitApprovalCheckpointCodec, Version: genkitApprovalCheckpointVersion, Payload: payload,
	}, nil
}

func decodeToolApprovalCheckpoint(checkpoint *api.ProviderCheckpoint) ([]*gkai.Message, error) {
	if checkpoint == nil {
		return nil, fmt.Errorf("genkit approval checkpoint is missing")
	}
	if checkpoint.Codec != genkitApprovalCheckpointCodec || checkpoint.Version != genkitApprovalCheckpointVersion {
		return nil, fmt.Errorf("unsupported genkit approval checkpoint %q version %d", checkpoint.Codec, checkpoint.Version)
	}
	var messages []*gkai.Message
	if err := json.Unmarshal(checkpoint.Payload, &messages); err != nil {
		return nil, fmt.Errorf("decode genkit approval checkpoint: %w", err)
	}
	if len(messages) == 0 {
		return nil, fmt.Errorf("genkit approval checkpoint has no messages")
	}
	if err := decodeCheckpointMetadata(messages); err != nil {
		return nil, err
	}
	return messages, nil
}

func cloneCheckpointMessages(messages []*gkai.Message) []*gkai.Message {
	cloned := make([]*gkai.Message, len(messages))
	for i, message := range messages {
		cloned[i] = message.Clone()
	}
	return cloned
}

func encodeCheckpointMetadata(messages []*gkai.Message) {
	for _, message := range messages {
		message.Metadata = encodeCheckpointMap(message.Metadata)
		for _, part := range message.Content {
			part.Metadata = encodeCheckpointMap(part.Metadata)
		}
	}
}

func encodeCheckpointMap(values map[string]any) map[string]any {
	for key, value := range values {
		values[key] = encodeCheckpointValue(value)
	}
	return values
}

func encodeCheckpointValue(value any) any {
	switch typed := value.(type) {
	case []byte:
		return map[string]any{checkpointBytesKey: base64.StdEncoding.EncodeToString(typed)}
	case map[string]any:
		return encodeCheckpointMap(typed)
	case []any:
		for i := range typed {
			typed[i] = encodeCheckpointValue(typed[i])
		}
		return typed
	default:
		return value
	}
}

func decodeCheckpointMetadata(messages []*gkai.Message) error {
	for _, message := range messages {
		if err := decodeCheckpointMap(message.Metadata); err != nil {
			return err
		}
		for _, part := range message.Content {
			if err := decodeCheckpointMap(part.Metadata); err != nil {
				return err
			}
		}
	}
	return nil
}

func decodeCheckpointMap(values map[string]any) error {
	for key, value := range values {
		decoded, err := decodeCheckpointValue(value)
		if err != nil {
			return fmt.Errorf("decode genkit checkpoint metadata %q: %w", key, err)
		}
		values[key] = decoded
	}
	return nil
}

func decodeCheckpointValue(value any) (any, error) {
	switch typed := value.(type) {
	case map[string]any:
		if encoded, ok := typed[checkpointBytesKey]; ok {
			if len(typed) != 1 {
				return nil, fmt.Errorf("byte envelope has unexpected fields")
			}
			text, ok := encoded.(string)
			if !ok {
				return nil, fmt.Errorf("byte envelope payload is %T", encoded)
			}
			decoded, err := base64.StdEncoding.DecodeString(text)
			if err != nil {
				return nil, fmt.Errorf("invalid byte envelope: %w", err)
			}
			return decoded, nil
		}
		if err := decodeCheckpointMap(typed); err != nil {
			return nil, err
		}
		return typed, nil
	case []any:
		for i := range typed {
			decoded, err := decodeCheckpointValue(typed[i])
			if err != nil {
				return nil, err
			}
			typed[i] = decoded
		}
		return typed, nil
	default:
		return value, nil
	}
}
