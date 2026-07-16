package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/attachments"
)

func handleAttachmentUpload(store *attachments.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, store.Limits().MaxRequestBytes)
		reader, err := r.MultipartReader()
		if err != nil {
			writeAttachmentError(w, http.StatusBadRequest, fmt.Errorf("read multipart upload: %w", err))
			return
		}
		var uploaded *api.AttachmentRef
		for {
			part, err := reader.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				writeAttachmentError(w, http.StatusBadRequest, fmt.Errorf("read multipart upload: %w", err))
				return
			}
			if part.FormName() != "file" || part.FileName() == "" {
				_ = part.Close()
				continue
			}
			if uploaded != nil {
				_ = part.Close()
				writeAttachmentError(w, http.StatusBadRequest, fmt.Errorf("upload exactly one file per request"))
				return
			}
			ref, err := store.Put(part, part.FileName(), part.Header.Get("Content-Type"))
			_ = part.Close()
			if err != nil {
				writeAttachmentError(w, http.StatusBadRequest, err)
				return
			}
			uploaded = &ref
		}
		if uploaded == nil {
			writeAttachmentError(w, http.StatusBadRequest, fmt.Errorf("multipart field file is required"))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(uploaded)
	}
}

func handleAttachmentGet(store *attachments.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		file, err := store.Open(r.PathValue("id"))
		if err != nil {
			writeAttachmentError(w, http.StatusNotFound, err)
			return
		}
		defer file.Close()
		sample := make([]byte, 512)
		read, err := file.Read(sample)
		if err != nil && err != io.EOF {
			writeAttachmentError(w, http.StatusInternalServerError, err)
			return
		}
		if _, err := file.Seek(0, io.SeekStart); err != nil {
			writeAttachmentError(w, http.StatusInternalServerError, err)
			return
		}
		w.Header().Set("Content-Type", http.DetectContentType(sample[:read]))
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if _, err := io.Copy(w, file); err != nil {
			return
		}
	}
}

func writeAttachmentError(w http.ResponseWriter, status int, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
}
