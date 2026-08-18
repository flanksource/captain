// The client half of the HTTPS transport (§8). Both directions of a task ride
// `git push`, so selecting a transport means selecting the environment git
// runs under, not writing a second protocol.

package gitagent

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/flanksource/clicky/text"
)

// TransportTarget is everything a push needs to reach an endpoint. The URL's
// scheme selects the transport; the other transport's fields are ignored.
type TransportTarget struct {
	// URL is the push URL: ssh://captain@host:7422/repo.git, or
	// https://host:8080/git/repo.git.
	URL string

	// SSHCommand, KeyPath and HostFingerprint drive ssh://: captain's own
	// transport rather than system ssh, a captain-managed key, and a host key
	// pinned by fingerprint. An empty SSHCommand means this binary.
	SSHCommand      string
	KeyPath         string
	HostFingerprint string

	// Token, CAPath and PinnedPublicKey drive https://. CAPath is the
	// endpoint's own certificate, which is its own trust anchor; leaving it
	// empty means the system trust store, which is right for a real
	// certificate and fails loudly for a self-signed one.
	Token           text.SensitiveString
	CAPath          string
	PinnedPublicKey string
}

// TransportEnv prepares env for a push to target.
//
// Inherited git configuration is stripped first. The HTTPS transport carries
// its settings in GIT_CONFIG_COUNT/KEY_n/VALUE_n, and git reads those by index:
// an inherited count higher than the one written here would leave a stale
// KEY_n/VALUE_n pair in force, silently overriding the credential or the trust
// anchor this push depends on.
func TransportEnv(env []string, target TransportTarget) ([]string, error) {
	switch scheme := EndpointScheme(target.URL); scheme {
	case "https":
		pairs, err := httpsTransportPairs(target)
		if err != nil {
			return nil, err
		}
		return envWith(withoutGitConfigPairs(env), pairs...), nil
	case "http":
		// Refused rather than downgraded: the bearer token rides an
		// Authorization header, so http:// would put a durable credential on
		// the wire in clear text.
		return nil, fmt.Errorf("endpoint %q uses http://; a captain token would cross the network in clear text — use https://", target.URL)
	case "ssh":
		pairs, err := sshTransportPairs(target)
		if err != nil {
			return nil, err
		}
		return envWith(env, pairs...), nil
	default:
		return nil, fmt.Errorf("endpoint %q uses unsupported scheme %q; captain speaks ssh:// and https://", target.URL, scheme)
	}
}

// sshTransportPairs builds the env for a push riding captain's GIT_SSH_COMMAND
// transport: no system ssh, key from a captain-managed path, host key pinned by
// fingerprint.
func sshTransportPairs(target TransportTarget) ([]string, error) {
	command := target.SSHCommand
	if command == "" {
		exe, err := executablePath()
		if err != nil {
			return nil, err
		}
		command = SSHTransportCommand(exe)
	}
	return []string{
		"GIT_SSH_COMMAND=" + command,
		"GIT_SSH_VARIANT=ssh", // an unrecognized command defaults to "simple", which cannot pass -p
		EnvSSHKey + "=" + target.KeyPath,
		EnvSSHHostFingerprint + "=" + target.HostFingerprint,
	}, nil
}

// httpsTransportPairs renders the transport as git configuration injected
// through the environment, so nothing is written to a config file that could
// outlive the push or be read by another process's git.
func httpsTransportPairs(target TransportTarget) ([]string, error) {
	if target.Token.IsEmpty() {
		return nil, fmt.Errorf("pushing to %s needs a captain token; mint one with `captain token create`", target.URL)
	}
	scope, err := configScope(target.URL)
	if err != nil {
		return nil, err
	}
	// Every setting is scoped to this endpoint. An unscoped extraHeader would
	// offer the credential to any host git contacted during the push, and an
	// unscoped sslCAInfo would make this self-signed certificate a trust anchor
	// for every HTTPS URL git touched.
	settings := [][2]string{
		{"http." + scope + ".extraHeader", "Authorization: Bearer " + target.Token.Value()},
	}
	if target.CAPath != "" {
		settings = append(settings, [2]string{"http." + scope + ".sslCAInfo", target.CAPath})
	}
	if target.PinnedPublicKey != "" {
		settings = append(settings, [2]string{"http." + scope + ".pinnedPubkey", target.PinnedPublicKey})
	}
	pairs := []string{fmt.Sprintf("GIT_CONFIG_COUNT=%d", len(settings))}
	for i, setting := range settings {
		pairs = append(pairs,
			fmt.Sprintf("GIT_CONFIG_KEY_%d=%s", i, setting[0]),
			fmt.Sprintf("GIT_CONFIG_VALUE_%d=%s", i, setting[1]))
	}
	return pairs, nil
}

// configScope is the URL git matches a http.<url>.* setting against: the
// endpoint's origin, whose path "/" prefixes every repository under it.
func configScope(pushURL string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(pushURL))
	if err != nil || parsed.Host == "" {
		return "", fmt.Errorf("push URL %q must be https://host[:port]/path", pushURL)
	}
	return parsed.Scheme + "://" + parsed.Host + "/", nil
}

// withoutGitConfigPairs drops inherited GIT_CONFIG_COUNT/KEY_n/VALUE_n.
//
// Deliberately not every GIT_CONFIG_* variable: GIT_CONFIG_GLOBAL and
// GIT_CONFIG_SYSTEM are how a sandbox or a test isolates git from the real
// user configuration, and dropping them would silently re-admit it.
func withoutGitConfigPairs(env []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		name, _, _ := strings.Cut(kv, "=")
		if name == "GIT_CONFIG_COUNT" ||
			strings.HasPrefix(name, "GIT_CONFIG_KEY_") ||
			strings.HasPrefix(name, "GIT_CONFIG_VALUE_") {
			continue
		}
		out = append(out, kv)
	}
	return out
}

// EndpointScheme reports which transport an endpoint selects. An endpoint with
// no scheme is ssh, which is the form written before HTTPS existed.
func EndpointScheme(endpoint string) string {
	scheme, _, found := strings.Cut(strings.TrimSpace(endpoint), "://")
	if !found {
		return "ssh"
	}
	return strings.ToLower(scheme)
}

// HTTPSRepoURL joins an https endpoint with a repository path, placing it under
// the transport's prefix.
func HTTPSRepoURL(endpoint, repoPath string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(endpoint))
	if err != nil || parsed.Host == "" {
		return "", fmt.Errorf("endpoint %q must be https://host[:port]", endpoint)
	}
	if parsed.Scheme != "https" {
		return "", fmt.Errorf("endpoint %q must use https://", endpoint)
	}
	if repoPath = strings.TrimPrefix(strings.TrimSpace(repoPath), "/"); repoPath == "" {
		return "", fmt.Errorf("endpoint %q needs a repository path", endpoint)
	}
	return parsed.Scheme + "://" + parsed.Host + GitHTTPPrefix + repoPath, nil
}
