package cli

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/flanksource/captain/pkg/session/deliver"
	sessionprocess "github.com/flanksource/captain/pkg/session/process"
	clickyprocess "github.com/flanksource/clicky/process"
)

type SessionDeliverOptions struct {
	ID   string `args:"true" help:"Captain session ID or provider session ID of a live session"`
	Text string `flag:"text" help:"Message to deliver into the session"`
}

// SessionDeliverRequest is the body of POST /api/captain/sessions/{id}/deliver.
type SessionDeliverRequest struct {
	Text string `json:"text"`
}

var (
	readProcessEnvironment = clickyprocess.Environment
	sessionDeliverer       = deliver.Deliverer{}
)

// RunSessionDeliver sends text into a live claude or codex session that captain
// did not launch, through its messaging socket, codex queue or cmux pane (see
// deliver.ChooseChannel). Unlike POST /{id}/message it never resumes the session
// in a second process: a session that is not live is a 409.
func RunSessionDeliver(ctx context.Context, opts SessionDeliverOptions) (deliver.Result, error) {
	if strings.TrimSpace(opts.Text) == "" {
		return deliver.Result{}, newChatError(http.StatusBadRequest, "message text is required")
	}
	target, err := liveDeliveryTarget(ctx, opts.ID)
	if err != nil {
		return deliver.Result{}, err
	}
	if _, err := deliver.ChooseChannel(target); err != nil {
		return deliver.Result{}, newChatError(http.StatusUnprocessableEntity, err.Error())
	}
	result, err := sessionDeliverer.Deliver(ctx, target, opts.Text)
	if err != nil {
		return deliver.Result{}, newChatError(http.StatusBadGateway, err.Error())
	}
	return result, nil
}

// liveDeliveryTarget finds the session's live process and re-reads its
// environment now, so the target reflects the process as it is rather than the
// last sample, and a PID since reused by another session is refused.
func liveDeliveryTarget(ctx context.Context, id string) (deliver.Target, error) {
	item, err := resolveSessionItem(ctx, id)
	if err != nil {
		return deliver.Target{}, err
	}
	live, err := RunSessionLive(ctx, SessionLiveOptions{Source: "all", All: true, Full: true})
	if err != nil {
		return deliver.Target{}, err
	}
	var record *SessionRecord
	for i := range live.Sessions {
		candidate := &live.Sessions[i]
		if candidate.Live != nil && candidate.Live.PID > 0 &&
			(candidate.ID == item.CaptainID || (item.ProviderSessionID != "" && candidate.Live.SessionID == item.ProviderSessionID)) {
			record = candidate
			break
		}
	}
	if record == nil {
		return deliver.Target{}, newChatError(http.StatusConflict, fmt.Sprintf("session %s has no live process", id))
	}
	env, err := readProcessEnvironment(ctx, record.Live.PID, clickyprocess.EnvironmentOptions{Keys: deliver.MessagingKeys})
	if err != nil {
		return deliver.Target{}, newChatError(http.StatusConflict,
			fmt.Sprintf("session %s's process %d is no longer readable: %v", id, record.Live.PID, err))
	}
	if _, declared := sessionprocess.IdentityFromEnvironment(env); declared != "" && declared != item.ProviderSessionID {
		return deliver.Target{}, newChatError(http.StatusConflict,
			fmt.Sprintf("process %d now runs session %s, not %s", record.Live.PID, declared, item.ProviderSessionID))
	}
	return deliver.Target{
		Source: record.Source, SessionID: item.ProviderSessionID, SessionFile: record.Live.SessionFile,
		Environment: env, Surface: record.Live.Surface,
	}, nil
}

func handleSessionDeliver(w http.ResponseWriter, r *http.Request) {
	var request SessionDeliverRequest
	if !decodeChatRequest(w, r, &request) {
		return
	}
	result, err := RunSessionDeliver(r.Context(), SessionDeliverOptions{ID: r.PathValue("id"), Text: request.Text})
	if err != nil {
		writeChatError(w, err)
		return
	}
	writeChatJSON(w, http.StatusAccepted, result)
}
