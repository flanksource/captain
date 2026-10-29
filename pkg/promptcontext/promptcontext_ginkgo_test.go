package promptcontext_test

import (
	"context"
	"testing/fstest"

	promptlib "github.com/flanksource/captain/pkg/ai/prompt"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/promptcontext"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

const (
	prURL      = "https://example.com/acme/repo/pull/7"
	prKey      = "gavel.pr.url"
	prStatus   = "Read live PR status: gavel pr status " + prURL + " --json --logs (see #99)"
	userPrompt = "Fix #12 before release"
)

func hookFile(hook, body string) *fstest.MapFile {
	return &fstest.MapFile{Data: []byte("---\nhook:\n" + hook + "\n---\n" + body + "\n")}
}

// prStatusHook appends the PR status block when the host sets the PR url; its
// output contains "#99", which the issue hook's pattern would match.
var prStatusHook = hookFile("  on: prompt.context\n  mode: append\n  when: '\""+prKey+"\" in metadata'",
	`Read live PR status: gavel pr status {{lookup metadata "`+prKey+`"}} --json --logs (see #99)`)

// issueHook substitutes the first issue reference with its expansion.
var issueHook = hookFile("  on: prompt.context\n  mode: substitute\n  match: '#(?P<num>\\d+)'\n  when: captures.num != \"0\"",
	"issue {{num}} ({{captures.num}})")

func source(label string, files fstest.MapFS) promptlib.HookSource {
	return promptlib.HookSource{FS: files, Label: label}
}

func discover(sources ...promptlib.HookSource) []promptcontext.Hook {
	hooks, err := promptcontext.Discover(sources...)
	Expect(err).NotTo(HaveOccurred())
	return hooks
}

func names(hooks []promptcontext.Hook) []string {
	out := make([]string, 0, len(hooks))
	for _, hook := range hooks {
		out = append(out, hook.Name+"@"+hook.Source)
	}
	return out
}

func prSpec() *api.Spec {
	return &api.Spec{Prompt: api.Prompt{User: userPrompt, Metadata: map[string]string{prKey: prURL}}}
}

var _ = Describe("Discover", func() {
	It("shadows by name, removes disabled hooks and ignores other events, in name order", func() {
		base := source("base", fstest.MapFS{
			"pr-status.prompt": prStatusHook,
			"issue.prompt":     hookFile("  on: prompt.context\n  mode: append\n  match: x", "base"),
			"retired.prompt":   hookFile("  on: prompt.context\n  mode: append\n  match: x", "retired"),
			"plan-tldr.prompt": hookFile("  on: plan.revision\n  mode: prepend", "tldr"),
			"plain.prompt":     &fstest.MapFile{Data: []byte("hello")},
		})
		local := source("local", fstest.MapFS{
			"issue.prompt":   issueHook,
			"retired.prompt": hookFile("  disabled: true", ""),
		})
		Expect(names(discover(base, local))).To(Equal([]string{"issue@local:issue.prompt", "pr-status@base:pr-status.prompt"}))
	})

	It("discovers hooks beside a prompt whose frontmatter is templated", func() {
		templated := &fstest.MapFile{Data: []byte("---\noutput:\n  schema:\n    minItems: 1\n{{#if maxCommits}}\n    maxItems: {{maxCommits}}\n{{/if}}\n---\nbody\n")}
		Expect(names(discover(source("dir", fstest.MapFS{"commit-grouping.prompt": templated, "pr-status.prompt": prStatusHook})))).
			To(Equal([]string{"pr-status@dir:pr-status.prompt"}))
	})

	It("fails naming the source of a prompt whose frontmatter is malformed", func() {
		_, err := promptcontext.Discover(source("dir", fstest.MapFS{"broken.prompt": &fstest.MapFile{Data: []byte("---\nmodel: [unfinished\n---\nx\n")}}))
		Expect(err).To(MatchError(SatisfyAll(ContainSubstring("dir:broken.prompt"), ContainSubstring("yaml:"))))
	})

	DescribeTable("fails loudly naming the hook and its source",
		func(hook, message string) {
			_, err := promptcontext.Discover(source("dir", fstest.MapFS{"broken.prompt": hookFile(hook, "x")}))
			Expect(err).To(MatchError(ContainSubstring("dir:broken.prompt")))
			Expect(err).To(MatchError(ContainSubstring(message)))
		},
		Entry("unconditional", "  on: prompt.context\n  mode: append", "hooks require `match` or `when`"),
		Entry("whole-prompt replace", "  on: prompt.context\n  mode: replace\n  match: x", "do not support mode replace"),
		Entry("invalid regex", "  on: prompt.context\n  mode: append\n  match: '('", "match"),
		Entry("capture shadowing a variable", "  on: prompt.context\n  mode: append\n  match: '(?P<prompt>x)'", `capture "prompt"`),
		Entry("undeclared CEL variable", "  on: prompt.context\n  mode: append\n  when: branch == 'main'", "undeclared reference to 'branch'"),
		Entry("non-bool CEL predicate", "  on: prompt.context\n  mode: append\n  when: prompt", "must return bool"),
		Entry("field without a model", "  on: prompt.context\n  mode: append\n  when: 'true'\n  field: text", "field requires render: model"),
	)
})

var _ = Describe("Apply", func() {
	var hooks []promptcontext.Hook

	BeforeEach(func() {
		hooks = discover(source("dir", fstest.MapFS{"pr-status.prompt": prStatusHook, "issue.prompt": issueHook}))
	})

	It("skips every hook whose match or predicate fails and leaves the spec untouched", func(ctx SpecContext) {
		spec := &api.Spec{Prompt: api.Prompt{User: "nothing to see"}}
		applied, err := promptcontext.Apply(ctx, spec, hooks)
		Expect(err).NotTo(HaveOccurred())
		Expect(applied).To(BeEmpty())
		Expect(*spec).To(Equal(api.Spec{Prompt: api.Prompt{User: "nothing to see"}}))
	})

	It("substitutes the matched span, appends a block and records the applied hooks", func(ctx SpecContext) {
		spec := prSpec()
		applied, err := promptcontext.Apply(ctx, spec, hooks)
		Expect(err).NotTo(HaveOccurred())
		Expect(applied).To(Equal(promptcontext.Applied{
			{Name: "issue", Source: "dir:issue.prompt"}, {Name: "pr-status", Source: "dir:pr-status.prompt"},
		}))
		Expect(spec.Prompt.User).To(Equal("Fix <!-- captain:context:issue replaced=\"#12\" -->\nissue 12 (12)\n<!-- /captain:context:issue --> before release" +
			"\n\n<!-- captain:context:pr-status -->\n" + prStatus + "\n<!-- /captain:context:pr-status -->"))
		Expect(spec.Prompt.Metadata).To(Equal(map[string]string{prKey: prURL, promptcontext.MetadataKey: "issue,pr-status"}))
	})

	It("is idempotent: re-applying strips its own blocks first, so output never re-matches", func(ctx SpecContext) {
		once := prSpec()
		_, err := promptcontext.Apply(ctx, once, hooks)
		Expect(err).NotTo(HaveOccurred())
		twice := prSpec()
		_, err = promptcontext.Apply(ctx, twice, hooks)
		Expect(err).NotTo(HaveOccurred())
		_, err = promptcontext.Apply(ctx, twice, hooks)
		Expect(err).NotTo(HaveOccurred())
		Expect(*twice).To(Equal(*once))
	})

	It("removes stale blocks and the metadata key when no hook applies any more", func(ctx SpecContext) {
		spec := &api.Spec{Prompt: api.Prompt{
			User:     promptlib.NewHookBlocks("captain:context").Wrap("pr-status", prStatus) + "\n\nplain request",
			Metadata: map[string]string{promptcontext.MetadataKey: "issue,pr-status"},
		}}
		applied, err := promptcontext.Apply(ctx, spec, hooks)
		Expect(err).NotTo(HaveOccurred())
		Expect(applied).To(BeEmpty())
		Expect(spec.Prompt).To(Equal(api.Prompt{User: "plain request"}))
	})

	DescribeTable("fails naming the hook",
		func(hook, body, message string) {
			broken := discover(source("dir", fstest.MapFS{"broken.prompt": hookFile(hook, body)}))
			_, err := promptcontext.Apply(context.Background(), prSpec(), broken)
			Expect(err).To(MatchError(ContainSubstring("context hook broken (dir:broken.prompt)")))
			Expect(err).To(MatchError(ContainSubstring(message)))
		},
		Entry("CEL evaluation error", "  on: prompt.context\n  mode: append\n  when: metadata['missing'] == 'x'", "x", "no such key"),
		Entry("empty output", "  on: prompt.context\n  mode: append\n  match: Fix", "{{captures.none}}", "empty output"),
	)
})
