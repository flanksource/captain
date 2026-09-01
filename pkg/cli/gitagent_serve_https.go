// The sidecar's HTTPS receive endpoint.
//
// It is the same smart-HTTP handler `captain serve` mounts for the mailbox, with
// the two role-shaped differences the transport already models: Role tells the
// hook shims which admission tier they are, and a nil Enroll serves no
// enrollment endpoint — a sidecar receives pushes and enrolls nobody.
//
// What is genuinely new is the identity resolver. The mailbox authenticates
// agents against the token store in its database; a sidecar has no database by
// design, and authenticates exactly one peer against the credential it minted
// for that peer at enrollment.
package cli

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/flanksource/captain/pkg/captaintoken"
	"github.com/flanksource/captain/pkg/gitagent"
	"github.com/flanksource/clicky"
)

// sidecarHTTPSPlan is what the listener needs, resolved by RunGitAgentServe.
type sidecarHTTPSPlan struct {
	listen    string
	root      string
	keysDir   string
	advertise string
	certPath  string
	keyPath   string
}

// serveSidecarHTTPS runs the receive endpoint until the context is cancelled.
func serveSidecarHTTPS(ctx context.Context, plan sidecarHTTPSPlan) error {
	host, err := advertiseHostname(plan.advertise)
	if err != nil {
		return err
	}
	credential, tlsConfig, err := sidecarTLSConfig(plan, host)
	if err != nil {
		return err
	}
	dispatch, err := gitagent.LoadDispatchCredential(filepath.Join(plan.keysDir, gitagent.DispatchCredentialName))
	if err != nil {
		return fmt.Errorf("%w\nthis endpoint authenticates its supervisor with a token minted at enrollment; "+
			"rerun with --token-file to enroll", err)
	}
	identify := sidecarIdentity(dispatch)
	handler, err := gitagent.NewHTTPHandler(gitagent.HTTPServerConfig{
		Root:     plan.root,
		Role:     gitagent.RoleSidecar,
		Identify: identify,
		// A sidecar enrolls nobody, so the endpoint is not served at all rather
		// than served and refusing.
		Enroll: nil,
		Log:    log.Warnf,
	})
	if err != nil {
		return err
	}
	mux := http.NewServeMux()
	mux.Handle(gitagent.GitHTTPPrefix, handler)
	mux.Handle("POST "+gitagent.AgentWhoamiPath, agentWhoamiHandler(identify, RunWhoami))

	server := &http.Server{
		Addr:      plan.listen,
		Handler:   mux,
		TLSConfig: tlsConfig,
		// ReadHeaderTimeout only, as `captain serve` does: a push runs the
		// receive hooks inline and a prompt hook can take minutes, so a
		// whole-request deadline would kill the work it is waiting for.
		ReadHeaderTimeout: 30 * time.Second,
	}
	clicky.Printf("captain git-agent sidecar serving %s on https://%s\n", plan.root, plan.listen)
	clicky.Printf("  certificate: %s (pin %s)\n", credential.CertPath, credential.PublicKeyPin)
	clicky.Printf("  dispatched to at: %s\n", plan.advertise)
	clicky.Printf("  runtime identity: %s\n", gitagent.AgentWhoamiPath)

	go func() {
		<-ctx.Done()
		_ = server.Close()
	}()
	if err := server.ListenAndServeTLS("", ""); err != nil && ctx.Err() == nil {
		return err
	}
	return nil
}

func sidecarTLSConfig(plan sidecarHTTPSPlan, host string) (*gitagent.TLSCredential, *tls.Config, error) {
	certPath, keyPath := strings.TrimSpace(plan.certPath), strings.TrimSpace(plan.keyPath)
	if (certPath == "") != (keyPath == "") {
		return nil, nil, fmt.Errorf("--tls-cert and --tls-key must be given together")
	}
	if certPath == "" {
		credential, err := gitagent.EnsureTLSCredential(plan.keysDir, []string{host})
		if err != nil {
			return nil, nil, err
		}
		return credential, serveTLSConfig(credential), nil
	}
	load := func() (*gitagent.TLSCredential, error) {
		credential, err := gitagent.LoadTLSCredential(certPath, keyPath)
		if err != nil {
			return nil, err
		}
		if err := credential.Covers([]string{host}); err != nil {
			return nil, err
		}
		return credential, nil
	}
	credential, err := load()
	if err != nil {
		return nil, nil, err
	}
	return credential, &tls.Config{
		MinVersion: tls.VersionTLS12,
		GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
			current, err := load()
			if err != nil {
				return nil, err
			}
			return &current.Certificate, nil
		},
	}, nil
}

type whoamiRunner func(WhoamiOptions) (any, error)

func agentWhoamiHandler(identify func(*http.Request) (string, error), run whoamiRunner) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if agent, err := identify(r); err != nil || agent == "" {
			http.Error(w, "captain: this request carries no agent identity", http.StatusForbidden)
			return
		}
		options, err := agentWhoamiOptions(r)
		if err != nil {
			http.Error(w, "captain: "+err.Error(), http.StatusBadRequest)
			return
		}
		result, err := run(options)
		if err != nil {
			http.Error(w, "captain: inspect agent runtimes: "+err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(result); err != nil {
			log.Warnf("git-agent whoami: write response: %v", err)
		}
	})
}

func agentWhoamiOptions(r *http.Request) (WhoamiOptions, error) {
	query := r.URL.Query()
	options := WhoamiOptions{Provider: strings.TrimSpace(query.Get("provider")), Mode: strings.TrimSpace(query.Get("mode")), Models: true}
	var err error
	if value := query.Get("models"); value != "" {
		options.Models, err = strconv.ParseBool(value)
		if err != nil {
			return WhoamiOptions{}, fmt.Errorf("models must be true or false")
		}
	}
	if value := query.Get("limit"); value != "" {
		options.Limit, err = strconv.Atoi(value)
		if err != nil || options.Limit < 0 {
			return WhoamiOptions{}, fmt.Errorf("limit must be a non-negative integer")
		}
	}
	if options.IncludeDisabled, err = queryBool(query.Get("disabled"), "disabled"); err != nil {
		return WhoamiOptions{}, err
	}
	if options.NoCache, err = queryBool(query.Get("no-cache"), "no-cache"); err != nil {
		return WhoamiOptions{}, err
	}
	return options, nil
}

func queryBool(value, name string) (bool, error) {
	if value == "" {
		return false, nil
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return false, fmt.Errorf("%s must be true or false", name)
	}
	return parsed, nil
}

// advertiseHostname is the name the supervisor dials, and so the only name this
// endpoint's certificate has to cover.
func advertiseHostname(advertise string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(advertise))
	if err != nil || parsed.Hostname() == "" {
		return "", fmt.Errorf("--advertise %q must be https://host[:port]/path to serve over https", advertise)
	}
	return parsed.Hostname(), nil
}

// sidecarIdentity resolves the supervisor's bearer token to the single identity
// a sidecar accepts.
//
// It is the HTTPS counterpart of authorizing the supervisor's dispatch key, and
// deliberately resolves to the same name: over ssh the supervisor is admitted as
// supervisorAgentID, so returning anything else here would give one peer two ref
// namespaces (R8.3) and make the hook shims see a different agent depending only
// on which wire the push arrived over.
//
// The credential is read once and closed over rather than re-read per request.
// Enrollment is the only thing that rotates it and runs before this listener
// exists, so nothing can go stale inside one process lifetime.
func sidecarIdentity(credential *gitagent.DispatchCredential) func(*http.Request) (string, error) {
	verifier := credential.Verifier(supervisorAgentID)
	return func(r *http.Request) (string, error) {
		presented, ok := captaintoken.BearerFromHeader(r.Header.Get("Authorization"))
		if !ok {
			// Logged here because the transport collapses every identity failure
			// into one generic 403 with no detail, which is right for the client
			// and useless for the operator.
			log.Warnf("git-agent sidecar: a push presented no bearer token")
			return "", fmt.Errorf("this endpoint authenticates its supervisor with the bearer token issued at enrollment")
		}
		record, err := verifier.VerifyScope(r.Context(), presented, captaintoken.ScopeGit)
		if err != nil {
			log.Warnf("git-agent sidecar: refused a dispatch push: %v", err)
			return "", err
		}
		return record.Agent, nil
	}
}
