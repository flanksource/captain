// Lists what a substrate holds: the actors of one atespace or of every atespace, and the workers
// they run on, with each worker's capacity and how much of it its actors are allocated.
package substrate

import (
	"context"
	"fmt"
	"strings"
	"time"

	ateapipb "github.com/flanksource/captain/pkg/substrate/internal/ateapi"
)

// Actor is one actor in a substrate: an environment, or anything else Substrate runs.
type Actor struct {
	Atespace, Name string
	// State is resuming, running, suspending, suspended, crashed, deleting or reverting.
	State string
	// Created is when the actor was created; zero when Substrate doesn't say.
	Created time.Time
}

// Worker is one worker pod that actors run on.
type Worker struct {
	Name, Pool, Node string
	// State is active, draining or unavailable.
	State string
	// Capacity is what the worker can hold, and Allocated what its actors are allocated of it,
	// their runtime's overhead included.
	Capacity, Allocated Resources
}

// Resources is an amount of a worker's CPU, memory and actor slots.
type Resources struct {
	CPUMillicores int64
	MemoryBytes   int64
	Actors        int32
}

// listPageSize is how many items one list call asks Substrate for.
const listPageSize = 100

// ListActors lists the actors of atespace, or of every atespace when atespace is empty.
func (c *Client) ListActors(ctx context.Context, atespace string) ([]Actor, error) {
	request := &ateapipb.ListActorsRequest{Atespace: atespace, Options: &ateapipb.ListOptions{PageSize: listPageSize}}
	return list(func(token string) ([]Actor, string, error) {
		request.Options.PageToken = token
		page, err := c.control.ListActors(ctx, request)
		if err != nil {
			if atespace == "" {
				return nil, "", fmt.Errorf("list the actors of every atespace: %w", err)
			}
			return nil, "", fmt.Errorf("list the actors of atespace %s: %w", atespace, err)
		}
		actors := make([]Actor, 0, len(page.GetActors()))
		for _, actor := range page.GetActors() {
			metadata := actor.GetMetadata()
			listed := Actor{Atespace: metadata.GetAtespace(), Name: metadata.GetName(), State: stateName(actor.GetStatus().GetState().String(), "ACTOR_STATE_")}
			if metadata.GetCreateTime() != nil {
				listed.Created = metadata.GetCreateTime().AsTime()
			}
			actors = append(actors, listed)
		}
		return actors, page.GetNextPageToken(), nil
	})
}

// ListWorkers lists the substrate's workers.
func (c *Client) ListWorkers(ctx context.Context) ([]Worker, error) {
	request := &ateapipb.ListWorkersRequest{Options: &ateapipb.ListOptions{PageSize: listPageSize}}
	return list(func(token string) ([]Worker, string, error) {
		request.Options.PageToken = token
		page, err := c.control.ListWorkers(ctx, request)
		if err != nil {
			return nil, "", fmt.Errorf("list substrate's workers: %w", err)
		}
		workers := make([]Worker, 0, len(page.GetWorkers()))
		for _, worker := range page.GetWorkers() {
			status := worker.GetStatus()
			workers = append(workers, Worker{
				Name: worker.GetMetadata().GetName(), Pool: worker.GetWorkerPool(), Node: worker.GetNodeName(),
				State:     stateName(status.GetState().String(), "WORKER_STATE_"),
				Capacity:  resources(status.GetCapacity()),
				Allocated: resources(status.GetAllocated()),
			})
		}
		return workers, page.GetNextPageToken(), nil
	})
}

// list collects every page that page returns, starting from the empty token and following each
// page's next token until it is empty.
func list[T any](page func(token string) ([]T, string, error)) ([]T, error) {
	var all []T
	token := ""
	for {
		items, next, err := page(token)
		if err != nil {
			return nil, err
		}
		all = append(all, items...)
		if next == "" {
			return all, nil
		}
		token = next
	}
}

// stateName is the lower-case name of a Substrate enum value without its prefix, e.g. running
// for ACTOR_STATE_RUNNING.
func stateName(value, prefix string) string {
	return strings.ToLower(strings.TrimPrefix(value, prefix))
}

func resources(r *ateapipb.WorkerResources) Resources {
	return Resources{CPUMillicores: r.GetCpuMillicores(), MemoryBytes: r.GetMemoryBytes(), Actors: r.GetActors()}
}
