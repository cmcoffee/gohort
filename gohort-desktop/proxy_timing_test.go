package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// timing_harness is the proxy's timing wired the way proxy.go wires it, in
// front of a stand-in server, with the log captured.
type timing_harness struct {
	pt    *proxy_timing
	proxy *httptest.Server
	mu    sync.Mutex
	lines []string
}

func new_timing_harness(t *testing.T, upstream http.Handler) *timing_harness {
	t.Helper()
	server := httptest.NewServer(upstream)
	t.Cleanup(server.Close)
	h := &timing_harness{}
	h.pt = &proxy_timing{logf: func(format string, args ...interface{}) {
		h.mu.Lock()
		h.lines = append(h.lines, fmt.Sprintf(format, args...))
		h.mu.Unlock()
	}}
	target, _ := url.Parse(server.URL)
	rp := httputil.NewSingleHostReverseProxy(target)
	rp.FlushInterval = -1
	rp.ModifyResponse = func(resp *http.Response) error {
		if f := flight_from(resp.Request.Context()); f != nil {
			f.saw_response(resp)
		}
		return nil
	}
	rp.ErrorHandler = func(rw http.ResponseWriter, req *http.Request, err error) {
		if f := flight_from(req.Context()); f != nil {
			f.failed()
		}
		rw.WriteHeader(http.StatusBadGateway)
	}
	h.proxy = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f, r := h.pt.begin(r)
		defer h.pt.end(f)
		rp.ServeHTTP(w, r)
	}))
	t.Cleanup(h.proxy.Close)
	return h
}

func (h *timing_harness) get(t *testing.T, path string) {
	t.Helper()
	resp, err := http.Get(h.proxy.URL + path)
	if err != nil {
		t.Error(err)
		return
	}
	resp.Body.Close()
}

// log is what the proxy logged once it has finished every request. The
// client has its answer a moment before the proxy's handler returns, and the
// line is written on the way out.
func (h *timing_harness) log() []string {
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		h.pt.mu.Lock()
		n := len(h.pt.in_flight)
		h.pt.mu.Unlock()
		if n == 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.lines...)
}

// A slow request says how long it took, when the server answered and how
// the proxy reached the server; a quick one says nothing, so the log is not
// flooded by the page's polling.
func TestASlowRequestSaysWhereTheTimeWent(t *testing.T) {
	h := new_timing_harness(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/slow" {
			time.Sleep(slow_request + 100*time.Millisecond)
		}
		w.Write([]byte("ok"))
	}))
	h.get(t, "/fast")
	if got := h.log(); len(got) != 0 {
		t.Fatalf("a quick request was logged: %q", got)
	}
	h.get(t, "/slow")
	got := h.log()
	if len(got) != 1 {
		t.Fatalf("want one line for the slow request, got %q", got)
	}
	line := got[0]
	t.Log(line)
	for _, want := range []string{
		"slow request: GET /slow 200 took ",
		"the server's first byte came at ",
		"0 other request(s) open when it arrived",
	} {
		if !strings.Contains(line, want) {
			t.Errorf("missing %q in %q", want, line)
		}
	}
	// The fast request left its connection idle, so the slow one reuses it.
	if !strings.Contains(line, "reused a connection") && !strings.Contains(line, "opened a new connection") {
		t.Errorf("the line must say how the proxy reached the server: %q", line)
	}
}

// A long poll and a stream take long on purpose and are never called slow.
func TestARequestThatWaitsOnPurposeIsNotSlow(t *testing.T) {
	h := new_timing_harness(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/stream" {
			w.Header().Set("Content-Type", "text/event-stream")
		}
		time.Sleep(slow_request + 100*time.Millisecond)
		w.Write([]byte("ok"))
	}))
	h.get(t, "/api/changes?wait=1")
	h.get(t, "/stream")
	if got := h.log(); len(got) != 0 {
		t.Fatalf("a request that waits on purpose was called slow: %q", got)
	}
}

// When the window has as many requests open as it keeps to one host, the log
// says so once and names what is holding them, longest open first.
func TestAFullWindowSaysWhatHoldsIt(t *testing.T) {
	release := make(chan struct{})
	h := new_timing_harness(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
	}))
	var wg sync.WaitGroup
	for i := 0; i < window_conn_limit+1; i++ {
		path := fmt.Sprintf("/hold%d?wait=1", i)
		wg.Add(1)
		go func() { defer wg.Done(); h.get(t, path) }()
		// Each one arrives in turn, so the open list has an order to keep.
		deadline := time.Now().Add(2 * time.Second)
		for {
			h.pt.mu.Lock()
			n := len(h.pt.in_flight)
			h.pt.mu.Unlock()
			if n == i+1 || time.Now().After(deadline) {
				break
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	close(release)
	wg.Wait()
	got := h.log()
	if len(got) != 1 {
		t.Fatalf("want the full window noted once, got %q", got)
	}
	line := got[0]
	if !strings.Contains(line, fmt.Sprintf("%d requests open in the window", window_conn_limit)) {
		t.Errorf("the note must say how many are open: %q", line)
	}
	if !strings.Contains(line, "GET /hold0 (") || !strings.Contains(line, "waits on purpose") {
		t.Errorf("the note must name what holds the window: %q", line)
	}
	if strings.Index(line, "/hold0 ") > strings.Index(line, "/hold1 ") {
		t.Errorf("the longest open comes first: %q", line)
	}
}

// A server that cannot be reached says so instead of timing an answer.
func TestAnUnreachableServerIsNamed(t *testing.T) {
	f := &flight{method: "GET", path: "/x"}
	f.failed()
	line := f.describe(2 * time.Second)
	for _, want := range []string{"GET /x 502 took 2s", "never got a connection to the server"} {
		if !strings.Contains(line, want) {
			t.Errorf("missing %q in %q", want, line)
		}
	}
}

// The real proxy carries the timing: every forwarded request is begun and
// ended, the response is recorded, and so is a failure to reach the server.
func TestTheProxyCarriesTheTiming(t *testing.T) {
	src, err := os.ReadFile("proxy.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"f, r := gp.timing.begin(r)\n\tdefer gp.timing.end(f)\n\tproxy.ServeHTTP(w, r)",
		"if f := flight_from(resp.Request.Context()); f != nil {\n\t\t\tf.saw_response(resp)",
		"if f := flight_from(req.Context()); f != nil {\n\t\t\tf.failed()",
	} {
		if !strings.Contains(string(src), want) {
			t.Errorf("proxy.go no longer has %q", want)
		}
	}
}
