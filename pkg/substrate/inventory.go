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
	Atespace string `json:"atespace"`
	Name     string `json:"name"`
	// Template is the name of the actor template the actor was created from, in its atespace;
	// empty when Substrate doesn't say.
	Template string `json:"template,omitempty"`
	// State is resuming, running, suspending, suspended, crashed, deleting or reverting;
	// unspecified when Substrate doesn't say, or the state's number for one this client doesn't know.
	State string `json:"state"`
	// Created is when the actor was created; zero when Substrate doesn't say.
	Created time.Time `json:"created,omitzero"`
	// Worker, Pool and Node are the worker Substrate placed the actor on, its pool and its node;
	// empty while the actor has no worker, as when it is suspended or crashed.
	Worker string `json:"worker,omitempty"`
	Pool   string `json:"pool,omitempty"`
	Node   string `json:"node,omitempty"`
}

// Worker is one of substrate's workers, the pods that actors run on. Name is Substrate's name for
// it.
type Worker struct {
	Name string `json:"name"`
	Pool string `json:"pool"`
	Node string `json:"node"`
	// State is active, draining or unavailable; unspecified when the worker hasn't reported one,
	// or the state's number for one this client doesn't know.
	State string `json:"state"`
	// Capacity is what the worker can hold, and Allocated what its actors are allocated of it,
	// their runtime's overhead included.
	Capacity  Resources `json:"capacity"`
	Allocated Resources `json:"allocated"`
}

// Resources is an amount of a worker's CPU, memory and actor slots.
type Resources struct {
	CPUMillicores int64 `json:"cpuMillicores"`
	MemoryBytes   int64 `json:"memoryBytes"`
	Actors        int32 `json:"actors"`
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
			actors = append(actors, actorOf(actor))
		}
		return actors, page.GetNextPageToken(), nil
	})
}

// GetActor reads the actor of environment id in the client's atespace: its state and the worker
// it is placed on. An actor that doesn't exist, or no longer does, is a NOT_FOUND error.
func (c *Client) GetActor(ctx context.Context, id string) (Actor, error) {
	actor, err := c.control.GetActor(ctx, &ateapipb.GetActorRequest{Actor: &ateapipb.ObjectRef{Atespace: c.options.Atespace, Name: id}})
	if err != nil {
		return Actor{}, fmt.Errorf("read the actor of substrate environment %s/%s: %w", c.options.Atespace, id, err)
	}
	return actorOf(actor), nil
}

// actorOf is actor as the client reports it.
func actorOf(actor *ateapipb.Actor) Actor {
	metadata, status := actor.GetMetadata(), actor.GetStatus()
	assignment := status.GetWorkerAssignment()
	read := Actor{
		Atespace: metadata.GetAtespace(), Name: metadata.GetName(), Template: actor.GetActorTemplate().GetName(),
		State:  stateName(status.GetState().String(), "ACTOR_STATE_"),
		Worker: assignment.GetWorker().GetName(), Pool: assignment.GetWorkerPool(), Node: assignment.GetNodeName(),
	}
	if metadata.GetCreateTime() != nil {
		read.Created = metadata.GetCreateTime().AsTime()
	}
	return read
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
