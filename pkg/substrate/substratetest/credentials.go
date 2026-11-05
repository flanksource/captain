// Asks a credential provider for a credential as Substrate's egress gateway does, so a provider's
// tests reach it over gRPC without Substrate's generated API.
package substratetest

import (
	"context"

	credproviderpb "github.com/flanksource/captain/pkg/substrate/internal/credprovider"
	"google.golang.org/grpc"
)

// FetchSecret asks the CredentialProvider at conn for the credential uri names, on behalf of the
// actor actorSpiffeID.
func FetchSecret(ctx context.Context, conn grpc.ClientConnInterface, uri, actorSpiffeID string) ([]byte, error) {
	response, err := credproviderpb.NewCredentialProviderClient(conn).FetchSecret(ctx, &credproviderpb.FetchSecretRequest{Uri: uri, ActorSpiffeId: actorSpiffeID})
	if err != nil {
		return nil, err
	}
	return response.GetOpaqueBytes(), nil
}
