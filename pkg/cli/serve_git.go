// Hosting the git-agent mailbox on `captain serve`.
//
// The supervisor's receive endpoint used to be a separate long-lived process
// (`captain sandbox git-agent serve --role mailbox`). Nothing about the
// protocol required that; it was a separate process only because it needed its
// own SSH listener. Over HTTPS it is a handler, and it belongs on the server
// that already holds the database the tokens live in.

package cli

import (
	"crypto/tls"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/flanksource/captain/pkg/captainconfig"
	"github.com/flanksource/captain/pkg/captaintoken"
	"github.com/flanksource/captain/pkg/database"
	"github.com/flanksource/captain/pkg/gitagent"
)

// serveCertificate resolves the certificate this server presents, or nil when
// it serves plain HTTP. A supplied certificate is used as-is; otherwise one is
// generated beside the git-agent keys on first use and reused thereafter,
// because re-issuing invalidates every agent that pinned the previous one.
//
// The "not serving TLS" case returns nil here rather than in each caller, so a
// server on plain HTTP cannot hand a joining agent a certificate it will never
// present — the agent would pin it and every later relay would fail.
func serveCertificate(opts ServeOptions) (*gitagent.TLSCredential, error) {
	cert, key := strings.TrimSpace(opts.TLSCert), strings.TrimSpace(opts.TLSKey)
	if (cert == "") != (key == "") {
		return nil, fmt.Errorf("--tls-cert and --tls-key must be given together")
	}
	if !opts.TLS && cert == "" {
		return nil, nil
	}
	if cert != "" {
		credential, err := gitagent.LoadTLSCredential(cert, key)
		if err != nil {
			return nil, err
		}
		// Checked at startup rather than discovered at first push: by then
		// every agent has already been enrolled against this certificate.
		return credential, credential.Covers(opts.TLSHosts)
	}
	keysDir, err := gitAgentKeysDir()
	if err != nil {
		return nil, err
	}
	return gitagent.EnsureTLSCredential(keysDir, opts.TLSHosts)
}

// serveTLSConfig renders a resolved certificate for http.Server, or nil for
// plain HTTP.
func serveTLSConfig(credential *gitagent.TLSCredential) *tls.Config {
	if credential == nil {
		return nil
	}
	log.Infof("Serving TLS with %s (pin %s)", credential.CertPath, credential.PublicKeyPin)
	return &tls.Config{
		Certificates: []tls.Certificate{credential.Certificate},
		MinVersion:   tls.VersionTLS12,
	}
}

// registerGitHandlers mounts the git smart-HTTP transport, serving the
// supervisor's mailbox root.
//
// The whole /git/ subtree is registered rather than the two exact endpoints,
// because Go's mux would otherwise fall through to the single-page-app
// catch-all — which answers any path whose last segment has no dot with 200 and
// an HTML body. `/git/x.git/info/refs` ends in "refs", so a git client would
// receive HTML and report a protocol error instead of a 404.
func registerGitHandlers(mux *http.ServeMux, db *database.DB, addr string, credential *gitagent.TLSCredential) error {
	root, err := gitAgentServedRoot()
	if err != nil {
		return err
	}
	offer, err := serveEnrollmentOffer(credential)
	if err != nil {
		return err
	}
	handler, err := gitagent.NewHTTPHandler(gitagent.HTTPServerConfig{
		Root:     root,
		Role:     gitagent.RoleMailbox,
		Identify: gitAgentIdentity(db),
		Enroll:   gitAgentEnroll(offer, gitAgentBackendName),
		Log:      log.Warnf,
	})
	if err != nil {
		return err
	}
	mux.Handle(gitagent.GitHTTPPrefix, handler)
	if err := recordServedGitMailbox(addr, root, credential); err != nil {
		// Fatal rather than logged: this server is the supervisor whether or not
		// it managed to say so, and a mailbox that nothing can find is the exact
		// silent half-configuration this package exists to prevent.
		return fmt.Errorf("publish this server as a git-agent mailbox: %w", err)
	}
	return nil
}

// recordServedGitMailbox publishes this server as a mailbox, the same way the
// standalone SSH one does, so `git-agent deploy` can find it without being told.
//
// It records the unusable shapes too — bound to loopback, or serving plain HTTP
// — because that is what lets deploy refuse with the flags to restart with
// instead of reporting that no mailbox has ever served here.
func recordServedGitMailbox(addr, root string, credential *gitagent.TLSCredential) error {
	record := mailboxRecord{Transport: transportHTTPS, Root: root, Listen: addr}
	if credential != nil {
		record.Identity, record.Encrypted = credential.PublicKeyPin, true
	}
	return captainconfig.Update(func(cfg *captainconfig.Config) error {
		backend, err := ensureGitAgentBackend(cfg, gitAgentBackendName)
		if err != nil {
			return err
		}
		// mailboxRoot stays a top-level key: gitagent.ServedRootFor reads it
		// directly, and both transports serve the same root.
		backend.Options["mailboxRoot"] = root
		setMailboxRecord(backend.Options, record)
		cfg.Sandbox.Backends[gitAgentBackendName] = backend
		return nil
	})
}

// gitAgentBackendName is the backend this server's mailbox enrolls into. The
// git-agent CLI defaults to the same name, so a supervisor and its operator
// address one roster.
const gitAgentBackendName = "git-agent"

// serveEnrollmentOffer is what this supervisor hands a joining agent: the
// dispatch key it must authorize, and the certificate its relays verify
// against. The certificate travels over the already-pinned exchange, which is
// the only channel where handing it over proves anything.
func serveEnrollmentOffer(credential *gitagent.TLSCredential) (gitagent.EnrollmentOffer, error) {
	keysDir, err := gitAgentKeysDir()
	if err != nil {
		return gitagent.EnrollmentOffer{}, err
	}
	_, dispatchFP, err := gitagent.EnsureKeyPair(filepath.Join(keysDir, dispatchKeyName))
	if err != nil {
		return gitagent.EnrollmentOffer{}, err
	}
	offer := gitagent.EnrollmentOffer{DispatchKey: dispatchFP}
	if credential == nil {
		// No TLS: an agent enrolling here relays over ssh, and has nothing to
		// verify a certificate against because there is none.
		return offer, nil
	}
	pem, err := credential.PEM()
	if err != nil {
		return gitagent.EnrollmentOffer{}, err
	}
	offer.CACertificate, offer.PinnedPublicKey = string(pem), credential.PublicKeyPin
	return offer, nil
}

// gitAgentEnroll completes the reverse direction of trust: the supervisor
// records the agent's endpoint and host key so it can dispatch there, and
// answers with what the agent needs to accept that dispatch.
//
// The agent has already been resolved from its token by the time this runs, so
// re-enrollment is a re-record of the same values rather than a second identity.
func gitAgentEnroll(offer gitagent.EnrollmentOffer, backend string) func(*http.Request, string, gitagent.EnrollRequest) (*gitagent.EnrollResponse, error) {
	return func(r *http.Request, agent string, req gitagent.EnrollRequest) (*gitagent.EnrollResponse, error) {
		directory := gitAgentDirectory{backend: backend, ctx: r.Context()}
		url := strings.TrimSpace(req.AdvertiseURL)
		if url == "" {
			return nil, fmt.Errorf(
				"agent %q advertised no endpoint; rerun its serve with --advertise ssh://host:port — "+
					"this server cannot infer one, because a request may have crossed a proxy or NAT", agent)
		}
		// No client-key fingerprint is recorded. Over SSH the handshake proves
		// one; here the agent could only assert it, and an asserted fingerprint
		// in the roster would let one agent claim another's key and have that
		// agent's pushes attributed to it. An HTTPS agent authenticates with its
		// token, which is what AdmitToken already resolved.
		err := directory.RecordAgent(gitagent.AgentEnrollment{
			Name:            agent,
			URL:             url,
			HostFingerprint: strings.TrimSpace(req.HostFingerprint),
			DispatchToken:   strings.TrimSpace(req.DispatchToken),
		})
		if err != nil {
			return nil, err
		}
		response := offer.ResponseFor(agent)
		return &response, nil
	}
}

// gitAgentIdentity resolves which agent a push speaks for.
//
// The token is already verified by the auth middleware; this maps it onto a
// name. A pool token has no single name, so admission allocates or reclaims a
// member slot — which is also what enforces max-agents.
//
// A loopback request carries no token at all. That is deliberate for the API,
// but a push has to name an agent: the ref namespace R8.3 confines it to is
// derived from that name, so an anonymous push would have no namespace. The
// agent it claims comes from a header, and is honoured only on loopback.
func gitAgentIdentity(db *database.DB) func(*http.Request) (string, error) {
	return func(r *http.Request) (string, error) {
		record, ok := TokenFromContext(r.Context())
		if !ok {
			return localGitAgentName(r)
		}
		return db.AdmitAPITokenAgent(r.Context(), record.ID, r.Header.Get(GitAgentNameHeader))
	}
}

// GitAgentNameHeader lets a local push declare which agent it is acting as,
// for the loopback case where there is no token to derive it from.
const GitAgentNameHeader = "X-Captain-Agent"

func localGitAgentName(r *http.Request) (string, error) {
	name := r.Header.Get(GitAgentNameHeader)
	if err := captaintoken.ValidateName(name); err != nil {
		return "", fmt.Errorf("a local push must declare its agent in %s: %w", GitAgentNameHeader, err)
	}
	return name, nil
}
