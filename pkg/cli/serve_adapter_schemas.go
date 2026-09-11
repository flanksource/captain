package cli

import (
	"net/http"

	"github.com/flanksource/captain/pkg/adapters"
)

func registerAdapterSchemaHandlers(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/captain/ai/adapter-schemas", handleAdapterSchemas())
}

func handleAdapterSchemas() http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		documents, err := adapters.Schemas()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeServeJSON(w, http.StatusOK, documents)
	}
}
