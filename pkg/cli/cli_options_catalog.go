package cli

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/clicky/rpc"
)

// handleCLIOptionsCatalog serves the JsonSchemaForm schema for a cmux backend's
// "extra cmux args" (ClaudeCmuxOptions / CodexCmuxOptions), reflected from the Go
// option structs via clicky's rpc.SchemaForStruct. It fails loud (400) on any
// backend that has no cmux CLI options.
func handleCLIOptionsCatalog() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		backend := api.Backend(strings.TrimSpace(r.URL.Query().Get("backend")))
		opts, err := api.CLIOptionsFor(backend)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(rpc.SchemaForStruct(opts)); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	}
}
