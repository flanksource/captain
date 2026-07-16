package cli

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/csv"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/attachments"
	"github.com/flanksource/clicky/aichat"
)

func attachmentRefsFromFlags(values []string) ([]api.AttachmentRef, error) {
	refs := make([]api.AttachmentRef, 0, len(values))
	for _, value := range values {
		reader := csv.NewReader(strings.NewReader(value))
		reader.FieldsPerRecord = -1
		fields, err := reader.Read()
		if err != nil {
			return nil, fmt.Errorf("parse --attach value %q: %w", value, err)
		}
		for _, field := range fields {
			field = strings.TrimSpace(field)
			if len(field) >= 2 && field[0] == '\'' && field[len(field)-1] == '\'' {
				field = strings.TrimSpace(field[1 : len(field)-1])
			}
			if field == "" {
				return nil, fmt.Errorf("empty attachment in --attach value %q", value)
			}
			ref := api.AttachmentRef{Path: field}
			switch {
			case strings.HasPrefix(field, api.AttachmentIDPrefix):
				ref = api.AttachmentRef{ID: field}
			case strings.HasPrefix(field, "http://"), strings.HasPrefix(field, "https://"):
				ref = api.AttachmentRef{URL: field}
			}
			if err := ref.Validate(); err != nil {
				return nil, err
			}
			refs = append(refs, ref)
		}
	}
	return refs, nil
}

type chatAttachmentResolver struct {
	store *attachments.Store
}

func (r chatAttachmentResolver) Resolve(ctx context.Context, inputs []aichat.AttachmentInput) ([]api.AttachmentRef, error) {
	limits := r.store.Limits()
	if len(inputs) > limits.MaxFiles {
		return nil, fmt.Errorf("attachment request has %d files and exceeds %d file limit", len(inputs), limits.MaxFiles)
	}
	refs := make([]api.AttachmentRef, 0, len(inputs))
	var total int64
	for i, input := range inputs {
		id := input.ID
		if id == "" {
			if _, suffix, ok := strings.Cut(input.URL, "/api/attachments/"); ok {
				id = suffix
			}
		}
		if id != "" {
			resolved, err := r.store.Resolve(ctx, []api.AttachmentRef{{
				ID: id, Filename: input.Filename, MediaType: input.MediaType,
			}}, "")
			if err != nil {
				return nil, fmt.Errorf("chat attachment %d: %w", i+1, err)
			}
			refs = append(refs, resolved[0])
			total += resolved[0].Size
			if total > limits.MaxRequestBytes {
				return nil, fmt.Errorf("attachments total %d bytes exceeds %d byte request limit", total, limits.MaxRequestBytes)
			}
			continue
		}
		mediaType, encoded, ok := parseLegacyDataURL(input.URL)
		if !ok {
			return nil, fmt.Errorf("chat attachment %d must be uploaded before use", i+1)
		}
		data, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			return nil, fmt.Errorf("chat attachment %d has invalid base64 data: %w", i+1, err)
		}
		ref, err := r.store.Put(bytes.NewReader(data), input.Filename, mediaType)
		if err != nil {
			return nil, fmt.Errorf("chat attachment %d: %w", i+1, err)
		}
		refs = append(refs, ref)
		total += ref.Size
		if total > limits.MaxRequestBytes {
			return nil, fmt.Errorf("attachments total %d bytes exceeds %d byte request limit", total, limits.MaxRequestBytes)
		}
	}
	return refs, nil
}

func parseLegacyDataURL(value string) (mediaType, encoded string, ok bool) {
	header, encoded, ok := strings.Cut(value, ",")
	if !ok || !strings.HasPrefix(header, "data:") || !strings.HasSuffix(header, ";base64") {
		return "", "", false
	}
	mediaType = strings.TrimSuffix(strings.TrimPrefix(header, "data:"), ";base64")
	return mediaType, encoded, mediaType != ""
}

func preparePromptAttachments(ctx context.Context, req *ai.Request, cfg ai.Config) error {
	if err := resolvePromptAttachments(ctx, req); err != nil {
		return err
	}
	if len(req.Prompt.Attachments) == 0 {
		return nil
	}
	model := req.Model
	if model.Name == "" {
		model = cfg.Model
	}
	models := append([]api.Model{model}, model.Fallbacks...)
	return ai.ValidateAttachmentCompatibility(models, req.Prompt.Attachments)
}

func resolvePromptAttachments(ctx context.Context, req *ai.Request) error {
	if len(req.Prompt.Attachments) == 0 {
		return nil
	}
	allPrepared := true
	for _, attachment := range req.Prompt.Attachments {
		allPrepared = allPrepared && attachment.IsPrepared()
	}
	if !allPrepared {
		baseDir := req.Cwd()
		if baseDir == "" {
			var err error
			baseDir, err = os.Getwd()
			if err != nil {
				return fmt.Errorf("resolve attachment working directory: %w", err)
			}
		}
		store, err := newAttachmentStore(baseDir)
		if err != nil {
			return err
		}
		resolved, err := store.Resolve(ctx, req.Prompt.Attachments, baseDir)
		if err != nil {
			return err
		}
		req.Prompt.Attachments = resolved
	}
	return nil
}

func newAttachmentStore(baseDir string) (*attachments.Store, error) {
	defaults := loadSavedConfig().Attachments.WithDefaults()
	directory := defaults.Directory
	if !filepath.IsAbs(directory) {
		directory = filepath.Join(baseDir, directory)
	}
	return attachments.NewStore(attachments.StoreOptions{
		Directory: directory,
		Limits: attachments.Limits{
			MaxFileBytes:    defaults.MaxFileBytes,
			MaxRequestBytes: defaults.MaxRequestBytes,
			MaxFiles:        defaults.MaxFiles,
		},
	})
}
