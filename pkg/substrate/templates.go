// Reads, lists, copies and deletes the actor templates of the client's atespace: the size each
// template's actors boot at, and copies of one at another size and runner image that build no
// golden snapshot.
package substrate

import (
	"context"
	"errors"
	"fmt"

	ateapipb "github.com/flanksource/captain/pkg/substrate/internal/ateapi"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

// Size is how much CPU and memory an actor boots with, which Substrate books against a worker's
// capacity and holds the actor to.
type Size struct {
	CPUMillicores int64
	MemoryBytes   int64
}

// Template is an actor template of the client's atespace.
type Template struct {
	Name string
	// UID is new each time a template of this name is created.
	UID  string
	Size Size
}

// ErrTemplateExists refuses a copy whose name another template of the atespace has.
var ErrTemplateExists = errors.New("an actor template of that name already exists")

// Template reads the client's atespace's template name.
func (c *Client) Template(ctx context.Context, name string) (Template, error) {
	template, err := c.getTemplate(ctx, name)
	if err != nil {
		return Template{}, err
	}
	return templateOf(template), nil
}

// ListTemplates lists the templates of the client's atespace.
func (c *Client) ListTemplates(ctx context.Context) ([]Template, error) {
	request := &ateapipb.ListActorTemplatesRequest{Atespace: c.options.Atespace, Options: &ateapipb.ListOptions{PageSize: listPageSize}}
	return list(func(token string) ([]Template, string, error) {
		request.Options.PageToken = token
		page, err := c.control.ListActorTemplates(ctx, request)
		if err != nil {
			return nil, "", fmt.Errorf("list the actor templates of atespace %s: %w", c.options.Atespace, err)
		}
		templates := make([]Template, 0, len(page.GetActorTemplates()))
		for _, template := range page.GetActorTemplates() {
			templates = append(templates, templateOf(template))
		}
		return templates, page.GetNextPageToken(), nil
	})
}

// CopyTemplate creates template to as a copy of template from, running what it runs, at size.
// The copy builds no golden snapshot, so its actors boot from its image; an actor asked for at
// once boots straight away. A name another template has is refused with ErrTemplateExists.
//
// image, when set, is what the copy's containers run in place of from's: an ate-env template has
// one, the guest's root filesystem, so a copy runs another release's runner on the same cluster
// settings. Empty keeps from's image.
func (c *Client) CopyTemplate(ctx context.Context, from, to string, size Size, image string) (Template, error) {
	if size.CPUMillicores <= 0 || size.MemoryBytes <= 0 {
		return Template{}, fmt.Errorf("copy actor template %s/%s to %s at %d millicores and %d bytes: an actor template's size needs both CPU and memory",
			c.options.Atespace, from, to, size.CPUMillicores, size.MemoryBytes)
	}
	source, err := c.getTemplate(ctx, from)
	if err != nil {
		return Template{}, fmt.Errorf("copy actor template %s/%s to %s: %w", c.options.Atespace, from, to, err)
	}
	copied := proto.Clone(source).(*ateapipb.ActorTemplate)
	copied.Metadata = &ateapipb.ResourceMetadata{Atespace: c.options.Atespace, Name: to}
	copied.Status = nil
	copied.Resources = &ateapipb.ResourceRequirements{Limits: &ateapipb.Resources{CpuMillicores: size.CPUMillicores, MemoryBytes: size.MemoryBytes}}
	if image != "" {
		for _, container := range copied.Containers {
			container.Image = image
		}
	}
	if copied.SnapshotConfig == nil {
		copied.SnapshotConfig = &ateapipb.SnapshotConfig{}
	}
	copied.SnapshotConfig.GoldenSnapshotConfig = &ateapipb.GoldenSnapshotConfig{Mode: ateapipb.GoldenSnapshotMode_GOLDEN_SNAPSHOT_MODE_DISABLED}
	// Only a golden actor runs under a golden egress policy, and Substrate refuses one without it.
	copied.GoldenEgressPolicy = nil
	created, err := c.control.CreateActorTemplate(ctx, &ateapipb.CreateActorTemplateRequest{ActorTemplate: copied})
	if status.Code(err) == codes.AlreadyExists {
		return Template{}, fmt.Errorf("copy actor template %s/%s to %s: %w: %v", c.options.Atespace, from, to, ErrTemplateExists, err)
	}
	if err != nil {
		return Template{}, fmt.Errorf("copy actor template %s/%s to %s: %w", c.options.Atespace, from, to, err)
	}
	return templateOf(created), nil
}

// DeleteTemplate deletes the client's atespace's template name. An environment created from it
// can't be deleted through env afterwards, since env suspends an environment before deleting it
// and Substrate can't suspend an actor whose template is gone; delete a template no actor runs.
func (c *Client) DeleteTemplate(ctx context.Context, name string) error {
	ref := &ateapipb.ObjectRef{Atespace: c.options.Atespace, Name: name}
	if _, err := c.control.DeleteActorTemplate(ctx, &ateapipb.DeleteActorTemplateRequest{ActorTemplate: ref}); err != nil {
		return fmt.Errorf("delete actor template %s/%s: %w", c.options.Atespace, name, err)
	}
	return nil
}

func (c *Client) getTemplate(ctx context.Context, name string) (*ateapipb.ActorTemplate, error) {
	ref := &ateapipb.ObjectRef{Atespace: c.options.Atespace, Name: name}
	template, err := c.control.GetActorTemplate(ctx, &ateapipb.GetActorTemplateRequest{ActorTemplate: ref})
	if err != nil {
		return nil, fmt.Errorf("read actor template %s/%s: %w", c.options.Atespace, name, err)
	}
	return template, nil
}

func templateOf(template *ateapipb.ActorTemplate) Template {
	limits := template.GetResources().GetLimits()
	return Template{
		Name: template.GetMetadata().GetName(), UID: template.GetMetadata().GetUid(),
		Size: Size{CPUMillicores: limits.GetCpuMillicores(), MemoryBytes: limits.GetMemoryBytes()},
	}
}
