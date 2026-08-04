// A minimal GIT_SSH_COMMAND implementation. Dispatch and relay pushes run
// git with GIT_SSH_COMMAND pointing at `captain sandbox git-agent ssh`, so no
// system ssh client is needed, the key never leaves captain-managed paths,
// and the server's host key is verified against a pinned fingerprint — the
// endpoint name is never trusted as identity.
package gitagent

import (
	"fmt"
	"io"
	"net"
	"os"
	"strings"

	gossh "golang.org/x/crypto/ssh"
)

// Environment variables the client reads; flags would be mangled by git's
// shell-splitting of GIT_SSH_COMMAND.
const (
	EnvSSHKey             = "CAPTAIN_SSH_KEY"
	EnvSSHHostFingerprint = "CAPTAIN_SSH_HOST_FINGERPRINT"
	EnvSSHUser            = "CAPTAIN_SSH_USER"
)

// SSHClientMain speaks git's ssh-command contract: argv is
// `[-4|-6] [-p port] [--] [user@]host command...`. It returns the process
// exit code, propagating the remote command's.
func SSHClientMain(args []string) int {
	code, err := runSSHClient(args, os.Stdin, os.Stdout, os.Stderr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "captain-ssh: %v\n", err)
		if code == 0 {
			code = 255
		}
	}
	return code
}

func runSSHClient(args []string, stdin io.Reader, stdout, stderr io.Writer) (int, error) {
	host, port, command, err := parseSSHArgs(args)
	if err != nil {
		return 255, err
	}
	keyPath := os.Getenv(EnvSSHKey)
	pinned := os.Getenv(EnvSSHHostFingerprint)
	if keyPath == "" || pinned == "" {
		return 255, fmt.Errorf("%s and %s must be set", EnvSSHKey, EnvSSHHostFingerprint)
	}
	user := os.Getenv(EnvSSHUser)
	if user == "" {
		user = "captain"
	}
	if at := strings.LastIndex(host, "@"); at >= 0 {
		user, host = host[:at], host[at+1:]
	}
	signer, _, err := EnsureKeyPair(keyPath)
	if err != nil {
		return 255, err
	}
	config := &gossh.ClientConfig{
		User: user,
		Auth: []gossh.AuthMethod{gossh.PublicKeys(signer)},
		HostKeyCallback: func(_ string, _ net.Addr, key gossh.PublicKey) error {
			if got := gossh.FingerprintSHA256(key); got != pinned {
				return fmt.Errorf("host key %s does not match the pinned %s", got, pinned)
			}
			return nil
		},
	}
	client, err := gossh.Dial("tcp", net.JoinHostPort(host, port), config)
	if err != nil {
		return 255, err
	}
	defer client.Close()
	session, err := client.NewSession()
	if err != nil {
		return 255, err
	}
	defer session.Close()
	session.Stdin = stdin
	session.Stdout = stdout
	session.Stderr = stderr
	if err := session.Run(command); err != nil {
		if exitErr, ok := err.(*gossh.ExitError); ok {
			return exitErr.ExitStatus(), nil
		}
		return 255, err
	}
	return 0, nil
}

func parseSSHArgs(args []string) (host, port, command string, err error) {
	port = "22"
	i := 0
loop:
	for i < len(args) {
		switch args[i] {
		case "-p":
			if i+1 >= len(args) {
				return "", "", "", fmt.Errorf("-p needs a port argument")
			}
			port = args[i+1]
			i += 2
		case "-4", "-6":
			i++
		case "--":
			i++
			break loop
		default:
			break loop
		}
	}
	if i >= len(args) {
		return "", "", "", fmt.Errorf("usage: [-p port] host command...")
	}
	host = args[i]
	command = strings.Join(args[i+1:], " ")
	if strings.TrimSpace(command) == "" {
		return "", "", "", fmt.Errorf("no remote command given (interactive sessions are not supported)")
	}
	return host, port, command, nil
}
