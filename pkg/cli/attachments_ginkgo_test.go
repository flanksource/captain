package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/flanksource/captain/pkg/aiflags"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/aichat"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/attachments"
)

var _ = Describe("attachment flags", func() {
	It("uses RFC 4180 parsing for repeated and comma-separated values", func() {
		refs, err := attachmentRefsFromFlags([]string{`"reports/q1,q2.pdf",https://example.com/chart.png`, "notes.pdf"})
		Expect(err).NotTo(HaveOccurred())
		Expect(refs).To(HaveLen(3))
		Expect(refs[0].Path).To(Equal("reports/q1,q2.pdf"))
		Expect(refs[1].URL).To(Equal("https://example.com/chart.png"))
		Expect(refs[2].Path).To(Equal("notes.pdf"))
	})

	It("rejects an empty CSV field", func() {
		_, err := attachmentRefsFromFlags([]string{"one.pdf,"})
		Expect(err).To(MatchError(ContainSubstring("empty attachment")))
	})

	It("removes balanced shell quotes around an absolute path", func() {
		path := "/Users/moshe/Desktop/Screenshot 2026-07-15 at 7.37.18.png"
		refs, err := attachmentRefsFromFlags([]string{"'" + path + "'"})
		Expect(err).NotTo(HaveOccurred())
		Expect(refs).To(Equal([]api.AttachmentRef{{Path: path}}))
	})

	It("maps the canonical prompt action attachment flag without losing quoted commas", func() {
		opts, err := actionFlagsToOptions(map[string]string{
			"attach": `"reports/q1,q2.pdf",notes.pdf`,
		})
		Expect(err).NotTo(HaveOccurred())
		refs, err := attachmentRefsFromFlags(opts.Attach)
		Expect(err).NotTo(HaveOccurred())
		Expect(refs).To(Equal([]api.AttachmentRef{
			{Path: "reports/q1,q2.pdf"},
			{Path: "notes.pdf"},
		}))
	})

	It("renders an attachment-only canonical prompt", func() {
		rendered, err := renderPromptCLI(context.Background(), "", AIPromptOptions{
			AIRuntimeOptions: AIRuntimeOptions{AIProviderOptions: AIProviderOptions{ModelFlags: aiflags.ModelFlags{Model: "gemini-2.5-pro"}}},
			Attach:           []string{"diagram.png"},
		}, "", "")
		Expect(err).NotTo(HaveOccurred())
		Expect(rendered.ValidationError).To(BeEmpty())
		Expect(rendered.Input.Prompt.Attachments).To(Equal([]api.AttachmentRef{{Path: "diagram.png"}}))
	})

	It("reports an unsupported batch attachment as one failed model", func() {
		dir := GinkgoT().TempDir()
		path := filepath.Join(dir, "diagram.png")
		content := append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 512)...)
		Expect(os.WriteFile(path, content, 0o600)).To(Succeed())

		originalExecute := executePromptRequestFunc
		DeferCleanup(func() { executePromptRequestFunc = originalExecute })
		executePromptRequestFunc = func(ctx context.Context, req ai.Request, cfg ai.Config, _ time.Duration, _ bool) (any, error) {
			if err := preparePromptAttachments(ctx, &req, cfg); err != nil {
				return nil, err
			}
			return AIPromptResult{Text: "ok", Model: req.Model.Name, Backend: string(req.Model.Backend)}, nil
		}

		req := ai.Request{Prompt: api.Prompt{
			User: "What is this image of?", Attachments: []api.AttachmentRef{{Path: path}},
		}}
		req.SetCwd(dir)
		result, err := executeSyncBatch(context.Background(), PromptRenderResult{
			Name: "attachment batch", Input: req,
		}, AIPromptOptions{MultiModels: []string{"*:sol"}})

		Expect(err).NotTo(HaveOccurred())
		Expect(result.Status).To(Equal("partial"))
		Expect(result.Succeeded).To(Equal(3))
		Expect(result.Failed).To(Equal(1))
		Expect(result.Runs).To(ContainElement(And(
			HaveField("Backend", string(api.BackendCodexCmux)),
			HaveField("Status", "failed"),
			HaveField("Error", ContainSubstring("does not accept image/png attachments")),
		)))
	})
})

var _ = Describe("attachment HTTP API", func() {
	It("uploads once and serves the durable blob by ID", func() {
		store, err := attachments.NewStore(attachments.StoreOptions{Directory: filepath.Join(GinkgoT().TempDir(), "attachments")})
		Expect(err).NotTo(HaveOccurred())
		var body bytes.Buffer
		writer := multipart.NewWriter(&body)
		part, err := writer.CreateFormFile("file", "diagram.png")
		Expect(err).NotTo(HaveOccurred())
		content := append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 512)...)
		_, err = part.Write(content)
		Expect(err).NotTo(HaveOccurred())
		Expect(writer.Close()).To(Succeed())

		upload := httptest.NewRequest(http.MethodPost, "/api/attachments", &body)
		upload.Header.Set("Content-Type", writer.FormDataContentType())
		uploadResponse := httptest.NewRecorder()
		handleAttachmentUpload(store)(uploadResponse, upload)
		Expect(uploadResponse.Code).To(Equal(http.StatusCreated))
		var ref api.AttachmentRef
		Expect(json.Unmarshal(uploadResponse.Body.Bytes(), &ref)).To(Succeed())

		download := httptest.NewRequest(http.MethodGet, "/api/attachments/"+ref.ID, nil)
		download.SetPathValue("id", ref.ID)
		downloadResponse := httptest.NewRecorder()
		handleAttachmentGet(store)(downloadResponse, download)
		Expect(downloadResponse.Code).To(Equal(http.StatusOK))
		Expect(downloadResponse.Body.Bytes()).To(Equal(content))
		Expect(downloadResponse.Header().Get("Content-Type")).To(Equal("image/png"))
	})
})

var _ = Describe("chat attachment resolver", func() {
	It("migrates a legacy data URL into the durable store", func() {
		store, err := attachments.NewStore(attachments.StoreOptions{Directory: filepath.Join(GinkgoT().TempDir(), "attachments")})
		Expect(err).NotTo(HaveOccurred())
		refs, err := (chatAttachmentResolver{store: store}).Resolve(context.Background(), []aichat.AttachmentInput{{
			URL: "data:image/png;base64,iVBORw0KGgo=", Filename: "legacy.png", MediaType: "image/png",
		}})
		Expect(err).NotTo(HaveOccurred())
		Expect(refs).To(HaveLen(1))
		Expect(refs[0].ID).To(HavePrefix(api.AttachmentIDPrefix))
		Expect(refs[0].IsPrepared()).To(BeTrue())
	})

	It("enforces aggregate request limits while migrating legacy parts", func() {
		store, err := attachments.NewStore(attachments.StoreOptions{
			Directory: filepath.Join(GinkgoT().TempDir(), "attachments"),
			Limits: attachments.Limits{
				MaxFileBytes:    1024,
				MaxRequestBytes: 1024,
				MaxFiles:        1,
			},
		})
		Expect(err).NotTo(HaveOccurred())
		_, err = (chatAttachmentResolver{store: store}).Resolve(context.Background(), []aichat.AttachmentInput{
			{URL: "data:image/png;base64,iVBORw0KGgo=", Filename: "one.png", MediaType: "image/png"},
			{URL: "data:image/png;base64,iVBORw0KGgo=", Filename: "two.png", MediaType: "image/png"},
		})
		Expect(err).To(MatchError(ContainSubstring("exceeds 1 file limit")))
	})
})

var _ = Describe("attachment garbage collection references", func() {
	It("retains durable IDs found in database JSON", func() {
		id := api.AttachmentIDPrefix + "ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789"
		references := attachmentReferencesFromContents([]string{`{"prompt":{"attachments":[{"id":"` + id + `"}]}}`})
		Expect(references).To(HaveKey(strings.ToLower(id)))
	})
})
