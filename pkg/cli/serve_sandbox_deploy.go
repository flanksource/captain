// HTTP surface for deploying git-agent sidecars from the web UI.
//
// The CLI's `deploy` is built around refusing rather than guessing: the two
// addresses this topology needs point in opposite directions, and getting
// either wrong produces an agent that enrolls, looks healthy, and fails at the
// first dispatch. A form that only discovers that on submit would reintroduce
// exactly that gap, so the preflight route runs the same detection read-only and
// the UI blocks on it before an operator types anything.

package cli

import (
	"context"
	"fmt"
	"net/http"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/flanksource/captain/pkg/gitagent/deploy"
)

func registerSandboxDeployHandlers(mux *http.ServeMux) {
	mux.Handle("GET /api/captain/sandbox/git-agent/deploy/preflight", handleGitAgentDeployPreflight())
	registerSandboxPickerHandlers(mux)
	mux.Handle("POST /api/captain/sandbox/git-agent/deployments", handleGitAgentDeploy())
	mux.Handle("PUT /api/captain/sandbox/git-agent/deployments/{name}", handleGitAgentUpdate())
	mux.Handle("DELETE /api/captain/sandbox/git-agent/deployments/{name}", handleGitAgentUndeploy())
}

// gitAgentDeployPreflight is what the UI needs to decide whether a target is
// usable at all, and what to prefill when it is.
type gitAgentDeployPreflight struct {
	Target string `json:"target"`
	// Ready is false when this host cannot deploy to this target right now.
	// Reason says why, in the same words the CLI would refuse with.
	Ready  bool   `json:"ready"`
	Reason string `json:"reason,omitempty"`

	// MailboxListen and HostFingerprint identify the supervisor a deployed agent
	// would relay to; Transport is the channel it relays over, https when
	// `captain serve` hosts the mailbox. All three come from a live probe, not
	// from configuration.
	MailboxListen   string `json:"mailboxListen,omitempty"`
	HostFingerprint string `json:"hostFingerprint,omitempty"`
	Transport       string `json:"transport,omitempty"`

	// Supervisor is the address the deployed agent uses to reach this mailbox,
	// and SupervisorFrom how it was derived. Empty with SupervisorRequired set
	// means the operator must supply one — no route back can be proven.
	Supervisor         string `json:"supervisor,omitempty"`
	SupervisorFrom     string `json:"supervisorFrom,omitempty"`
	SupervisorRequired bool   `json:"supervisorRequired"`

	// SupervisorCandidates are this host's non-loopback addresses rendered as
	// endpoints of the mailbox that answered, so the field SupervisorRequired
	// makes mandatory is a picker rather than a blank box. Enumerated rather
	// than probed — see supervisorCandidates — so the list is an offer and a
	// typed address remains just as valid.
	SupervisorCandidates []string `json:"supervisorCandidates,omitempty"`

	Namespace   string `json:"namespace,omitempty"`
	KubeContext string `json:"kubeContext,omitempty"`
	Runtime     string `json:"runtime,omitempty"`

	// InCluster and DomainRequired mirror the CLI: outside the cluster there is
	// no address to advertise that the supervisor could route to, so the form
	// must demand a domain the same way SupervisorRequired makes it demand an
	// address. Both are reported so the UI can say WHICH topology it is about to
	// create rather than only which flag is missing.
	InCluster      bool `json:"inCluster"`
	DomainRequired bool `json:"domainRequired"`

	// IngressClasses are the classes this cluster actually has. An Ingress naming
	// a class no controller implements is accepted and then never routed, so
	// turning this into a picker removes the most silent failure in the feature.
	// Empty on a Forbidden list, exactly as the namespace picker allows a typed
	// value — "none found" and "may not look" both leave the operator typing.
	IngressClasses []string `json:"ingressClasses,omitempty"`

	// CertManagerInstalled comes from one discovery call. Without it an
	// --ingress-issuer annotation is inert and the controller answers for the
	// host with its own default certificate.
	CertManagerInstalled bool `json:"certManagerInstalled"`
}

// handleGitAgentDeployPreflight probes one target without changing anything.
//
// Read-only, so it is not behind validateLocalConfigurationRequest: it reports
// what the CLI would report, and answering "there is no live mailbox" to a
// caller who could already list the roster discloses nothing new.
func handleGitAgentDeployPreflight() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		request, err := parseGitAgentDeployPreflightRequest(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		// Bounded, because this runs when a modal opens rather than when an
		// operator asked for a deploy: `docker info` against a stopped daemon
		// can hang for tens of seconds, and a form that sits blank that long
		// reads as broken. A timeout is itself a usable answer.
		ctx, cancel := context.WithTimeout(r.Context(), preflightTimeout)
		defer cancel()
		writeServeJSON(w, http.StatusOK, gitAgentDeployPreflightFor(ctx, request))
	})
}

type gitAgentDeployPreflightRequest struct {
	Target      deploy.Target
	Backend     string
	Transport   mailboxTransport
	KubeContext string
}

func parseGitAgentDeployPreflightRequest(r *http.Request) (gitAgentDeployPreflightRequest, error) {
	query := r.URL.Query()
	target, err := deploy.ParseTarget(strings.TrimSpace(query.Get("target")))
	if err != nil {
		return gitAgentDeployPreflightRequest{}, err
	}
	transport, err := parseMailboxTransport(query.Get("transport"))
	if err != nil {
		return gitAgentDeployPreflightRequest{}, err
	}
	return gitAgentDeployPreflightRequest{
		Target: target, Backend: sandboxBackendParam(r), Transport: transport,
		KubeContext: strings.TrimSpace(query.Get("kubeContext")),
	}, nil
}

// preflightTimeout bounds the probes. Everything here is a local daemon socket,
// a loopback handshake, or one API call to a configured cluster; none of them is
// slow when it is going to succeed at all.
const preflightTimeout = 8 * time.Second

// gitAgentDeployPreflightFor runs the detection a deploy would run, and reports
// the first thing that would stop it rather than erroring.
//
// A failed preflight is information the UI renders, not a failed request: "no
// live mailbox on this host" is the expected answer on a machine that has never
// run one, and a 500 would make it look like a bug.
func gitAgentDeployPreflightFor(ctx context.Context, request gitAgentDeployPreflightRequest) gitAgentDeployPreflight {
	result := gitAgentDeployPreflight{
		Target:             string(request.Target),
		SupervisorRequired: request.Target == deploy.TargetKubernetes,
	}
	if request.Target == deploy.TargetDocker {
		result.Runtime = dockerHostDescription()
		if err := deploy.DockerAvailable(ctx); err != nil {
			result.Reason = preflightReason(ctx, err, dockerHostDescription()+" did not answer")
			return result
		}
	}

	// needOffHost mirrors the deploy: a Kubernetes deployment always supplies its
	// own supervisor address, so proving this host answers on its LAN address
	// would be a probe of something nothing uses.
	mailbox, err := detectMailbox(ctx, mailboxDetection{
		Backend: request.Backend, NeedOffHost: request.Target == deploy.TargetDocker,
		Transport: request.Transport,
	})
	if err != nil {
		result.Reason = preflightReason(ctx, err, "the mailbox probe did not finish")
		return result
	}
	result.MailboxListen, result.HostFingerprint = mailbox.Listen, mailbox.HostFingerprint
	result.Transport = string(mailbox.Transport)
	result.SupervisorCandidates = supervisorCandidates(mailbox)

	if request.Target == deploy.TargetKubernetes {
		client, namespace, err := kubernetesClient(kubeClientOptions{Context: request.KubeContext})
		if err != nil {
			result.Reason = err.Error()
			return result
		}
		result.Namespace = namespace
		result.KubeContext = request.KubeContext
		// A version call rather than a client construction: a kubeconfig that
		// parses but points at a cluster that is gone would otherwise pass
		// preflight and fail at apply, after the token is already minted.
		version, err := client.Discovery().ServerVersion()
		if err != nil {
			result.Reason = fmt.Sprintf("the kubeconfig resolves but the cluster is unreachable: %v", err)
			return result
		}
		result.Runtime = "kubernetes " + version.GitVersion
		// The domain is required for exactly the reason the supervisor address is:
		// captain cannot prove a route it did not create.
		result.InCluster = runningInCluster()
		result.DomainRequired = !result.InCluster
		result.IngressClasses = listIngressClasses(ctx, client)
		_, err = client.Discovery().ServerResourcesForGroupVersion("cert-manager.io/v1")
		result.CertManagerInstalled = err == nil
		// Deliberately ready without a supervisor address: the operator supplies
		// it, and SupervisorRequired tells the form to demand one. Resolving it
		// here would only produce the refusal the CLI already gives.
		result.Ready = true
		return result
	}

	supervisor, from, err := resolveSupervisorAddress(request.Target, mailbox, "")
	if err != nil {
		result.Reason = err.Error()
		return result
	}
	// The certificate has to cover the name the agent will dial. Left to the
	// deploy, this surfaces after the token is minted; left to the agent, it
	// surfaces as a TLS error on the first relay.
	if err := verifySupervisorNameIsCovered(ctx, mailbox, supervisor); err != nil {
		result.Reason = preflightReason(ctx, err, "the certificate probe did not finish")
		return result
	}
	result.Supervisor, result.SupervisorFrom, result.Ready = supervisor, from, true
	return result
}

// gitAgentDeployRequest is the subset of the CLI's flags the UI exposes.
//
// Deliberately a subset: the command has thirty flags and most are sizing and
// security defaults that are already correct, so a form rendering all of them
// would be worse than the CLI rather than better. What is here is what cannot
// be defaulted — an identity, a route the runtime cannot prove, and the model
// credentials without which the agent enrolls and then fails its first task.
type gitAgentDeployRequest struct {
	Name string `json:"name"`
	GitAgentDeploymentConfig
	CreateNamespace bool `json:"createNamespace,omitempty"`
	Replace         bool `json:"replace,omitempty"`
	DryRun          bool `json:"dryRun,omitempty"`
}

// options merges the request over the CLI's own defaults, so the UI and the
// command deploy the same thing when the UI leaves a field blank.
func (req gitAgentDeployRequest) options(backend string) GitAgentDeployOptions {
	opts := req.GitAgentDeploymentConfig.options(strings.TrimSpace(req.Name), backend)
	opts.Replace, opts.DryRun, opts.CreateNamespace = req.Replace, req.DryRun, req.CreateNamespace
	return opts
}

func handleGitAgentDeploy() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := validateLocalConfigurationRequest(r); err != nil {
			http.Error(w, err.Error(), http.StatusForbidden)
			return
		}
		var request gitAgentDeployRequest
		if err := decodeServeJSONBody(w, r, &request); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if strings.TrimSpace(request.Name) == "" {
			http.Error(w, "agent name is required", http.StatusBadRequest)
			return
		}
		result, err := RunGitAgentDeploy(r.Context(), request.options(sandboxBackendParam(r)))
		if err != nil {
			http.Error(w, err.Error(), serveRunStatus(err, http.StatusBadRequest))
			return
		}
		writeServeJSON(w, http.StatusOK, result)
	})
}

func handleGitAgentUndeploy() http.Handler {
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
		query := r.URL.Query()
		result, err := RunGitAgentUndeploy(r.Context(), GitAgentUndeployOptions{
			Name:    name,
			Backend: sandboxBackendParam(r),
			// Empty resolves to whatever deploy recorded, which is what the UI
			// wants: tearing down the wrong runtime removes nothing and reports
			// success, leaving a live sidecar on the network.
			Target:         strings.TrimSpace(query.Get("target")),
			Purge:          queryFlag(query.Get("purge")),
			KeepEnrollment: queryFlag(query.Get("keepEnrollment")),
			DryRun:         queryFlag(query.Get("dryRun")),
		})
		if err != nil {
			http.Error(w, err.Error(), serveRunStatus(err, http.StatusBadRequest))
			return
		}
		writeServeJSON(w, http.StatusOK, result)
	})
}

// preflightReason keeps a probe that ran out of time from reading as a probe
// that answered. "docker is not reachable" and "docker did not answer in 8s"
// call for different next steps, and only one of them means anything is wrong.
func preflightReason(ctx context.Context, err error, timedOut string) string {
	if ctx.Err() != nil {
		return fmt.Sprintf("%s within %s; re-check once it is up", timedOut, preflightTimeout)
	}
	return err.Error()
}

func queryFlag(value string) bool {
	return strings.EqualFold(strings.TrimSpace(value), "true")
}

// defaultGitAgentDeployOptions is the options struct the CLI would build from
// its own flag defaults.
//
// Read from the `default:` struct tags rather than restated here, because a
// restated copy drifts: the UI would keep deploying a 4Gi memory limit after the
// flag moved on, and nothing would fail to say so.
func defaultGitAgentDeployOptions() GitAgentDeployOptions {
	var opts GitAgentDeployOptions
	applyStructDefaults(&opts)
	return opts
}

// applyStructDefaults fills a struct's fields from their `default:` tags. It
// covers the kinds a CLI options struct uses; anything else keeps its zero
// value, which is what an untagged field would get from the flag binder too.
func applyStructDefaults(target any) {
	value := reflect.ValueOf(target).Elem()
	structType := value.Type()
	for i := range structType.NumField() {
		tag := structType.Field(i).Tag.Get("default")
		if tag == "" || !value.Field(i).CanSet() {
			continue
		}
		field := value.Field(i)
		switch field.Kind() {
		case reflect.String:
			field.SetString(tag)
		case reflect.Bool:
			if parsed, err := strconv.ParseBool(tag); err == nil {
				field.SetBool(parsed)
			}
		case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
			if parsed, err := strconv.ParseInt(tag, 10, 64); err == nil {
				field.SetInt(parsed)
			}
		}
	}
}

// listIngressClasses reports the controllers this cluster has, or nothing when
// it will not say. An empty list is not a refusal: the operator can still type a
// class, the same way the namespace picker allows one.
func listIngressClasses(ctx context.Context, client kubernetes.Interface) []string {
	list, err := client.NetworkingV1().IngressClasses().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(list.Items))
	for _, item := range list.Items {
		names = append(names, item.Name)
	}
	sort.Strings(names)
	return names
}
