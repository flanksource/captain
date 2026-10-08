package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// These specs join the package's existing suite (see session_get_multi_test.go);
// Ginkgo permits only one RunSpecs per package.

// writeTempFile writes body to a uniquely named file under a fresh temp dir and
// returns its path, so an `@` reference has something real to resolve.
func writeTempFile(name, body string) string {
	path := filepath.Join(GinkgoT().TempDir(), name)
	Expect(os.WriteFile(path, []byte(body), 0o600)).To(Succeed())
	return path
}

func ctx() context.Context { return context.Background() }

// renderedUser is the prompt body as the provider would receive it: literal or
// templated, with any unconsumed stdin appended.
func renderedUser(body promptBody, vars promptVarsResult, stdin string) string {
	req, err := promptFrontmatter(body, vars, stdin)
	Expect(err).ToNot(HaveOccurred())
	return req.Prompt.User
}

var _ = Describe("prompt flag decoding", func() {
	Describe("@file expansion", func() {
		It("loads the file contents for --prompt/-p", func() {
			path := writeTempFile("brief.md", "the real prompt body")

			opts, err := actionFlagsToOptions(map[string]string{"prompt": "@" + path})

			Expect(err).ToNot(HaveOccurred())
			Expect(opts.Prompt).To(Equal("the real prompt body"))
		})

		It("loads the file contents for --system and --append-system", func() {
			system := writeTempFile("system.md", "you are terse")
			appended := writeTempFile("append.md", "also cite sources")

			opts, err := actionFlagsToOptions(map[string]string{
				"system":        "@" + system,
				"append-system": "@" + appended,
			})

			Expect(err).ToNot(HaveOccurred())
			Expect(opts.System).To(Equal("you are terse"))
			Expect(opts.AppendSystem).To(Equal("also cite sources"))
		})

		It("leaves a value without the @ prefix untouched", func() {
			opts, err := actionFlagsToOptions(map[string]string{"prompt": "summarize this"})

			Expect(err).ToNot(HaveOccurred())
			Expect(opts.Prompt).To(Equal("summarize this"))
		})

		It("refuses to read a private key", func() {
			key := writeTempFile("server.pem", "-----BEGIN PRIVATE KEY-----")

			_, err := actionFlagsToOptions(map[string]string{"prompt": "@" + key})

			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("refusing to read"))
		})

		It("reports a missing file rather than passing the path through", func() {
			_, err := actionFlagsToOptions(map[string]string{"prompt": "@/nonexistent/prompt.md"})

			Expect(err).To(HaveOccurred())
		})
	})

	// Slice/bool/int decoding is covered by TestActionFlagsToOptions_DecodesSlicesBoolsInts.

	Describe("literal vs template bodies", func() {
		It("delivers a piped document containing Handlebars verbatim", func() {
			doc := "Review this template: {{ .Foo }} and {{#if x}}y{{/if}}"

			body, err := loadPromptContent(ctx(), promptContentOptions{Stdin: doc})

			Expect(err).ToNot(HaveOccurred())
			Expect(body.Literal).To(BeTrue())
			Expect(renderedUser(body, promptVarsResult{}, doc)).To(Equal(doc))
		})

		It("does not read a leading --- block of piped data as frontmatter", func() {
			doc := "---\nmodel: attacker-chosen\n---\nthe actual document"

			body, err := loadPromptContent(ctx(), promptContentOptions{Stdin: doc})

			Expect(err).ToNot(HaveOccurred())
			Expect(renderedUser(body, promptVarsResult{}, doc)).To(Equal(doc))
		})

		It("templates piped text once --var is supplied", func() {
			body, err := loadPromptContent(ctx(), promptContentOptions{Stdin: "hello {{name}}", HasVars: true})

			Expect(err).ToNot(HaveOccurred())
			Expect(body.Literal).To(BeFalse())
			vars := promptVarsResult{Data: map[string]any{"name": "world"}}
			Expect(renderedUser(body, vars, "hello {{name}}")).To(Equal("hello world"))
		})

		It("templates -p text that asks for {{input}} by name", func() {
			opts := AIPromptOptions{Prompt: "Review: {{input}}"}

			body, err := loadPromptContent(ctx(), promptContentOptions{Prompt: opts, Stdin: "the diff"})

			Expect(err).ToNot(HaveOccurred())
			Expect(body.Literal).To(BeFalse())
			vars := promptVarsResult{Data: map[string]any{"input": "the diff"}}
			Expect(renderedUser(body, vars, "the diff")).To(Equal("Review: the diff"))
		})
	})

	Describe("piped bytes nothing consumed", func() {
		It("appends stdin to a template that never references {{input}}", func() {
			// The commit-prompt shape: declares {{patch}}, never {{input}}.
			tmpl := "---\nmodel: claude-sonnet-5\n---\n{{role \"user\"}}\nDIFF:\n{{patch}}"
			body := promptBody{Text: tmpl, Source: "commit.prompt"}

			user := renderedUser(body, promptVarsResult{Data: map[string]any{"input": "the diff"}}, "the diff")

			Expect(user).To(ContainSubstring("DIFF:"))
			Expect(user).To(HaveSuffix("the diff"))
		})

		It("does not append stdin a template already consumed", func() {
			tmpl := "---\nmodel: claude-sonnet-5\n---\n{{role \"user\"}}\nReview: {{input}}"
			body := promptBody{Text: tmpl, Source: "review.prompt"}

			user := renderedUser(body, promptVarsResult{Data: map[string]any{"input": "the diff"}}, "the diff")

			Expect(user).To(Equal("Review: the diff"))
		})

		It("does not append stdin an explicit -V key=- already bound", func() {
			tmpl := "---\nmodel: claude-sonnet-5\n---\n{{role \"user\"}}\nDIFF:\n{{patch}}"
			body := promptBody{Text: tmpl, Source: "commit.prompt"}
			vars := promptVarsResult{Data: map[string]any{"patch": "the diff"}, StdinBoundToVar: true}

			user := renderedUser(body, vars, "the diff")

			Expect(strings.Count(user, "the diff")).To(Equal(1))
		})
	})

	Describe("explicit stdin markers", func() {
		It("reads stdin for -p -", func() {
			body, err := loadPromptContent(ctx(), promptContentOptions{
				Prompt: AIPromptOptions{Prompt: "-"},
				Stdin:  "piped body",
			})

			Expect(err).ToNot(HaveOccurred())
			Expect(body.Text).To(Equal("piped body"))
			Expect(body.UsedStdin).To(BeTrue())
		})

		It("refuses -p - when nothing was piped", func() {
			_, err := loadPromptContent(ctx(), promptContentOptions{Prompt: AIPromptOptions{Prompt: "-"}})

			Expect(err).To(HaveOccurred())
		})
	})

	Describe("--var values", func() {
		It("expands an @file value and binds - to stdin", func() {
			path := writeTempFile("patch.diff", "the patch text")

			data, boundStdin, err := parseVars([]string{"patch=@" + path, "note=-"}, "piped note")

			Expect(err).ToNot(HaveOccurred())
			Expect(boundStdin).To(BeTrue())
			Expect(data).To(Equal(map[string]any{"patch": "the patch text", "note": "piped note"}))
		})

		It("refuses - when nothing was piped", func() {
			_, _, err := parseVars([]string{"note=-"}, "")

			Expect(err).To(HaveOccurred())
		})
	})
})
