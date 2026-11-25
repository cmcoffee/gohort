package main

// Timing for the window's requests through the local proxy, for "why did that
// page take so long". The server's own log shows only its share. This shows
// the rest, in the app's log (Show Logs):
//
//   - A slow request: how long it took here, when the server's first byte
//     came, and whether the proxy had to open (and handshake) a new
//     connection to the server for it.
//   - The window running out of connections: a browser engine keeps about six
//     requests open to one host over plain HTTP, and the window talks to this
//     proxy that way. Long polls and streams hold theirs for as long as they
//     wait, so when six are open a new request waits inside the window, before
//     the proxy sees it. The proxy cannot time that wait; it can say when the
//     window is full and what is holding it.
//
// A request that waits on purpose (a long poll, a stream, a websocket) is never
// called slow: taking long is its job.

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/http"
	"net/http/httptrace"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/cmcoffee/gohort/gohort-desktop/core"
)

const (
	// slow_request is how long an ordinary request runs before it is logged.
	slow_request = 500 * time.Millisecond
	// window_conn_limit is how many requests a browser engine keeps open to
	// one host over plain HTTP; past it, new ones wait in the window.
	window_conn_limit = 6
	// full_note_gap keeps the window-full note to one per stretch of it.
	full_note_gap = 30 * time.Second
)

// proxy_timing tracks the requests the proxy is carrying. Its zero value is
// ready to use.
type proxy_timing struct {
	mu        sync.Mutex
	in_flight map[*flight]struct{}
	last_full time.Time
	now       func() time.Time
	logf      func(format string, args ...interface{})
}

// flight is one request through the proxy.
type flight struct {
	method string
	path   string
	start  time.Time
	// others is how many requests were already open when this one arrived.
	others int

	mu         sync.Mutex
	hold       bool // waits on purpose: a long poll, a stream, a websocket
	status     int
	reused     bool
	got_conn   bool
	conn_start time.Time
	conn_done  time.Time
	tls_start  time.Time
	tls_done   time.Time
	first_byte time.Time
}

type flight_key struct{}

// flight_from is the request's flight, nil when it has none.
func flight_from(ctx context.Context) *flight {
	f, _ := ctx.Value(flight_key{}).(*flight)
	return f
}

func (pt *proxy_timing) clock() time.Time {
	if pt.now != nil {
		return pt.now()
	}
	return time.Now()
}

func (pt *proxy_timing) log(format string, args ...interface{}) {
	if pt.logf != nil {
		pt.logf(format, args...)
		return
	}
	core.Log(fmt.Sprintf(format, args...))
}

// begin starts timing r and returns the request to forward, which carries the
// flight and a trace of the proxy's connection to the server.
func (pt *proxy_timing) begin(r *http.Request) (*flight, *http.Request) {
	f := &flight{
		method: r.Method,
		path:   r.URL.Path,
		start:  pt.clock(),
		hold:   waits_on_purpose(r),
	}
	pt.mu.Lock()
	if pt.in_flight == nil {
		pt.in_flight = make(map[*flight]struct{})
	}
	f.others = len(pt.in_flight)
	pt.in_flight[f] = struct{}{}
	full := len(pt.in_flight) >= window_conn_limit && f.start.Sub(pt.last_full) >= full_note_gap
	var open string
	if full {
		pt.last_full = f.start
		open = pt.describe_open(f.start)
	}
	n := len(pt.in_flight)
	pt.mu.Unlock()
	if full {
		pt.log("[gohort-desktop] %d requests open in the window, the most it keeps to one host; a new one waits in the window until one ends. Open: %s", n, open)
	}

	ctx := context.WithValue(r.Context(), flight_key{}, f)
	ctx = httptrace.WithClientTrace(ctx, f.trace(pt.clock))
	return f, r.WithContext(ctx)
}

// end stops timing f and logs it when it was slow. It leaves the open list
// last, so a request still counts as open until its line is written.
func (pt *proxy_timing) end(f *flight) {
	defer func() {
		pt.mu.Lock()
		delete(pt.in_flight, f)
		pt.mu.Unlock()
	}()
	took := pt.clock().Sub(f.start)
	f.mu.Lock()
	slow := !f.hold && took >= slow_request
	var line string
	if slow {
		line = f.describe(took)
	}
	f.mu.Unlock()
	if slow {
		pt.log("[gohort-desktop] slow request: %s", line)
	}
}

// describe_open lists the open requests, longest open first. Called with
// pt.mu held.
func (pt *proxy_timing) describe_open(now time.Time) string {
	open := make([]*flight, 0, len(pt.in_flight))
	for f := range pt.in_flight {
		open = append(open, f)
	}
	sort.Slice(open, func(i, j int) bool { return open[i].start.Before(open[j].start) })
	parts := make([]string, 0, len(open))
	for _, f := range open {
		f.mu.Lock()
		s := fmt.Sprintf("%s %s (%s", f.method, f.path, round(now.Sub(f.start)))
		if f.hold {
			s += ", waits on purpose"
		}
		f.mu.Unlock()
		parts = append(parts, s+")")
	}
	return strings.Join(parts, "; ")
}

// saw_response records what the server sent back. A stream is a request that
// waits on purpose even when nothing about the request said so.
func (f *flight) saw_response(resp *http.Response) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.status = resp.StatusCode
	if strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") || resp.StatusCode == http.StatusSwitchingProtocols {
		f.hold = true
	}
}

// failed records that the server could not be reached.
func (f *flight) failed() {
	f.mu.Lock()
	f.status = http.StatusBadGateway
	f.mu.Unlock()
}

// trace times the proxy's connection to the server for this request.
func (f *flight) trace(clock func() time.Time) *httptrace.ClientTrace {
	set := func(at *time.Time) {
		f.mu.Lock()
		if at.IsZero() {
			*at = clock()
		}
		f.mu.Unlock()
	}
	return &httptrace.ClientTrace{
		ConnectStart:      func(string, string) { set(&f.conn_start) },
		ConnectDone:       func(string, string, error) { set(&f.conn_done) },
		TLSHandshakeStart: func() { set(&f.tls_start) },
		TLSHandshakeDone:  func(tls.ConnectionState, error) { set(&f.tls_done) },
		GotConn: func(info httptrace.GotConnInfo) {
			f.mu.Lock()
			f.got_conn = true
			f.reused = info.Reused
			f.mu.Unlock()
		},
		GotFirstResponseByte: func() { set(&f.first_byte) },
	}
}

// describe is the slow-request line. Called with f.mu held.
func (f *flight) describe(took time.Duration) string {
	status := "no answer"
	if f.status != 0 {
		status = fmt.Sprint(f.status)
	}
	s := fmt.Sprintf("%s %s %s took %s", f.method, f.path, status, round(took))
	if !f.first_byte.IsZero() {
		s += fmt.Sprintf("; the server's first byte came at %s", round(f.first_byte.Sub(f.start)))
	}
	switch {
	case !f.got_conn:
		s += "; never got a connection to the server"
	case f.reused:
		s += "; reused a connection"
	case !f.conn_start.IsZero() && !f.conn_done.IsZero():
		s += fmt.Sprintf("; opened a new connection (%s", round(f.conn_done.Sub(f.conn_start)))
		if !f.tls_start.IsZero() && !f.tls_done.IsZero() {
			s += fmt.Sprintf(", TLS %s", round(f.tls_done.Sub(f.tls_start)))
		}
		s += ")"
	}
	return s + fmt.Sprintf("; %d other request(s) open when it arrived", f.others)
}

// waits_on_purpose reports whether a request is one that holds its
// connection by design: a long poll (wait=1), a stream, or a websocket.
func waits_on_purpose(r *http.Request) bool {
	if r.URL.Query().Get("wait") == "1" {
		return true
	}
	if r.Header.Get("Upgrade") != "" {
		return true
	}
	return strings.Contains(r.Header.Get("Accept"), "text/event-stream")
}

func round(d time.Duration) time.Duration {
	if d < time.Second {
		return d.Round(time.Millisecond)
	}
	return d.Round(10 * time.Millisecond)
}
