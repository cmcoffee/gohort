package admin

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// What the Capabilities panels for embeddings, transcription and image
// generation get back, through the real routes and the gate in front of them:
// each config read and saved, its key kept on a save that sends the
// placeholder and never sent back, its model list, and its connectivity test
// against servers standing in for a provider. Each as an administrator, as a
// user who is not one, by the wrong method and with a broken or missing body,
// and then what the activity log says happened. No test here reaches past this
// machine. Compared with testdata/media_contract.golden.
//
// Run with UPDATE_GOLDEN=1 to rewrite the golden after a change that is meant.
func TestMediaRoutesContract(t *testing.T) {
	adb := &DBase{Store: kvlite.MemStore()}
	adb.Set(AuthTable, "user:alice", AuthUser{Username: "alice"})
	adb.Set(AuthTable, "user:root", AuthUser{Username: "root", Admin: true})
	prevAuth, prevRoot, prevEmbed, prevSTT := AuthDB, RootDB, GetEmbeddingConfig(), GetTranscribeConfig()
	AuthDB = func() Database { return adb }
	RootDB = &DBase{Store: kvlite.MemStore()}
	t.Cleanup(func() {
		AuthDB, RootDB = prevAuth, prevRoot
		SetEmbeddingConfig(prevEmbed)
		SetTranscribeConfig(prevSTT)
	})

	// One server stands in for every provider, by path: an embedder with a
	// model list, an STT server that takes the key, one that refuses it, and
	// a whisper.cpp that has no /models but answers at its root.
	stub := http.NewServeMux()
	stub.HandleFunc("/embed/v1/models", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"data":[{"id":"nomic-embed-text"},{"id":"bge-m3"}]}`))
	})
	stub.HandleFunc("/embed/v1/embeddings", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"data":[{"embedding":[0.1,0.2,0.3]}]}`))
	})
	stub.HandleFunc("/stt-ok/v1/models", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer s3cr3t-stt" {
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		w.Write([]byte(`{"data":[]}`))
	})
	stub.HandleFunc("/stt-deny/v1/models", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "no", http.StatusForbidden)
	})
	stub.HandleFunc("/whisper/", func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/models") {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte("<html>whisper.cpp</html>"))
	})
	srv := httptest.NewServer(stub)
	defer srv.Close()

	ct := &contract{
		t:       t,
		secrets: []string{"s3cr3t-embed", "s3cr3t-stt", "s3cr3t-img"},
		rewrite: strings.NewReplacer(srv.URL, "<stub>"),
	}
	app := &AdminApp{}
	mux := http.NewServeMux()
	app.RegisterRoutes(mux, "")
	do := func(user, method, path, body string) { ct.run(mux, adb, user, method, path, body) }
	// keeps holds a save that sent the placeholder to the key it already had:
	// the reply cannot show it (the key is masked there), and a test that
	// would use it reaches a provider this test does not call.
	keeps := func(table, key, want string) {
		t.Helper()
		var got string
		switch table {
		case ImageTable:
			adb.Get(table, key, &got)
		case EmbeddingTable:
			var cfg EmbeddingConfig
			adb.Get(table, key, &cfg)
			got = cfg.APIKey
		case TranscribeTable:
			var cfg TranscribeConfig
			adb.Get(table, key, &cfg)
			got = cfg.APIKey
		}
		if got != want {
			t.Errorf("a save sending the placeholder left %s's key as %q", table, got)
		}
	}

	for _, p := range []string{"/api/embeddings", "/api/transcribe", "/api/image-gen", "/api/embeddings/models"} {
		do("", "GET", p, "")
		do("alice", "GET", p, "")
		do("root", "GET", p, "")
		do("root", "PUT", p, "{}")
	}
	for _, p := range []string{"/api/embeddings/test", "/api/transcribe/test", "/api/image-gen/test"} {
		do("alice", "POST", p, `{"enabled":true}`)
		do("root", "GET", p, "")
		do("root", "POST", p, `{"enabled":`)
		do("root", "POST", p, ``)
	}

	embed := `"endpoint":"` + srv.URL + `/embed/v1","model":"nomic-embed-text"`
	do("alice", "POST", "/api/embeddings", `{"enabled":true,`+embed+`,"api_key":"s3cr3t-embed-x"}`)
	do("root", "POST", "/api/embeddings", `{"enabled":true,"provider":"local",`+embed+`,"api_key":"s3cr3t-embed-1"}`)
	do("root", "GET", "/api/embeddings", "")
	do("root", "POST", "/api/embeddings", `{"enabled":true,"provider":"local",`+embed+`,"api_key":"(unchanged)"}`)
	keeps(EmbeddingTable, "current", "s3cr3t-embed-1")
	do("root", "GET", "/api/embeddings/models", "")
	do("root", "POST", "/api/embeddings", `{"enabled":true,"provider":"peer:nope"}`)
	do("root", "POST", "/api/embeddings", `{"enabled":`)
	do("root", "POST", "/api/embeddings", ``)
	do("root", "POST", "/api/embeddings/test", `{"enabled":false,`+embed+`}`)
	do("root", "POST", "/api/embeddings/test", `{"enabled":true,"model":"m"}`)
	do("root", "POST", "/api/embeddings/test", `{"enabled":true,"provider":"peer:nope"}`)
	do("root", "POST", "/api/embeddings/test", `{"enabled":true,`+embed+`,"api_key":"(unchanged)"}`)
	do("root", "POST", "/api/embeddings/test", `{"enabled":true,"endpoint":"`+srv.URL+`/embed/v1"}`)
	do("root", "POST", "/api/embeddings/test", `{"enabled":true,"endpoint":"http://127.0.0.1:1/v1"}`)

	do("alice", "POST", "/api/transcribe", `{"enabled":true,"endpoint":"`+srv.URL+`/stt-ok/v1","api_key":"s3cr3t-stt-x"}`)
	do("root", "POST", "/api/transcribe", `{"enabled":true,"endpoint":"`+srv.URL+`/stt-ok/v1","model":"whisper-1","api_key":"s3cr3t-stt"}`)
	do("root", "GET", "/api/transcribe", "")
	do("root", "POST", "/api/transcribe", `{"enabled":true,"endpoint":"`+srv.URL+`/stt-ok/v1","model":"whisper-1","api_key":"(unchanged)"}`)
	keeps(TranscribeTable, "current", "s3cr3t-stt")
	do("root", "POST", "/api/transcribe", `{"enabled":true,"provider":"peer:nope"}`)
	do("root", "POST", "/api/transcribe", `{"enabled":`)
	do("root", "POST", "/api/transcribe/test", `{"enabled":false}`)
	do("root", "POST", "/api/transcribe/test", `{"enabled":true}`)
	do("root", "POST", "/api/transcribe/test", `{"enabled":true,"provider":"peer:nope"}`)
	do("root", "POST", "/api/transcribe/test", `{"enabled":true,"endpoint":"`+srv.URL+`/stt-ok/v1","api_key":"(unchanged)"}`)
	do("root", "POST", "/api/transcribe/test", `{"enabled":true,"endpoint":"`+srv.URL+`/stt-ok/v1","api_key":"wrong"}`)
	do("root", "POST", "/api/transcribe/test", `{"enabled":true,"endpoint":"`+srv.URL+`/stt-deny/v1"}`)
	do("root", "POST", "/api/transcribe/test", `{"enabled":true,"endpoint":"`+srv.URL+`/whisper/v1"}`)
	do("root", "POST", "/api/transcribe/test", `{"enabled":true,"endpoint":"`+srv.URL+`/nothing/v1"}`)
	do("root", "POST", "/api/transcribe/test", `{"enabled":true,"endpoint":"http://127.0.0.1:1/v1"}`)

	do("alice", "POST", "/api/image-gen", `{"provider":"openai","api_key":"s3cr3t-img-x"}`)
	do("root", "POST", "/api/image-gen", `{"provider":"openai","api_key":"s3cr3t-img"}`)
	do("root", "GET", "/api/image-gen", "")
	do("root", "POST", "/api/image-gen", `{"provider":"gemini","api_key":"(unchanged)"}`)
	keeps(ImageTable, "api_key", "s3cr3t-img")
	do("root", "GET", "/api/image-gen", "")
	do("root", "POST", "/api/image-gen", `{"provider":`)
	do("root", "POST", "/api/image-gen/test", `{"provider":""}`)
	do("root", "POST", "/api/image-gen/test", `{"provider":"none"}`)
	do("root", "POST", "/api/image-gen/test", `{"provider":"elsewhere","api_key":"(unchanged)"}`)
	do("root", "POST", "/api/image-gen", `{"provider":"gemini","api_key":""}`)
	do("root", "POST", "/api/image-gen/test", `{"provider":"gemini","api_key":"(unchanged)"}`)

	ct.activity(mux, adb, "root")
	ct.check(t, "testdata/media_contract.golden")
}
