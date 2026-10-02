package mcpserver

// The SSE stream is gated like tools/call: no credential, no held connection.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

func TestSSEStreamNeedsTheSameAuthAsToolsCall(t *testing.T) {
	// Laid out as in production: the auth store IS the root store, and the
	// app gets a bucket of it. A policy set where the admin sets it has to be
	// read here, which an app reading its own bucket never did.
	prevAuth, prevRoot := AuthDB, RootDB
	authDB := &DBase{Store: kvlite.MemStore()}
	AuthSetUser(authDB, "user-a", "pw-a-123", false)
	AuthDB = func() Database { return authDB }
	RootDB = authDB
	t.Cleanup(func() { AuthDB, RootDB = prevAuth, prevRoot })
	token := AuthCreateSession(authDB, "user-a")

	app := &MCPServer{}
	app.DB = authDB.Bucket("mcpserver")

	open := func(withCookie bool) *httptest.ResponseRecorder {
		// The stream runs until the client leaves; leave after a moment so an
		// admitted stream returns and an unguarded one cannot hang the test.
		ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		defer cancel()
		r := httptest.NewRequest(http.MethodGet, "/mcp/", nil).WithContext(ctx)
		r.Header.Set("Accept", "text/event-stream")
		if withCookie {
			r.AddCookie(&http.Cookie{Name: "gohort_session", Value: token})
		}
		w := httptest.NewRecorder()
		app.handle(w, r)
		return w
	}

	if w := open(false); w.Code != http.StatusUnauthorized {
		t.Errorf("an SSE stream with no credential: status %d, want 401", w.Code)
	}
	w := open(true)
	if w.Code != http.StatusOK || w.Header().Get("Content-Type") != "text/event-stream" {
		t.Errorf("an SSE stream with a valid session: status %d, type %q", w.Code, w.Header().Get("Content-Type"))
	}

	// An admin who has not enabled MCP for this user closes the stream too.
	SetFeatureAllowedUsers(AuthDB(), MCPFeatureKey, []string{"someone-else"})
	if w := open(true); w.Code != http.StatusForbidden {
		t.Errorf("an SSE stream for a user MCP is not enabled for: status %d, want 403", w.Code)
	}
}
