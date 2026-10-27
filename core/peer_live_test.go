package core

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cmcoffee/snugforge/apiclient"
)

// A borrowed model runs on this machine's GPU and competes with everything the
// operator is doing on it. Until now that was invisible: a turn took longer,
// nothing said why, and the only honest description was "it got slow". The
// scheduler had been counting it per caller the whole time; nothing read it out.

// TestAPeerRunningTheModelIsVisible — the reported gap, end to end through the
// same registry the live pill reads.
func TestAPeerRunningTheModelIsVisible(t *testing.T) {
	StartLlamacppScheduler(2)
	if err := AcquireLlamacppSlot(context.Background(), "peer:studio-mac"); err != nil {
		t.Fatal(err)
	}
	defer ReleaseLlamacppSlot("peer:studio-mac")

	var found *LiveEntry
	for _, e := range peerLiveEntries() {
		if strings.Contains(e.Label, "studio-mac") {
			row := e
			found = &row
		}
	}
	if found == nil {
		t.Fatal("a peer holding a model slot produces no live row — the GPU is busy with " +
			"someone else's work and nothing on screen says so")
	}
	if !found.Background {
		t.Error("peer work is not marked Background — it started without the viewer, which is " +
			"exactly what that flag means and how the pill paints it differently")
	}
	if found.App != "Peers" {
		t.Errorf("row is filed under %q", found.App)
	}
	if !strings.Contains(found.Label, "using this machine's model") {
		t.Errorf("the row does not say what the peer is doing: %q", found.Label)
	}
}

// TestLocalWorkIsNotDuplicated — apps already contribute live rows for their own
// turns. Reporting them here too would double every row in the ribbon.
func TestLocalWorkIsNotDuplicated(t *testing.T) {
	StartLlamacppScheduler(2)
	if err := AcquireLlamacppSlot(context.Background(), "chat-session-42"); err != nil {
		t.Fatal(err)
	}
	defer ReleaseLlamacppSlot("chat-session-42")
	for _, e := range peerLiveEntries() {
		if strings.Contains(e.Label, "chat-session-42") {
			t.Errorf("local work appeared as a peer row: %q", e.Label)
		}
	}
}

// TestQueuedPeerWorkReadsDifferently — "a peer is using the GPU" and "a peer is
// waiting behind you" are different facts, and only the first means the machine
// is busy with someone else's work right now.
func TestQueuedPeerWorkReadsDifferently(t *testing.T) {
	rows := peerRowsFrom(OllamaSchedStats{
		Callers: map[string]int{"peer:mac": 1},
		Queued:  map[string]int{"peer:laptop": 3},
	}, peerModelWork)

	var running, queued *LiveEntry
	for i := range rows {
		switch {
		case strings.HasPrefix(rows[i].Label, "Peer mac:"):
			running = &rows[i]
		case strings.HasPrefix(rows[i].Label, "Peer laptop:"):
			queued = &rows[i]
		}
	}
	if running == nil || queued == nil {
		t.Fatalf("expected a running row and a queued row, got %d rows", len(rows))
	}
	if running.Queued {
		t.Error("in-flight work is marked queued")
	}
	if !queued.Queued {
		t.Error("waiting work is not marked queued, so it reads as using the GPU right now")
	}
	if queued.Label != "Peer laptop: 3 waiting for this machine's model" {
		t.Errorf("the queued row does not say how deep it is: %q", queued.Label)
	}
}

// TestTheRowNamesThePeer — the key's LABEL is what somebody typed so they could
// recognize the far side later. A row reading "peer:3f2a…" answers none of the
// question it exists to answer.
func TestTheRowNamesThePeer(t *testing.T) {
	if got := peerDisplayName("peer:studio-mac"); got != "Peer studio-mac" {
		t.Errorf("display name = %q", got)
	}
	// A blank label still produces something readable rather than "Peer ".
	if got := peerDisplayName("peer:"); got != "A peer" {
		t.Errorf("an unlabelled key renders as %q", got)
	}
}

// TestConcurrentWorkSaysHowMuch — one peer holding several slots is a different
// situation from one holding one, and the ribbon has room to say which.
func TestConcurrentWorkSaysHowMuch(t *testing.T) {
	rows := peerRowsFrom(OllamaSchedStats{Callers: map[string]int{"peer:mac": 3}}, peerImageWork)
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want one", len(rows))
	}
	if !strings.Contains(rows[0].Label, "3 at once") {
		t.Errorf("a peer holding three slots reads as one: %q", rows[0].Label)
	}
}

// TestOrderIsStableAcrossPolls — the ribbon is polled, and a row that moves
// reads as a new row. Map iteration would reshuffle these every second.
func TestOrderIsStableAcrossPolls(t *testing.T) {
	StartLlamacppScheduler(4)
	for _, p := range []string{"peer:a", "peer:b", "peer:c"} {
		if err := AcquireLlamacppSlot(context.Background(), p); err != nil {
			t.Fatal(err)
		}
		defer ReleaseLlamacppSlot(p)
	}
	first := peerLiveEntries()
	for i := 0; i < 20; i++ {
		again := peerLiveEntries()
		if len(again) != len(first) {
			t.Fatalf("row count is unstable: %d vs %d", len(first), len(again))
		}
		for j := range first {
			if again[j].ID != first[j].ID {
				t.Fatal("row order is unstable between polls — the ribbon would reshuffle every refresh")
			}
		}
	}
}

// A borrowed model call says what it is for, and the machine serving it shows
// that on the peer's row. Before, every borrowed call read "running a model":
// a history fold, a memory pass and an agent's turn looked alike, and none of
// them traced to anything on either machine.

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
	done3 := notePeerCall("peer:gpu-box", w("scribe", ""))
	rows := peerRowsFrom(OllamaSchedStats{Callers: map[string]int{"peer:gpu-box": 3}}, peerModelWork)
	if len(rows) != 1 || rows[0].Label != "Peer gpu-box: remote scribe is using this machine's worker model (3 at once)" ||
		rows[0].Status != "only the model runs here; remote scribe: history.fold (2), 1 other" {
		t.Fatalf("the peer's row should say which app, which model and what its calls are for: %+v", rows)
	}
	done4 := notePeerCall("peer:gpu-box", peerCall{tier: "lead", app: "chat", route: "memory.gate"})
	rows = peerRowsFrom(OllamaSchedStats{Callers: map[string]int{"peer:gpu-box": 4}}, peerModelWork)
	if got := rows[0].Label; got != "Peer gpu-box: remote chat and scribe are using this machine's worker and lead models (4 at once)" {
		t.Errorf("two apps on both tiers read %q", got)
	}
	if got := rows[0].Status; got != "only the model runs here; remote chat: memory.gate; remote scribe: history.fold (2), 1 other" {
		t.Errorf("two apps' calls read %q", got)
	}
	done1()
	done2()
	done3()
	done4()
	rows = peerRowsFrom(OllamaSchedStats{Callers: map[string]int{"peer:gpu-box": 1}}, peerModelWork)
	if rows[0].Label != "Peer gpu-box: using this machine's model" || rows[0].Status != "only the model runs here" {
		t.Errorf("finished calls are still counted: %+v", rows[0])
	}
	// Calls that name nothing list nothing, rather than "tasks: 1 other".
	done5 := notePeerCall("peer:gpu-box", w("scribe", ""))
	defer done5()
	if got := peerRowsFrom(OllamaSchedStats{Callers: map[string]int{"peer:gpu-box": 1}}, peerModelWork)[0].Status; got != "only the model runs here" {
		t.Errorf("an unnamed call alone reads %q", got)
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
	if got := peerModelDoing(peerModelWork, during); got != "remote scribe is using this machine's worker model" || during.routes != "remote scribe: history.fold" {
		t.Errorf("while it ran, the peer's row read %q, its calls %q", got, during.routes)
	}
	if after := peerCallsFor("peer:mac"); after.routes != "" {
		t.Errorf("the ended call is still counted: %q", after)
	}
}
