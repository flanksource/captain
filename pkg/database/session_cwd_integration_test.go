package database

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// FindSessionIDByCWD is now a plain equality test on an indexed column rather
// than an rtrim() expression, which only stays correct while every write site
// settles the spelling first. Drop normalization from one of them and the
// heuristic silently stops matching those sessions.
func TestFindSessionIDByCWDMatchesEverySpelling(t *testing.T) {
	db := openIngestTestDB(t)
	modTime := time.Now().UTC().Truncate(time.Second)

	created, err := db.CreateOrGetSession(t.Context(), CreateSessionInput{
		ProviderSessionID: "cwd-created", Source: "codex", HostID: "test-host", CWD: "/home/dev/created/",
	})
	require.NoError(t, err)
	assert.Equal(t, "/home/dev/created", created.CWD, "CreateOrGetSession must store one spelling")

	ingested := testIngestBatch(modTime, 1024)
	ingested.Session.CWD = "/home/dev/example//"
	persisted, err := db.IngestTranscript(t.Context(), ingested)
	require.NoError(t, err)
	assert.Equal(t, "/home/dev/example", persisted.CWD, "the ingest projection must store one spelling")

	// The rewrite only pays off if the schema actually carries the index the
	// equality test was written for.
	var definition string
	require.NoError(t, db.Gorm().Raw(
		`SELECT indexdef FROM pg_indexes WHERE indexname = 'captain_sessions_source_cwd_idx'`).
		Scan(&definition).Error)
	assert.Contains(t, definition, "(source, cwd)")
	assert.Contains(t, definition, "WHERE (parent_session_id IS NULL)")

	for _, test := range []struct {
		name, source, cwd string
		want              string
	}{
		{name: "created session, exact directory", source: "codex", cwd: "/home/dev/created", want: created.ID.String()},
		{name: "created session, trailing slash", source: "codex", cwd: "/home/dev/created/", want: created.ID.String()},
		{name: "ingested session, exact directory", source: "claude", cwd: "/home/dev/example", want: persisted.ID.String()},
		{name: "ingested session, trailing slash", source: "claude", cwd: "/home/dev/example/", want: persisted.ID.String()},
		{name: "another source in the same directory does not match", source: "codex", cwd: "/home/dev/example"},
		{name: "an unknown directory does not match", source: "claude", cwd: "/home/dev/nowhere"},
	} {
		t.Run(test.name, func(t *testing.T) {
			found, err := db.FindSessionIDByCWD(t.Context(), test.source, test.cwd)
			require.NoError(t, err)
			if test.want == "" {
				assert.Zero(t, found)
				return
			}
			assert.Equal(t, test.want, found.String())
		})
	}
}
