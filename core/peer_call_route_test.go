package core

// A borrowed model call says what it is for, and the machine serving it shows
// that on the peer's row. Before, every borrowed call read "running a model":
// a history fold, a memory pass and an agent's turn looked alike, and none of
// them traced to anything on either machine.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cmcoffee/snugforge/apiclient"
)

func TestABorrowedCallSaysWhatItIsFor(t *testing.T) {
	defer scratchPeerStore(t)()
	got := make(chan string, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got <- r.Header.Get(peerCallHeader)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer srv.Close()
	k := grantFor(t)
	RootDB.Set(remotePeersTable, "gpu-box", RemotePeer{Name: "gpu-box", BaseURL: srv.URL, Key: k.Key, Caps: []string{PeerCapModels}})
	InvalidatePeerResolution()

	// The real client, sending through the real peer hook.
	llm := newOpenAILLM("", "m", srv.URL, &apiclient.APIClient{RequestTimeout: llmRequestTimeout()})
	llm.(*openAIClient).api.AuthFunc = peerModelAuth("gpu-box")
	if _, err := llm.Chat(context.Background(), []Message{{Role: "user", Content: "hi"}}, WithCaller("history.fold")); err != nil {
		t.Fatal(err)
	}
	if h := <-got; h != "caller:history.fold" {
		t.Errorf("the call went out saying it is for %q, want caller:history.fold", h)
	}
}

func TestTheServingSideShowsWhatABorrowedCallIsFor(t *testing.T) {
	for header, want := range map[string]string{
		"caller:history.fold":   "caller:history.fold",
		"memory.gate":           "memory.gate",
		"<script>":              "",
		"a name with spaces":    "",
		strings.Repeat("x", 65): "",
	} {
		r := httptest.NewRequest(http.MethodPost, "/", nil)
		r.Header.Set(peerCallHeader, header)
		if got := peerCallRoute(r); got != want {
			t.Errorf("header %q read as %q, want %q", header, got, want)
		}
	}

	w := func(app, route string) peerCall { return peerCall{tier: "worker", app: app, route: route} }
	done1 := notePeerCall("peer:gpu-box", w("scribe", "caller:history.fold"))
	done2 := notePeerCall("peer:gpu-box", w("scribe", "caller:history.fold"))
	done3 := notePeerCall("peer:gpu-box", w("", ""))
	rows := peerRowsFrom(OllamaSchedStats{Callers: map[string]int{"peer:gpu-box": 3}}, peerModelWork)
	if len(rows) != 1 || rows[0].Label != "Peer gpu-box: remote scribe is using this machine's worker model (3 at once)" ||
		rows[0].Status != "only the model runs here; its calls: caller:history.fold (2), unnamed" {
		t.Fatalf("the peer's row should say which app, which model and what its calls are for: %+v", rows)
	}
	done4 := notePeerCall("peer:gpu-box", peerCall{tier: "lead", app: "chat", route: "memory.gate"})
	if got := peerRowsFrom(OllamaSchedStats{Callers: map[string]int{"peer:gpu-box": 4}}, peerModelWork)[0].Label; got != "Peer gpu-box: remote chat and scribe are using this machine's worker and lead models (4 at once)" {
		t.Errorf("two apps on both tiers read %q", got)
	}
	done1()
	done2()
	done3()
	done4()
	rows = peerRowsFrom(OllamaSchedStats{Callers: map[string]int{"peer:gpu-box": 1}}, peerModelWork)
	if rows[0].Label != "Peer gpu-box: using this machine's model" || rows[0].Status != "only the model runs here" {
		t.Errorf("finished calls are still counted: %+v", rows[0])
	}
}

// Served end to end: while the borrowed call is on the model, the peer's row
// says what it is for, and it stops saying so when the call ends.
func TestTheServingSideCountsACallWhileItRuns(t *testing.T) {
	db := peerModelDB(t)
	var during peerCallsNow
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		during = peerCallsFor("peer:mac")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"hi"}}]}`))
	}))
	defer upstream.Close()
	setTier(db, LLMTable, "llama.cpp", "qwen-27b", upstream.URL+"/v1")
	pk, _ := MintPeerKey("mac", []string{PeerCapModels}, 0)
	r := httptest.NewRequest(http.MethodPost, "/api/peer/v1/chat/completions",
		strings.NewReader(`{"model":"qwen-27b","messages":[{"role":"user","content":"hi"}]}`))
	r.Header.Set(peerKeyHeader, peerAuth(t, pk))
	r.Header.Set(peerCallHeader, "caller:history.fold")
	r.Header.Set(peerAppHeader, "scribe")
	w := httptest.NewRecorder()
	HandlePeerChatCompletions(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	if got := peerModelDoing(peerModelWork, during); got != "remote scribe is using this machine's worker model" || during.routes != "caller:history.fold" {
		t.Errorf("while it ran, the peer's row read %q, its calls %q", got, during.routes)
	}
	if after := peerCallsFor("peer:mac"); after.routes != "" {
		t.Errorf("the ended call is still counted: %q", after)
	}
}
