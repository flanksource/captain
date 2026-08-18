// What a serving process publishes about the mailbox it hosts, so other captain
// processes on the host can find it without being told.
//
// The record is keyed by transport because two different processes write it:
// `captain sandbox git-agent serve --role mailbox` serves ssh, and `captain
// serve` serves https. A single key would mean the second to start silently
// erased the first — and `captain serve` starts constantly, for the web UI.
package cli

import (
	"fmt"
	"net"
	"strconv"
	"strings"
)

// mailboxTransport is the channel a recorded mailbox answers on. It is also the
// scheme of the URL an agent relays to, so the two cannot drift.
type mailboxTransport string

const (
	transportSSH   mailboxTransport = "ssh"
	transportHTTPS mailboxTransport = "https"
)

func parseMailboxTransport(value string) (mailboxTransport, error) {
	switch transport := mailboxTransport(strings.ToLower(strings.TrimSpace(value))); transport {
	case transportSSH, transportHTTPS:
		return transport, nil
	case "":
		return "", nil
	default:
		return "", fmt.Errorf("unknown transport %q; captain speaks %s and %s", value, transportSSH, transportHTTPS)
	}
}

// mailboxRecord is one live-or-recently-live mailbox on this host.
type mailboxRecord struct {
	Transport mailboxTransport
	// Root is the served repository root; Listen the bind address as given.
	Root   string
	Listen string
	// Identity is what a client pins: an SSH host-key fingerprint, or a TLS
	// public-key pin. Which one is decided by Transport, and both are compared
	// the same way — against what the endpoint actually presents.
	Identity string
	// Encrypted reports whether a credential can cross this channel. It is
	// recorded even when false: `captain serve` without --tls hosts the handler
	// over plain HTTP, and saying so precisely is the difference between
	// "restart it with --tls" and "no mailbox has ever served here".
	Encrypted bool
}

// Port parses the listen address's port, which is what a reach-back URL needs.
func (r mailboxRecord) Port() (int, error) {
	_, portText, err := net.SplitHostPort(r.Listen)
	if err != nil {
		return 0, fmt.Errorf("recorded mailbox listen address %q is not [host]:port: %w", r.Listen, err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		return 0, fmt.Errorf("recorded mailbox port %q is not a number", portText)
	}
	return port, nil
}

// LoopbackURL is the address this host probes the mailbox on to prove it is
// live and is this host's own.
func (r mailboxRecord) LoopbackURL() (string, error) {
	port, err := r.Port()
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s://%s", r.Transport, net.JoinHostPort("127.0.0.1", strconv.Itoa(port))), nil
}

// mailboxOptionKey is the backend option holding the records, keyed by transport.
const mailboxOptionKey = "mailbox"

// setMailboxRecord writes one transport's record, leaving the other's alone.
func setMailboxRecord(options map[string]any, record mailboxRecord) {
	records, _ := options[mailboxOptionKey].(map[string]any)
	if records == nil {
		records = map[string]any{}
	}
	entry := map[string]any{
		"root":     record.Root,
		"listen":   record.Listen,
		"identity": record.Identity,
	}
	if record.Transport == transportHTTPS {
		entry["tls"] = record.Encrypted
	}
	records[string(record.Transport)] = entry
	options[mailboxOptionKey] = records
}

// clearMailboxRecord drops a record that named the given address.
//
// One address serves one role, so a record left by an earlier mailbox would
// otherwise claim a port that now answers as something else — and over ssh both
// roles present the same host key, so no probe could tell them apart.
func clearMailboxRecord(options map[string]any, transport mailboxTransport, listen string) {
	records, _ := options[mailboxOptionKey].(map[string]any)
	entry, _ := records[string(transport)].(map[string]any)
	if entry == nil {
		return
	}
	if recorded, _ := entry["listen"].(string); recorded != listen {
		return
	}
	delete(records, string(transport))
	if len(records) == 0 {
		delete(options, mailboxOptionKey)
		return
	}
	options[mailboxOptionKey] = records
}

// mailboxRecords reads every recorded mailbox, keyed by transport.
func mailboxRecords(options map[string]any) map[mailboxTransport]mailboxRecord {
	records, _ := options[mailboxOptionKey].(map[string]any)
	out := map[mailboxTransport]mailboxRecord{}
	for _, transport := range []mailboxTransport{transportHTTPS, transportSSH} {
		entry, _ := records[string(transport)].(map[string]any)
		if entry == nil {
			continue
		}
		listen, _ := entry["listen"].(string)
		if strings.TrimSpace(listen) == "" {
			continue
		}
		root, _ := entry["root"].(string)
		identity, _ := entry["identity"].(string)
		// ssh is encrypted by construction; https only once the server actually
		// negotiated TLS, which `captain serve` does only under --tls.
		encrypted := transport == transportSSH
		if transport == transportHTTPS {
			encrypted, _ = entry["tls"].(bool)
		}
		out[transport] = mailboxRecord{
			Transport: transport,
			Root:      root,
			Listen:    strings.TrimSpace(listen),
			Identity:  strings.TrimSpace(identity),
			Encrypted: encrypted,
		}
	}
	return out
}
