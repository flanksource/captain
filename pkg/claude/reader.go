package claude

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
)

// ReadHistoryFile reads all entries from a JSONL history file
func ReadHistoryFile(path string) ([]HistoryEntry, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return ReadHistory(f)
}

// ReadHistory reads all entries from a JSONL reader
func ReadHistory(r io.Reader) ([]HistoryEntry, error) {
	var entries []HistoryEntry
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 1024*1024), 10*1024*1024) // 10MB max line

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var entry HistoryEntry
		if err := json.Unmarshal(line, &entry); err != nil {
			return entries, err
		}
		entries = append(entries, entry)
	}

	return entries, scanner.Err()
}

// HistoryIterator provides streaming access to JSONL history
type HistoryIterator struct {
	scanner *bufio.Scanner
	current HistoryEntry
	err     error
}

// NewHistoryIterator creates an iterator for streaming large files
func NewHistoryIterator(r io.Reader) *HistoryIterator {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 1024*1024), 10*1024*1024)
	return &HistoryIterator{scanner: scanner}
}

// Next advances to the next entry, returns false when done or on error
func (it *HistoryIterator) Next() bool {
	for it.scanner.Scan() {
		line := it.scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		it.current = HistoryEntry{}
		if err := json.Unmarshal(line, &it.current); err != nil {
			it.err = err
			return false
		}
		return true
	}

	it.err = it.scanner.Err()
	return false
}

// Entry returns the current entry
func (it *HistoryIterator) Entry() HistoryEntry {
	return it.current
}

// Err returns any error encountered during iteration
func (it *HistoryIterator) Err() error {
	return it.err
}
