package aichat_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"

	. "github.com/onsi/gomega"

	"github.com/flanksource/captain/pkg/aichat"
	"github.com/flanksource/captain/pkg/api"
)

type fakeAttachmentResolver struct{}

func (fakeAttachmentResolver) Resolve(_ context.Context, inputs []aichat.AttachmentInput) ([]api.AttachmentRef, error) {
	refs := make([]api.AttachmentRef, len(inputs))
	for i, input := range inputs {
		refs[i] = api.AttachmentRef{
			ID:       api.AttachmentIDPrefix + "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			Filename: input.Filename, MediaType: input.MediaType,
		}.WithPreparedContent(api.AttachmentContent{Bytes: []byte("image")})
	}
	return refs, nil
}

func requestJSON(method, path string, body any) *http.Request {
	var payload bytes.Buffer
	Expect(json.NewEncoder(&payload).Encode(body)).To(Succeed())
	return httptest.NewRequest(method, path, &payload)
}
