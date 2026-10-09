package core

import (
	"context"
	"testing"
)

// A tool an app hands the loop reads the turn's session off its context,
// and gets nothing outside a run.
func TestAToolReadsTheSessionOffItsContext(t *testing.T) {
	sess := &ToolSession{}
	if got := ToolSessionFromContext(sess.ContextWithSession(context.Background())); got != sess {
		t.Error("the session did not come back off the context")
	}
	if ToolSessionFromContext(context.Background()) != nil || ToolSessionFromContext(nil) != nil {
		t.Error("a context outside a run should carry no session")
	}
	var none *ToolSession
	if ToolSessionFromContext(none.ContextWithSession(context.Background())) != nil {
		t.Error("a nil session should not be put on the context")
	}
}
