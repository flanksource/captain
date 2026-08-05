package gitagent

import "testing"

// git invokes GIT_SSH_COMMAND with options of its own — `-o SendEnv=…` in
// particular. Mistaking one for the hostname makes every dispatch and relay
// push fail with a DNS lookup of the option itself.
func TestParseSSHArgs(t *testing.T) {
	cases := []struct {
		name     string
		args     []string
		host     string
		port     string
		command  string
		wantFail bool
	}{
		{
			name: "git's own invocation",
			args: []string{"-o", "SendEnv=GIT_PROTOCOL", "-p", "7502", "captain@127.0.0.1", "git-receive-pack", "'/repo.git'"},
			host: "captain@127.0.0.1", port: "7502",
			command: "git-receive-pack '/repo.git'",
		},
		{
			name: "no options",
			args: []string{"host", "git-receive-pack", "'r.git'"},
			host: "host", port: "22", command: "git-receive-pack 'r.git'",
		},
		{
			name: "joined option values",
			args: []string{"-p2222", "-oBatchMode=yes", "host", "cmd"},
			host: "host", port: "2222", command: "cmd",
		},
		{
			name: "standalone switches and a separator",
			args: []string{"-4", "-q", "--", "host", "cmd"},
			host: "host", port: "22", command: "cmd",
		},
		{
			name: "unmodelled option does not become the host",
			args: []string{"-i", "/tmp/key", "-T", "host", "cmd"},
			host: "host", port: "22", command: "cmd",
		},
		{name: "no command", args: []string{"host"}, wantFail: true},
		{name: "nothing", args: nil, wantFail: true},
		{name: "dangling option value", args: []string{"-o"}, wantFail: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			host, port, command, err := parseSSHArgs(tc.args)
			if tc.wantFail {
				if err == nil {
					t.Fatalf("want an error, got host=%q port=%q command=%q", host, port, command)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if host != tc.host || port != tc.port || command != tc.command {
				t.Fatalf("host=%q port=%q command=%q, want %q %q %q",
					host, port, command, tc.host, tc.port, tc.command)
			}
		})
	}
}
