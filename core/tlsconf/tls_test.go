package tlsconf

// The dashboard server drops a client that never finishes its headers, and
// sets no whole-request deadline that would cut a stream or an upload.

import (
	"io"
	"net"
	"net/http"
	"testing"
	"time"
)

func TestServerDropsSlowHeaders(t *testing.T) {
	prev := serverReadHeaderTimeout
	t.Cleanup(func() { serverReadHeaderTimeout = prev })
	serverReadHeaderTimeout = 200 * time.Millisecond

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := newServer("", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })

	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	// Half a request line and never the blank line that ends the headers.
	if _, err := io.WriteString(conn, "GET / HTTP/1.1\r\nHost: x\r\n"); err != nil {
		t.Fatal(err)
	}
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	start := time.Now()
	_, err = io.ReadAll(conn)
	if ne, ok := err.(net.Error); ok && ne.Timeout() {
		t.Fatalf("the server held a connection with unfinished headers for %s", time.Since(start).Round(time.Millisecond))
	}
}

func TestServerHasNoWholeRequestDeadline(t *testing.T) {
	srv := newServer("", nil)
	if srv.ReadTimeout != 0 || srv.WriteTimeout != 0 {
		t.Errorf("ReadTimeout=%s WriteTimeout=%s: either one cuts SSE streams, long turns and large uploads", srv.ReadTimeout, srv.WriteTimeout)
	}
	if srv.ReadHeaderTimeout <= 0 || srv.IdleTimeout <= 0 {
		t.Errorf("ReadHeaderTimeout=%s IdleTimeout=%s: both should be set", srv.ReadHeaderTimeout, srv.IdleTimeout)
	}
}
