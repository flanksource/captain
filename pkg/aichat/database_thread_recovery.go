package aichat

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/flanksource/captain/pkg/ai/history"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/database"
	"github.com/flanksource/captain/pkg/session"
	"github.com/google/uuid"
)

var ErrHistoryUnavailable = errors.New("provider history unavailable")

func (s *DatabaseThreadStore) recoverMessages(
	ctx context.Context,
	overview database.SessionOverview,
	turns []database.SessionTurn,
	cached []session.Message,
) ([]session.Message, error) {
	if !needsProviderHistory(overview, turns, cached) {
		return cached, nil
	}
	providerID := stringPointer(overview.ProviderSessionID)
	if providerID == "" {
		return nil, fmt.Errorf("%w: terminal chat session %s has no provider session ID", ErrHistoryUnavailable, overview.ID)
	}
	transcript, err := s.db.GetTranscriptSession(ctx, overview.ID)
	if err != nil && !errors.Is(err, database.ErrSessionNotFound) {
		return nil, err
	}
	source := providerHistorySource(overview, transcript)
	path, err := locateProviderHistory(source, providerID, transcript)
	if err != nil {
		return nil, fmt.Errorf("%w for %s: %v", ErrHistoryUnavailable, providerID, err)
	}
	recovered, err := parseProviderHistory(source, path, providerID)
	if err != nil {
		return nil, fmt.Errorf("%w for %s: %v", ErrHistoryUnavailable, providerID, err)
	}
	if transcript == nil {
		transcript, err = s.db.CreateOrGetSession(ctx, database.CreateSessionInput{
			ProviderSessionID: providerID, Source: source, Provider: providerForHistorySource(source), HostID: overview.HostID,
			ParentSessionID: &overview.ID, ParentRelation: database.SessionParentRelationTranscript,
			Path: path, CWD: stringPointer(overview.CWD),
		})
		if err != nil {
			return nil, fmt.Errorf("bind recovered provider history: %w", err)
		}
	}
	if err := s.registerRecoveredSource(ctx, transcript.ID, source, providerID, path); err != nil {
		return nil, err
	}
	providerMessages, _ := recovered.ToUIMessages()
	return mergeRecoveredMessages(cached, providerMessages, recovered.Turns, turns), nil
}

func needsProviderHistory(overview database.SessionOverview, turns []database.SessionTurn, messages []session.Message) bool {
	if stringPointer(overview.ProviderSessionID) == "" {
		return false
	}
	switch database.SessionLifecycleStatus(overview.LifecycleStatus) {
	case database.SessionLifecycleSucceeded, database.SessionLifecycleFailed,
		database.SessionLifecycleCancelled, database.SessionLifecycleInterrupted:
	default:
		return false
	}
	if len(turns) == 0 {
		return false
	}
	userByTurn := make(map[string]bool, len(turns))
	assistantByTurn := make(map[string]bool, len(turns))
	for _, message := range messages {
		switch message.Role {
		case "user":
			userByTurn[message.TurnID] = true
		case "assistant":
			assistantByTurn[message.TurnID] = true
		}
	}
	for _, turn := range turns {
		turnID := turn.ID.String()
		if turn.Status != string(database.TurnStatusOpen) && userByTurn[turnID] && !assistantByTurn[turnID] {
			return true
		}
	}
	return false
}

func providerHistorySource(overview database.SessionOverview, transcript *database.Session) string {
	if transcript != nil && (transcript.Source == "claude" || transcript.Source == "codex") {
		return transcript.Source
	}
	// Which transcript a run leaves is a property of the provider family and of
	// running locally at all — every local Claude mode writes a `claude`
	// transcript, and the API mode writes none.
	if overview.ModelProvider == nil || overview.ModelMode == nil {
		return ""
	}
	if api.RuntimeMode(*overview.ModelMode) == api.ModeAPI {
		return ""
	}
	provider, known := api.ProviderByName(*overview.ModelProvider)
	if !known || (provider != api.Anthropic && provider != api.OpenAI) {
		return ""
	}
	return provider.AgentName
}

func locateProviderHistory(source, providerID string, transcript *database.Session) (string, error) {
	if transcript != nil && strings.TrimSpace(transcript.Path) != "" {
		if _, err := os.Stat(transcript.Path); err == nil {
			return transcript.Path, nil
		}
	}
	switch source {
	case "claude":
		return history.FindSessionFile(providerID)
	case "codex":
		return findCodexHistory(providerID)
	case "":
		claudePath, claudeErr := history.FindSessionFile(providerID)
		codexPath, codexErr := findCodexHistory(providerID)
		if claudeErr == nil && codexErr != nil {
			return claudePath, nil
		}
		if codexErr == nil && claudeErr != nil {
			return codexPath, nil
		}
		if claudeErr == nil {
			return "", fmt.Errorf("provider session exists in both Claude and Codex history")
		}
		return "", errors.Join(claudeErr, codexErr)
	default:
		return "", fmt.Errorf("unsupported provider history source %q", source)
	}
}

func findCodexHistory(providerID string) (string, error) {
	files, err := history.FindCodexSessionFiles()
	if err != nil {
		return "", err
	}
	for _, path := range files {
		if !strings.Contains(filepath.Base(path), providerID) {
			continue
		}
		info, readErr := history.ReadCodexSessionMeta(path)
		if readErr == nil && info != nil && info.ID == providerID {
			return path, nil
		}
	}
	return "", fmt.Errorf("codex session %s was not found", providerID)
}

func parseProviderHistory(source, path, providerID string) (*session.Session, error) {
	switch source {
	case "claude":
		recovered, info, err := session.BuildTranscriptFile(path)
		if err != nil {
			return nil, err
		}
		if info.RootSessionID != providerID {
			return nil, fmt.Errorf("claude transcript identifies %s", info.RootSessionID)
		}
		return recovered, nil
	case "codex":
		recovered, err := session.BuildCodexFile(path)
		if err != nil {
			return nil, err
		}
		if recovered.ID != providerID {
			return nil, fmt.Errorf("codex transcript identifies %s", recovered.ID)
		}
		return recovered, nil
	default:
		return nil, fmt.Errorf("cannot determine provider history source")
	}
}

func (s *DatabaseThreadStore) registerRecoveredSource(
	ctx context.Context,
	sessionID uuid.UUID,
	source, providerID, path string,
) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("%w for %s: stat %s: %v", ErrHistoryUnavailable, providerID, path, err)
	}
	return s.db.RegisterSessionSource(ctx, sessionID, database.IngestSourceInput{
		SourceKind: source, Path: path, SourceIdentity: providerID,
		ParserVersion: session.TranscriptParserVersion, ByteOffset: info.Size(), ObservedSize: info.Size(),
		ObservedModTime: info.ModTime(),
	})
}

func providerForHistorySource(source string) string {
	if source == "claude" {
		return "anthropic"
	}
	return "openai"
}

func mergeRecoveredMessages(
	cached, recovered []session.Message,
	recoveredTurns []session.Turn,
	turns []database.SessionTurn,
) []session.Message {
	targetTurnByRecoveredID := make(map[string]string, len(recoveredTurns))
	targetTurnByMessage := make(map[string]string)
	for index, turn := range recoveredTurns {
		if index >= len(turns) {
			break
		}
		target := turns[index].ID.String()
		targetTurnByRecoveredID[turn.ID] = target
		for _, messageID := range turn.MessageIDs {
			targetTurnByMessage[messageID] = target
		}
	}
	used := make([]bool, len(cached))
	merged := make([]session.Message, 0, len(cached)+len(recovered))
	for _, message := range recovered {
		target := targetTurnByMessage[message.ID]
		if target == "" {
			target = targetTurnByRecoveredID[message.TurnID]
		}
		if target != "" {
			message.TurnID = target
		}
		for index := range cached {
			if !used[index] && cached[index].Role == message.Role && cached[index].TurnID == message.TurnID {
				message, used[index] = cached[index], true
				break
			}
		}
		merged = append(merged, message)
	}
	for index, message := range cached {
		if !used[index] {
			merged = append(merged, message)
		}
	}
	return merged
}
