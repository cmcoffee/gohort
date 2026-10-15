package appscript

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/notices"
	"github.com/cmcoffee/snugforge/kvlite"
)

// An action's script tells the person it ran for something: the notice lands
// in their notifications, the same title again folds into it, and only the
// first is forwarded. A data source may not notify.
func TestAnActionNotifiesThePersonItRanFor(t *testing.T) {
	cache, err := os.UserCacheDir()
	if err != nil {
		t.Skip("no cache directory for folders outside /tmp")
	}
	root, err := os.MkdirTemp(cache, "gohort-notify-test-")
	if err != nil {
		t.Skip(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	prevWS := WorkspacesDir()
	SetWorkspacesDir(filepath.Join(root, "ws"))
	t.Cleanup(func() { SetWorkspacesDir(prevWS) })
	prevRoot := RootDB
	RootDB = &DBase{Store: kvlite.MemStore()}
	t.Cleanup(func() { RootDB = prevRoot })
	var mu sync.Mutex
	var forwarded []string
	prevFwd := NoticeForwarder
	NoticeForwarder = func(user, source, title, body string) {
		mu.Lock()
		forwarded = append(forwarded, user+"|"+title)
		mu.Unlock()
	}
	t.Cleanup(func() { NoticeForwarder = prevFwd })

	script := `
import json
from gohort import notify
a = notify("3 new cases", "CASE-1, CASE-2, CASE-3")
b = notify("3 new cases", "again")
print(json.dumps({"message": "ok", "result": {"a": a, "b": b}}))
`
	out, err := Job{Owner: "alice", Slug: "prep", Kind: "action", Name: "check", Script: script, Args: map[string]any{}, Caller: "bob"}.Run()
	if err != nil {
		t.Fatalf("run: %v\n%s", err, out)
	}
	if strings.Contains(out, "sandbox") && strings.Contains(out, "not") && !strings.Contains(out, "sent") {
		t.Skipf("no sandbox here: %s", out)
	}
	if !strings.Contains(out, `"a": "sent"`) || !strings.Contains(out, "noted again (2 times") {
		t.Errorf("script saw: %s", out)
	}
	got := notices.List(RootDB, "bob")
	if len(got) != 1 || got[0].Title != "3 new cases" || got[0].Count != 2 || !strings.Contains(got[0].Body, "/apps/prep/") {
		t.Errorf("bob's notices: %+v", got)
	}
	if len(notices.List(RootDB, "alice")) != 0 {
		t.Error("the owner was notified of a run somebody else made")
	}
	for i := 0; i < 50; i++ { // forwarding is async
		mu.Lock()
		n := len(forwarded)
		mu.Unlock()
		if n > 0 {
			break
		}
		sleepBriefly()
	}
	mu.Lock()
	if len(forwarded) != 1 || forwarded[0] != "bob|3 new cases" {
		t.Errorf("forwarded: %v", forwarded)
	}
	mu.Unlock()

	// A data source may not.
	out, _ = Job{Owner: "alice", Slug: "prep", Kind: "data", Name: "list", Script: `
import json
try:
    from gohort import notify
    notify("hi")
    r = "sent"
except Exception as e:
    r = str(e)
print(json.dumps({"r": r}))
`, Args: map[string]any{}, Caller: "bob"}.Run()
	if strings.Contains(out, `"r": "sent"`) {
		t.Errorf("a data source notified: %s", out)
	}
}

func sleepBriefly() { time.Sleep(20 * time.Millisecond) }
