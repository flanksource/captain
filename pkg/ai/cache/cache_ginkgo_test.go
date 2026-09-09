package cache_test

import (
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	. "github.com/onsi/gomega/gstruct"

	"github.com/flanksource/captain/pkg/ai/cache"
)

// cgoOnlySQLiteDrivers register their driver only under `//go:build cgo`, so a
// binary built with CGO_ENABLED=0 — which is every goreleaser artifact and the
// linux binary baked into the sandbox image — links a stub that fails on the
// first query. modernc.org/sqlite is pure Go and is already linked through
// commons-db/connection, so it is the only sqlite engine this repo may import.
var cgoOnlySQLiteDrivers = []string{"github.com/mattn/go-sqlite3"}

func newCache(ttl time.Duration) *cache.Cache {
	GinkgoHelper()
	c, err := cache.New(cache.Config{DBPath: filepath.Join(GinkgoT().TempDir(), "cache.db"), TTL: ttl})
	Expect(err).NotTo(HaveOccurred())
	DeferCleanup(c.Close)
	return c
}

func entry(prompt, response string) *cache.Entry {
	return &cache.Entry{
		Model:        "claude-opus-5",
		Provider:     "anthropic",
		Prompt:       prompt,
		Response:     response,
		TokensInput:  120,
		TokensOutput: 40,
		TokensTotal:  160,
		CostUSD:      0.25,
		DurationMS:   1500,
	}
}

var _ = Describe("AI response cache", func() {
	It("round-trips an entry through the pure-Go sqlite driver", func() {
		c := newCache(time.Hour)
		Expect(c.Set(entry("what is 2+2?", "4"))).To(Succeed())

		got, err := c.Get("what is 2+2?", "claude-opus-5")
		Expect(err).NotTo(HaveOccurred())
		Expect(*got).To(MatchFields(IgnoreExtras, Fields{
			"Model":        Equal("claude-opus-5"),
			"Provider":     Equal("anthropic"),
			"Prompt":       Equal("what is 2+2?"),
			"Response":     Equal("4"),
			"TokensInput":  Equal(120),
			"TokensOutput": Equal(40),
			"TokensTotal":  Equal(160),
			"CostUSD":      Equal(0.25),
			"DurationMS":   Equal(int64(1500)),
		}))
	})

	It("reads the TIMESTAMP columns back as times rather than raw strings", func() {
		c := newCache(time.Hour)
		Expect(c.Set(entry("when?", "now"))).To(Succeed())

		got, err := c.Get("when?", "claude-opus-5")
		Expect(err).NotTo(HaveOccurred())
		Expect(got.CreatedAt).To(BeTemporally("~", time.Now(), time.Minute))
		Expect(got.AccessedAt).To(BeTemporally("~", time.Now(), time.Minute))
		Expect(got.ExpiresAt).NotTo(BeNil())
		Expect(*got.ExpiresAt).To(BeTemporally("~", time.Now().Add(time.Hour), time.Minute))
	})

	It("misses on a prompt that was never cached", func() {
		c := newCache(time.Hour)
		Expect(c.Set(entry("cached", "yes"))).To(Succeed())

		_, err := c.Get("never cached", "claude-opus-5")
		Expect(err).To(MatchError(cache.ErrNotFound))
	})

	It("does not serve an entry past its TTL", func() {
		c := newCache(time.Millisecond)
		Expect(c.Set(entry("stale", "old answer"))).To(Succeed())

		Eventually(func() error {
			_, err := c.Get("stale", "claude-opus-5")
			return err
		}, time.Second, 10*time.Millisecond).Should(MatchError(cache.ErrNotFound))
	})

	It("refuses to read or write when caching is disabled", func() {
		c, err := cache.New(cache.Config{
			DBPath:  filepath.Join(GinkgoT().TempDir(), "cache.db"),
			TTL:     time.Hour,
			NoCache: true,
		})
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(c.Close)

		Expect(c.Set(entry("ignored", "ignored"))).To(Succeed())
		_, err = c.Get("ignored", "claude-opus-5")
		Expect(err).To(MatchError(cache.ErrCacheDisabled))
	})

	It("aggregates stats across entries, including the MIN/MAX request times", func() {
		c := newCache(time.Hour)
		Expect(c.Set(entry("first", "1"))).To(Succeed())
		Expect(c.Set(entry("second", "2"))).To(Succeed())

		stats, err := c.GetStats()
		Expect(err).NotTo(HaveOccurred())
		Expect(stats).To(HaveLen(1))
		Expect(stats[0]).To(MatchFields(IgnoreExtras, Fields{
			"Model":             Equal("claude-opus-5"),
			"Provider":          Equal("anthropic"),
			"TotalRequests":     Equal(int64(2)),
			"TotalInputTokens":  Equal(int64(240)),
			"TotalOutputTokens": Equal(int64(80)),
			"TotalCost":         BeNumerically("~", 0.5, 0.0001),
		}))
		Expect(stats[0].FirstRequest).To(BeTemporally("~", time.Now(), time.Minute))
		Expect(stats[0].LastRequest).To(BeTemporally("~", time.Now(), time.Minute))
	})

	It("clears every cached entry", func() {
		c := newCache(time.Hour)
		Expect(c.Set(entry("drop me", "gone"))).To(Succeed())
		Expect(c.Clear()).To(Succeed())

		_, err := c.Get("drop me", "claude-opus-5")
		Expect(err).To(MatchError(cache.ErrNotFound))
	})
})

var _ = Describe("sqlite driver choice", func() {
	It("keeps every captain package free of cgo-only sqlite drivers", func() {
		root, err := filepath.Abs(filepath.Join("..", "..", ".."))
		Expect(err).NotTo(HaveOccurred())

		offenders := map[string][]string{}
		err = filepath.WalkDir(root, func(path string, dirEntry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if dirEntry.IsDir() {
				// Dot-directories hold scratch worktrees (.tmp, .shell) that carry
				// their own copy of this file, and hack/ is untracked scratch.
				name := dirEntry.Name()
				if path != root && (strings.HasPrefix(name, ".") || name == "node_modules" || name == "hack") {
					return fs.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") {
				return nil
			}
			file, parseErr := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
			if parseErr != nil {
				return parseErr
			}
			for _, spec := range file.Imports {
				imported, unquoteErr := strconv.Unquote(spec.Path.Value)
				if unquoteErr != nil {
					return unquoteErr
				}
				for _, driver := range cgoOnlySQLiteDrivers {
					if imported == driver {
						rel, _ := filepath.Rel(root, path)
						offenders[rel] = append(offenders[rel], imported)
					}
				}
			}
			return nil
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(offenders).To(BeEmpty())
	})
})
