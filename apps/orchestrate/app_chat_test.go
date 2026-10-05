package orchestrate

import (
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/ui"
	"github.com/cmcoffee/snugforge/kvlite"
)

// Panel fills every chat endpoint an app left out, with the app's scope on
// the session URLs, and keeps the ones it set.
func TestAppChatPanelFillsTheChatEndpoints(t *testing.T) {
	c := AppChat{Prefix: "chat/", Query: "guide={scope}"}
	p := c.Panel(ui.AgentLoopPanel{SendURL: "chat/send?guide={scope}", ListURL: "mine"})
	for name, got := range map[string]string{
		"truncate": p.TruncateURL, "rename": p.RenameURL, "diagnostics": p.DiagnosticsURL,
		"block resolve": p.BlockResolveURL, "load": p.LoadURL, "delete": p.DeleteURL,
	} {
		if !strings.HasPrefix(got, "chat/sessions/") || !strings.HasSuffix(got, "?guide={scope}") {
			t.Errorf("%s = %q", name, got)
		}
	}
	if p.CancelURL != "chat/cancel" || p.ConfirmURL != "chat/confirm" || p.InjectURL != "chat/inject" || p.RunsURLBase != "api/runs/" {
		t.Fatalf("panel = %+v", p)
	}
	if p.ListURL != "mine" || p.SendURL != "chat/send?guide={scope}" {
		t.Fatalf("an app's own URL was replaced: list %q, send %q", p.ListURL, p.SendURL)
	}
}

// ServeAppChat answers the paths Panel names and leaves the rest, the send
// above all, to the app.
func TestServeAppChatRoutesOnlyItsOwn(t *testing.T) {
	T := &OrchestrateApp{AppCore: AppCore{DB: &DBase{Store: kvlite.MemStore()}}}
	c := AppChat{Prefix: "chat/"}
	for _, path := range []string{"chat/send", "chat/active", "guides", "chat/unknown"} {
		if T.ServeAppChat(httptest.NewRecorder(), httptest.NewRequest("GET", "/"+path, nil), AgentRecord{ID: "a"}, c, path, "") {
			t.Errorf("%s was taken", path)
		}
	}
	for _, path := range []string{"chat/cancel", "chat/inject", "chat/confirm", "chat/sessions", "chat/sessions/s1", "chat/sessions/s1/rename",
		"chat/sessions/s1/diagnostics", "chat/sessions/s1/blocks/b1/resolve", "api/runs/active", "api/runs/r1/stream"} {
		if !T.ServeAppChat(httptest.NewRecorder(), httptest.NewRequest("GET", "/"+path, nil), AgentRecord{ID: "a"}, c, path, "") {
			t.Errorf("%s was not answered", path)
		}
	}
}
