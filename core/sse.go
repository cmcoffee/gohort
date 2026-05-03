package core

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
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
