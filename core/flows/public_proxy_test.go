package flows

import (
	"context"
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

// Through the proxy, an address inside the network is refused, for HTTPS
// (CONNECT) and plain HTTP alike: the server is real and answers, and the
// proxy still will not dial it.
func TestTheDownloadProxyRefusesInside(t *testing.T) {
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("inside")) }))
	defer plain.Close()
	secure := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("inside")) }))
	defer secure.Close()
	p, err := startPublicProxy()
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	via, _ := url.Parse(p.URL())
	c := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(via), TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}
	for _, target := range []string{plain.URL, secure.URL} {
		resp, err := c.Get(target)
		if err == nil {
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if string(body) == "inside" {
				t.Errorf("the proxy reached %s", target)
			}
		}
	}
}

// And it carries what it may reach, both ways, so a download that goes
// through it works.
func TestTheDownloadProxyCarriesWhatItMayReach(t *testing.T) {
	prev := ProxyDial
	ProxyDial = func(ctx context.Context, network, addr string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, addr)
	}
	t.Cleanup(func() { ProxyDial = prev })
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("plain ok")) }))
	defer plain.Close()
	secure := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("secure ok")) }))
	defer secure.Close()
	p, err := startPublicProxy()
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	via, _ := url.Parse(p.URL())
	c := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(via), TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}
	for target, want := range map[string]string{plain.URL: "plain ok", secure.URL: "secure ok"} {
		resp, err := c.Get(target)
		if err != nil {
			t.Fatalf("%s through the proxy: %v", target, err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if string(body) != want {
			t.Errorf("%s through the proxy answered %q", target, body)
		}
	}
}
