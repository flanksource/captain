package credentials_test

import (
	"os"
	"path/filepath"
	"sync"

	"github.com/flanksource/captain/pkg/credentials"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Vault", func() {
	var path string

	BeforeEach(func() {
		path = filepath.Join(GinkgoT().TempDir(), ".config", "captain", "vault")
	})

	It("treats a missing vault as empty", func() {
		values, err := credentials.NewVault(path).Load()
		Expect(err).NotTo(HaveOccurred())
		Expect(values).To(BeEmpty())
	})

	It("writes a private directory and atomically round-trips provider tokens", func() {
		vault := credentials.NewVault(path)
		Expect(vault.Set("openai", "sk-openai-example")).To(Succeed())
		Expect(vault.Set("anthropic", "sk-ant-example")).To(Succeed())

		values, err := vault.Load()
		Expect(err).NotTo(HaveOccurred())
		Expect(values).To(Equal(map[string]string{
			"anthropic": "sk-ant-example",
			"openai":    "sk-openai-example",
		}))

		dirInfo, err := os.Stat(filepath.Dir(path))
		Expect(err).NotTo(HaveOccurred())
		Expect(dirInfo.Mode().Perm()).To(Equal(os.FileMode(0o700)))
		fileInfo, err := os.Stat(path)
		Expect(err).NotTo(HaveOccurred())
		Expect(fileInfo.Mode().Perm()).To(Equal(os.FileMode(0o600)))
	})

	It("fails on malformed content without overwriting it", func() {
		Expect(os.MkdirAll(filepath.Dir(path), 0o700)).To(Succeed())
		invalid := []byte(`{"openai":`)
		Expect(os.WriteFile(path, invalid, 0o600)).To(Succeed())

		Expect(credentials.NewVault(path).Set("openai", "replacement")).NotTo(Succeed())
		contents, err := os.ReadFile(path)
		Expect(err).NotTo(HaveOccurred())
		Expect(contents).To(Equal(invalid))
	})

	It("serializes concurrent provider updates", func() {
		vault := credentials.NewVault(path)
		providers := map[string]string{
			"anthropic": "ant-token",
			"openai":    "openai-token",
			"gemini":    "gemini-token",
			"deepseek":  "deepseek-token",
		}
		var wg sync.WaitGroup
		for provider, token := range providers {
			wg.Add(1)
			go func() {
				defer GinkgoRecover()
				defer wg.Done()
				Expect(vault.Set(provider, token)).To(Succeed())
			}()
		}
		wg.Wait()

		values, err := vault.Load()
		Expect(err).NotTo(HaveOccurred())
		Expect(values).To(Equal(providers))
	})

	It("resolves vault before environment and reports the source", func() {
		vault := credentials.NewVault(path)
		Expect(vault.Set("gemini", "vault-token")).To(Succeed())

		resolved, err := vault.Resolve("gemini", []string{"GEMINI_API_KEY"}, func(string) string { return "env-token" })
		Expect(err).NotTo(HaveOccurred())
		Expect(resolved).To(Equal(credentials.Resolved{Token: "vault-token", Source: credentials.SourceVault}))

		resolved, err = vault.Resolve("openai", []string{"OPENAI_API_KEY"}, func(string) string { return "env-token" })
		Expect(err).NotTo(HaveOccurred())
		Expect(resolved).To(Equal(credentials.Resolved{Token: "env-token", Source: credentials.SourceEnvironment, Detail: "OPENAI_API_KEY"}))
	})
})
