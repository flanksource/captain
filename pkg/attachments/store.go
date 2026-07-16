package attachments

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/flanksource/captain/pkg/api"
)

type StoreOptions struct {
	Directory  string
	Limits     Limits
	HTTPClient *http.Client
}

type Store struct {
	directory  string
	limits     Limits
	httpClient *http.Client
}

func NewStore(opts StoreOptions) (*Store, error) {
	if strings.TrimSpace(opts.Directory) == "" {
		return nil, errors.New("attachment store directory is required")
	}
	limits := opts.Limits.withDefaults()
	if err := limits.validate(); err != nil {
		return nil, err
	}
	directory, err := filepath.Abs(opts.Directory)
	if err != nil {
		return nil, fmt.Errorf("resolve attachment store directory: %w", err)
	}
	if err := ensurePrivateDirectory(directory); err != nil {
		return nil, err
	}
	client := opts.HTTPClient
	if client == nil {
		client = &http.Client{
			Timeout: 30 * time.Second,
			CheckRedirect: func(_ *http.Request, via []*http.Request) error {
				if len(via) >= 5 {
					return fmt.Errorf("attachment download exceeded 5 redirects")
				}
				return nil
			},
		}
	}
	return &Store{directory: directory, limits: limits, httpClient: client}, nil
}

func (s *Store) Limits() Limits { return s.limits }

func (s *Store) Directory() string { return s.directory }

func (s *Store) Put(reader io.Reader, filename, declaredMediaType string) (api.AttachmentRef, error) {
	content, err := readLimited(reader, s.limits.MaxFileBytes)
	if err != nil {
		return api.AttachmentRef{}, err
	}
	mediaType := detectMediaType(content, filename)
	if declaredMediaType != "" && canonicalMediaType(declaredMediaType) != "application/octet-stream" && canonicalMediaType(declaredMediaType) != mediaType {
		return api.AttachmentRef{}, fmt.Errorf("declared media type %s does not match detected %s", declaredMediaType, mediaType)
	}
	id, path, err := s.persist(content)
	if err != nil {
		return api.AttachmentRef{}, err
	}
	digest := strings.TrimPrefix(id, api.AttachmentIDPrefix)
	return api.AttachmentRef{
		ID: id, Filename: filename, MediaType: mediaType,
		Size: int64(len(content)), SHA256: digest,
	}.WithPreparedContent(api.AttachmentContent{Bytes: content, Path: path}), nil
}

func (s *Store) Path(id string) string {
	digest := strings.TrimPrefix(id, api.AttachmentIDPrefix)
	if len(digest) < 2 {
		return ""
	}
	return filepath.Join(s.directory, "sha256", digest[:2], digest)
}

func (s *Store) Open(id string) (*os.File, error) {
	ref := api.AttachmentRef{ID: id}
	if err := ref.Validate(); err != nil {
		return nil, err
	}
	file, err := os.Open(s.Path(id))
	if err != nil {
		return nil, fmt.Errorf("open attachment %s: %w", id, err)
	}
	return file, nil
}

func (s *Store) persist(content []byte) (string, string, error) {
	digest := fmt.Sprintf("%x", sha256.Sum256(content))
	id := api.AttachmentIDPrefix + digest
	path := s.Path(id)
	if info, err := os.Stat(path); err == nil {
		if !info.Mode().IsRegular() {
			return "", "", fmt.Errorf("attachment path %s is not a regular file", path)
		}
		return id, path, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", "", fmt.Errorf("inspect attachment path %s: %w", path, err)
	}
	dir := filepath.Dir(path)
	if err := ensurePrivateDirectory(dir); err != nil {
		return "", "", err
	}
	tmp, err := os.CreateTemp(dir, ".attachment-*")
	if err != nil {
		return "", "", fmt.Errorf("create attachment temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return "", "", fmt.Errorf("set attachment permissions: %w", err)
	}
	if _, err := tmp.Write(content); err != nil {
		_ = tmp.Close()
		return "", "", fmt.Errorf("write attachment: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return "", "", fmt.Errorf("close attachment: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return "", "", fmt.Errorf("publish attachment: %w", err)
	}
	return id, path, nil
}

func ensurePrivateDirectory(path string) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return fmt.Errorf("create attachment directory %s: %w", path, err)
	}
	if err := os.Chmod(path, 0o700); err != nil {
		return fmt.Errorf("set attachment directory permissions %s: %w", path, err)
	}
	return nil
}

func readLimited(reader io.Reader, limit int64) ([]byte, error) {
	content, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(content)) > limit {
		return nil, fmt.Errorf("attachment exceeds %d byte file limit", limit)
	}
	return content, nil
}

func invalidLimit(name string, value any) error {
	return fmt.Errorf("attachment %s must be positive, got %v", name, value)
}
