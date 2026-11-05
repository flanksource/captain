// Serves Substrate's CredentialProvider API, which the egress gateway asks for the credential an
// ate-secret:// URI names, from a function, so whoever serves the URIs resolves them.
package substrate

import (
	"context"

	credproviderpb "github.com/flanksource/captain/pkg/substrate/internal/credprovider"
	"google.golang.org/grpc"
)

// FetchSecret resolves uri, asked for on behalf of the actor actorSpiffeID, to the credential the
// egress gateway injects. The code of a gRPC status error tells the gateway whether to retry:
// Unavailable and DeadlineExceeded are retried, every other code refuses the request.
type FetchSecret func(ctx context.Context, uri, actorSpiffeID string) ([]byte, error)

// RegisterCredentialProvider serves fetch as server's CredentialProvider service.
func RegisterCredentialProvider(server *grpc.Server, fetch FetchSecret) {
	credproviderpb.RegisterCredentialProviderServer(server, credentialProvider{fetch: fetch})
}

type credentialProvider struct {
	credproviderpb.UnimplementedCredentialProviderServer
	fetch FetchSecret
}

func (p credentialProvider) FetchSecret(ctx context.Context, req *credproviderpb.FetchSecretRequest) (*credproviderpb.FetchSecretResponse, error) {
	value, err := p.fetch(ctx, req.GetUri(), req.GetActorSpiffeId())
	if err != nil {
		return nil, err
	}
	return &credproviderpb.FetchSecretResponse{OpaqueBytes: value}, nil
}
