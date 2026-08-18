// Enrollment over HTTPS (§8). Same exchange as the SSH one, same response —
// only the channel and how the supervisor's identity is proven differ.

package gitagent

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// enrollHTTPS presents a captain token to an HTTPS supervisor and returns what
// it offers back, including the certificate the agent's later relays verify
// against.
//
// Trust is pinned, never taken on first use: pin is the sha256// public-key pin
// printed by `git-agent add`, and it is the HTTPS counterpart of the SSH host
// fingerprint. A supervisor that presents a different certificate is refused
// before the token is sent, so a credential is never handed to an impostor.
func enrollHTTPS(ctx context.Context, endpoint, token, pin string, req EnrollRequest) (*EnrollResponse, error) {
	pin = strings.TrimSpace(pin)
	if pin == "" {
		return nil, fmt.Errorf("enrollment requires the supervisor's certificate pin (printed by `captain sandbox git-agent add`)")
	}
	url, err := HTTPSRepoURL(endpoint, EnrollEndpoint)
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+token)

	response, err := pinnedClient(pin).Do(request)
	if err != nil {
		return nil, fmt.Errorf("enrollment refused: %w", err)
	}
	defer response.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(response.Body, maxEnrollRequestBytes))
	if err != nil {
		return nil, err
	}
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("enrollment refused: %s", strings.TrimSpace(string(payload)))
	}
	var resp EnrollResponse
	if err := json.Unmarshal(payload, &resp); err != nil {
		return nil, fmt.Errorf("unparseable enrollment response %q: %w", strings.TrimSpace(string(payload)), err)
	}
	if resp.Agent == "" || resp.DispatchKey == "" {
		return nil, fmt.Errorf("enrollment response is missing the agent name or the supervisor's dispatch key")
	}
	return &resp, nil
}

// pinnedClient verifies the server by its public-key pin rather than by a
// chain, because a self-signed supervisor has no chain and the agent has not
// been handed its certificate yet — that is what this exchange delivers.
func pinnedClient(pin string) *http.Client {
	return &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{
			// Verification is not skipped, it is replaced: the callback below
			// is the whole check, and it is stricter than a chain walk.
			InsecureSkipVerify: true, //nolint:gosec // VerifyPeerCertificate pins the exact key
			MinVersion:         tls.VersionTLS12,
			VerifyPeerCertificate: func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
				if len(rawCerts) == 0 {
					return fmt.Errorf("supervisor presented no certificate")
				}
				leaf, err := x509.ParseCertificate(rawCerts[0])
				if err != nil {
					return fmt.Errorf("parse supervisor certificate: %w", err)
				}
				got, err := publicKeyPin(leaf)
				if err != nil {
					return err
				}
				if got != pin {
					return fmt.Errorf("supervisor certificate pin %s does not match the pinned %s", got, pin)
				}
				return nil
			},
		}},
	}
}
