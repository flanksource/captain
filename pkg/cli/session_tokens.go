package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/database"
	sessiontokens "github.com/flanksource/captain/pkg/session/tokens"
)

type SessionTokensOptions struct {
	ID     string   `args:"true" help:"Captain session ID or provider session prefix"`
	Method string   `flag:"method" default:"estimate" help:"estimate (offline) or provider (count endpoint only)"`
	RowIDs []string `flag:"row-id" help:"Canonical transcript row ID; repeatable, omitted means all rows"`
}

func RunSessionTokens(ctx context.Context, opts SessionTokensOptions) (sessiontokens.Result, error) {
	return sizeSessionTokens(ctx, opts.ID, sessiontokens.Options{Method: opts.Method, RowIDs: opts.RowIDs})
}

func sizeSessionTokens(ctx context.Context, id string, opts sessiontokens.Options) (sessiontokens.Result, error) {
	result, err := RunSessionGet(ctx, SessionGetOptions{ID: id, Limit: 0})
	if err != nil {
		return sessiontokens.Result{}, err
	}
	item, _, err := primarySessionItem(id, result)
	if err != nil {
		return sessiontokens.Result{}, err
	}
	if item.Detail == nil {
		return sessiontokens.Result{}, fmt.Errorf("%w: session has no transcript", database.ErrSessionNotFound)
	}
	return sessiontokens.Size(ctx, item.Detail, opts)
}

func handleSessionTokens(w http.ResponseWriter, r *http.Request) {
	var opts sessiontokens.Options
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&opts); err != nil {
		writeSessionError(w, http.StatusBadRequest, err.Error(), nil)
		return
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		writeSessionError(w, http.StatusBadRequest, "body must contain one JSON object", nil)
		return
	}
	if opts.Method != "estimate" && opts.Method != "provider" {
		writeSessionError(w, http.StatusBadRequest, "method must be estimate or provider", nil)
		return
	}
	result, err := sizeSessionTokens(r.Context(), r.PathValue("id"), opts)
	if err != nil {
		status := sessionErrorStatus(err)
		if errors.Is(err, sessiontokens.ErrInvalidRequest) {
			status = http.StatusBadRequest
		}
		if errors.Is(err, sessiontokens.ErrRevisionConflict) {
			status = http.StatusConflict
		}
		if errors.Is(err, api.ErrTokenCountingUnsupported) {
			status = http.StatusUnprocessableEntity
		}
		writeSessionError(w, status, err.Error(), nil)
		return
	}
	writeChatJSON(w, http.StatusOK, result)
}
