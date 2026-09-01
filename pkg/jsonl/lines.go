// Package jsonl reads newline-delimited records without a fixed line cap. A
// transcript line can be tens of megabytes — a Codex compaction record carries
// the replaced history verbatim — and a bufio.Scanner with a maximum token size
// turns such a line into "token too long", which silently ends the file there.
package jsonl

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"iter"
)

// Reader yields one record per call; memory follows the longest line only.
type Reader struct {
	r *bufio.Reader
}

func NewReader(r io.Reader) *Reader {
	return &Reader{r: bufio.NewReader(r)}
}

// Next returns the next line without its line terminator (LF or CRLF). The
// returned slice is freshly allocated and safe to retain. A final line with no
// terminator is returned as-is; the call after the last line returns io.EOF.
func (r *Reader) Next() ([]byte, error) {
	line, err := r.r.ReadBytes('\n')
	if errors.Is(err, io.EOF) {
		if len(line) == 0 {
			return nil, io.EOF
		}
		return trimEOL(line), nil
	}
	if err != nil {
		return nil, err
	}
	return trimEOL(line), nil
}

// Lines ranges over every line of r. A read error is yielded once, after which
// the iteration stops; io.EOF is never yielded.
func Lines(r io.Reader) iter.Seq2[[]byte, error] {
	return func(yield func([]byte, error) bool) {
		reader := NewReader(r)
		for {
			line, err := reader.Next()
			if errors.Is(err, io.EOF) {
				return
			}
			if err != nil {
				yield(nil, err)
				return
			}
			if !yield(line, nil) {
				return
			}
		}
	}
}

func trimEOL(line []byte) []byte {
	return bytes.TrimRight(line, "\r\n")
}
