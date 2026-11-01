package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/flanksource/captain/pkg/adapters"
	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
)

func registerAdapterSchemaHandlers(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/captain/ai/adapter-schemas", handleAdapterSchemas())
	mux.HandleFunc("POST /api/captain/ai/adapter-schemas/validate", handleAdapterSchemaValidation)
}

func handleAdapterSchemaValidation(w http.ResponseWriter, r *http.Request) {
	var request struct {
		ID    string          `json:"id"`
		Value json.RawMessage `json:"value"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		http.Error(w, fmt.Sprintf("invalid validation request: %v", err), http.StatusBadRequest)
		return
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		http.Error(w, "validation request must contain a single JSON document", http.StatusBadRequest)
		return
	}
	if request.ID == "" {
		http.Error(w, "id is required", http.StatusBadRequest)
		return
	}
	if len(request.Value) == 0 {
		http.Error(w, "value is required", http.StatusBadRequest)
		return
	}
	value, err := jsonschema.UnmarshalJSON(bytes.NewReader(request.Value))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	result, err := adapters.ValidateResource(request.ID, value)
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, adapters.ErrUnknownSchema) {
			status = http.StatusNotFound
		}
		http.Error(w, err.Error(), status)
		return
	}
	writeServeJSON(w, http.StatusOK, result)
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
