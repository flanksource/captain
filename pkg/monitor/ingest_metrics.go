package monitor

import (
	"sync/atomic"
	"time"
)

// ingestMetrics counts what the ingest loop does with the transcripts it sees.
//
// The number to watch is messages parsed against messages offered. A transcript
// is always re-parsed in full — turn aggregates are running totals over the
// whole file — but only the lines past the high-water mark are offered to the
// database. The two therefore diverge in normal operation and converge only on
// a first ingest or a parser bump.
//
// What must not happen is the ratio staying near 1 across many ingests of the
// same file: that means the mark is not being honoured and every append is
// re-submitting the entire transcript. That regression is what drove 4.26M
// insert round trips to write roughly 7,000 rows.
type ingestMetrics struct {
	filesConsidered atomic.Int64
	filesIngested   atomic.Int64
	messagesParsed  atomic.Int64
	messagesOffered atomic.Int64
	writeNanos      atomic.Int64
}

// IngestStats is a point-in-time read of the ingest counters. FilesConsidered
// counts transcripts a backfill scan looked at; hook-driven ingests go straight
// to a known file and are counted only as ingested.
type IngestStats struct {
	FilesConsidered int64         `json:"filesConsidered"`
	FilesIngested   int64         `json:"filesIngested"`
	MessagesParsed  int64         `json:"messagesParsed"`
	MessagesOffered int64         `json:"messagesOffered"`
	WriteDuration   time.Duration `json:"writeDuration"`
}

// OfferRatio is the fraction of parsed messages that reached the database. It
// falls towards zero as sessions grow, and sitting at 1.0 over a long run means
// the incremental path is not working.
func (s IngestStats) OfferRatio() float64 {
	if s.MessagesParsed == 0 {
		return 0
	}
	return float64(s.MessagesOffered) / float64(s.MessagesParsed)
}

func (m *ingestMetrics) snapshot() IngestStats {
	return IngestStats{
		FilesConsidered: m.filesConsidered.Load(),
		FilesIngested:   m.filesIngested.Load(),
		MessagesParsed:  m.messagesParsed.Load(),
		MessagesOffered: m.messagesOffered.Load(),
		WriteDuration:   time.Duration(m.writeNanos.Load()),
	}
}

// IngestStats reports the monitor's cumulative ingest counters. Embedders serve
// it next to their runtime profiler so a CPU-hungry monitor can be attributed
// without a database session.
func (m *Monitor) IngestStats() IngestStats {
	return m.ingest.snapshot()
}
