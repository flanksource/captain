package bash

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestDetectInterpreter(t *testing.T) {
	tests := []struct {
		name     string
		command  string
		expected string
	}{
		{"python3 -c", `python3 -c "print('hi')"`, "Python"},
		{"python3 heredoc", "python3 << 'PYEOF'\nprint('hi')\nPYEOF", "Python"},
		{"python2", `python2 -c "print 'hi'"`, "Python"},
		{"python3.11", `python3.11 -c "print('hi')"`, "Python"},
		{"python3.12 heredoc", "python3.12 << 'EOF'\nimport sys\nEOF", "Python"},
		{"node -e", `node -e "console.log('hi')"`, "Node"},
		{"ruby -e", `ruby -e "puts 'hi'"`, "Ruby"},
		{"perl -e", `perl -e "print 'hi'"`, "Perl"},
		{"php -r", `php -r "echo 'hi';"`, "PHP"},
		{"bash -c", `bash -c "echo hello"`, ""},
		{"sh -c", `sh -c "echo hello"`, ""},
		{"go build", "go build ./...", ""},
		{"ls -la", "ls -la", ""},
		{"empty", "", ""},
		{"full path python3", `/usr/bin/python3 -c "print('hi')"`, "Python"},
		{"full path node", `/usr/local/bin/node -e "console.log(1)"`, "Node"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, DetectInterpreter(tt.command))
		})
	}
}

func TestExtractInterpreterBody(t *testing.T) {
	tests := []struct {
		name     string
		command  string
		expected string
	}{
		{"heredoc", "python3 << 'PYEOF'\nprint('hello')\nprint('world')\nPYEOF", "print('hello')\nprint('world')"},
		{"inline -c", `python3 -c "print('hi')"`, "print('hi')"},
		{"inline -e", `node -e "console.log(1)"`, "console.log(1)"},
		{"no flag", `python3 script.py`, `python3 script.py`},
		{"single line heredoc", "python3 << 'EOF'\nprint(1)\nEOF", "print(1)"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, ExtractInterpreterBody(tt.command))
		})
	}
}

func TestInterpreterLanguage(t *testing.T) {
	tests := []struct {
		interpreter string
		expected    string
	}{
		{"Python", "python"},
		{"Node", "javascript"},
		{"Ruby", "ruby"},
		{"Perl", "perl"},
		{"PHP", "php"},
		{"", "bash"},
		{"Unknown", "bash"},
	}

	for _, tt := range tests {
		t.Run(tt.interpreter, func(t *testing.T) {
			assert.Equal(t, tt.expected, InterpreterLanguage(tt.interpreter))
		})
	}
}
