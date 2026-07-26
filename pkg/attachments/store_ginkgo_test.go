package attachments_test

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/attachments"
)

var _ = Describe("Store", func() {
	It("resolves a local file into an immutable content-addressed attachment", func() {
		root := GinkgoT().TempDir()
		input := filepath.Join(root, "diagram.png")
		content := append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 512)...)
		Expect(os.WriteFile(input, content, 0o600)).To(Succeed())

		store, err := attachments.NewStore(attachments.StoreOptions{Directory: filepath.Join(root, ".captain", "attachments")})
		Expect(err).NotTo(HaveOccurred())
		resolved, err := store.Resolve(context.Background(), []api.AttachmentRef{{Path: input}}, "")
		Expect(err).NotTo(HaveOccurred())
		Expect(resolved).To(HaveLen(1))

		digest := fmt.Sprintf("%x", sha256.Sum256(content))
		Expect(resolved[0].ID).To(Equal(api.AttachmentIDPrefix + digest))
		Expect(resolved[0].Filename).To(Equal("diagram.png"))
		Expect(resolved[0].MediaType).To(Equal("image/png"))
		Expect(resolved[0].Size).To(Equal(int64(len(content))))
		Expect(resolved[0].SHA256).To(Equal(digest))
		prepared, ok := resolved[0].PreparedContent()
		Expect(ok).To(BeTrue())
		Expect(prepared.Bytes).To(Equal(content))
		Expect(prepared.Path).To(Equal(store.Path(resolved[0].ID)))

		info, err := os.Stat(store.Path(resolved[0].ID))
		Expect(err).NotTo(HaveOccurred())
		Expect(info.Mode().Perm()).To(Equal(os.FileMode(0o600)))
	})

	It("rejects declared media types that disagree with detected content", func() {
		root := GinkgoT().TempDir()
		input := filepath.Join(root, "invoice.pdf")
		Expect(os.WriteFile(input, []byte("plain text, not a PDF"), 0o600)).To(Succeed())
		store, err := attachments.NewStore(attachments.StoreOptions{Directory: filepath.Join(root, "store")})
		Expect(err).NotTo(HaveOccurred())

		_, err = store.Resolve(context.Background(), []api.AttachmentRef{{Path: input, MediaType: "application/pdf"}}, "")
		Expect(err).To(MatchError(ContainSubstring("declared media type application/pdf does not match detected text/plain")))
	})

	It("enforces file, request, and count limits before execution", func() {
		root := GinkgoT().TempDir()
		input := filepath.Join(root, "large.txt")
		Expect(os.WriteFile(input, []byte("12345"), 0o600)).To(Succeed())
		store, err := attachments.NewStore(attachments.StoreOptions{
			Directory: filepath.Join(root, "store"),
			Limits: attachments.Limits{
				MaxFileBytes:    4,
				MaxRequestBytes: 8,
				MaxFiles:        1,
			},
		})
		Expect(err).NotTo(HaveOccurred())

		_, err = store.Resolve(context.Background(), []api.AttachmentRef{{Path: input}}, "")
		Expect(err).To(MatchError(ContainSubstring("exceeds 4 byte file limit")))
		_, err = store.Resolve(context.Background(), []api.AttachmentRef{{Path: input}, {Path: input}}, "")
		Expect(err).To(MatchError(ContainSubstring("exceeds 1 file limit")))
	})

	It("does not follow attachment symlinks outside the store", func() {
		root := GinkgoT().TempDir()
		store, err := attachments.NewStore(attachments.StoreOptions{Directory: filepath.Join(root, "store")})
		Expect(err).NotTo(HaveOccurred())

		outside := filepath.Join(root, "outside.txt")
		Expect(os.WriteFile(outside, []byte("secret"), 0o600)).To(Succeed())
		id := api.AttachmentIDPrefix + strings.Repeat("a", sha256.Size*2)
		path := store.Path(id)
		Expect(os.MkdirAll(filepath.Dir(path), 0o700)).To(Succeed())
		Expect(os.Symlink(outside, path)).To(Succeed())

		file, err := store.Open(id)
		if file != nil {
			DeferCleanup(file.Close)
		}
		Expect(err).To(HaveOccurred())
	})

	It("garbage-collects only old unreferenced blobs and supports dry-run", func() {
		root := GinkgoT().TempDir()
		store, err := attachments.NewStore(attachments.StoreOptions{Directory: filepath.Join(root, "store")})
		Expect(err).NotTo(HaveOccurred())
		kept, err := store.Put(strings.NewReader("keep"), "keep.txt", "text/plain")
		Expect(err).NotTo(HaveOccurred())
		removed, err := store.Put(strings.NewReader("remove"), "remove.txt", "text/plain")
		Expect(err).NotTo(HaveOccurred())
		old := time.Now().Add(-31 * 24 * time.Hour)
		Expect(os.Chtimes(store.Path(kept.ID), old, old)).To(Succeed())
		Expect(os.Chtimes(store.Path(removed.ID), old, old)).To(Succeed())

		dryRun, err := store.GC(map[string]struct{}{kept.ID: {}}, 30*24*time.Hour, true)
		Expect(err).NotTo(HaveOccurred())
		Expect(dryRun.RemovedIDs).To(Equal([]string{removed.ID}))
		_, err = os.Stat(store.Path(removed.ID))
		Expect(err).NotTo(HaveOccurred())

		result, err := store.GC(map[string]struct{}{kept.ID: {}}, 30*24*time.Hour, false)
		Expect(err).NotTo(HaveOccurred())
		Expect(result.RemovedIDs).To(Equal([]string{removed.ID}))
		Expect(store.Path(kept.ID)).To(BeAnExistingFile())
		Expect(store.Path(removed.ID)).NotTo(BeAnExistingFile())
	})
})
