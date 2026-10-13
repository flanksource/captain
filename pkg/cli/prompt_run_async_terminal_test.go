package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/flanksource/captain/pkg/api"
)

// An async run that a host follows over HTTP must reach a terminal snapshot —
// done, with the outcome in its summary — whether its verification passes or
// fails, so a client that polls (or reconnects) after the end can read it.
func TestLaunchAsyncRun_SnapshotReportsTerminalOutcome(t *testing.T) {
	for _, tc := range []struct {
		name    string
		command string
		success bool
	}{
		{name: "verify passes", command: "true", success: true},
		{name: "verify fails", command: "sh -c 'echo not done; exit 1'", success: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			isolateSavedAI(t)
			rendered := workflowRendered(&api.Verify{Commands: []string{tc.command}, MaxIterations: 1})
			rendered.Input.SetCwd(t.TempDir())

			started, err := launchAsyncRun("workflow-test", rendered, false)
			if err != nil {
				t.Fatal(err)
			}

			handler := PromptRunHandler()
			var body promptRunSnapshotBody
			deadline := time.Now().Add(30 * time.Second)
			for {
				recorder := httptest.NewRecorder()
				handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/"+started.RunID, nil))
				if recorder.Code != http.StatusOK {
					t.Fatalf("snapshot status = %d (%s), want 200 while the run is retained", recorder.Code, recorder.Body.String())
				}
				if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
					t.Fatal(err)
				}
				if body.Done {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("run never reported done; last snapshot: %s", recorder.Body.String())
				}
				time.Sleep(50 * time.Millisecond)
			}

			if body.Summary == nil {
				t.Fatalf("done snapshot carries no summary (error %q)", body.Error)
			}
			if body.Summary.Success != tc.success {
				t.Fatalf("summary.success = %v, want %v (summary %+v, error %q)", body.Summary.Success, tc.success, *body.Summary, body.Error)
			}
			if !tc.success && body.Summary.Error == "" && body.Error == "" {
				t.Fatalf("a failed run's snapshot names no error: %+v", *body.Summary)
			}
		})
	}
}
