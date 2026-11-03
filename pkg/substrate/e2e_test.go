// The substrate client against a live cluster: an environment is created, a file goes in, a
// process turns it into another that comes out, and the environment is deleted.
package substrate_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/flanksource/captain/pkg/substrate"
	"github.com/flanksource/captain/pkg/substrate/substratetest"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAnEnvironmentAgainstACluster(t *testing.T) {
	cluster := substratetest.FromEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	client, err := substrate.Dial(cluster.Options)
	require.NoError(t, err)
	defer client.Close()

	env, err := client.Create(ctx, substrate.Environment{ID: "captain-e2e-" + uuid.NewString()[:8], Image: cluster.Image})
	require.NoError(t, err)
	defer func() { assert.NoError(t, env.Delete(context.WithoutCancel(ctx))) }()

	require.NoError(t, env.WriteFile(ctx, "/tmp/captain-e2e/in.txt", 0o644, strings.NewReader("from the caller\n")))
	proc, err := env.Start(ctx, substrate.Process{Command: []string{"sh", "-c", "cat in.txt; tr a-z A-Z < in.txt > out.txt"}, Cwd: "/tmp/captain-e2e"})
	require.NoError(t, err)
	output := readAll(t, proc.Output(ctx))
	exit, err := proc.Wait(ctx)
	require.NoError(t, err)
	assert.Equal(t, 0, exit, "output: %s", output)
	assert.Equal(t, "from the caller\n", output)

	out, err := env.ReadFile(ctx, "/tmp/captain-e2e/out.txt")
	require.NoError(t, err)
	assert.Equal(t, "FROM THE CALLER\n", readAll(t, out))
}
