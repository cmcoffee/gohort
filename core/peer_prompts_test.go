package core

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/cmcoffee/oddjob/core/prompts"
	"github.com/cmcoffee/snugforge/kvlite"
)

const (
	peerPromptsTestKey  = "test.peer_prompts.block"
	peerPromptsTestText = "[Peer block: check the tool output before answering, {n} times over.]"
)

var registerPeerPromptsBlock sync.Once

// peerPromptsStore registers a block and gives the prompt layer a scratch store.
func peerPromptsStore(t *testing.T) {
	t.Helper()
	registerPeerPromptsBlock.Do(func() {
		prompts.RegisterPromptBlock(prompts.PromptBlock{Key: peerPromptsTestKey, Title: "peer test", Text: peerPromptsTestText})
	})
	prompts.SetPromptOverrideDB(&DBase{Store: kvlite.MemStore()})
	t.Cleanup(func() { prompts.SetPromptOverrideDB(nil) })
}

// The serving side hands out its overrides of registered blocks and nothing
// else: not this operator's style rules, not a tier's own text.
func TestPeerPromptsServesOnlyRegisteredBlockOverrides(t *testing.T) {
	db := peerModelDB(t)
	peerPromptsStore(t)
	setTier(db, LLMTable, "llama.cpp", "qwen-27b", "http://127.0.0.1:8080/v1")
	style := prompts.BuiltinStyleRules()[0]
	prompts.SetPromptOverride(peerPromptsTestKey, "[Peer block: tuned here, {n} times.]")
	prompts.SetPromptOverride(style.Key, "this operator's house style")
	prompts.SetPromptTierOverride(prompts.TierWorker, peerPromptsTestKey, "worker only", "qwen-27b")
	pk, _ := MintPeerKey("mac", []string{PeerCapModels}, 0)

	r := httptest.NewRequest(http.MethodGet, "/api/peer/v1/prompts", nil)
	r.Header.Set(peerKeyHeader, peerAuth(t, pk))
	w := httptest.NewRecorder()
	handlePeerPrompts(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("got %d: %s", w.Code, w.Body.String())
	}
	var out peerPromptsBody
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Model != "qwen-27b" {
		t.Errorf("model %q, want the worker's model id", out.Model)
	}
	if len(out.Blocks) != 1 || out.Blocks[peerPromptsTestKey] != "[Peer block: tuned here, {n} times.]" {
		t.Errorf("served %v, want only the registered block's shared override", out.Blocks)
	}
	if got := peerModelsInfo("/api/peer/v1"); len(got) == 0 || got[0].Prompts != "/api/peer/v1/prompts" {
		t.Errorf("the manifest does not say where the wording is: %+v", got)
	}
}

// Gated like /models: a key without the grant is refused, and so is any key
// when there is no local model to lend.
func TestPeerPromptsIsGatedLikeModels(t *testing.T) {
	db := peerModelDB(t)
	peerPromptsStore(t)
	setTier(db, LLMTable, "llama.cpp", "qwen-27b", "http://127.0.0.1:8080/v1")
	ask := func(k PeerKey) int {
		r := httptest.NewRequest(http.MethodGet, "/api/peer/v1/prompts", nil)
		r.Header.Set(peerKeyHeader, peerAuth(t, k))
		w := httptest.NewRecorder()
		handlePeerPrompts(w, r)
		return w.Code
	}
	emb, _ := MintPeerKey("emb", []string{PeerCapEmbeddings}, 0)
	if code := ask(emb); code != http.StatusForbidden {
		t.Errorf("a key without the models grant got %d, want 403", code)
	}
	models, _ := MintPeerKey("mac", []string{PeerCapModels}, 0)
	setTier(db, LLMTable, "anthropic", "claude-opus-5", "")
	if code := ask(models); code != http.StatusServiceUnavailable {
		t.Errorf("an instance with no model to lend got %d, want 503", code)
	}
}

// The borrowing side: a worker pointed at a peer reads its wording, applies
// what fits, keeps it through a failed refresh, and drops it once the worker
// is no longer that peer's model.
func TestPeerPromptsFollowTheWorkersPeer(t *testing.T) {
	db := peerModelDB(t)
	peerPromptsStore(t)
	var sentAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/peer/v1/prompts" {
			http.NotFound(w, r)
			return
		}
		sentAuth = r.Header.Get("Authorization")
		json.NewEncoder(w).Encode(peerPromptsBody{Model: "qwen-27b", Blocks: map[string]string{
			peerPromptsTestKey:  "[Peer block: the peer's words, {n} times.]",
			"test.not_here":     "a block this build does not have",
			"framework.unknown": "another",
		}})
	}))
	putTestPeer(RemotePeer{Name: "den", BaseURL: srv.URL, Key: "static-key", Caps: []string{PeerCapModels}})
	prompts.SetPromptOverride(peerPromptsTestKey, "[Peer block: local words, {n} times.]")
	db.Set(LLMTable, "provider", PeerProviderValue("den"))

	var s peerPromptSync
	s.tick(context.Background(), time.Now())
	if sentAuth == "" {
		t.Error("the fetch carried no credential")
	}
	if got := prompts.EffectivePromptText(peerPromptsTestKey, peerPromptsTestText); got != "[Peer block: the peer's words, {n} times.]" {
		t.Fatalf("the peer's wording does not govern: %q", got)
	}
	if st := prompts.PeerPromptLayer(); st.Source != "den" || st.Model != "qwen-27b" || st.Count != 1 {
		t.Fatalf("status %+v, want den / qwen-27b / 1 block", st)
	}

	// The peer goes down: the last wording stays.
	srv.Close()
	s.sync(context.Background(), "den")
	if !s.failing {
		t.Error("a failed refresh did not start a failure streak")
	}
	if got, _ := prompts.PromptOverride(peerPromptsTestKey); got != "[Peer block: the peer's words, {n} times.]" {
		t.Fatalf("a failed refresh dropped the last wording: %q", got)
	}

	// The worker moves to a local model: the local override governs again.
	db.Set(LLMTable, "provider", "llama.cpp")
	s.tick(context.Background(), time.Now())
	if got := prompts.EffectivePromptText(peerPromptsTestKey, peerPromptsTestText); got != "[Peer block: local words, {n} times.]" {
		t.Fatalf("after the worker left the peer: %q", got)
	}
	if prompts.PeerPromptLayer().Source != "" {
		t.Error("the layer is still reported after the worker left the peer")
	}
}

// An older peer has no /prompts and answers 404: that is no wording, not an
// error, and nothing of an earlier copy is kept.
func TestPeerPromptsFromAnOlderPeerAreEmpty(t *testing.T) {
	db := peerModelDB(t)
	peerPromptsStore(t)
	srv := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(srv.Close)
	putTestPeer(RemotePeer{Name: "old", BaseURL: srv.URL, Key: "static-key", Caps: []string{PeerCapModels}})
	db.Set(LLMTable, "provider", PeerProviderValue("old"))

	body, err := fetchPeerPrompts(context.Background(), RemotePeer{Name: "old", BaseURL: srv.URL, Key: "static-key"})
	if err != nil || len(body.Blocks) != 0 {
		t.Fatalf("a 404 gave %+v, %v; want empty and no error", body, err)
	}
	prompts.SetPeerPromptLayer("old", "m", time.Now(), map[string]string{peerPromptsTestKey: "[Peer block: stale, {n}.]"})
	var s peerPromptSync
	s.tick(context.Background(), time.Now())
	if s.failing {
		t.Error("a 404 was treated as a failure")
	}
	if st := prompts.PeerPromptLayer(); st.Source != "old" || st.Count != 0 {
		t.Errorf("status %+v, want the peer named with no blocks", st)
	}
	if _, ok := prompts.PromptOverride(peerPromptsTestKey); ok {
		t.Error("an earlier copy survived an answer of no wording")
	}
}
