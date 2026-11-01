package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/media"
)

// A connector whose host answered "too many requests" is left alone until
// the time it named: a call that cannot wait that long fails at once without
// sending, rather than adding to the refusals.
func TestARateLimitedConnectorIsWaitedOut(t *testing.T) {
	world(t)
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.Header().Set("Retry-After", "30")
		http.Error(w, `{"error":"rate limit reached"}`, http.StatusTooManyRequests)
	}))
	defer srv.Close()
	install(t, "busy", embedSpec("m"))
	c, _ := GetConnector(RootDB, "busy")
	if err := SaveSettings(c, "", map[string]string{"endpoint": srv.URL}, map[string]string{"api_key": "k"}); err != nil {
		t.Fatal(err)
	}
	if _, err := Call(context.Background(), nil, "busy", "embed.text", map[string]any{"texts": []string{"a"}}); err == nil {
		t.Fatal("the refused call reported success")
	}
	if d := media.PausedFor(srv.URL); d < 25*time.Second || d > 30*time.Second {
		t.Errorf("paused for %s, want the 30s Retry-After", d)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err := Call(ctx, nil, "busy", "embed.text", map[string]any{"texts": []string{"b"}})
	if err == nil || !strings.Contains(err.Error(), "rate limited") || atomic.LoadInt32(&calls) != 1 {
		t.Errorf("err %v after %d call(s)", err, calls)
	}
}
