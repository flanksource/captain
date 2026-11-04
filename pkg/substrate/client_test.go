// Specs for the substrate client against an in-process Substrate: environments, their egress
// policies, files in and out, and processes whose output, exit and kill are real.
package substrate_test

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/flanksource/captain/pkg/substrate"
	"github.com/flanksource/captain/pkg/substrate/substratetest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

var anthropic = substrate.Credential{
	Host: "api.anthropic.com", Header: "x-api-key", Env: "ANTHROPIC_API_KEY",
	URI: "ate-secret://k8s.io/captain/anthropic/key",
}

func dial(t *testing.T, server *substratetest.Server, configure ...func(*substrate.Options)) *substrate.Client {
	t.Helper()
	options := server.Options()
	for _, apply := range configure {
		apply(&options)
	}
	client, err := substrate.Dial(options)
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })
	return client
}

func testContext(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func readAll(t *testing.T, r io.ReadCloser) string {
	t.Helper()
	defer r.Close()
	data, err := io.ReadAll(r)
	require.NoError(t, err)
	return string(data)
}

func TestCreateMakesTheEnvironmentAndItsEgressPolicy(t *testing.T) {
	server := substratetest.Start(t)
	client := dial(t, server, func(o *substrate.Options) { o.Template = "captain-runner" })

	env, err := client.Create(testContext(t), substrate.Environment{
		ID: "run-1",
		Egress: substrate.Egress{Passthrough: []substrate.HostPort{{Host: "github.com", Port: 443}}, Credentials: []substrate.Credential{anthropic}},
	})
	require.NoError(t, err)
	assert.Equal(t, "run-1", env.ID())
	assert.Equal(t, "spiffe://substrate-actor.local/actor/ate-env/run-1", env.Actor())

	created, ok := server.Environment("ate-env", "run-1")
	require.True(t, ok, "the environment is created in the default atespace")
	assert.Equal(t, "captain-runner", created.Template)
	policy, ok := server.Policy("ate-env", "run-1")
	require.True(t, ok, "the environment's actor gets its egress policy")
	assert.Len(t, policy.GetRules(), 2)
}

func TestCreateDeletesTheEnvironmentWhenItsPolicyIsRefused(t *testing.T) {
	server := substratetest.Start(t)
	server.RefusePolicy = status.Error(codes.PermissionDenied, "not an editor of atespace ate-env")

	_, err := dial(t, server).Create(testContext(t), substrate.Environment{ID: "run-1"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "set the egress policy of substrate environment ate-env/run-1")
	assert.Contains(t, err.Error(), "not an editor of atespace ate-env")
	assert.Equal(t, []string{"ate-env/run-1"}, server.Deleted(), "an environment without its policy is never left behind")
}

func TestFilesGoInAndOutAndTheProcessRunsOnThem(t *testing.T) {
	server := substratetest.Start(t)
	ctx := testContext(t)
	env, err := dial(t, server).Create(ctx, substrate.Environment{ID: "run-1"})
	require.NoError(t, err)

	require.NoError(t, env.WriteFile(ctx, "in/task.txt", 0o644, strings.NewReader("the task\n")))
	proc, err := env.Start(ctx, substrate.Process{
		Command: []string{"sh", "-c", "cat task.txt; echo working >&2; mkdir -p ../out; echo done > ../out/result.txt; exit 3"},
		Cwd:     "in",
	})
	require.NoError(t, err)

	output := readAll(t, proc.Output(ctx))
	assert.Contains(t, output, "the task\n")
	assert.Contains(t, output, "working\n")
	exit, err := proc.Wait(ctx)
	require.NoError(t, err)
	assert.Equal(t, 3, exit)

	result, err := env.ReadFile(ctx, "out/result.txt")
	require.NoError(t, err)
	assert.Equal(t, "done\n", readAll(t, result))
}

func TestReadingAMissingFileSaysWhichFile(t *testing.T) {
	server := substratetest.Start(t)
	ctx := testContext(t)
	env, err := dial(t, server).Create(ctx, substrate.Environment{ID: "run-1"})
	require.NoError(t, err)

	file, err := env.ReadFile(ctx, "out/response.json")
	if err == nil {
		_, err = io.ReadAll(file)
	}
	require.Error(t, err)
	assert.Contains(t, err.Error(), "read out/response.json from substrate environment ate-env/run-1")
	assert.Equal(t, codes.NotFound, status.Code(errors.Unwrap(err)))
}

func TestOutputReconnectsWhenTheRouterEndsTheStream(t *testing.T) {
	server := substratetest.Start(t)
	server.StreamLimit = 1
	ctx := testContext(t)
	env, err := dial(t, server).Create(ctx, substrate.Environment{ID: "run-1"})
	require.NoError(t, err)

	proc, err := env.Start(ctx, substrate.Process{Command: []string{"sh", "-c", "for i in 1 2 3 4 5; do echo line $i; sleep 0.05; done"}})
	require.NoError(t, err)
	assert.Equal(t, "line 1\nline 2\nline 3\nline 4\nline 5\n", readAll(t, proc.Output(ctx)), "each line exactly once, across reconnections")
}

func TestTheProcessHoldsOnlyPlaceholdersForInjectedCredentials(t *testing.T) {
	server := substratetest.Start(t)
	ctx := testContext(t)
	client := dial(t, server, func(o *substrate.Options) { o.TrustBundle = "/run/ate/egress-trust-bundle/trust-bundle.pem" })
	env, err := client.Create(ctx, substrate.Environment{ID: "run-1", Egress: substrate.Egress{Credentials: []substrate.Credential{anthropic}}})
	require.NoError(t, err)

	proc, err := env.Start(ctx, substrate.Process{Command: []string{"sh", "-c", `echo "$ANTHROPIC_API_KEY $NODE_EXTRA_CA_CERTS $TASK"`}, Env: map[string]string{"TASK": "review"}})
	require.NoError(t, err)
	assert.Equal(t, substrate.Placeholder+" /run/ate/egress-trust-bundle/trust-bundle.pem review\n", readAll(t, proc.Output(ctx)))

	_, err = env.Start(ctx, substrate.Process{Command: []string{"true"}, Env: map[string]string{"ANTHROPIC_API_KEY": "sk-ant-api-real"}})
	require.EqualError(t, err, "ANTHROPIC_API_KEY would enter substrate environment ate-env/run-1; the egress gateway injects api.anthropic.com's credential, so the process gets a placeholder")
}

func TestKillStopsTheProcessAndToleratesOneThatExited(t *testing.T) {
	server := substratetest.Start(t)
	ctx := testContext(t)
	env, err := dial(t, server).Create(ctx, substrate.Environment{ID: "run-1"})
	require.NoError(t, err)

	proc, err := env.Start(ctx, substrate.Process{Command: []string{"sleep", "60"}})
	require.NoError(t, err)
	require.NoError(t, proc.Kill(ctx))
	exit, err := proc.Wait(ctx)
	require.NoError(t, err)
	assert.Equal(t, 137, exit, "SIGKILL")
	require.NoError(t, proc.Kill(ctx), "killing a process that already exited is not an error")
}

func TestDeleteRemovesTheEnvironment(t *testing.T) {
	server := substratetest.Start(t)
	ctx := testContext(t)
	env, err := dial(t, server).Create(ctx, substrate.Environment{ID: "run-1"})
	require.NoError(t, err)

	require.NoError(t, env.Delete(ctx))
	_, live := server.Environment("ate-env", "run-1")
	assert.False(t, live)
	err = env.Delete(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "delete substrate environment ate-env/run-1")
}

func TestDeleteSaysWhySubstrateRefusedIt(t *testing.T) {
	server := substratetest.Start(t)
	ctx := testContext(t)
	env, err := dial(t, server).Create(ctx, substrate.Environment{ID: "run-1"})
	require.NoError(t, err)

	server.RefuseDelete = status.Error(codes.Internal, "object-store plugin upload failed")
	require.EqualError(t, env.Delete(ctx), "delete substrate environment ate-env/run-1: rpc error: code = Internal desc = object-store plugin upload failed")
	_, live := server.Environment("ate-env", "run-1")
	assert.True(t, live, "a refused delete leaves the environment")
}

func TestDialNeedsBothAPIs(t *testing.T) {
	_, err := substrate.Dial(substrate.Options{EnvAPI: "env:7777"})
	require.EqualError(t, err, "substrate needs the ate-env-api and ate-api-server addresses")
}

func TestInputReachesAProcessStartedWithStdin(t *testing.T) {
	server := substratetest.Start(t)
	ctx := testContext(t)
	env, err := dial(t, server).Create(ctx, substrate.Environment{ID: "run-1"})
	require.NoError(t, err)

	proc, err := env.Start(ctx, substrate.Process{Command: []string{"sh", "-c", "while read line; do echo got $line; done; echo closed"}, Stdin: true})
	require.NoError(t, err)
	input := proc.Input(ctx)
	_, err = io.WriteString(input, "first\n")
	require.NoError(t, err)
	_, err = io.WriteString(input, "second\n")
	require.NoError(t, err)
	require.NoError(t, input.Close())
	assert.Equal(t, "got first\ngot second\nclosed\n", readAll(t, proc.Stdout(ctx)))
}

func TestInputIsRefusedForAProcessWithoutStdin(t *testing.T) {
	server := substratetest.Start(t)
	ctx := testContext(t)
	env, err := dial(t, server).Create(ctx, substrate.Environment{ID: "run-1"})
	require.NoError(t, err)

	proc, err := env.Start(ctx, substrate.Process{Command: []string{"sleep", "60"}})
	require.NoError(t, err)
	_, err = io.WriteString(proc.Input(ctx), "ignored\n")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "write to the stdin of "+proc.ID()+" in substrate environment ate-env/run-1")
	require.NoError(t, proc.Kill(ctx))
}

func TestStdoutAndStderrAreFollowedApart(t *testing.T) {
	server := substratetest.Start(t)
	server.StreamLimit = 1
	ctx := testContext(t)
	env, err := dial(t, server).Create(ctx, substrate.Environment{ID: "run-1"})
	require.NoError(t, err)

	proc, err := env.Start(ctx, substrate.Process{Command: []string{"sh", "-c", "for i in 1 2 3; do echo out $i; echo err $i >&2; sleep 0.05; done"}})
	require.NoError(t, err)
	stderr := proc.Stderr(ctx)
	assert.Equal(t, "out 1\nout 2\nout 3\n", readAll(t, proc.Stdout(ctx)))
	assert.Equal(t, "err 1\nerr 2\nerr 3\n", readAll(t, stderr))
}

func TestSweepDeletesTheEnvironmentsLeftBehind(t *testing.T) {
	server := substratetest.Start(t)
	server.ListPageSize = 1
	ctx := testContext(t)
	client := dial(t, server)
	for _, id := range []string{"captain-run-left-1", "captain-run-left-2", "gavel-run-1"} {
		_, err := client.Create(ctx, substrate.Environment{ID: id})
		require.NoError(t, err)
	}
	cutoff := time.Now()
	_, err := client.Create(ctx, substrate.Environment{ID: "captain-run-live"})
	require.NoError(t, err)

	swept, err := client.Sweep(ctx, "captain-run-", cutoff)
	require.NoError(t, err)
	assert.Equal(t, []string{"captain-run-left-1", "captain-run-left-2"}, swept)
	assert.ElementsMatch(t, []string{"ate-env/captain-run-left-1", "ate-env/captain-run-left-2"}, server.Deleted())
	for _, live := range []string{"captain-run-live", "gavel-run-1"} {
		_, ok := server.Environment("ate-env", live)
		assert.True(t, ok, "%s is newer than the cutoff or isn't the prefix's", live)
	}
}

func TestSweepSaysWhichEnvironmentItCouldNotDelete(t *testing.T) {
	server := substratetest.Start(t)
	ctx := testContext(t)
	client := dial(t, server)
	_, err := client.Create(ctx, substrate.Environment{ID: "captain-run-left-1"})
	require.NoError(t, err)
	server.RefuseDelete = status.Error(codes.Internal, "object-store plugin upload failed")

	_, err = client.Sweep(ctx, "captain-run-", time.Now())
	require.EqualError(t, err, "delete substrate environment ate-env/captain-run-left-1: rpc error: code = Internal desc = object-store plugin upload failed")
}
