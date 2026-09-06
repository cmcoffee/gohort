package orchestrate

import (
	"encoding/json"
	"io"
	"net/http"
	"sync"
	"time"
)

// sseWriter wraps an SSE-frame destination. Each Send assembles
// one full SSE frame and writes it atomically, so a downstream
// per-Write consumer (Run.Append) captures whole frames.
//
// Two destinations, kept separate on purpose:
//
//   - live: the current HTTP response, what the originating client
//     sees in real time. Optional — run-only mode (after the
//     originator disconnected) leaves this nil.
//   - run: the in-memory Run buffer. Optional — exposed agents and
//     the design endpoint use the response-only path.
//
// Send / SendChatEvent write to BOTH (they're real events worth
// replaying). Ping writes ONLY to live (it's a keepalive comment;
// putting it in the buffer would inflate sequence numbers on the
// server without inflating the client's received-event counter,
// which would break the since=N reconnect protocol).
type sseWriter struct {
	live    io.Writer    // optional; nil = no live client
	flusher http.Flusher // optional; nil for non-flushable destinations
	run     *Run         // optional; nil = no buffer
	mu      sync.Mutex
}

func newSSEWriter(w http.ResponseWriter) *sseWriter {
	f, _ := w.(http.Flusher)
	return &sseWriter{live: w, flusher: f}
}

// newTeeSSEWriter writes Send/SendChatEvent frames to BOTH the live
// HTTP response AND the given Run's event buffer. Pings go to live
// only. Used by handleSend so a fresh /api/runs/<id>/stream
// subscriber after a reconnect can replay every real event from any
// sequence number.
func newTeeSSEWriter(w http.ResponseWriter, run *Run) *sseWriter {
	f, _ := w.(http.Flusher)
	return &sseWriter{live: w, flusher: f, run: run}
}

// detachLive drops the live HTTP-response writer. After this returns,
// emit() writes only to the run buffer. Used by the disconnect
// watchdog in handleSend so a client that navigates away can't wedge
// the loop on a now-dead TCP write. Run-buffer subscribers (a fresh
// /api/runs/<id>/stream client) continue receiving events fine.
//
// Idempotent. Takes the same mutex emit() holds, so it serializes
// cleanly with in-flight writes: any current write completes (or
// errors), then live drops to nil before the next write.
func (s *sseWriter) detachLive() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.live = nil
	s.flusher = nil
}

// emit writes one assembled SSE frame to whichever destinations are
// configured. The toBuffer flag distinguishes real events (true)
// from keepalive comments (false) so pings stay out of the replay
// buffer.
func (s *sseWriter) emit(frame []byte, toBuffer bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.live != nil {
		_, _ = s.live.Write(frame)
		if s.flusher != nil {
			s.flusher.Flush()
		}
	}
	if toBuffer && s.run != nil {
		s.run.Append(frame)
	}
}

// Send writes one SSE event in the AgentLoopPanel protocol shape:
// `data: <json>\n\n`. Buffered for replay.
func (s *sseWriter) Send(payload map[string]any) {
	if s == nil {
		return
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return
	}
	frame := make([]byte, 0, len(body)+8)
	frame = append(frame, "data: "...)
	frame = append(frame, body...)
	frame = append(frame, '\n', '\n')
	s.emit(frame, true)
}

// SendChatEvent writes an SSE event in the ChatPanel runtime's format:
// `event: <type>\ndata: <json>\n\n`. ChatPanel's parser dispatches on
// the SSE event-name and ignores any frame without one. Used by the
// design endpoint (which mounts a ChatPanel); AgentLoopPanel uses
// Send() — different parser, different shape, kept distinct so
// callers pick the one matching their UI primitive.
func (s *sseWriter) SendChatEvent(eventType string, payload map[string]any) {
	if s == nil {
		return
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return
	}
	frame := make([]byte, 0, len(body)+len(eventType)+16)
	frame = append(frame, "event: "...)
	frame = append(frame, eventType...)
	frame = append(frame, "\ndata: "...)
	frame = append(frame, body...)
	frame = append(frame, '\n', '\n')
	s.emit(frame, true)
}

// Ping writes an SSE comment line (`: keepalive\n\n`) to the live
// destination only. Comments are silently dropped by EventSource
// clients but keep the TCP connection alive through proxies / CDNs
// that close idle streams.
func (s *sseWriter) Ping() {
	if s == nil {
		return
	}
	s.emit([]byte(": keepalive\n\n"), false)
}

// startKeepalive fires SSE comment pings every 10 seconds until the
// returned stop function is called. Use during long LLM calls so the
// connection stays open through nginx/CDN proxy_read_timeout (60s
// default in nginx, 100s in CloudFlare).
func startKeepalive(sse *sseWriter) func() {
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				sse.Ping()
			}
		}
	}()
	return func() {
		close(stop)
		<-done
	}
}
