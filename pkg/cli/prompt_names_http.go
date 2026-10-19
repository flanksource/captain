package cli

import (
	"net/http"
)

func handleNamedPrompts(w http.ResponseWriter, r *http.Request) {
	db, err := captainDB(r.Context())
	if err != nil {
		writeSessionError(w, serveRunStatus(err, http.StatusInternalServerError), err.Error(), nil)
		return
	}
	names, err := db.ListNamedPrompts(r.Context())
	if err != nil {
		writeSessionError(w, http.StatusInternalServerError, err.Error(), nil)
		return
	}
	writeServeJSON(w, http.StatusOK, names)
}
