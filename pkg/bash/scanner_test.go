package bash

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestScanner_Scan_SafeCommands(t *testing.T) {
	scanner := NewScanner("/Users/test/project", nil)

	tests := []struct {
		name    string
		command string
		allowed bool
	}{
		{
			name:    "ls is safe",
			command: "ls -la",
			allowed: true,
		},
		{
			name:    "grep pipe is safe",
			command: "cat file.txt | grep foo",
			allowed: true,
		},
		{
			name:    "awk pipe is safe",
			command: "cat file.txt | awk '{print $1}'",
			allowed: true,
		},
		{
			name:    "write to CWD is safe",
			command: "echo hello > output.txt",
			allowed: true,
		},
		{
			name:    "write to /tmp is safe",
			command: "echo hello > /tmp/output.txt",
			allowed: true,
		},
		{
			name:    "redirect to /dev/null is safe",
			command: "command 2>/dev/null",
			allowed: true,
		},
		{
			name:    "go build is safe (dev tool)",
			command: "go build ./...",
			allowed: true,
		},
		{
			name:    "make is safe (dev tool)",
			command: "make test",
			allowed: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := scanner.Scan(tt.command)
			assert.Equal(t, tt.allowed, result.Allowed, "command: %s, violations: %v", tt.command, result.Violations)
		})
	}
}

func TestScanner_Scan_BlockedCommands(t *testing.T) {
	scanner := NewScanner("/Users/test/project", nil)

	tests := []struct {
		name    string
		command string
	}{
		{
			name:    "rm -rf on system path",
			command: "rm -rf /etc/config",
		},
		{
			name:    "write to /etc",
			command: "echo test > /etc/hosts",
		},
		{
			name:    "chmod on system file",
			command: "chmod 777 /usr/bin/sudo",
		},
		{
			name:    "curl (network operation)",
			command: "curl https://example.com",
		},
		{
			name:    "npm install (package install)",
			command: "npm install -g package",
		},
		{
			name:    "pip install (package install)",
			command: "pip install malicious-package",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := scanner.Scan(tt.command)
			assert.False(t, result.Allowed, "expected blocked for: %s", tt.command)
			assert.NotEmpty(t, result.Violations, "expected violations for: %s", tt.command)
		})
	}
}

func TestScanner_Scan_PipeInheritance(t *testing.T) {
	scanner := NewScanner("/Users/test/project", nil)

	tests := []struct {
		name    string
		command string
		allowed bool
	}{
		{
			name:    "safe pipe with dangerous redirect",
			command: "cat file.txt | grep foo > /etc/hosts",
			allowed: false,
		},
		{
			name:    "safe pipe with safe redirect",
			command: "cat file.txt | grep foo > output.txt",
			allowed: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := scanner.Scan(tt.command)
			assert.Equal(t, tt.allowed, result.Allowed)
		})
	}
}

func TestScanner_Scan_ParseError(t *testing.T) {
	scanner := NewScanner("/Users/test/project", nil)
	result := scanner.Scan("if [ -f file.txt")

	assert.False(t, result.Allowed)
	assert.NotEmpty(t, result.ParseError)
}

func TestScanner_Scan_WithWhitelist(t *testing.T) {
	config := &Config{
		WhitelistedCommands: []string{
			"curl https://trusted.example.com",
			"npm install",
		},
	}

	scanner := NewScanner("/Users/test/project", config)

	tests := []struct {
		name    string
		command string
		allowed bool
	}{
		{
			name:    "whitelisted curl is safe",
			command: "curl https://trusted.example.com",
			allowed: true,
		},
		{
			name:    "whitelisted npm install is safe",
			command: "npm install",
			allowed: true,
		},
		{
			name:    "non-whitelisted curl is blocked",
			command: "curl https://evil.example.com",
			allowed: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := scanner.Scan(tt.command)
			assert.Equal(t, tt.allowed, result.Allowed)
		})
	}
}

func TestScanner_Scan_DeleteOperations(t *testing.T) {
	scanner := NewScanner("/Users/test/project", nil)

	tests := []struct {
		name    string
		command string
		allowed bool
	}{
		{
			name:    "rm -rf on CWD file is safe",
			command: "rm -rf ./build",
			allowed: true,
		},
		{
			name:    "rm -rf on system path is blocked",
			command: "rm -rf /var/log",
			allowed: false,
		},
		{
			name:    "rm -f on system file is blocked",
			command: "rm -f /etc/passwd",
			allowed: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := scanner.Scan(tt.command)
			assert.Equal(t, tt.allowed, result.Allowed)
		})
	}
}

func TestScanner_Scan_FindExecCommands(t *testing.T) {
	scanner := NewScanner("/Users/test/project", nil)

	tests := []struct {
		name    string
		command string
		allowed bool
	}{
		{
			name:    "find without -exec is safe",
			command: `find . -name "*.txt"`,
			allowed: true,
		},
		{
			name:    "find -exec with safe command (grep)",
			command: `find . -name "*.go" -exec grep "TODO" {} \;`,
			allowed: true,
		},
		{
			name:    "find -exec with rm -rf from system path is blocked",
			command: `find / -name "*.log" -exec rm -rf {} \;`,
			allowed: false,
		},
		{
			name:    "find -exec with rm -rf from /etc is blocked",
			command: `find /etc -type f -exec rm -rf {} \;`,
			allowed: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := scanner.Scan(tt.command)
			assert.Equal(t, tt.allowed, result.Allowed, "command: %s, violations: %v", tt.command, result.Violations)
		})
	}
}
