package cli

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/flanksource/captain/pkg/monitor"
)

// handleMonitorHookEvent accepts normalized hook events from `captain hook
// monitor notify` and enqueues them on the serve monitor. Delivery is
// fire-and-forget for the sender: 202 means enqueued, not ingested.
func handleMonitorHookEvent() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		provider := strings.TrimSpace(r.PathValue("provider"))
		if provider != "claude" && provider != "codex" {
			http.Error(w, fmt.Sprintf("unknown hook provider %q", provider), http.StatusBadRequest)
			return
		}
		var ev monitor.HookEvent
		if err := json.NewDecoder(r.Body).Decode(&ev); err != nil {
			http.Error(w, "invalid hook event payload: "+err.Error(), http.StatusBadRequest)
			return
		}
		if strings.TrimSpace(ev.Event) == "" {
			http.Error(w, "hook event name is required", http.StatusBadRequest)
			return
		}
		mon := serveMonitor()
		if mon == nil {
			http.Error(w, "session monitor is not running", http.StatusServiceUnavailable)
			return
		}
		ev.Provider = provider
		ev.ReceivedAt = time.Now().UTC()
		mon.NotifyHookEvent(ev)
		w.WriteHeader(http.StatusAccepted)
	}
}
