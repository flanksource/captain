// Specs for what a substrate holds, against an in-process Substrate: the actors of one atespace or
// of all of them, and the workers with their capacity and what actors use of it.
package substrate_test

import (
	"testing"
	"time"

	"github.com/flanksource/captain/pkg/substrate"
	"github.com/flanksource/captain/pkg/substrate/substratetest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestListActorsListsOneAtespaceOrEveryAtespace(t *testing.T) {
	server := substratetest.Start(t)
	server.ListPageSize = 1
	ctx := testContext(t)
	for atespace, ids := range map[string][]string{"ate-env": {"run-2", "run-1"}, "gavel": {"run-3"}} {
		client := dial(t, server, func(o *substrate.Options) { o.Atespace = atespace })
		for _, id := range ids {
			_, err := client.Create(ctx, substrate.Environment{ID: id})
			require.NoError(t, err)
		}
	}
	client := dial(t, server)
	names := func(actors []substrate.Actor) []string {
		var names []string
		for _, actor := range actors {
			names = append(names, actor.Atespace+"/"+actor.Name)
		}
		return names
	}

	inOne, err := client.ListActors(ctx, "ate-env")
	require.NoError(t, err)
	assert.Equal(t, []string{"ate-env/run-1", "ate-env/run-2"}, names(inOne))

	all, err := client.ListActors(ctx, "")
	require.NoError(t, err)
	assert.Equal(t, []string{"ate-env/run-1", "ate-env/run-2", "gavel/run-3"}, names(all))
	assert.Equal(t, "running", all[0].State)
	assert.WithinDuration(t, time.Now(), all[0].Created, time.Minute)
}

func TestListWorkersReportsTheirCapacityAndWhatActorsUse(t *testing.T) {
	server := substratetest.Start(t)
	server.ListPageSize = 1
	workers := []substrate.Worker{
		{
			Name: "pool-a-0", Pool: "pool-a", Node: "node-1", State: "active",
			Capacity:  substrate.Resources{CPUMillicores: 4000, MemoryBytes: 8 << 30, Actors: 10},
			Allocated: substrate.Resources{CPUMillicores: 1500, MemoryBytes: 2 << 30, Actors: 3},
		},
		{
			Name: "pool-a-1", Pool: "pool-a", Node: "node-2", State: "draining",
			Capacity: substrate.Resources{CPUMillicores: 4000, MemoryBytes: 8 << 30, Actors: 10},
		},
	}
	for _, worker := range workers {
		server.AddWorker(worker)
	}

	got, err := dial(t, server).ListWorkers(testContext(t))
	require.NoError(t, err)
	assert.Equal(t, workers, got)
}
