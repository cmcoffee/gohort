package mcpclient

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// expiringMCP is a server that ends the session out from under the client
// once, the way a restart or an idle timeout does: the first tools/call
// carrying the established session id is refused with 404, and a fresh
// initialize is accepted normally.
type expiringMCP struct {
	mu        sync.Mutex
	session   int  // bumped per initialize, so each session gets its own id
	expired   bool // whether the first session has been killed yet
	calls     int  // tools/call requests that reached the handler
	refusals  int  // tools/call requests refused as an expired session
	inits     int
	sawNewSID bool // the replayed call carried the SECOND session's id
}

func (s *expiringMCP) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req rpcRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		s.mu.Lock()
		defer s.mu.Unlock()

		if req.Method == "initialize" {
			s.inits++
			s.session++
			w.Header().Set("Mcp-Session-Id", sessionName(s.session))
			w.Header().Set("Content-Type", "application/json")
			w.Write(mustJSON(rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: mustJSON(map[string]any{
				"protocolVersion": protocolVersion,
				"serverInfo":      map[string]any{"name": "expiring", "version": "1"},
			})}))
			return
		}

		sid := r.Header.Get("Mcp-Session-Id")
		if req.Method == "tools/call" && !s.expired {
			// Kill the session the client thinks it holds.
			s.expired = true
			s.refusals++
			http.Error(w, "session not found", http.StatusNotFound)
			return
		}
		if req.ID == 0 { // notifications/initialized
			w.WriteHeader(http.StatusAccepted)
			return
		}
		if req.Method == "tools/call" {
			s.calls++
			s.sawNewSID = sid == sessionName(s.session)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(mustJSON(rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: mustJSON(map[string]any{
			"content": []map[string]any{{"type": "text", "text": "ok"}},
		})}))
	}
}

func sessionName(n int) string {
	return "sess-" + string(rune('a'+n))
}

// A server that expires the session mid-conversation is the ordinary case,
// not an outage: nothing above the transport ever re-initialized, so the
// first tool call after an idle gap failed and every call after it failed the
// same way for the life of the process.
func TestCallReopensAnExpiredSession(t *testing.T) {
	stub := &expiringMCP{}
	srv := httptest.NewServer(stub.handler())
	defer srv.Close()

	c := New(NewHTTPTransport(srv.URL, HTTPOptions{}))
	defer c.Close()
	ctx := context.Background()
	if err := c.Initialize(ctx); err != nil {
		t.Fatalf("initialize: %v", err)
	}

	out, err := c.CallTool(ctx, "search", map[string]any{"query": "x"})
	if err != nil {
		t.Fatalf("call after session expiry should have reconnected and retried, got: %v", err)
	}
	if out != "ok" {
		t.Fatalf("result = %q, want ok", out)
	}
	if stub.refusals != 1 {
		t.Errorf("refusals = %d, want 1 (the test server should have expired exactly one session)", stub.refusals)
	}
	if stub.inits != 2 {
		t.Errorf("initialize count = %d, want 2 (the first handshake plus the reconnect)", stub.inits)
	}
	if !stub.sawNewSID {
		t.Error("the replayed call did not carry the NEW session id — the stale one was not dropped")
	}
}

// A 404 from a transport that holds no session is a bad URL. Re-initializing
// there would loop against an endpoint that was never there.
func TestNotFoundWithoutASessionIsNotExpiry(t *testing.T) {
	var inits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req rpcRequest
		json.NewDecoder(r.Body).Decode(&req)
		if req.Method == "initialize" {
			inits++
		}
		http.Error(w, "no such endpoint", http.StatusNotFound)
	}))
	defer srv.Close()

	c := New(NewHTTPTransport(srv.URL, HTTPOptions{}))
	defer c.Close()
	if err := c.Initialize(context.Background()); err == nil {
		t.Fatal("expected initialize against a 404 endpoint to fail")
	}
	if inits != 1 {
		t.Errorf("initialize attempts = %d, want 1 — a 404 with no session must not trigger a reconnect loop", inits)
	}
}
