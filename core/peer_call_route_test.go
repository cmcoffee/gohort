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

	done1 := notePeerCallRoute("peer:gpu-box", "caller:history.fold")
	done2 := notePeerCallRoute("peer:gpu-box", "caller:history.fold")
	done3 := notePeerCallRoute("peer:gpu-box", "")
	rows := peerRowsFrom(OllamaSchedStats{Callers: map[string]int{"peer:gpu-box": 3}}, "running a model")
	if len(rows) != 1 || rows[0].Status != "on this machine's hardware, for its caller:history.fold (2), unnamed" {
		t.Fatalf("the peer's row should say what its calls are for: %+v", rows)
	}
	done1()
	done2()
	done3()
	rows = peerRowsFrom(OllamaSchedStats{Callers: map[string]int{"peer:gpu-box": 1}}, "running a model")
	if rows[0].Status != "on this machine's hardware" {
		t.Errorf("finished calls are still counted: %q", rows[0].Status)
	}
}

// Served end to end: while the borrowed call is on the model, the peer's row
// says what it is for, and it stops saying so when the call ends.
func TestTheServingSideCountsACallWhileItRuns(t *testing.T) {
	db := peerModelDB(t)
	var during string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		during = peerCallRoutesFor("peer:mac")
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
	if during != ", for its scribe (caller:history.fold)" {
		t.Errorf("while it ran, the peer's calls read %q", during)
	}
	if after := peerCallRoutesFor("peer:mac"); after != "" {
		t.Errorf("the ended call is still counted: %q", after)
	}
}
