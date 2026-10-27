package bridges

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// What the Bridges page and its owner's browser get back from each JSON
// endpoint, through the real routes: signed out, as the owner, as another
// user, as an administrator, by the wrong method, and with no body or a
// broken one. Compared with testdata/routes_contract.golden, so a change to
// how a handler is written that moves a status or a reply shows up as a diff.
//
// Run with UPDATE_GOLDEN=1 to rewrite the golden after a change that is meant.
func TestRoutesContract(t *testing.T) {
	f := newOwnershipFixture(t)
	mux := http.NewServeMux()
	f.T.RegisterRoutes(mux, "/bridges")

	SaveChannel(RootDB, Channel{ID: "ch-alice", Owner: "alice", Name: "Alice's line", AgentID: "seed-chat"})
	f.T.saveBridgeKey(BridgeKey{ID: "key-alice", Name: "laptop", Key: "secret", Owner: "alice", Service: "api", Enabled: true, Created: "2026-01-01T00:00:00Z"})

	var log strings.Builder
	hex := regexp.MustCompile(`[0-9a-f]{12,48}`)
	stamp := regexp.MustCompile(`\d{4}-\d\d-\d\dT\d\d:\d\d:\d\dZ`)
	do := func(user, method, path, body string) {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		if user != "" {
			r.AddCookie(&http.Cookie{Name: "gohort_session", Value: AuthCreateSession(f.adb, user)})
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		out := strings.TrimSpace(w.Body.String())
		// A list's order is not part of the contract (the conversation list
		// comes back in store order), so compare it sorted.
		var items []json.RawMessage
		if strings.HasPrefix(out, "[") && json.Unmarshal([]byte(out), &items) == nil {
			rows := make([]string, len(items))
			for i, it := range items {
				// Masked first: a row's random id must not decide its place.
				rows[i] = stamp.ReplaceAllString(hex.ReplaceAllString(string(it), "<hex>"), "<time>")
			}
			sort.Strings(rows)
			out = "[" + strings.Join(rows, ",") + "]"
		}
		out = hex.ReplaceAllString(out, "<hex>")
		out = stamp.ReplaceAllString(out, "<time>")
		who := user
		if who == "" {
			who = "(signed out)"
		}
		fmt.Fprintf(&log, "%s %s %s %q\n  %d %s\n", who, method, path, body, w.Code, out)
	}

	for _, p := range []string{"/bridges/api/bridges", "/bridges/api/conversations", "/bridges/api/incoming-convos", "/bridges/api/agent-channels"} {
		do("", "GET", p, "")
		do("alice", "GET", p, "")
		do("alice", "POST", p, "{}")
	}
	do("alice", "GET", "/bridges/api/relay-tail?id=key-alice", "")
	do("bob", "GET", "/bridges/api/relay-tail?id=key-alice", "")
	for _, p := range []string{"/bridges/api/conv-info/", "/bridges/api/messages/"} {
		do("alice", "GET", p+"chat-alice", "")
		do("bob", "GET", p+"chat-alice", "")
		do("alice", "GET", p, "")
	}

	do("alice", "GET", "/bridges/api/keys", "")
	do("alice", "POST", "/bridges/api/keys", `{"name":"phone"}`)
	do("alice", "POST", "/bridges/api/keys", "")
	do("alice", "POST", "/bridges/api/keys", `{"name":`)
	do("alice", "POST", "/bridges/api/keys", `{"service":"imessage"}`)
	do("root", "POST", "/bridges/api/keys", `{"service":"imessage","name":"relay"}`)
	do("alice", "DELETE", "/bridges/api/keys", "")
	do("alice", "PATCH", "/bridges/api/keys/key-alice", `{"enabled":false}`)
	do("alice", "GET", "/bridges/api/bridges", "")
	do("alice", "POST", "/bridges/api/keys/key-alice", `{"enabled":true}`)
	do("alice", "PATCH", "/bridges/api/keys/key-alice", ``)
	do("alice", "PATCH", "/bridges/api/keys/key-alice", `{"enabled":`)
	do("alice", "GET", "/bridges/api/keys/key-alice", "")
	do("bob", "PATCH", "/bridges/api/keys/key-alice", `{"enabled":false}`)
	do("bob", "DELETE", "/bridges/api/keys/key-alice", "")
	do("alice", "PATCH", "/bridges/api/keys/nope", `{"enabled":true}`)
	do("alice", "DELETE", "/bridges/api/keys/nope", "")
	do("", "DELETE", "/bridges/api/keys/key-alice", "")
	do("alice", "GET", "/bridges/api/bridges", "")
	do("alice", "DELETE", "/bridges/api/keys/key-alice", "")
	do("alice", "GET", "/bridges/api/bridges", "")

	do("alice", "GET", "/bridges/api/config", "")
	do("alice", "PATCH", "/bridges/api/config", `{"self_name":"Alice"}`)
	do("root", "PATCH", "/bridges/api/config", `{"self_name":"Root"}`)
	do("root", "PATCH", "/bridges/api/config", ``)
	do("root", "PATCH", "/bridges/api/config", `{"self_name":`)
	do("root", "DELETE", "/bridges/api/config", "")
	do("alice", "POST", "/bridges/api/panic", "")
	do("root", "GET", "/bridges/api/panic", "")
	do("root", "POST", "/bridges/api/panic", "")
	do("root", "GET", "/bridges/api/config", "")

	do("alice", "GET", "/bridges/api/add-convo", "")
	do("alice", "POST", "/bridges/api/add-convo?chat_id=chat-alice", "")
	do("alice", "POST", "/bridges/api/add-convo", `{"handle":"+15550100"}`)
	do("alice", "POST", "/bridges/api/add-convo", `{}`)
	do("alice", "POST", "/bridges/api/add-convo", `{"handle":`)
	do("bob", "POST", "/bridges/api/add-convo?chat_id=chat-alice", "")
	do("alice", "DELETE", "/bridges/api/add-convo", "")

	do("alice", "POST", "/bridges/api/connect-channel?chat_id=chat-alice&channel_id=ch-alice", "")
	do("alice", "GET", "/bridges/api/agent-channels", "")
	do("alice", "PATCH", "/bridges/api/set-autoreply?id=ch-alice", `{"auto_reply":false}`)
	do("alice", "PATCH", "/bridges/api/set-autoreply?id=ch-alice", ``)
	do("alice", "PATCH", "/bridges/api/set-autoreply?id=ch-alice", `{"auto_reply":`)
	do("alice", "GET", "/bridges/api/set-autoreply?id=ch-alice", "")
	do("alice", "PATCH", "/bridges/api/set-autoreply?id=nope", `{"auto_reply":true}`)
	do("alice", "GET", "/bridges/api/conversations", "")
	do("alice", "POST", "/bridges/api/connect-channel?chat_id=chat-alice", "")
	do("alice", "POST", "/bridges/api/connect-channel", `{}`)
	do("alice", "POST", "/bridges/api/connect-channel", `{"chat_id":`)
	do("alice", "GET", "/bridges/api/connect-channel?chat_id=chat-alice", "")
	do("bob", "POST", "/bridges/api/connect-channel?chat_id=chat-alice", "")
	do("alice", "POST", "/bridges/api/connect-channel?chat_id=chat-alice&channel_id=nope", "")

	do("alice", "POST", "/bridges/api/webhook-secret", `{"connector":"nope","secret":"x"}`)
	do("alice", "POST", "/bridges/api/webhook-secret", `{"connector":`)
	do("alice", "GET", "/bridges/api/webhook-secret", "")

	do("alice", "PATCH", "/bridges/api/conversation/chat-alice", `{"display_name":"Renamed"}`)
	do("alice", "PATCH", "/bridges/api/conversation/chat-alice", `{"display_name":`)
	do("alice", "PATCH", "/bridges/api/conversation/chat-alice", ``)
	do("bob", "PATCH", "/bridges/api/conversation/chat-alice", `{"display_name":"mine"}`)
	do("alice", "PATCH", "/bridges/api/conversation/", `{}`)
	do("alice", "GET", "/bridges/api/conversation/chat-alice", "")
	do("alice", "DELETE", "/bridges/api/conversation/chat-alice", "")
	do("alice", "GET", "/bridges/api/conversations", "")

	got := log.String()
	const golden = "testdata/routes_contract.golden"
	if os.Getenv("UPDATE_GOLDEN") != "" {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(golden, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("read %s: %v (run with UPDATE_GOLDEN=1 to create it)", golden, err)
	}
	if got != string(want) {
		gl, wl := strings.Split(got, "\n"), strings.Split(string(want), "\n")
		for i := 0; i < len(gl) || i < len(wl); i++ {
			var g, w string
			if i < len(gl) {
				g = gl[i]
			}
			if i < len(wl) {
				w = wl[i]
			}
			if g != w {
				t.Fatalf("reply differs from %s at line %d:\n got: %s\nwant: %s", golden, i+1, g, w)
			}
		}
	}
}
