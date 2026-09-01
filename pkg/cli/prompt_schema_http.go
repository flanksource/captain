package cli

import (
	"encoding/json"
	"net/http"
)

// handlePromptSchema serves the prompt/spec editor schema document
// (PromptSchemaDocument): the reflected spec/prompt schemas plus the dynamic
// backends[] catalog (each with its per-backend cliArgs schema and available
// models) and the if/then conditionals. The webapp SpecEditor drives its
// per-backend CLI-arg options from this one cached document instead of
// re-fetching a flat schema per backend.
func handlePromptSchema() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		doc, err := PromptSchemaDocument(r.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(doc); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	}
}
