package exectool

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/types"
	"github.com/flanksource/commons/logger"

	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/credproxy"
)

// releasedValue is one granted header's real value, as a credential release
// hands it to a sidecar-proxied tool's proxy.
type releasedValue struct {
	Grant  string `json:"grant"`
	Header string `json:"header"`
	Value  string `json:"value"`
}

// ServeGrantRelease serves, once, the real values of grants to the client that
// presents the release's token: the sidecar-proxied tool's proxy, fetching them
// through the service tunnel. Values are resolved from the supervisor's stores
// at the moment of release. After one successful release the listener closes,
// so a second reader — the agent replaying the tunnel, say — gets nothing.
func ServeGrantRelease(ctx context.Context, tool string, grants []credproxy.Grant) (api.CredentialRelease, func() error, error) {
	token, err := credproxy.NewSecret()
	if err != nil {
		return api.CredentialRelease{}, nil, err
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return api.CredentialRelease{}, nil, fmt.Errorf("credential release for %s: %w", tool, err)
	}
	dbctx := dbcontext.NewContext(ctx)
	var once sync.Once
	server := &http.Server{ReadHeaderTimeout: 30 * time.Second}
	server.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		presented, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if subtle.ConstantTimeCompare([]byte(presented), []byte(token)) != 1 {
			logger.Warnf("credential release for tools.%s: refused a request without the release token", tool)
			http.Error(w, "release token required", http.StatusUnauthorized)
			return
		}
		first := false
		once.Do(func() { first = true })
		if !first {
			logger.Warnf("credential release for tools.%s: refused a second release", tool)
			http.Error(w, "already released", http.StatusGone)
			return
		}
		// Whatever happens next, this release is spent and the server goes.
		defer func() { go func() { _ = server.Shutdown(context.Background()) }() }()
		values, err := resolveGrantValues(dbctx, grants)
		if err != nil {
			logger.Warnf("credential release for tools.%s: %v", tool, err)
			http.Error(w, "credential resolution failed", http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(values); err != nil {
			logger.Warnf("credential release for tools.%s: %v", tool, err)
			return
		}
		logger.Infof("credential release for tools.%s: released %s", tool, releasedNames(values))
	})
	go func() { _ = server.Serve(listener) }()
	return api.CredentialRelease{Addr: listener.Addr().String(), Token: token}, server.Close, nil
}

func resolveGrantValues(ctx dbcontext.Context, grants []credproxy.Grant) ([]releasedValue, error) {
	var values []releasedValue
	for _, grant := range grants {
		for _, header := range grant.Headers {
			value, err := ctx.GetEnvValueFromCache(header.Value, ctx.GetNamespace())
			if err != nil {
				return nil, fmt.Errorf("grant %s header %s: %w", grant.Name, header.Name, err)
			}
			values = append(values, releasedValue{Grant: grant.Name, Header: header.Name, Value: value})
		}
	}
	return values, nil
}

func releasedNames(values []releasedValue) string {
	names := make([]string, len(values))
	for i, value := range values {
		names[i] = value.Grant + "/" + value.Header
	}
	return strings.Join(names, ", ")
}

// FetchGrantRelease reads a credential release once and returns the tool's
// grants carrying the real values, in memory only. Every released value must
// fill a header the declaration grants, and every granted header must be
// filled: a release that disagrees with the declaration is refused.
func FetchGrantRelease(ctx context.Context, url, token string, grants []credproxy.Grant) ([]credproxy.Grant, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	response, err := (&http.Client{Timeout: 60 * time.Second}).Do(request)
	if err != nil {
		return nil, fmt.Errorf("fetch credential release: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 1024))
		return nil, fmt.Errorf("credential release refused (%s): %s", response.Status, strings.TrimSpace(string(body)))
	}
	var values []releasedValue
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&values); err != nil {
		return nil, fmt.Errorf("decode credential release: %w", err)
	}
	return withReleasedValues(grants, values)
}

func withReleasedValues(grants []credproxy.Grant, values []releasedValue) ([]credproxy.Grant, error) {
	byHeader := map[string]string{}
	for _, value := range values {
		byHeader[value.Grant+"\x00"+strings.ToLower(value.Header)] = value.Value
	}
	filled := make([]credproxy.Grant, len(grants))
	for i, grant := range grants {
		filled[i] = grant
		filled[i].Headers = make([]credproxy.HeaderGrant, len(grant.Headers))
		for j, header := range grant.Headers {
			key := grant.Name + "\x00" + strings.ToLower(header.Name)
			value, ok := byHeader[key]
			if !ok {
				return nil, fmt.Errorf("credential release carries no value for grant %s header %s", grant.Name, header.Name)
			}
			delete(byHeader, key)
			filled[i].Headers[j] = credproxy.HeaderGrant{Name: header.Name, Value: types.EnvVar{ValueStatic: value}}
		}
	}
	if len(byHeader) > 0 {
		return nil, errors.New("credential release carries values for headers the tool does not grant")
	}
	return filled, nil
}
