package cli

import (
	"context"
	"fmt"
	"net/http"
	"strings"
)

type gitAgentDeployRunner func(context.Context, GitAgentDeployOptions) (any, error)

func handleGitAgentUpdate() http.Handler {
	return handleGitAgentUpdateWithRunner(RunGitAgentDeploy)
}

func handleGitAgentUpdateWithRunner(run gitAgentDeployRunner) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := validateLocalConfigurationRequest(r); err != nil {
			http.Error(w, err.Error(), http.StatusForbidden)
			return
		}
		name := strings.TrimSpace(r.PathValue("name"))
		if name == "" {
			http.Error(w, "agent name is required", http.StatusBadRequest)
			return
		}
		backend := sandboxBackendParam(r)
		recorded, found := lookupDeployment(backend, name)
		if !found {
			http.Error(w, fmt.Sprintf("deployment %q was not found", name), http.StatusNotFound)
			return
		}
		if recorded.Config == nil {
			http.Error(w, "deployment has no saved edit configuration; redeploy it before editing", http.StatusConflict)
			return
		}
		var request gitAgentDeployRequest
		if err := decodeServeJSONBody(w, r, &request); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := validateDeploymentEdit(recorded, request.GitAgentDeploymentConfig); err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		request.Name, request.Replace, request.CreateNamespace = name, true, false
		opts := request.options(backend)
		opts.reuseEnrollment = true
		result, err := run(r.Context(), opts)
		if err != nil {
			http.Error(w, err.Error(), serveRunStatus(err, http.StatusBadRequest))
			return
		}
		writeServeJSON(w, http.StatusOK, result)
	})
}

func validateDeploymentEdit(recorded GitAgentDeployment, requested GitAgentDeploymentConfig) error {
	if strings.TrimSpace(requested.Target) != recorded.Target {
		return fmt.Errorf("editing cannot move a deployment from %s to %s; deploy a new agent instead",
			recorded.Target, strings.TrimSpace(requested.Target))
	}
	if strings.TrimSpace(requested.Namespace) != recorded.Namespace {
		return fmt.Errorf("editing cannot move deployment %s from namespace %q to %q; deploy a new agent instead",
			recorded.Workload, recorded.Namespace, strings.TrimSpace(requested.Namespace))
	}
	if recorded.Config == nil {
		return fmt.Errorf("deployment has no saved edit configuration")
	}
	for _, identity := range []struct {
		label     string
		recorded  string
		requested string
	}{
		{"transport", recorded.Config.Transport, requested.Transport},
		{"supervisor address", recorded.Config.SupervisorAddress, requested.SupervisorAddress},
		{"advertised endpoint", recorded.Config.Advertise, requested.Advertise},
	} {
		if strings.TrimSpace(identity.requested) != strings.TrimSpace(identity.recorded) {
			return fmt.Errorf("editing cannot change the deployment %s from %q to %q without re-enrollment",
				identity.label, strings.TrimSpace(identity.recorded), strings.TrimSpace(identity.requested))
		}
	}
	return nil
}
