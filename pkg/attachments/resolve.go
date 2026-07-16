package attachments

import (
	"context"
	"fmt"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/flanksource/captain/pkg/api"
)

func (s *Store) Resolve(ctx context.Context, refs []api.AttachmentRef, baseDir string) ([]api.AttachmentRef, error) {
	if len(refs) > s.limits.MaxFiles {
		return nil, fmt.Errorf("attachment request has %d files and exceeds %d file limit", len(refs), s.limits.MaxFiles)
	}
	resolved := make([]api.AttachmentRef, 0, len(refs))
	var total int64
	for i, ref := range refs {
		if err := ref.Validate(); err != nil {
			return nil, fmt.Errorf("attachment %d: %w", i+1, err)
		}
		prepared, err := s.resolveOne(ctx, ref, baseDir)
		if err != nil {
			return nil, fmt.Errorf("attachment %d: %w", i+1, err)
		}
		total += prepared.Size
		if total > s.limits.MaxRequestBytes {
			return nil, fmt.Errorf("attachments total %d bytes exceeds %d byte request limit", total, s.limits.MaxRequestBytes)
		}
		resolved = append(resolved, prepared)
	}
	return resolved, nil
}

func (s *Store) resolveOne(ctx context.Context, ref api.AttachmentRef, baseDir string) (api.AttachmentRef, error) {
	content, filename, err := s.readSource(ctx, ref, baseDir)
	if err != nil {
		return api.AttachmentRef{}, err
	}
	mediaType := detectMediaType(content, filename)
	if ref.MediaType != "" && canonicalMediaType(ref.MediaType) != mediaType {
		return api.AttachmentRef{}, fmt.Errorf("declared media type %s does not match detected %s", ref.MediaType, mediaType)
	}
	id, path, err := s.persist(content)
	if err != nil {
		return api.AttachmentRef{}, err
	}
	digest := strings.TrimPrefix(id, api.AttachmentIDPrefix)
	if ref.SHA256 != "" && !strings.EqualFold(ref.SHA256, digest) {
		return api.AttachmentRef{}, fmt.Errorf("declared sha256 %s does not match content %s", ref.SHA256, digest)
	}
	if ref.Filename != "" {
		filename = ref.Filename
	}
	return api.AttachmentRef{
		ID:        id,
		Filename:  filename,
		MediaType: mediaType,
		Size:      int64(len(content)),
		SHA256:    digest,
	}.WithPreparedContent(api.AttachmentContent{Bytes: content, Path: path}), nil
}

func (s *Store) readSource(ctx context.Context, ref api.AttachmentRef, baseDir string) ([]byte, string, error) {
	switch {
	case ref.ID != "":
		file, err := s.Open(ref.ID)
		if err != nil {
			return nil, "", err
		}
		defer file.Close()
		content, err := readLimited(file, s.limits.MaxFileBytes)
		return content, ref.Filename, err
	case ref.Path != "":
		path := ref.Path
		if !filepath.IsAbs(path) {
			path = filepath.Join(baseDir, path)
		}
		file, err := os.Open(path)
		if err != nil {
			return nil, "", fmt.Errorf("open attachment path %s: %w", path, err)
		}
		defer file.Close()
		content, err := readLimited(file, s.limits.MaxFileBytes)
		return content, filepath.Base(path), err
	case ref.URL != "":
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, ref.URL, nil)
		if err != nil {
			return nil, "", fmt.Errorf("create attachment request: %w", err)
		}
		response, err := s.httpClient.Do(request)
		if err != nil {
			return nil, "", fmt.Errorf("download attachment: %w", err)
		}
		defer response.Body.Close()
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			return nil, "", fmt.Errorf("download attachment: unexpected HTTP status %s", response.Status)
		}
		content, err := readLimited(response.Body, s.limits.MaxFileBytes)
		parsed, _ := url.Parse(ref.URL)
		return content, filepath.Base(parsed.Path), err
	default:
		return nil, "", fmt.Errorf("attachment source is required")
	}
}

func detectMediaType(content []byte, filename string) string {
	mediaType := canonicalMediaType(http.DetectContentType(content))
	if mediaType == "application/octet-stream" {
		if extensionType := canonicalMediaType(mime.TypeByExtension(filepath.Ext(filename))); extensionType != "" {
			return extensionType
		}
	}
	return mediaType
}

func canonicalMediaType(value string) string {
	mediaType, _, err := mime.ParseMediaType(value)
	if err != nil {
		return strings.ToLower(strings.TrimSpace(value))
	}
	return strings.ToLower(mediaType)
}
