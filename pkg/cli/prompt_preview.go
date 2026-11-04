package cli

import "encoding/json"

// Preview returns an independent presentation snapshot of the prepared render.
func (p *PreparedPrompt) Preview() (PromptRenderResult, error) {
	data, err := json.Marshal(p.rendered)
	if err != nil {
		return PromptRenderResult{}, err
	}
	var view PromptRenderResult
	err = json.Unmarshal(data, &view)
	return view, err
}
