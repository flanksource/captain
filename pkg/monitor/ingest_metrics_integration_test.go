package monitor

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A monitor that is quietly re-writing whole transcripts looks identical from
// the outside to one that is not: the row counts are the same either way,
// because the writes conflict away. The counters are what make the difference
// visible, so they have to be read from a real ingest rather than trusted.
func TestIngestStatsSeparateWholeFileParsesFromIncrementalWrites(t *testing.T) {
	db := openMonitorTestDB(t)
	path := writeFixtureHome(t)

	m, err := New(Config{DB: db, HostID: "test-host"})
	require.NoError(t, err)
	ingestor := newIngestor(m)

	m.backfill(t.Context(), ingestor)
	first := m.IngestStats()
	require.EqualValues(t, 1, first.FilesIngested)
	assert.EqualValues(t, 2, first.MessagesParsed)
	assert.EqualValues(t, 2, first.MessagesOffered, "a file seen for the first time is written in full")
	assert.EqualValues(t, 1, first.OfferRatio())
	assert.Positive(t, first.WriteDuration, "the write phase must be timed")

	m.backfill(t.Context(), ingestor)
	unchanged := m.IngestStats()
	assert.EqualValues(t, 2, unchanged.FilesConsidered, "the second scan looks at the file again")
	assert.EqualValues(t, 1, unchanged.FilesIngested, "an unchanged file is never re-ingested")
	assert.Equal(t, first.MessagesParsed, unchanged.MessagesParsed, "a skipped file is not even parsed")

	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	require.NoError(t, err)
	_, err = f.WriteString(fixtureAppendLine)
	require.NoError(t, err)
	require.NoError(t, f.Close())

	m.backfill(t.Context(), ingestor)
	appended := m.IngestStats()
	assert.EqualValues(t, 2, appended.FilesIngested)
	assert.EqualValues(t, 5, appended.MessagesParsed, "the append re-parses all three lines")
	assert.EqualValues(t, 3, appended.MessagesOffered, "but only the appended line is offered")
	assert.Less(t, appended.OfferRatio(), 1.0,
		"a ratio that stays at 1.0 across appends means the high-water mark is not being honoured")
}
