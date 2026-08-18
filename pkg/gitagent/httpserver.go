// The git smart-HTTP transport (§2/§8). It is the same endpoint as the SSH one
// wearing a different coat: the identity is resolved differently, but the
// repository is still resolved through ResolveRepoPath, receive-pack is still
// the only verb served (R2.3), and the hooks still do all the vetting.
//
// git is shelled out to rather than reimplemented, exactly as the SSH path does
// it, because the installed hook set is the entire admission tier — a native Go
// implementation would not run it.

package gitagent

import (
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
)

// GitHTTPPrefix is the path the transport is served under. The whole subtree is
// registered so it takes precedence over a single-page-app catch-all, which
// would otherwise answer /git/x.git/info/refs with 200 and an HTML body — and a
// git client reports that as a baffling protocol error rather than a 404.
const GitHTTPPrefix = "/git/"

// AgentWhoamiPath is the authenticated runtime identity endpoint a sidecar exposes.
const AgentWhoamiPath = "/api/v1/whoami"

const (
	receivePackService = "git-receive-pack"
	uploadPackService  = "git-upload-pack"
	// EnrollEndpoint completes the bidirectional trust exchange over HTTPS, so
	// a supervisor hosted on `captain serve` needs no SSH listener at all. It
	// sits under the same prefix because it is authorized by the same token and
	// the same middleware.
	EnrollEndpoint = "enroll"
)

// HTTPServerConfig configures the smart-HTTP transport.
type HTTPServerConfig struct {
	// Root is the directory whose repositories may be pushed to.
	Root string
	Role ReceiverRole
	// Identify resolves the agent a request speaks for. Returning an error
	// refuses the push: the agent name is what the ref-namespace rules (R8.3)
	// are enforced against, so an anonymous push would have no namespace to be
	// confined to.
	Identify func(*http.Request) (string, error)
	// Enroll completes the reverse direction of trust for an already-identified
	// agent. Leaving it nil serves no enrollment endpoint, which is right for a
	// transport that only receives pushes.
	Enroll func(r *http.Request, agent string, req EnrollRequest) (*EnrollResponse, error)
	// Log receives transport-level failures that never reach the client, such
	// as receive-pack's own stderr. Optional.
	Log func(format string, args ...any)
}

// NewHTTPHandler builds the transport. Mount it at GitHTTPPrefix.
func NewHTTPHandler(cfg HTTPServerConfig) (http.Handler, error) {
	if cfg.Root == "" || cfg.Identify == nil {
		return nil, errors.New("git-agent HTTP transport needs a repo root and an identity resolver")
	}
	if cfg.Log == nil {
		cfg.Log = func(string, ...any) {}
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { cfg.serve(w, r) }), nil
}

func (cfg HTTPServerConfig) serve(w http.ResponseWriter, r *http.Request) {
	if strings.TrimPrefix(r.URL.Path, GitHTTPPrefix) == EnrollEndpoint {
		cfg.enroll(w, r)
		return
	}
	repoArg, endpoint, ok := splitGitPath(r.URL.Path)
	if !ok {
		// Deliberately not a 404: the dumb-HTTP paths (/objects/…, /HEAD,
		// /info/packs) would serve the whole repository to anyone who can reach
		// it, and saying "not found" invites a client to keep guessing.
		refuse(w, http.StatusForbidden, "this endpoint speaks "+receivePackService+" only (R2.3)")
		return
	}
	agent, err := cfg.Identify(r)
	if err != nil || agent == "" {
		refuse(w, http.StatusForbidden, "this request carries no agent identity, so it has no ref namespace to be confined to")
		return
	}
	repo, err := ResolveRepoPath(cfg.Root, repoArg)
	if err != nil {
		// ResolveRepoPath answers containment violations and missing
		// repositories alike; both are "there is nothing here for you".
		refuse(w, http.StatusNotFound, err.Error())
		return
	}
	switch {
	case r.Method == http.MethodGet && endpoint == "info/refs":
		cfg.advertise(w, r, repo, agent)
	case r.Method == http.MethodPost && endpoint == receivePackService:
		cfg.receivePack(w, r, repo, agent)
	case endpoint == uploadPackService:
		// A shared upload-pack leaks every task namespace to every enrolled
		// agent, which is the reason R2.3 exists.
		refuse(w, http.StatusForbidden, uploadPackService+" is not served (R2.3)")
	default:
		refuse(w, http.StatusMethodNotAllowed, r.Method+" is not served on "+endpoint)
	}
}

// enroll completes the bidirectional trust exchange.
//
// The agent is already authenticated by the time this runs — the token is what
// authorizes enrollment, and resolving it is what allocates or reclaims a pool
// member's name. This is idempotent by construction: a durable token admitted
// twice yields the same agent, which is what lets a restarting sidecar re-run
// its whole startup path unguarded.
func (cfg HTTPServerConfig) enroll(w http.ResponseWriter, r *http.Request) {
	if cfg.Enroll == nil {
		refuse(w, http.StatusNotFound, "this endpoint does not enroll agents")
		return
	}
	if r.Method != http.MethodPost {
		refuse(w, http.StatusMethodNotAllowed, r.Method+" is not served on "+EnrollEndpoint)
		return
	}
	var request EnrollRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, maxEnrollRequestBytes)).Decode(&request); err != nil {
		refuse(w, http.StatusBadRequest, "unparseable enrollment request: "+err.Error())
		return
	}
	agent, err := cfg.Identify(r)
	if err != nil || agent == "" {
		// The message carries the reason: unlike a push, an operator is
		// watching this one and a bare 403 gives them nothing to act on.
		refuse(w, http.StatusForbidden, enrollRefusalDetail(err))
		return
	}
	response, err := cfg.Enroll(r, agent, request)
	if err != nil {
		cfg.Log("git-agent enroll %s: %v", agent, err)
		refuse(w, http.StatusBadRequest, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(response); err != nil {
		cfg.Log("git-agent enroll %s: write response: %v", agent, err)
	}
}

// maxEnrollRequestBytes bounds an enrollment body. It carries a URL, a port and
// a fingerprint; anything larger is not a captain agent.
const maxEnrollRequestBytes = 64 << 10

func enrollRefusalDetail(err error) string {
	if err != nil {
		return "enrollment refused: " + err.Error()
	}
	return "enrollment refused: this request carries no agent identity"
}

// advertise answers the ref advertisement that opens a push. The advertisement
// includes side-band-64k, which is what carries the verdict feedback the hooks
// write — so nothing under admit.go or feedback.go changes for this transport.
func (cfg HTTPServerConfig) advertise(w http.ResponseWriter, r *http.Request, repo, agent string) {
	if service := r.URL.Query().Get("service"); service != receivePackService {
		refuse(w, http.StatusForbidden, "service "+service+" is not served: this endpoint speaks "+receivePackService+" only (R2.3)")
		return
	}
	proc := exec.CommandContext(r.Context(), "git", "receive-pack", "--stateless-rpc", "--advertise-refs", repo)
	proc.Env = cfg.processEnv(agent)
	var stderr strings.Builder
	proc.Stderr = &stderr
	advertisement, err := proc.Output()
	if err != nil {
		cfg.Log("git-agent advertise %s: %v: %s", repo, err, stderr.String())
		refuse(w, http.StatusInternalServerError, "cannot advertise refs for this repository")
		return
	}
	writeGitHeaders(w, "application/x-"+receivePackService+"-advertisement")
	// Smart HTTP frames the advertisement with a service banner and a flush
	// packet; without them git falls back to the dumb protocol and fails.
	if _, err := io.WriteString(w, pktLine("# service="+receivePackService+"\n")+"0000"); err != nil {
		return
	}
	_, _ = w.Write(advertisement)
}

// receivePack relays the push itself.
func (cfg HTTPServerConfig) receivePack(w http.ResponseWriter, r *http.Request, repo, agent string) {
	body, err := requestBody(r)
	if err != nil {
		refuse(w, http.StatusBadRequest, err.Error())
		return
	}
	defer body.Close()

	proc := exec.CommandContext(r.Context(), "git", "receive-pack", "--stateless-rpc", repo)
	proc.Env = cfg.processEnv(agent)
	proc.Stdin = body
	// Flushed per write so hook output reaches the client while the push is
	// still running. Buffering it would hold a rejection until the hooks
	// finished, and a prompt hook can take minutes.
	proc.Stdout = &flushWriter{writer: w, controller: http.NewResponseController(w)}
	var stderr strings.Builder
	proc.Stderr = &stderr

	writeGitHeaders(w, "application/x-"+receivePackService+"-result")
	if err := proc.Run(); err != nil {
		// The status is already sent, so the client learns of a failure through
		// the truncated stream. Everything a client can act on rides the
		// sideband; this is for the operator.
		cfg.Log("git-agent receive-pack %s for %s: %v: %s", repo, agent, err, stderr.String())
		return
	}
	if stderr.Len() > 0 {
		cfg.Log("git-agent receive-pack %s for %s: %s", repo, agent, stderr.String())
	}
}

// processEnv injects the identity the hook shims read, exactly as the SSH
// transport does — which is why the hooks need no knowledge of either.
func (cfg HTTPServerConfig) processEnv(agent string) []string {
	return envWith(os.Environ(), EnvAgentName+"="+agent, EnvRole+"="+string(cfg.Role))
}

// splitGitPath separates the repository from the endpoint it is being asked
// for. The repository can contain slashes, so it is taken as whatever precedes
// a known endpoint rather than as a fixed number of segments.
func splitGitPath(path string) (repo, endpoint string, ok bool) {
	trimmed, found := strings.CutPrefix(path, GitHTTPPrefix)
	if !found {
		return "", "", false
	}
	for _, candidate := range []string{"info/refs", receivePackService, uploadPackService} {
		if repo, found := strings.CutSuffix(trimmed, "/"+candidate); found && repo != "" {
			return repo, candidate, true
		}
	}
	return "", "", false
}

// requestBody unwraps the transfer encoding git may have applied. git gzips a
// push body when it is large enough, and receive-pack expects it decoded.
func requestBody(r *http.Request) (io.ReadCloser, error) {
	if !strings.EqualFold(strings.TrimSpace(r.Header.Get("Content-Encoding")), "gzip") {
		return r.Body, nil
	}
	reader, err := gzip.NewReader(r.Body)
	if err != nil {
		return nil, fmt.Errorf("decode gzipped push body: %w", err)
	}
	return reader, nil
}

func writeGitHeaders(w http.ResponseWriter, contentType string) {
	header := w.Header()
	header.Set("Content-Type", contentType)
	// Smart HTTP requires these: a caching proxy that served a stale ref
	// advertisement would make a push fail on refs that no longer exist.
	header.Set("Cache-Control", "no-cache, max-age=0, must-revalidate")
	header.Set("Pragma", "no-cache")
	header.Set("Expires", "Fri, 01 Jan 1980 00:00:00 GMT")
}

func refuse(w http.ResponseWriter, status int, message string) {
	http.Error(w, "captain: "+message, status)
}

// pktLine frames a string in git's length-prefixed packet format.
func pktLine(payload string) string {
	return fmt.Sprintf("%04x%s", len(payload)+4, payload)
}

// flushWriter pushes each write through to the client immediately, so sideband
// progress and hook rejections stream rather than arriving at the end.
type flushWriter struct {
	writer     io.Writer
	controller *http.ResponseController
}

func (f *flushWriter) Write(p []byte) (int, error) {
	n, err := f.writer.Write(p)
	if err != nil {
		return n, err
	}
	// A writer that cannot flush is not an error: the response still arrives,
	// it just arrives all at once.
	_ = f.controller.Flush()
	return n, nil
}
