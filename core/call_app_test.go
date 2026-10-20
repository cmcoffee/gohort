package core

// A borrowed call names the app that asked, not only its route. Seen live: a
// peer's row read app.orchestrate.worker for work Scribe asked for, since
// Scribe's chat runs on orchestrate's agent loop, and a call that named no
// caller showed as unknown.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/cmcoffee/snugforge/apiclient"
)

func TestARequestIsTheWorkOfTheAppItCameInThrough(t *testing.T) {
	noteAppPath("/scribe", "scribe")
	t.Cleanup(func() { appPaths.Lock(); delete(appPaths.m, "/scribe"); appPaths.Unlock() })
	var got string
	h := callAppMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { got = CallAppOf(r.Context()) }))
	for path, want := range map[string]string{"/scribe/chat/api/send": "scribe", "/scribe": "scribe", "/scribex/a": "", "/": ""} {
		got = "-"
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, path, nil))
		if got != want {
			t.Errorf("%s is the work of %q, want %q", path, got, want)
		}
	}
	// The first name stays.
	if a := CallAppOf(WithCallApp(WithCallApp(context.Background(), "scribe"), "orchestrate")); a != "scribe" {
		t.Errorf("a later layer renamed the app to %q", a)
	}
}

func TestABorrowedCallSaysWhichAppAsked(t *testing.T) {
	defer scratchPeerStore(t)()
	got := make(chan string, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got <- r.Header.Get(peerAppHeader)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer srv.Close()
	k := grantFor(t)
	RootDB.Set(remotePeersTable, "gpu-box", RemotePeer{Name: "gpu-box", BaseURL: srv.URL, Key: k.Key, Caps: []string{PeerCapModels}})
	InvalidatePeerResolution()
	llm := newOpenAILLM("", "m", srv.URL, &apiclient.APIClient{RequestTimeout: llmRequestTimeout()})
	llm.(*openAIClient).api.AuthFunc = peerModelAuth("gpu-box")
	orch := &AppCore{LLM: llm}
	orch.SetAppName("orchestrate")
	msgs := []Message{{Role: "user", Content: "hi"}}

	// Scribe's request, run on orchestrate's core, is Scribe's.
	if _, err := orch.WorkerChat(WithCallApp(context.Background(), "scribe"), msgs); err != nil {
		t.Fatal(err)
	}
	if a := <-got; a != "scribe" {
		t.Errorf("the call went out as %q's, want scribe's", a)
	}
	// Work with no request behind it is the core's own app's.
	if _, err := orch.CreateSession(WORKER).Chat(context.Background(), msgs); err != nil {
		t.Fatal(err)
	}
	if a := <-got; a != "orchestrate" {
		t.Errorf("a core's own call went out as %q's", a)
	}

	// The serving side shows it beside the route, and drops what is no name.
	for app, want := range map[string]string{"scribe": "scribe (memory.gate)", "": "memory.gate", "<b>": "memory.gate"} {
		r := httptest.NewRequest(http.MethodPost, "/", nil)
		r.Header.Set(peerCallHeader, "memory.gate")
		r.Header.Set(peerAppHeader, app)
		if l := peerCallLabel(r); l != want {
			t.Errorf("app %q is counted as %q, want %q", app, l, want)
		}
	}
}
