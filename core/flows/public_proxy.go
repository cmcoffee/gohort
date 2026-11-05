package flows

// A forward proxy for one download, reaching public addresses only.
//
// The video download runs yt-dlp with the network open, because it has to
// reach the site. Checking the link it was given was the whole of the guard:
// the site's redirects, the playlist's fragment URLs and the name a host
// resolves to were all yt-dlp's to follow, and any of them can lead inside
// (the cloud metadata address, a service on the LAN, a port on this machine).
// A fragment fetched from there and stitched into the file comes back to the
// person who asked. So yt-dlp is pointed at this proxy for every request it
// makes, and the proxy dials only a public address, checked on the address it
// is about to connect to, after resolution, for every hop.
//
// yt-dlp is the trusted half: the links are not, but the program honours the
// proxy it is told to use. The proxy lives for one run, on this machine's
// loopback, and is closed when the run ends.

import (
	"context"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/cmcoffee/gohort/core/sourcehooks"
)

// ProxyDial connects for the download proxy: public addresses only. Exported,
// like URLGuard, so a test in another package can let its own local server
// through; nothing else sets it.
var ProxyDial = func(ctx context.Context, network, addr string) (net.Conn, error) {
	return sourcehooks.PublicDialer().DialContext(ctx, network, addr)
}

type publicProxy struct {
	ln  net.Listener
	srv *http.Server
	tr  *http.Transport
}

// startPublicProxy starts a proxy on loopback; Close stops it.
func startPublicProxy() (*publicProxy, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	p := &publicProxy{ln: ln}
	p.tr = &http.Transport{
		Proxy:                 nil,
		DialContext:           func(ctx context.Context, n, a string) (net.Conn, error) { return ProxyDial(ctx, n, a) },
		ResponseHeaderTimeout: 60 * time.Second,
		MaxIdleConnsPerHost:   4,
	}
	p.srv = &http.Server{Handler: http.HandlerFunc(p.serve), ReadHeaderTimeout: 30 * time.Second}
	go p.srv.Serve(ln)
	return p, nil
}

// URL is what yt-dlp's --proxy takes.
func (p *publicProxy) URL() string { return "http://" + p.ln.Addr().String() }

func (p *publicProxy) Close() {
	p.srv.Close()
	p.tr.CloseIdleConnections()
}

func (p *publicProxy) serve(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodConnect {
		p.tunnel(w, r)
		return
	}
	p.forward(w, r)
}

// tunnel is HTTPS: a byte pipe to the host, once the dial has checked where
// it goes.
func (p *publicProxy) tunnel(w http.ResponseWriter, r *http.Request) {
	dst, err := ProxyDial(r.Context(), "tcp", r.Host)
	if err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		dst.Close()
		http.Error(w, "no tunnel", http.StatusInternalServerError)
		return
	}
	src, buf, err := hj.Hijack()
	if err != nil {
		dst.Close()
		return
	}
	if _, err := src.Write([]byte("HTTP/1.1 200 Connection established\r\n\r\n")); err != nil {
		src.Close()
		dst.Close()
		return
	}
	if n := buf.Reader.Buffered(); n > 0 {
		pending, _ := buf.Reader.Peek(n)
		dst.Write(pending)
	}
	var once sync.Once
	closeBoth := func() { once.Do(func() { src.Close(); dst.Close() }) }
	go func() { io.Copy(dst, src); closeBoth() }()
	io.Copy(src, dst)
	closeBoth()
}

// hopHeaders are the proxy's own, not passed on.
var hopHeaders = []string{"Connection", "Proxy-Connection", "Proxy-Authorization", "Proxy-Authenticate", "Keep-Alive", "Te", "Trailer", "Transfer-Encoding", "Upgrade"}

// forward is plain HTTP: the request re-sent through a transport that dials
// public addresses only. Redirects are yt-dlp's to follow, back through here.
func (p *publicProxy) forward(w http.ResponseWriter, r *http.Request) {
	if r.URL.Scheme != "http" || r.URL.Host == "" {
		http.Error(w, "a proxy request names an absolute http:// URL", http.StatusBadRequest)
		return
	}
	out := r.Clone(r.Context())
	out.RequestURI = ""
	for _, h := range hopHeaders {
		out.Header.Del(h)
	}
	resp, err := p.tr.RoundTrip(out)
	if err != nil {
		code := http.StatusBadGateway
		if strings.Contains(err.Error(), "non-public") {
			code = http.StatusForbidden
		}
		http.Error(w, err.Error(), code)
		return
	}
	defer resp.Body.Close()
	for _, h := range hopHeaders {
		resp.Header.Del(h)
	}
	for k, vs := range resp.Header {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}
