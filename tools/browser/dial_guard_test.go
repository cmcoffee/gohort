package browser

import (
	"bufio"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// The browser's proxy refuses an address that is not public, for plain http
// and for a CONNECT tunnel alike, and forwards one it allows.
func TestTheBrowserCannotReachThePrivateNetwork(t *testing.T) {
	inside := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "internal secret") }))
	defer inside.Close()

	addr, err := startDialGuard(&dialGuard{})
	if err != nil {
		t.Fatal(err)
	}
	proxied := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(&url.URL{Scheme: "http", Host: addr})}}
	resp, err := proxied.Get(inside.URL)
	if err == nil {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if strings.Contains(string(body), "internal secret") {
			t.Fatal("plain http reached a loopback server through the guard")
		}
	}

	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	host := strings.TrimPrefix(inside.URL, "http://")
	io.WriteString(conn, "CONNECT "+host+" HTTP/1.1\r\nHost: "+host+"\r\n\r\n")
	line, _ := bufio.NewReader(conn).ReadString('\n')
	if strings.Contains(line, " 200 ") {
		t.Fatalf("a tunnel to loopback was opened: %q", line)
	}

	// The same proxy told loopback is fine forwards: the refusal above is
	// the address check, not a broken proxy.
	open, _ := startDialGuard(&dialGuard{allowed: func(net.IP) bool { return true }})
	proxied = &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(&url.URL{Scheme: "http", Host: open})}}
	resp, err = proxied.Get(inside.URL)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != "internal secret" {
		t.Errorf("an allowed address was not forwarded: %q", body)
	}
}
