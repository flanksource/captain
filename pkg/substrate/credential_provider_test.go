// Specs for RegisterCredentialProvider over real gRPC, asked as the egress gateway asks: the fetch
// function's credential is the answer, and its gRPC status reaches the gateway unchanged.
package substrate_test

import (
	"context"
	"errors"
	"net"
	"testing"

	"github.com/flanksource/captain/pkg/substrate"
	"github.com/flanksource/captain/pkg/substrate/substratetest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
)

// serveCredentials serves fetch as a credential provider and returns a connection to it.
func serveCredentials(t *testing.T, fetch substrate.FetchSecret) *grpc.ClientConn {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	server := grpc.NewServer()
	substrate.RegisterCredentialProvider(server, fetch)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)
	conn, err := grpc.NewClient(listener.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func TestTheCredentialProviderAnswersWithWhatFetchResolves(t *testing.T) {
	var askedURI, askedActor string
	conn := serveCredentials(t, func(_ context.Context, uri, actor string) ([]byte, error) {
		askedURI, askedActor = uri, actor
		return []byte("sk-ant-key"), nil
	})

	value, err := substratetest.FetchSecret(testContext(t), conn, "ate-secret://gavel/beta/run-1/anthropic", "spiffe://cluster.local/ns/ate-env/sa/run-1")
	require.NoError(t, err)
	assert.Equal(t, "sk-ant-key", string(value))
	assert.Equal(t, "ate-secret://gavel/beta/run-1/anthropic", askedURI)
	assert.Equal(t, "spiffe://cluster.local/ns/ate-env/sa/run-1", askedActor)
}

func TestTheCredentialProviderPassesFetchsCodeToTheGateway(t *testing.T) {
	conn := serveCredentials(t, func(context.Context, string, string) ([]byte, error) {
		return nil, status.Error(codes.Unavailable, "gavel beta is unreachable")
	})

	_, err := substratetest.FetchSecret(testContext(t), conn, "ate-secret://gavel/beta/run-1/anthropic", "spiffe://cluster.local/ns/ate-env/sa/run-1")
	assert.Equal(t, codes.Unavailable, status.Code(err))
	assert.Equal(t, "gavel beta is unreachable", status.Convert(err).Message())
}

func TestTheCredentialProviderRefusesWhenFetchFailsWithoutACode(t *testing.T) {
	conn := serveCredentials(t, func(context.Context, string, string) ([]byte, error) {
		return nil, errors.New("no key for run-1")
	})

	_, err := substratetest.FetchSecret(testContext(t), conn, "ate-secret://gavel/beta/run-1/anthropic", "spiffe://cluster.local/ns/ate-env/sa/run-1")
	assert.Equal(t, codes.Unknown, status.Code(err))
	assert.Equal(t, "no key for run-1", status.Convert(err).Message())
}
