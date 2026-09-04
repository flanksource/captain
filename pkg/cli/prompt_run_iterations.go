package cli

import (
	"encoding/json"

	"github.com/flanksource/captain/pkg/api"
)

// resultJSONWithVerify puts the run's final report on result_json under
// `verify`, beside the prompt's own structured output. It copies rather than
// mutating: the same map is the CLI summary's StructuredOutput, which is the
// prompt's answer and nothing else.
//
// The per-turn rows that accompany it come from promptrun.IterationRecords —
// the one derivation every host shares.
func resultJSONWithVerify(structured map[string]any, report *api.VerifyReport) map[string]any {
	if report == nil {
		return structured
	}
	raw, err := json.Marshal(report)
	if err != nil {
		log.Errorf("prompt run result_json: encoding the verify report of %q failed: %v", report.Name, err)
		return structured
	}
	var encoded map[string]any
	if err := json.Unmarshal(raw, &encoded); err != nil {
		log.Errorf("prompt run result_json: decoding the verify report of %q failed: %v", report.Name, err)
		return structured
	}
	merged := make(map[string]any, len(structured)+1)
	for key, value := range structured {
		merged[key] = value
	}
	merged["verify"] = encoded
	return merged
}
