package jsonl

import (
	"errors"
	"io"
	"strings"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestJSONL(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "JSONL line reader")
}

// failingReader returns its content, then a read error instead of EOF.
type failingReader struct {
	content string
	err     error
	done    bool
}

func (r *failingReader) Read(p []byte) (int, error) {
	if !r.done {
		r.done = true
		return copy(p, r.content), nil
	}
	return 0, r.err
}

func collect(r io.Reader) ([]string, error) {
	var lines []string
	for line, err := range Lines(r) {
		if err != nil {
			return lines, err
		}
		lines = append(lines, string(line))
	}
	return lines, nil
}

var _ = Describe("Lines", func() {
	It("yields every line without its terminator, including an unterminated last line", func() {
		lines, err := collect(strings.NewReader("a\r\nb\n\nc"))
		Expect(err).NotTo(HaveOccurred())
		Expect(lines).To(Equal([]string{"a", "b", "", "c"}))
	})

	It("yields nothing for empty input", func() {
		lines, err := collect(strings.NewReader(""))
		Expect(err).NotTo(HaveOccurred())
		Expect(lines).To(BeEmpty())
	})

	It("reads a line far beyond bufio.Scanner's default and the parsers' old 10 MiB cap", func() {
		const size = 12 << 20
		huge := strings.Repeat("x", size)
		lines, err := collect(strings.NewReader("first\n" + huge + "\nlast\n"))
		Expect(err).NotTo(HaveOccurred())
		Expect(lines).To(HaveLen(3))
		Expect(lines[1]).To(HaveLen(size))
		Expect(lines[2]).To(Equal("last"))
	})

	It("surfaces a read error once after the lines that preceded it", func() {
		boom := errors.New("disk gone")
		lines, err := collect(&failingReader{content: "ok\n", err: boom})
		Expect(err).To(MatchError(boom))
		Expect(lines).To(Equal([]string{"ok"}))
	})

	It("stops when the consumer breaks out of the loop", func() {
		seen := 0
		for _, err := range Lines(strings.NewReader("1\n2\n3\n")) {
			Expect(err).NotTo(HaveOccurred())
			seen++
			break
		}
		Expect(seen).To(Equal(1))
	})
})

var _ = Describe("Reader", func() {
	It("returns io.EOF only once the input is exhausted", func() {
		reader := NewReader(strings.NewReader("one\ntwo"))
		first, err := reader.Next()
		Expect(err).NotTo(HaveOccurred())
		Expect(string(first)).To(Equal("one"))
		second, err := reader.Next()
		Expect(err).NotTo(HaveOccurred())
		Expect(string(second)).To(Equal("two"))
		_, err = reader.Next()
		Expect(err).To(MatchError(io.EOF))
	})
})
