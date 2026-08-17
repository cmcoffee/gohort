package account

// The connect popup explains itself, whatever happens.
//
// This runs in a 600x760 window with no chrome, opened by a Reconnect
// button. Two of its exits used to be a bare http.Error: a blank white
// box whose only message to somebody who clicked Reconnect is that
// Reconnect does not work. Those are exactly the cases where the
// integration was renamed or its auth mode changed under a card that
// still lists it — a stale link, which is the likeliest way to get here
// wrongly.

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestTheConnectPopupAlwaysRendersAPage(t *testing.T) {
	// The result page itself: styled, self-contained, and it signals the
	// opener ONLY on success — a failure that told the card "connected"
	// would be worse than one that said nothing.
	w := httptest.NewRecorder()
	mcpConnectResultPage(w, "There is no integration called \"atlassian\" any more.")
	body := w.Body.String()
	if !strings.Contains(body, "<!DOCTYPE html>") || !strings.Contains(body, "no integration called") {
		t.Fatalf("a failure should render a page that says what happened:\n%s", body)
	}
	if strings.Contains(body, "gohort-mcp-connected") {
		t.Error("a failure must not tell the opener it connected")
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("the popup should be told it is HTML, got %q", ct)
	}

	// And success does signal, or the card behind it never refreshes and
	// the connection looks like it failed.
	w = httptest.NewRecorder()
	mcpConnectResultPage(w, "Connected. You can close this tab and return to your conversation.")
	if !strings.Contains(w.Body.String(), "postMessage('gohort-mcp-connected'") {
		t.Error("a successful connect should wake the card that opened it")
	}

	// The message is escaped: a server name arrives from a query string.
	w = httptest.NewRecorder()
	mcpConnectResultPage(w, `no integration called "<script>alert(1)</script>"`)
	if strings.Contains(w.Body.String(), "<script>alert(1)") {
		t.Error("the message is not escaped")
	}
}
