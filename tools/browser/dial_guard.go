package browser

// The browser's way out.
//
// RefuseNonPublicHost reads a URL as written, which is the right first answer
// and the wrong last one for a browser: Chromium resolves names and dials on
// its own, so a hostname that resolves to 10.0.0.5, a public page that
// redirects to http://169.254.169.254/, a script that fetches an internal
// address, and a DNS answer that changes between the check and the connect
// all went straight past it. This instance often sits inside a network the
// person asking (or a peer) cannot reach, so that was a proxy into the LAN.
//
// So Chromium is launched behind this loopback proxy and has no other route
// out. The proxy checks the address it actually dials, after resolution, on
// every connection: each subresource, redirect and websocket included. The
// browser's implicit "never proxy loopback" rule is switched off, and UDP
// that would skip the proxy (WebRTC) is refused.

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/cmcoffee/gohort/core/sourcehooks"
)

// dialGuard is the proxy: CONNECT for https and websockets, absolute-URI
// requests for plain http.
type dialGuard struct {
	// allowed decides an address after resolution; nil means public only.
	allowed func(net.IP) bool
	tr      *http.Transport
	once    sync.Once
}

func (g *dialGuard) permits(ip net.IP) bool {
	if g.allowed != nil {
		return g.allowed(ip)
	}
	return !sourcehooks.NonPublicIP(ip)
}

// dial connects to address only when the address dialled is one permits allows.
func (g *dialGuard) dial(ctx context.Context, network, address string) (net.Conn, error) {
	d := &net.Dialer{
		Timeout:   15 * time.Second,
		KeepAlive: 30 * time.Second,
		Control: func(_, addr string, _ syscall.RawConn) error {
			host, _, err := net.SplitHostPort(addr)
			if err != nil {
				return err
			}
			if ip := net.ParseIP(host); ip == nil || !g.permits(ip) {
				return fmt.Errorf("refusing to connect to non-public address %s", host)
			}
			return nil
		},
	}
	return d.DialContext(ctx, network, address)
}

func (g *dialGuard) transport() *http.Transport {
	g.once.Do(func() {
		g.tr = &http.Transport{
			Proxy:                 nil,
			DialContext:           g.dial,
			TLSHandshakeTimeout:   15 * time.Second,
			ResponseHeaderTimeout: 60 * time.Second,
			MaxIdleConns:          32,
			IdleConnTimeout:       60 * time.Second,
		}
	})
	return g.tr
}

func (g *dialGuard) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodConnect {
		g.tunnel(w, r)
		return
	}
	if r.URL == nil || !r.URL.IsAbs() || (r.URL.Scheme != "http" && r.URL.Scheme != "https") {
		http.Error(w, "this proxy forwards absolute http(s) requests only", http.StatusBadRequest)
		return
	}
	out := r.Clone(r.Context())
	out.RequestURI = ""
	for _, h := range []string{"Proxy-Connection", "Proxy-Authorization", "Connection", "Keep-Alive", "Te", "Trailer", "Upgrade"} {
		out.Header.Del(h)
	}
	resp, err := g.transport().RoundTrip(out)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	for k, vs := range resp.Header {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}

// tunnel serves CONNECT: dial the target through the check, then splice.
func (g *dialGuard) tunnel(w http.ResponseWriter, r *http.Request) {
	target := r.Host
	if _, _, err := net.SplitHostPort(target); err != nil {
		target = net.JoinHostPort(strings.Trim(target, "[]"), "443")
	}
	up, err := g.dial(r.Context(), "tcp", target)
	if err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		up.Close()
		http.Error(w, "tunnel unsupported", http.StatusInternalServerError)
		return
	}
	down, buf, err := hj.Hijack()
	if err != nil {
		up.Close()
		return
	}
	down.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n"))
	if n := buf.Reader.Buffered(); n > 0 {
		pending, _ := buf.Reader.Peek(n)
		up.Write(pending)
	}
	go func() { io.Copy(up, down); up.Close() }()
	io.Copy(down, up)
	down.Close()
}

// startDialGuard serves g on a loopback port and returns its address.
func startDialGuard(g *dialGuard) (string, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	srv := &http.Server{Handler: g, ReadHeaderTimeout: 30 * time.Second}
	go srv.Serve(ln)
	return ln.Addr().String(), nil
}
