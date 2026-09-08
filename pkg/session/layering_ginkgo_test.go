package session

import (
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

// storePackages are the packages that fetch sessions. The session model and its
// pure composition describe a session; query is the explicit store adapter.
//
// This is enforced here rather than in arch-unit.yaml because Gavel cannot run
// arch-unit as a linter (its known set is golangci-lint, ruff, eslint,
// react-doctor, tsc, vale, jscpd, betterleaks, oxlint, pyright, markdownlint)
// and the arch-unit binary has no standalone check command — a rules file would
// have been an inert gate. A test actually runs.
var storePackages = []string{
	"github.com/flanksource/captain/pkg/database",
	"github.com/flanksource/captain/pkg/aichat",
	"github.com/flanksource/captain/pkg/cli",
	"github.com/flanksource/captain/pkg/monitor",
}

var _ = ginkgo.Describe("session package layering", func() {
	ginkgo.It("keeps the model and its composition free of the packages that fetch sessions", func() {
		// Two independent session assemblers grew before this was enforced — one
		// in pkg/cli, one in pkg/aichat — and they silently disagreed about which
		// facts a session had. The one-way arrow is what stops a third appearing:
		// pkg/database already imports pkg/session, so ports are expressed in
		// session types and satisfied structurally. An import the other way would
		// invert that and reopen the door.
		offenders := map[string][]string{}

		err := filepath.WalkDir(".", func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() {
				if path == "query" {
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
				for _, forbidden := range storePackages {
					if imported == forbidden || strings.HasPrefix(imported, forbidden+"/") {
						offenders[path] = append(offenders[path], imported)
					}
				}
			}
			return nil
		})

		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(offenders).To(gomega.BeEmpty(),
			"pkg/session model and pure composition must not import a session store; store-backed reads belong in pkg/session/query")
	})
})
