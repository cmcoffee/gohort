package core

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

// SSEWriter wraps an http.ResponseWriter for Server-Sent Events streaming.
type SSEWriter struct {
	mu      sync.Mutex
	w       http.ResponseWriter
	flusher http.Flusher
}

// NewSSEWriter sets the standard SSE headers and returns a writer.
// Returns an error if the ResponseWriter does not support flushing.
func NewSSEWriter(w http.ResponseWriter) (*SSEWriter, error) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		return nil, fmt.Errorf("streaming not supported")
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("X-Accel-Buffering", "no") // disable nginx proxy buffering
	return &SSEWriter{w: w, flusher: flusher}, nil
}

// SendComment writes an SSE comment (prefixed with ":") to keep the connection alive.
// Returns an error if the client has disconnected.
func (s *SSEWriter) SendComment(text string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := fmt.Fprintf(s.w, ": %s\n\n", text)
	if err != nil {
		return err
	}
	s.flusher.Flush()
	return nil
}

// StartKeepalive ticks every interval and writes a comment-line to
// the SSE stream so reverse proxies / browser EventSource don't drop
// the connection during long quiet periods (LLM thinking before
// first content chunk, slow tool calls, etc.). Returns a stop
// function the caller defers — call it BEFORE the final event for
// cleanest output, though it's safe to call after as well.
//
// Standard usage at the top of an SSE handler:
//
//	sse, err := NewSSEWriter(w)
//	if err != nil { ... }
//	defer sse.StartKeepalive(10 * time.Second)()
//
// 10s interval is the recommended default — well under nginx's 60s
// proxy_read_timeout default, gentle enough that the comment overhead
// is invisible.
func (s *SSEWriter) StartKeepalive(interval time.Duration) func() {
	if interval <= 0 {
		interval = 10 * time.Second
	}
	stopped := new(atomic.Bool)
	done := make(chan struct{})
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				if stopped.Load() {
					return
				}
				_ = s.SendComment("ka")
			}
		}
	}()
	return func() {
		if stopped.CompareAndSwap(false, true) {
			close(done)
		}
	}
}

// Send marshals v to JSON and writes it as an SSE data event.
func (s *SSEWriter) Send(v interface{}) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(s.w, "data: %s\n\n", data)
	if err != nil {
		return err
	}
	s.flusher.Flush()
	return nil
}

// SendNamed writes an SSE event with an explicit event type so the
// browser-side EventSource (or a manual SSE parser) can route it via
// addEventListener / a switch on the event name. Required by core/ui's
// ChatPanel runtime, which dispatches on event name (chunk, done,
// session, status, error, tool_call, tool_result).
func (s *SSEWriter) SendNamed(event string, v interface{}) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(s.w, "event: %s\ndata: %s\n\n", event, data)
	if err != nil {
		return err
	}
	s.flusher.Flush()
	return nil
}
