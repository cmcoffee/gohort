package core

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/cmcoffee/gohort/core/internal/mcpclient"
)

// A failed CALL and a failed CONNECTION need opposite handling: one is
// answered and reported to the model, the other has to retire the link so the
// next call re-dials. Getting this backwards either drops a healthy
// connection over a bad argument, or leaves a dead one in place answering
// every later call with the same error — which is what "the tools stopped
// working until a restart" looked like.
func TestConnectionLostClassification(t *testing.T) {
	cases := []struct {
		name string
		err  error
		lost bool
	}{
		{"no error", nil, false},
		{"tool reported failure", &mcpclient.ToolError{Text: "missing argument"}, false},
		{"wrapped tool failure", fmt.Errorf("calling remote: %w", &mcpclient.ToolError{Text: "boom"}), false},
		{"json-rpc error", &mcpclient.RPCError{Code: -32601, Message: "no method"}, false},
		{"caller's deadline", context.DeadlineExceeded, false},
		{"caller cancelled", context.Canceled, false},
		{"transport failure", errors.New("dial tcp: connection refused"), true},
		{"http status", fmt.Errorf("mcp tools/call: http 502: bad gateway"), true},
		{"session gone and unrecoverable", fmt.Errorf("%w; reconnect failed: refused", mcpclient.ErrSessionExpired), true},
	}
	for _, c := range cases {
		if got := mcpConnectionLost(c.err); got != c.lost {
			t.Errorf("%s: mcpConnectionLost = %v, want %v", c.name, got, c.lost)
		}
	}
}
