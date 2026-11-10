// The substrate client against a live cluster: files go in and come out of an environment intact,
// whatever their size, a process in it works on them, and the caller may list what a sweep and an inventory need.
package substrate_test

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"io"
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

	env, err := client.Create(ctx, substrate.Environment{ID: "captain-e2e-" + uuid.NewString()[:8]})
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

// fiftyMB is the size of the file TestLargeFilesRoundTripAgainstACluster moves each way: larger
// than any buffer between the client and the guest, so a proxy that buffers or cuts a stream
// shows.
const fiftyMB = 50 << 20

func TestLargeFilesRoundTripAgainstACluster(t *testing.T) {
	cluster := substratetest.FromEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	client, err := substrate.Dial(cluster.Options)
	require.NoError(t, err)
	defer client.Close()

	env, err := client.Create(ctx, substrate.Environment{ID: "captain-e2e-" + uuid.NewString()[:8]})
	require.NoError(t, err)
	defer func() { assert.NoError(t, env.Delete(context.WithoutCancel(ctx))) }()

	sent := sha256.New()
	start := time.Now()
	require.NoError(t, env.WriteFile(ctx, "/tmp/captain-e2e/large.bin", 0o644, io.TeeReader(io.LimitReader(rand.Reader, fiftyMB), sent)))
	t.Logf("wrote %d MiB in %s", fiftyMB>>20, time.Since(start))
	want := hex.EncodeToString(sent.Sum(nil))

	proc, err := env.Start(ctx, substrate.Process{Command: []string{"sh", "-c", "sha256sum large.bin | cut -d' ' -f1"}, Cwd: "/tmp/captain-e2e"})
	require.NoError(t, err)
	assert.Equal(t, want+"\n", readAll(t, proc.Output(ctx)), "the file arrived intact")

	start = time.Now()
	file, err := env.ReadFile(ctx, "/tmp/captain-e2e/large.bin")
	require.NoError(t, err)
	received := sha256.New()
	n, err := io.Copy(received, file)
	require.NoError(t, err)
	require.NoError(t, file.Close())
	t.Logf("read %d MiB in %s", n>>20, time.Since(start))
	assert.Equal(t, int64(fiftyMB), n)
	assert.Equal(t, want, hex.EncodeToString(received.Sum(nil)), "the file came back intact")
}

// A caller's identity may list the atespace's actors, as a sweep must. The prefix matches no
// environment, so nothing is deleted.
func TestSweepListsTheActorsOfACluster(t *testing.T) {
	cluster := substratetest.FromEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	client, err := substrate.Dial(cluster.Options)
	require.NoError(t, err)
	defer client.Close()

	swept, err := client.Sweep(ctx, "captain-e2e-no-such-prefix-", time.Now())
	require.NoError(t, err)
	assert.Empty(t, swept)
}

// A caller's identity may list the cluster's workers and the actors of every atespace, as an
// inventory of the substrate must.
func TestInventoryOfACluster(t *testing.T) {
	cluster := substratetest.FromEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	client, err := substrate.Dial(cluster.Options)
	require.NoError(t, err)
	defer client.Close()

	workers, err := client.ListWorkers(ctx)
	require.NoError(t, err)
	assert.NotEmpty(t, workers, "a cluster that runs environments has workers")
	_, err = client.ListActors(ctx, "")
	require.NoError(t, err)
}
