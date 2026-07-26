package database

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// The session-by-directory lookup is a plain equality test, so a directory that
// two writers spell differently is two different directories to the database.
func TestNormalizeCWDGivesOneSpellingPerDirectory(t *testing.T) {
	for _, test := range []struct{ name, value, want string }{
		{name: "an already normal path is untouched", value: "/home/dev/example", want: "/home/dev/example"},
		{name: "a trailing slash is dropped", value: "/home/dev/example/", want: "/home/dev/example"},
		{name: "repeated trailing slashes are dropped", value: "/home/dev/example///", want: "/home/dev/example"},
		{name: "surrounding whitespace is dropped", value: "  /home/dev/example/  ", want: "/home/dev/example"},
		// Stripping root's slash would leave the empty string, which the lookup
		// treats as "no directory" rather than as the root directory.
		{name: "root keeps its slash", value: "/", want: "/"},
		{name: "a path of only slashes collapses to root", value: "///", want: "/"},
		{name: "an empty value stays empty", value: "   ", want: ""},
		{name: "a relative path is normalized too", value: "example/", want: "example"},
	} {
		t.Run(test.name, func(t *testing.T) {
			assert.Equal(t, test.want, normalizeCWD(test.value))
		})
	}
}
