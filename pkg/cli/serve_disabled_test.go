package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/captainconfig"
	"github.com/flanksource/clicky/rpc"
)

func TestDisabledPutSavesNormalizesAndInstalls(t *testing.T) {
	setupDisabledTest(t)

	response := serveDisabledRequest(t, `{"modes":[" CMUX ","cmux"],"providers":["DeepSeek"],
		"backends":["gemini-cli"],"models":["gemini/Veo-3"],"efforts":["  ultra"]}`)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}

	var result disabledSelectionsRequest
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	// Enum axes are canonicalized and de-duplicated; a model id keeps its case.
	want := disabledSelectionsRequest{
		Modes: []string{"cmux"}, Providers: []string{"deepseek"},
		Backends: []string{"gemini-cli"}, Models: []string{"gemini/Veo-3"}, Efforts: []string{"ultra"},
	}
	if !reflect.DeepEqual(result, want) {
		t.Fatalf("response = %+v, want %+v", result, want)
	}

	config, _, err := captainconfig.Load()
	if err != nil {
		t.Fatalf("Load(): %v", err)
	}
	if !reflect.DeepEqual(config.AI.Disabled.Modes, []string{"cmux"}) {
		t.Fatalf("persisted modes = %v", config.AI.Disabled.Modes)
	}
	if !api.Disabled().Mode(api.ModeCmux) {
		t.Fatal("the write path did not install the new set process-wide")
	}
}

func TestDisabledPutPreservesUnrelatedConfiguration(t *testing.T) {
	setupDisabledTest(t)
	if err := captainconfig.Save(captainconfig.Config{
		AI:      captainconfig.AIDefaults{DefaultProvider: "gemini"},
		Prompts: captainconfig.PromptDefaults{Dirs: []string{"/repo/prompts"}},
	}); err != nil {
		t.Fatalf("seed config: %v", err)
	}

	if response := serveDisabledRequest(t, `{"modes":["cmux"],"providers":[],"backends":[],"models":[],"efforts":[]}`); response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}

	config, _, err := captainconfig.Load()
	if err != nil || config.AI.DefaultProvider != "gemini" || len(config.Prompts.Dirs) != 1 {
		t.Fatalf("config=%+v err=%v", config, err)
	}
}

func TestDisabledPutRejectsInvalidSets(t *testing.T) {
	for name, body := range map[string]string{
		"unknown mode":     `{"modes":["telepathy"],"providers":[],"backends":[],"models":[],"efforts":[]}`,
		"unknown provider": `{"modes":[],"providers":["acme"],"backends":[],"models":[],"efforts":[]}`,
		"unknown backend":  `{"modes":[],"providers":[],"backends":["claude-carrier-pigeon"],"models":[],"efforts":[]}`,
		"unknown effort":   `{"modes":[],"providers":[],"backends":[],"models":[],"efforts":["frantic"]}`,
		"every provider":   `{"modes":[],"providers":["anthropic","openai","gemini","deepseek"],"backends":[],"models":[],"efforts":[]}`,
		"every effort":     `{"modes":[],"providers":[],"backends":[],"models":[],"efforts":["low","medium","high","xhigh","max","ultra"]}`,
		"every mode":       `{"modes":["api","cli","agent","cmux"],"providers":[],"backends":[],"models":[],"efforts":[]}`,
		"active provider stranded": `{"modes":[],"providers":[],"models":[],"efforts":[],
			"backends":["anthropic","claude-cli","claude-agent","claude-cmux"]}`,
	} {
		t.Run(name, func(t *testing.T) {
			setupDisabledTest(t)

			response := serveDisabledRequest(t, body)

			if response.Code != http.StatusUnprocessableEntity {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			if config, exists, err := captainconfig.Load(); err != nil || exists {
				t.Fatalf("a rejected set was persisted: config=%+v exists=%v err=%v", config, exists, err)
			}
			if !api.Disabled().Empty() {
				t.Fatal("a rejected set was installed process-wide")
			}
		})
	}
}

func TestDisabledPutRejectsNonLocalRequests(t *testing.T) {
	setupDisabledTest(t)
	mux := http.NewServeMux()
	registerDisabledHandlers(mux)
	request := httptest.NewRequest(http.MethodPut, "/api/captain/ai/disabled",
		strings.NewReader(`{"modes":["cmux"],"providers":[],"backends":[],"models":[],"efforts":[]}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", "https://captain.example.com")
	request.Host = "captain.example.com"
	request.RemoteAddr = "203.0.113.7:1234"
	response := httptest.NewRecorder()

	mux.ServeHTTP(response, request)

	if response.Code != http.StatusForbidden {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestDisabledPutRejectsUnknownFields(t *testing.T) {
	setupDisabledTest(t)

	response := serveDisabledRequest(t, `{"modes":[],"providers":[],"backends":[],"models":[],"efforts":[],"kinds":["x"]}`)

	if response.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestDisabledOpenAPIPaths(t *testing.T) {
	spec := &rpc.OpenAPISpec{}
	addCaptainDisabledPaths(spec)
	if _, ok := spec.Paths["/api/captain/ai/disabled"]; !ok {
		t.Fatal("missing disabled-selections OpenAPI path")
	}
}

func setupDisabledTest(t *testing.T) {
	t.Helper()
	captainconfig.SetPathForTesting(filepath.Join(t.TempDir(), ".captain.yaml"))
	api.SetDisabled(api.DisabledSet{})
	t.Cleanup(func() {
		captainconfig.SetPathForTesting("")
		api.SetDisabled(api.DisabledSet{})
	})
}

func serveDisabledRequest(t *testing.T, body string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	registerDisabledHandlers(mux)
	request := httptest.NewRequest(http.MethodPut, "/api/captain/ai/disabled", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", "http://localhost:9020")
	request.Host = "localhost:9020"
	request.RemoteAddr = "127.0.0.1:1234"
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	return response
}
