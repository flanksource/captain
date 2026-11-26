// Specs for the actor templates of a client's atespace, against an in-process Substrate: reading,
// listing, copying one at another size and runner image without a golden snapshot, and deleting.
package substrate_test

import (
	"errors"
	"testing"

	"github.com/flanksource/captain/pkg/substrate"
	ateapipb "github.com/flanksource/captain/pkg/substrate/internal/ateapi"
	"github.com/flanksource/captain/pkg/substrate/substratetest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

var runnerSize = substrate.Size{CPUMillicores: 2000, MemoryBytes: 4 << 30}

func TestTemplateReadsItsSizeAndUID(t *testing.T) {
	server := substratetest.Start(t)
	server.AddTemplate("ate-env", "gavel-runner", runnerSize)

	template, err := dial(t, server).Template(testContext(t), "gavel-runner")
	require.NoError(t, err)
	assert.Equal(t, "gavel-runner", template.Name)
	assert.Equal(t, runnerSize, template.Size)
	assert.NotEmpty(t, template.UID)
}

func TestTemplateSaysWhichTemplateIsMissing(t *testing.T) {
	server := substratetest.Start(t)

	_, err := dial(t, server).Template(testContext(t), "gavel-runner")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "actor template ate-env/gavel-runner")
	assert.Contains(t, err.Error(), "not found")
}

func TestCopyTemplateKeepsTheSpecAtItsSizeWithoutAGoldenSnapshot(t *testing.T) {
	server := substratetest.Start(t)
	server.AddTemplate("ate-env", "gavel-runner", runnerSize)
	client := dial(t, server)
	ctx := testContext(t)
	size := substrate.Size{CPUMillicores: 1000, MemoryBytes: 2 << 30}

	copied, err := client.CopyTemplate(ctx, "gavel-runner", "gavel-org-1", size, "")
	require.NoError(t, err)
	assert.Equal(t, "gavel-org-1", copied.Name)
	assert.Equal(t, size, copied.Size)

	base, _ := server.Template("ate-env", "gavel-runner")
	got, ok := server.Template("ate-env", "gavel-org-1")
	require.True(t, ok)
	assert.NotEqual(t, base.GetMetadata().GetUid(), copied.UID)
	assert.True(t, proto.Equal(&ateapipb.ActorTemplate{Containers: base.GetContainers(), Volumes: base.GetVolumes(), WorkerSelector: base.GetWorkerSelector()},
		&ateapipb.ActorTemplate{Containers: got.GetContainers(), Volumes: got.GetVolumes(), WorkerSelector: got.GetWorkerSelector()}),
		"the copy runs what the template it was copied from runs")
	assert.Equal(t, base.GetSnapshotConfig().GetPreferredFidelity(), got.GetSnapshotConfig().GetPreferredFidelity())
	assert.Equal(t, ateapipb.GoldenSnapshotMode_GOLDEN_SNAPSHOT_MODE_DISABLED, got.GetSnapshotConfig().GetGoldenSnapshotConfig().GetMode())
	assert.Nil(t, got.GetGoldenEgressPolicy(), "a template without a golden snapshot has no golden actor to give a policy")

	read, err := client.Template(ctx, "gavel-org-1")
	require.NoError(t, err)
	assert.Equal(t, copied, read)
}

// gavel copies the cluster's runner template with the runner image of its own release, so each
// release's runs execute that release's runner without the template being registered again.
func TestCopyTemplateRunsTheImageItIsGiven(t *testing.T) {
	server := substratetest.Start(t)
	server.AddTemplate("ate-env", "gavel-runner", runnerSize)
	const image = "ghcr.io/flanksource/gavel-runner@sha256:8c91"

	_, err := dial(t, server).CopyTemplate(testContext(t), "gavel-runner", "gavel-org-1", runnerSize, image)
	require.NoError(t, err)

	base, _ := server.Template("ate-env", "gavel-runner")
	got, ok := server.Template("ate-env", "gavel-org-1")
	require.True(t, ok)
	require.Len(t, got.GetContainers(), 1)
	assert.Equal(t, image, got.GetContainers()[0].GetImage())
	runs := proto.Clone(base.GetContainers()[0]).(*ateapipb.Container)
	runs.Image = image
	assert.True(t, proto.Equal(runs, got.GetContainers()[0]), "only the image changes: the guest's command and mounts are the template's")
	assert.True(t, proto.Equal(&ateapipb.ActorTemplate{Volumes: base.GetVolumes(), WorkerSelector: base.GetWorkerSelector()},
		&ateapipb.ActorTemplate{Volumes: got.GetVolumes(), WorkerSelector: got.GetWorkerSelector()}))
	unchanged, _ := server.Template("ate-env", "gavel-runner")
	assert.Equal(t, base.GetContainers()[0].GetImage(), unchanged.GetContainers()[0].GetImage(), "the template copied from keeps its image")
}

func TestCopyTemplateRefusesANameAlreadyTaken(t *testing.T) {
	server := substratetest.Start(t)
	server.AddTemplate("ate-env", "gavel-runner", runnerSize)
	server.AddTemplate("ate-env", "gavel-org-1", runnerSize)

	_, err := dial(t, server).CopyTemplate(testContext(t), "gavel-runner", "gavel-org-1", runnerSize, "")
	require.ErrorIs(t, err, substrate.ErrTemplateExists)
	assert.Contains(t, err.Error(), "copy actor template ate-env/gavel-runner to gavel-org-1")
}

func TestCopyTemplateSaysWhichTemplateIsMissing(t *testing.T) {
	server := substratetest.Start(t)

	_, err := dial(t, server).CopyTemplate(testContext(t), "gavel-runner", "gavel-org-1", runnerSize, "")
	require.Error(t, err)
	assert.False(t, errors.Is(err, substrate.ErrTemplateExists))
	assert.Contains(t, err.Error(), "actor template ate-env/gavel-runner")
	assert.Contains(t, err.Error(), "not found")
}

func TestCopyTemplateRefusesASizeWithoutCPUOrMemory(t *testing.T) {
	server := substratetest.Start(t)
	server.AddTemplate("ate-env", "gavel-runner", runnerSize)
	client := dial(t, server)

	for _, size := range []substrate.Size{{MemoryBytes: 2 << 30}, {CPUMillicores: 1000}} {
		_, err := client.CopyTemplate(testContext(t), "gavel-runner", "gavel-org-1", size, "")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "an actor template's size needs both CPU and memory")
	}
	_, ok := server.Template("ate-env", "gavel-org-1")
	assert.False(t, ok)
}

func TestListTemplatesListsTheClientsAtespaceByName(t *testing.T) {
	server := substratetest.Start(t)
	server.ListPageSize = 1
	server.AddTemplate("ate-env", "gavel-org-2", runnerSize)
	server.AddTemplate("ate-env", "gavel-org-1", runnerSize)
	server.AddTemplate("other", "gavel-org-3", runnerSize)

	templates, err := dial(t, server).ListTemplates(testContext(t))
	require.NoError(t, err)
	var names []string
	for _, template := range templates {
		names = append(names, template.Name)
		assert.Equal(t, runnerSize, template.Size)
	}
	assert.Equal(t, []string{"gavel-org-1", "gavel-org-2"}, names)
}

func TestDeleteTemplateDeletesItAndSaysWhichIsMissing(t *testing.T) {
	server := substratetest.Start(t)
	server.AddTemplate("ate-env", "gavel-org-1", runnerSize)
	client := dial(t, server)
	ctx := testContext(t)

	require.NoError(t, client.DeleteTemplate(ctx, "gavel-org-1"))
	_, ok := server.Template("ate-env", "gavel-org-1")
	assert.False(t, ok)

	err := client.DeleteTemplate(ctx, "gavel-org-1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "delete actor template ate-env/gavel-org-1")
	assert.Contains(t, err.Error(), "not found")
}

func TestListActorsNamesTheTemplateEachActorRuns(t *testing.T) {
	server := substratetest.Start(t)
	ctx := testContext(t)
	for id, template := range map[string]string{"run-1": "gavel-org-1", "run-2": "gavel-org-2"} {
		_, err := dial(t, server, func(o *substrate.Options) { o.Template = template }).Create(ctx, substrate.Environment{ID: id})
		require.NoError(t, err)
	}

	actors, err := dial(t, server).ListActors(ctx, "ate-env")
	require.NoError(t, err)
	require.Len(t, actors, 2)
	assert.Equal(t, "gavel-org-1", actors[0].Template)
	assert.Equal(t, "gavel-org-2", actors[1].Template)
}
