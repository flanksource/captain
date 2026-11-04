// A substrate backend's options, as ~/.captain.yaml spells them: how to reach the substrate, the
// image the environment runs, what it may reach, and the program that runs the run in it.
package substraterun

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/substrate"
)

// backendOptions is what a substrate backend's options say, keyed as ~/.captain.yaml keys them.
type backendOptions struct {
	EnvAPI      string              `json:"envAPI"`
	Control     string              `json:"control"`
	TokenFile   string              `json:"tokenFile"`
	CAFile      string              `json:"caFile"`
	ClientCert  string              `json:"clientCert"`
	ClientKey   string              `json:"clientKey"`
	Atespace    string              `json:"atespace"`
	Template    string              `json:"template"`
	Image       string              `json:"image"`
	TrustBundle string              `json:"trustBundle"`
	Egress      []string            `json:"egress"`
	Credentials []credentialOptions `json:"credentials"`
	Command     *[]string           `json:"command"`
	// MaxLifetime is the longest a run may last. An environment of the backend's older than that
	// was left behind by a caller that died, and is swept.
	MaxLifetime string `json:"maxLifetime"`
	// Environment and Ref are a run's own, for a caller that builds each run's selection: the ID
	// of the run's environment, and the commit the run starts from instead of the caller's
	// checkout.
	Environment string `json:"environment"`
	Ref         string `json:"ref"`
	// Branch is a run's own: the branch its commits come back on, in the run's repository.
	Branch string `json:"branch"`
	// Env is a run's own variables for the environment's program, such as a host's run token. It
	// is set on the process alone, never written into the environment's files.
	Env map[string]string `json:"env"`
}

// credentialOptions names a model key the egress gateway injects, and the URI the cluster's
// credential provider resolves it from: by whose it is and what kind (provider, auth), or in full
// (host, header, prefix, env), for a host whose keys aren't one of substrate's kinds.
type credentialOptions struct {
	Provider string `json:"provider"`
	Auth     string `json:"auth"`
	Host     string `json:"host"`
	Header   string `json:"header"`
	Prefix   string `json:"prefix"`
	Env      string `json:"env"`
	URI      string `json:"uri"`
}

// errCredentialHalfNamed is a credential named neither by kind nor in full.
var errCredentialHalfNamed = errors.New("names the host, header and env the gateway injects into, or a provider and auth, not some of them")

// credential is the credential named, without its URI.
func (c credentialOptions) credential() (substrate.Credential, error) {
	byKind := c.Provider != "" || c.Auth != ""
	inFull := c.Host != "" || c.Header != "" || c.Prefix != "" || c.Env != ""
	if byKind == inFull || (inFull && (c.Host == "" || c.Header == "" || c.Env == "")) {
		return substrate.Credential{}, errCredentialHalfNamed
	}
	if inFull {
		return substrate.Credential{Host: c.Host, Header: c.Header, Prefix: c.Prefix, Env: c.Env}, nil
	}
	return substrate.CredentialFor(c.Provider, c.Auth)
}

// options is a decoded substrate backend.
type options struct {
	client  substrate.Options
	image   string
	egress  substrate.Egress
	command []string
	// environment is the run's environment ID; empty names it after the run.
	environment string
	// ref is the commit the run starts from; empty starts it from the caller's checkout.
	ref string
	// maxLifetime is the longest a run may last, after which its environment may be swept.
	maxLifetime time.Duration
	// env is the run's own variables for the environment's program.
	env map[string]string
	// branch is the branch the run's commits come back on; empty names it after the run.
	branch string
}

// defaultMaxLifetime is a backend's maxLifetime when it sets none.
const defaultMaxLifetime = 24 * time.Hour

// environmentID is the form of an environment's ID, which substrate names its actor by.
var environmentID = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

// runTask is the program the environment runs by default: captain's own side of a whole run.
var runTask = []string{"captain", "sandbox", "substrate", "run-task"}

// decodeOptions decodes cfg's options, refusing an unknown key, a missing required one, an image
// not pinned by digest and a credential the egress gateway can't inject. Each error names the
// backend.
func decodeOptions(cfg api.SandboxConfig) (options, error) {
	decoded, err := decode(cfg.Options)
	if err != nil {
		return options{}, fmt.Errorf("sandbox backend %q: %w", cfg.Name, err)
	}
	return decoded, nil
}

func decode(raw map[string]any) (options, error) {
	if err := knownKeys(raw); err != nil {
		return options{}, err
	}
	data, err := json.Marshal(raw)
	if err != nil {
		return options{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var in backendOptions
	if err := decoder.Decode(&in); err != nil {
		return options{}, err
	}
	switch {
	case in.EnvAPI == "":
		return options{}, errors.New("envAPI is required")
	case in.Control == "":
		return options{}, errors.New("control is required")
	case in.Image == "":
		return options{}, errors.New("image is required")
	case (in.ClientCert == "") != (in.ClientKey == ""):
		return options{}, errors.New("clientCert and clientKey go together")
	case in.Command != nil && len(*in.Command) == 0:
		return options{}, errors.New("command, when set, names the program the environment runs")
	case in.Environment != "" && !environmentID.MatchString(in.Environment):
		return options{}, fmt.Errorf("environment %q must be lowercase letters, digits and '-', starting with a letter", in.Environment)
	}
	if err := substrate.PinnedImage(in.Image); err != nil {
		return options{}, fmt.Errorf("image: %w", err)
	}
	maxLifetime := defaultMaxLifetime
	if in.MaxLifetime != "" {
		var err error
		if maxLifetime, err = time.ParseDuration(in.MaxLifetime); err != nil {
			return options{}, fmt.Errorf("maxLifetime %q: %w", in.MaxLifetime, err)
		}
		if maxLifetime <= 0 {
			return options{}, fmt.Errorf("maxLifetime must be positive, got %q", in.MaxLifetime)
		}
	}
	out := options{
		client: substrate.Options{
			EnvAPI: in.EnvAPI, Control: in.Control, ControlTokenFile: in.TokenFile, CAFile: in.CAFile,
			ClientCertFile: in.ClientCert, ClientKeyFile: in.ClientKey, Atespace: in.Atespace,
			Template: in.Template, TrustBundle: in.TrustBundle,
		},
		image:       in.Image,
		command:     runTask,
		environment: in.Environment,
		ref:         in.Ref,
		maxLifetime: maxLifetime,
		env:         in.Env,
		branch:      in.Branch,
	}
	if out.client.Atespace == "" {
		out.client.Atespace = "ate-env"
	}
	if in.Command != nil {
		out.command = *in.Command
	}
	for _, entry := range in.Egress {
		if err := addEgress(&out.egress, entry); err != nil {
			return options{}, fmt.Errorf("egress %q: %w", entry, err)
		}
	}
	for i, named := range in.Credentials {
		credential, err := named.credential()
		if errors.Is(err, errCredentialHalfNamed) {
			return options{}, fmt.Errorf("credentials[%d] %w", i, err)
		}
		if err != nil {
			return options{}, fmt.Errorf("credentials[%d]: %w", i, err)
		}
		if named.URI == "" {
			return options{}, fmt.Errorf("credentials[%d] (%s %s) needs the uri the egress gateway resolves its key from", i, named.Provider, named.Auth)
		}
		credential.URI = named.URI
		out.egress.Credentials = append(out.egress.Credentials, credential)
	}
	return out, nil
}

// addEgress adds what entry lets the environment reach: a host over TLS on 443, host:port over
// TLS on that port, or http://host[:port] in cleartext, on 80 unless it names a port. TLS passes
// through the egress gateway undecrypted.
func addEgress(egress *substrate.Egress, entry string) error {
	rest, cleartext := strings.CutPrefix(entry, "http://")
	if !cleartext && strings.Contains(entry, "://") {
		return errors.New("a host, host:port, or http://host[:port]")
	}
	port := 443
	if cleartext {
		port = 80
	}
	host := strings.TrimSuffix(rest, "/")
	if name, number, ok := strings.Cut(host, ":"); ok {
		parsed, err := strconv.Atoi(number)
		if err != nil {
			return errors.New("the port must be a number")
		}
		host, port = name, parsed
	}
	if cleartext {
		egress.HTTP = append(egress.HTTP, substrate.HostPort{Host: host, Port: port})
	} else {
		egress.Passthrough = append(egress.Passthrough, substrate.HostPort{Host: host, Port: port})
	}
	return nil
}

// knownKeys refuses a key the options don't have. JSON matches keys to fields regardless of
// case, which would take a misspelt key for the right one.
func knownKeys(raw map[string]any) error {
	if err := keysOf(raw, reflect.TypeFor[backendOptions](), ""); err != nil {
		return err
	}
	credentials, _ := raw["credentials"].([]any)
	for i, credential := range credentials {
		if named, ok := credential.(map[string]any); ok {
			if err := keysOf(named, reflect.TypeFor[credentialOptions](), fmt.Sprintf("credentials[%d].", i)); err != nil {
				return err
			}
		}
	}
	return nil
}

func keysOf(raw map[string]any, of reflect.Type, prefix string) error {
	known := map[string]bool{}
	for field := range of.Fields() {
		known[field.Tag.Get("json")] = true
	}
	for _, key := range slices.Sorted(maps.Keys(raw)) {
		if !known[key] {
			return fmt.Errorf("unknown option %q", prefix+key)
		}
	}
	return nil
}
