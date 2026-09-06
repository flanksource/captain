package aichat

import (
	"encoding/json"

	"github.com/flanksource/captain/pkg/api"
)

var chatSettingFields = map[string]string{
	"model": "model", "reasoningEffort": "effort", "temperature": "temperature",
	"budget": "budget", "toolPreferences": "toolPreferences",
}

func (r ChatRequest) WithExplicit(paths ...string) ChatRequest {
	r.Explicit = (api.Spec{Explicit: r.Explicit}).WithExplicit(paths...).Explicit
	return r
}

func (r *ChatRequest) captureSettings(fields map[string]json.RawMessage) error {
	projected := map[string]json.RawMessage{}
	for wire, field := range chatSettingFields {
		if value, present := fields[wire]; present {
			projected[field] = value
		}
	}
	if mode, present := fields["permissionMode"]; present {
		value, err := json.Marshal(map[string]json.RawMessage{"mode": mode})
		if err != nil {
			return err
		}
		projected["permissions"] = value
	}
	data, err := json.Marshal(projected)
	if err != nil {
		return err
	}
	var settings api.Spec
	if err := json.Unmarshal(data, &settings); err != nil {
		return err
	}
	r.Explicit = settings.Explicit
	return nil
}

func (r ChatRequest) MarshalJSON() ([]byte, error) {
	type wireChatRequest ChatRequest
	data, err := json.Marshal(wireChatRequest(r))
	if err != nil {
		return nil, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return nil, err
	}
	settings := api.Spec{Explicit: r.Explicit, Model: api.Model{Name: r.Model, Effort: r.ReasoningEffort, Temperature: r.Temperature},
		Budget: r.Budget, ToolPreferences: r.ToolPreferences, Permissions: api.Permissions{Mode: r.PermissionMode},
	}
	data, err = json.Marshal(settings)
	if err != nil {
		return nil, err
	}
	var projected map[string]json.RawMessage
	if err := json.Unmarshal(data, &projected); err != nil {
		return nil, err
	}
	for wire, field := range chatSettingFields {
		delete(fields, wire)
		if value, present := projected[field]; present {
			fields[wire] = value
		}
	}
	if r.Explicit.Has("/permissions/mode") {
		fields["permissionMode"], err = json.Marshal(r.PermissionMode)
		if err != nil {
			return nil, err
		}
	}
	return json.Marshal(fields)
}
