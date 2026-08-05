package cli

import (
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	. "github.com/onsi/gomega/gstruct"
)

var _ = Describe("Database contexts", Serial, func() {
	// writeGavelDBConfig points HOME at a scratch dir and writes db.json there,
	// so context resolution reads a known configuration.
	writeGavelDBConfig := func(body string) {
		home := GinkgoT().TempDir()
		GinkgoT().Setenv("HOME", home)
		dir := filepath.Join(home, ".config", "gavel")
		Expect(os.MkdirAll(dir, 0o755)).To(Succeed())
		Expect(os.WriteFile(filepath.Join(dir, "db.json"), []byte(body), 0o644)).To(Succeed())
		resetDatabaseContextCache()
	}

	BeforeEach(func() {
		databaseURLs = nil
		databaseContextFlagValue = ""
		GinkgoT().Setenv(databaseContextsEnv, "")
		GinkgoT().Setenv(databaseContextEnv, "")
		GinkgoT().Setenv("HOME", GinkgoT().TempDir())
		resetDatabaseContextCache()
	})

	AfterEach(func() {
		databaseURLs = nil
		databaseContextFlagValue = ""
		resetDatabaseContextCache()
	})

	Describe("configuration sources", func() {
		It("declares only the default context when nothing is configured", func() {
			contexts, err := databaseContexts()

			Expect(err).NotTo(HaveOccurred())
			Expect(contexts).To(HaveLen(1))
			Expect(contexts[0].Name).To(Equal(defaultDatabaseContextName))
			Expect(contexts[0].Default).To(BeTrue())
		})

		It("declares only the default context for a legacy db.json", func() {
			writeGavelDBConfig(`{"mode":"dsn","dsn":"postgres://legacy/gavel"}`)

			contexts, err := databaseContexts()

			Expect(err).NotTo(HaveOccurred())
			Expect(databaseContextNames(contexts)).To(Equal([]string{defaultDatabaseContextName}))
		})

		It("reads named contexts from db.json", func() {
			writeGavelDBConfig(`{"mode":"dsn","dsn":"postgres://local/gavel","contexts":{
				"prod":{"dsn":"postgres://reader@prod/gavel","label":"Production"},
				"box2":{"dsn":"postgres://moshe@box2/gavel","readOnly":false}}}`)

			contexts, err := databaseContexts()

			Expect(err).NotTo(HaveOccurred())
			Expect(databaseContextNames(contexts)).To(Equal([]string{defaultDatabaseContextName, "box2", "prod"}))
			prod, err := lookupDatabaseContext("prod")
			Expect(err).NotTo(HaveOccurred())
			Expect(prod).To(MatchFields(IgnoreExtras, Fields{
				"Label": Equal("Production"), "DSN": Equal("postgres://reader@prod/gavel"),
				"ReadOnly": BeTrue(), "Default": BeFalse(),
			}))
			box2, err := lookupDatabaseContext("box2")
			Expect(err).NotTo(HaveOccurred())
			Expect(box2.ReadOnly).To(BeFalse(), "readOnly:false must survive into the resolved context")
		})

		It("declares contexts from the environment", func() {
			GinkgoT().Setenv(databaseContextsEnv, "ci=postgres://ci/gavel;box2=postgres://box2/gavel")
			resetDatabaseContextCache()

			contexts, err := databaseContexts()

			Expect(err).NotTo(HaveOccurred())
			Expect(databaseContextNames(contexts)).To(Equal([]string{defaultDatabaseContextName, "box2", "ci"}))
		})

		It("prefers the flag over the environment over db.json for the same name", func() {
			writeGavelDBConfig(`{"contexts":{"prod":{"dsn":"postgres://config/gavel"}}}`)
			GinkgoT().Setenv(databaseContextsEnv, "prod=postgres://env/gavel")
			databaseURLs = []string{"prod=postgres://flag/gavel"}
			resetDatabaseContextCache()

			prod, err := lookupDatabaseContext("prod")

			Expect(err).NotTo(HaveOccurred())
			Expect(prod.DSN).To(Equal("postgres://flag/gavel"))
			Expect(prod.Source).To(Equal("--" + databaseURLFlag))
		})

		It("rejects a context named default", func() {
			writeGavelDBConfig(`{"contexts":{"default":{"dsn":"postgres://other/gavel"}}}`)

			_, err := databaseContexts()

			Expect(err).To(MatchError(ContainSubstring("reserved context name")))
		})

		It("rejects an environment entry whose name is not a valid context name", func() {
			GinkgoT().Setenv(databaseContextsEnv, "Prod Box=postgres://prod/gavel")
			resetDatabaseContextCache()

			_, err := databaseContexts()

			Expect(err).To(MatchError(ContainSubstring("must be name=dsn")))
		})

		It("rejects a db.json context name that is not a valid context name", func() {
			writeGavelDBConfig(`{"contexts":{"Prod Box":{"dsn":"postgres://prod/gavel"}}}`)

			_, err := databaseContexts()

			Expect(err).To(MatchError(ContainSubstring("invalid context name")))
		})

		It("rejects an empty dsn", func() {
			writeGavelDBConfig(`{"contexts":{"prod":{"dsn":"  "}}}`)

			_, err := databaseContexts()

			Expect(err).To(MatchError(ContainSubstring(`context "prod" has an empty dsn`)))
		})

		It("reports the configured names when a context is unknown", func() {
			GinkgoT().Setenv(databaseContextsEnv, "ci=postgres://ci/gavel")
			resetDatabaseContextCache()

			_, err := lookupDatabaseContext("nope")

			Expect(err).To(MatchError(errUnknownDatabaseContext))
			Expect(err.Error()).To(ContainSubstring("configured: default, ci"))
		})
	})

	Describe("--db-url spec parsing", func() {
		It("treats a bare URL as the default context's DSN", func() {
			databaseURLs = []string{"postgres://flag/captain"}
			resetDatabaseContextCache()

			override, err := defaultDatabaseURLOverride()
			contexts, contextsErr := databaseContexts()

			Expect(err).NotTo(HaveOccurred())
			Expect(override).To(Equal("postgres://flag/captain"))
			Expect(contextsErr).NotTo(HaveOccurred())
			Expect(databaseContextNames(contexts)).To(Equal([]string{defaultDatabaseContextName}))
		})

		It("treats a libpq keyword DSN as unnamed despite its = signs", func() {
			databaseURLs = []string{"host=localhost dbname=gavel user=moshe"}
			resetDatabaseContextCache()

			override, err := defaultDatabaseURLOverride()

			Expect(err).NotTo(HaveOccurred())
			Expect(override).To(Equal("host=localhost dbname=gavel user=moshe"))
		})

		It("rejects two unnamed DSNs", func() {
			databaseURLs = []string{"postgres://one/captain", "postgres://two/captain"}

			_, err := defaultDatabaseURLOverride()

			Expect(err).To(MatchError(ContainSubstring("at most one unnamed DSN")))
		})

		It("keeps a named DSN out of the default context", func() {
			databaseURLs = []string{"prod=postgres://prod/gavel"}
			resetDatabaseContextCache()

			override, err := defaultDatabaseURLOverride()

			Expect(err).NotTo(HaveOccurred())
			Expect(override).To(BeEmpty())
		})
	})

	Describe("active context resolution", func() {
		BeforeEach(func() {
			GinkgoT().Setenv(databaseContextsEnv, "ctxflag=postgres://flag/gavel;ctxenv=postgres://env/gavel;ctxvalue=postgres://value/gavel")
			resetDatabaseContextCache()
		})

		It("defaults when nothing selects a context", func(ctx SpecContext) {
			Expect(activeDatabaseContextName(ctx)).To(Equal(defaultDatabaseContextName))
		})

		It("uses the environment when no flag is set", func(ctx SpecContext) {
			GinkgoT().Setenv(databaseContextEnv, "ctxenv")

			Expect(activeDatabaseContextName(ctx)).To(Equal("ctxenv"))
		})

		It("prefers the flag over the environment", func(ctx SpecContext) {
			GinkgoT().Setenv(databaseContextEnv, "ctxenv")
			databaseContextFlagValue = "ctxflag"

			Expect(activeDatabaseContextName(ctx)).To(Equal("ctxflag"))
		})

		It("prefers the context value over the flag", func(ctx SpecContext) {
			GinkgoT().Setenv(databaseContextEnv, "ctxenv")
			databaseContextFlagValue = "ctxflag"

			Expect(activeDatabaseContextName(ContextWithDatabaseContext(ctx, "ctxvalue"))).To(Equal("ctxvalue"))
		})

		It("rejects an unknown --context before the command runs", func(ctx SpecContext) {
			databaseContextFlagValue = "missing"

			_, err := ResolveDatabaseContextName(ctx)

			Expect(err).To(MatchError(errUnknownDatabaseContext))
		})
	})
})
