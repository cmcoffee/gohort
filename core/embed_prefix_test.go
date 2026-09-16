package core

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// An asymmetric model sees each side marked for what it is: the query prefix
// on a search, the document prefix on what is stored, neither on the raw
// pass-through the peer endpoint uses.
func TestEmbedPrefixesMarkEachSide(t *testing.T) {
	var got []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Input []string `json:"input"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		got = append(got, req.Input...)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"embedding":[0.1,0.2]}]}`))
	}))
	defer srv.Close()
	cfg := EmbeddingConfig{Enabled: true, Endpoint: srv.URL, Model: "e5", QueryPrefix: "query: ", DocPrefix: "passage: "}
	prev := GetEmbeddingConfig()
	defer SetEmbeddingConfig(prev)
	SetEmbeddingConfig(cfg)

	ctx := context.Background()
	for _, call := range []func() ([]float32, error){
		func() ([]float32, error) { return Embed(ctx, "how do I shrink a volume") },
		func() ([]float32, error) { return embedDocument(ctx, "Shrinking an LVM volume") },
		func() ([]float32, error) { return embedRaw(ctx, cfg, "as is") },
	} {
		if _, err := call(); err != nil {
			t.Fatal(err)
		}
	}
	want := []string{"query: how do I shrink a volume", "passage: Shrinking an LVM volume", "as is"}
	if len(got) != len(want) {
		t.Fatalf("got %d embed calls, want %d: %q", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("call %d sent %q, want %q", i, got[i], want[i])
		}
	}
}

// The document prefix is part of the space a stored vector lives in: it shows
// in the version facts are stamped with and the stamp chunks carry, so a
// prefix change makes old vectors non-comparable instead of silently wrong.
// The query prefix is not, since it never touches a stored vector.
func TestDocPrefixIsPartOfTheEmbeddingSpace(t *testing.T) {
	prev := GetEmbeddingConfig()
	defer SetEmbeddingConfig(prev)
	base := EmbeddingConfig{Enabled: true, Endpoint: "http://x", Model: "m"}
	SetEmbeddingConfig(base)
	v0, s0 := EmbedVersion(), currentEmbedModel()
	withQuery := base
	withQuery.QueryPrefix = "query: "
	SetEmbeddingConfig(withQuery)
	if EmbedVersion() != v0 || currentEmbedModel() != s0 {
		t.Fatal("a query prefix must not change the space")
	}
	withDoc := base
	withDoc.DocPrefix = "passage: "
	SetEmbeddingConfig(withDoc)
	if EmbedVersion() == v0 || currentEmbedModel() == s0 {
		t.Fatal("a document prefix must change the space")
	}
	if chunkVectorComparable(&EmbeddedChunk{Vector: []float32{1}, Model: "m"}, []float32{1}, currentEmbedModel()) {
		t.Fatal("a chunk stamped without the prefix must not compare against the prefixed space")
	}
	if s := base.spaceStamp(); s != "m" {
		t.Fatalf("no prefix keeps the bare model name so existing rows still match, got %q", s)
	}
}
