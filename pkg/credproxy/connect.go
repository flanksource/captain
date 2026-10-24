package credproxy

import (
	"bufio"
	"crypto/tls"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// serveConnect terminates a CONNECT tunnel to a granted HTTPS destination with
// a certificate the proxy's Authority mints for it, then runs every request in
// the tunnel through the same grant pipeline as a plain proxy request. Without
// an Authority, or for a destination no https grant names, CONNECT is refused:
// a tunnel the proxy cannot read is a tunnel it cannot police.
func (p *Proxy) serveConnect(w http.ResponseWriter, r *http.Request) {
	if p.Authority == nil {
		p.audit(Decision{Method: r.Method, Destination: r.Host, Verdict: "rejected", Reason: "CONNECT is not served without a proxy authority; requests must be inspectable"})
		http.Error(w, "captain-proxy: CONNECT is not served", http.StatusForbidden)
		return
	}
	host, port, err := net.SplitHostPort(r.Host)
	if err != nil {
		p.audit(Decision{Method: r.Method, Destination: r.Host, Verdict: "rejected", Reason: "CONNECT authority is not host:port"})
		http.Error(w, "captain-proxy: CONNECT authority must be host:port", http.StatusBadRequest)
		return
	}
	if len(p.grantsFor(host, port, "https")) == 0 {
		p.audit(Decision{Method: r.Method, Destination: r.Host, Verdict: "rejected", Reason: "destination not granted (deny by default)"})
		http.Error(w, "captain-proxy: destination not granted", http.StatusForbidden)
		return
	}
	leaf, err := p.Authority.leaf(host)
	if err != nil {
		p.audit(Decision{Method: r.Method, Destination: r.Host, Verdict: "error", Reason: err.Error()})
		http.Error(w, "captain-proxy: cannot terminate the tunnel", http.StatusBadGateway)
		return
	}
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "captain-proxy: connection cannot be tunnelled", http.StatusInternalServerError)
		return
	}
	conn, buffered, err := hijacker.Hijack()
	if err != nil {
		return
	}
	if _, err := conn.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n")); err != nil {
		_ = conn.Close()
		return
	}
	tunnel := tls.Server(bufferedConn{Conn: conn, reader: buffered.Reader}, &tls.Config{
		Certificates: []tls.Certificate{*leaf},
		NextProtos:   []string{"http/1.1"},
		MinVersion:   tls.VersionTLS12,
	})
	authority := r.Host
	server := &http.Server{
		Handler:           http.HandlerFunc(func(w http.ResponseWriter, inner *http.Request) { p.serveTunnelled(w, inner, authority) }),
		ReadHeaderTimeout: 30 * time.Second,
	}
	_ = server.Serve(newOneConnListener(tunnel))
}

// serveTunnelled restores the destination the tunnel was opened for and hands
// the request to the grant pipeline. The request's own Host must name the same
// destination: honouring a different one would let a granted tunnel front for
// an ungranted host.
func (p *Proxy) serveTunnelled(w http.ResponseWriter, r *http.Request, authority string) {
	if withDefaultPort(r.Host, "443") != withDefaultPort(authority, "443") {
		p.audit(Decision{Method: r.Method, Destination: authority, Verdict: "rejected", Reason: "tunnelled Host " + r.Host + " differs from the CONNECT destination"})
		http.Error(w, "captain-proxy: Host differs from the tunnel's destination", http.StatusForbidden)
		return
	}
	r.URL.Scheme = "https"
	r.URL.Host = authority
	p.ServeHTTP(w, r)
}

func withDefaultPort(hostport, port string) string {
	if _, _, err := net.SplitHostPort(hostport); err == nil {
		return strings.ToLower(hostport)
	}
	return strings.ToLower(net.JoinHostPort(hostport, port))
}

// bufferedConn reads first whatever the CONNECT request's reader had already
// buffered past the request line — the start of the client's TLS handshake.
type bufferedConn struct {
	net.Conn
	reader *bufio.Reader
}

func (c bufferedConn) Read(b []byte) (int, error) { return c.reader.Read(b) }

// oneConnListener serves exactly one connection to an http.Server, then
// reports itself closed once that connection is, ending Serve.
type oneConnListener struct {
	conn net.Conn
	once sync.Once
	done chan struct{}
}

func newOneConnListener(conn net.Conn) *oneConnListener {
	return &oneConnListener{conn: conn, done: make(chan struct{})}
}

func (l *oneConnListener) Accept() (net.Conn, error) {
	var conn net.Conn
	l.once.Do(func() { conn = &notifyCloseConn{Conn: l.conn, done: l.done} })
	if conn != nil {
		return conn, nil
	}
	<-l.done
	return nil, net.ErrClosed
}

func (l *oneConnListener) Close() error   { return nil }
func (l *oneConnListener) Addr() net.Addr { return l.conn.LocalAddr() }

type notifyCloseConn struct {
	net.Conn
	done chan struct{}
	once sync.Once
}

func (c *notifyCloseConn) Close() error {
	c.once.Do(func() { close(c.done) })
	return c.Conn.Close()
}
