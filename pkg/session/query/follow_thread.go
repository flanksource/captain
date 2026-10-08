package query

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/flanksource/captain/pkg/claude"
	"github.com/flanksource/captain/pkg/database"
	"github.com/flanksource/captain/pkg/session"
	"github.com/google/uuid"
)

// followThread is one read of the identity a follower tracks.
type followThread struct {
	folded  []FoldedOverview
	primary FoldedOverview
	// source is the row whose thread holds the messages: the session itself
	// once it stores any, otherwise the transcript row the monitor ingests into.
	source database.SessionOverview
}

func (f *follower) resolve(ctx context.Context) (followThread, error) {
	folded, err := ResolveFolded(ctx, f.db, f.identity)
	if err != nil {
		return followThread{}, fmt.Errorf("resolve Captain session %s: %w", f.identity, err)
	}
	if len(folded) == 0 {
		return followThread{}, fmt.Errorf("%w: %s", database.ErrSessionNotFound, f.identity)
	}
	primary, err := threadRoot(f.identity, folded)
	if err != nil {
		return followThread{}, err
	}
	thread := followThread{folded: folded, primary: primary, source: primary.Session}
	if primary.Session.MessageCount == 0 && primary.Transcript != nil {
		thread.source = *primary.Transcript
	}
	for _, item := range folded {
		f.owners[item.Session.ID.String()] = item.Session
		if item.Transcript != nil {
			f.owners[item.Transcript.ID.String()] = *item.Transcript
		}
	}
	return thread, nil
}

// threadRoot is the one resolved session whose parent is outside the result.
// An identity naming several unrelated sessions cannot be followed as one.
func threadRoot(identity string, folded []FoldedOverview) (FoldedOverview, error) {
	present := make(map[uuid.UUID]bool, len(folded))
	for _, item := range folded {
		present[item.Session.ID] = true
	}
	var roots []FoldedOverview
	for _, item := range folded {
		if item.Session.ParentSessionID == nil || !present[*item.Session.ParentSessionID] {
			roots = append(roots, item)
		}
	}
	if len(roots) != 1 {
		return FoldedOverview{}, fmt.Errorf("%w: identity %s names %d unrelated sessions; follow one Captain session id",
			database.ErrSessionConflict, identity, len(roots))
	}
	return roots[0], nil
}

// sessionIDs is every session whose change can alter what the follower emits.
func (t followThread) sessionIDs(rows []database.TranscriptMessage) []string {
	seen := map[uuid.UUID]bool{}
	var ids []string
	add := func(id uuid.UUID) {
		if !seen[id] {
			seen[id] = true
			ids = append(ids, id.String())
		}
	}
	for _, item := range t.folded {
		add(item.Session.ID)
		if item.Transcript != nil {
			add(item.Transcript.ID)
		}
	}
	for _, row := range rows {
		add(row.SessionID)
	}
	return ids
}

// threadMessages projects the thread's stored rows. Until the transcript
// reaches the database the provider's log file is the only source, and it is
// dropped the moment the database carries messages.
func (f *follower) threadMessages(ctx context.Context, thread followThread, rows []database.TranscriptMessage) ([]session.Message, error) {
	if len(rows) > 0 {
		f.stopTail()
		return f.projectRows(ctx, rows)
	}
	path, source, ok := preIngestTranscript(thread.primary)
	if !ok {
		f.stopTail()
		return nil, nil
	}
	if f.tail == nil || f.tail.path != path {
		f.stopTail()
		tail, err := startTranscriptTail(path, source)
		if err != nil {
			return nil, fmt.Errorf("follow Captain session %s transcript %s: %w", f.identity, path, err)
		}
		f.tail = tail
	}
	return f.tail.messages()
}

func (f *follower) projectRows(ctx context.Context, rows []database.TranscriptMessage) ([]session.Message, error) {
	messages := make([]session.Message, len(rows))
	for i := range rows {
		message, err := projectSessionMessage(rows[i])
		if err != nil {
			return nil, err
		}
		owner, err := f.owner(ctx, rows[i].SessionID)
		if err != nil {
			return nil, err
		}
		message.AgentID = owner.ID.String()
		message.Provenance = &session.Provenance{
			Timestamp: rows[i].OccurredAt, CWD: stringValue(owner.CWD), Source: owner.Source,
			Model: stringValue(rows[i].Model), SessionID: stringValue(owner.ProviderSessionID), AgentID: message.AgentID,
		}
		if rows[i].SourceLine != nil {
			message.SourceLine = *rows[i].SourceLine
		}
		messages[i] = message
	}
	return messages, nil
}

func (f *follower) owner(ctx context.Context, id uuid.UUID) (database.SessionOverview, error) {
	if owner, ok := f.owners[id.String()]; ok {
		return owner, nil
	}
	rows, err := f.db.ListSessionOverviewsByIdentity(ctx, id.String())
	if err != nil {
		return database.SessionOverview{}, fmt.Errorf("load owner %s of a followed Captain message: %w", id, err)
	}
	for _, row := range rows {
		if row.ID == id {
			f.owners[id.String()] = row
			return row, nil
		}
	}
	return database.SessionOverview{}, fmt.Errorf("%w: owner %s of a followed Captain message", database.ErrSessionNotFound, id)
}

// preIngestTranscript locates the provider log of a session whose transcript
// has not been ingested: the recorded path, or for Claude the log its
// pre-generated session id is written to under the working directory.
func preIngestTranscript(primary FoldedOverview) (path, source string, ok bool) {
	row := primary.Session
	if primary.Transcript != nil {
		row = *primary.Transcript
	}
	if recorded := overviewTranscriptPath(row); recorded != "" {
		return recorded, row.Source, true
	}
	cwd, providerID := stringValue(row.CWD), stringValue(row.ProviderSessionID)
	if row.Source != "claude" || cwd == "" || providerID == "" {
		return "", "", false
	}
	return filepath.Join(claude.GetProjectsDir(), claude.NormalizePath(cwd), providerID+".jsonl"), row.Source, true
}

func (f *follower) stopTail() {
	if f.tail != nil {
		f.tail.close()
		f.tail = nil
	}
}

func (f *follower) tailChannels() (<-chan struct{}, <-chan error) {
	if f.tail == nil {
		return nil, nil
	}
	return f.tail.signal, f.tail.errs
}
