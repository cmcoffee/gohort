package admin

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeModelServer answers the probe's requests the way one kind of server
// does: routes maps a path to the JSON it returns; anything else is a 404.
func fakeModelServer(t *testing.T, routes map[string]string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, ok := routes[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// Each kind of server is told apart by what it says about itself, with the
// model and context window it reports.
func TestProbeTellsServersApart(t *testing.T) {
	vllm := fakeModelServer(t, map[string]string{
		"/v1/models": `{"object":"list","data":[{"id":"qwen","owned_by":"vllm","root":"/opt/vllm/models/Qwen-27B-NVFP4","max_model_len":204800}]}`,
	})
	llama := fakeModelServer(t, map[string]string{
		"/v1/models": `{"data":[{"id":"/models/qwen-27b.gguf","owned_by":"llamacpp","meta":{"n_ctx":131072}}]}`,
	})
	// An older llama.cpp without meta.n_ctx: the context comes from /props.
	llamaOld := fakeModelServer(t, map[string]string{
		"/v1/models": `{"data":[{"id":"local","owned_by":"llamacpp"}]}`,
		"/props":     `{"default_generation_settings":{"n_ctx":32768}}`,
	})
	ollama := fakeModelServer(t, map[string]string{
		"/api/version": `{"version":"0.9.0"}`,
	})
	unknown := fakeModelServer(t, map[string]string{})

	for _, c := range []struct {
		name, endpoint string
		want           serverProbe
	}{
		{"vllm", vllm + "/v1", serverProbe{Kind: "vllm", Model: "qwen (Qwen-27B-NVFP4)", Context: 204800}},
		{"llama.cpp", llama + "/v1", serverProbe{Kind: "llama.cpp", Model: "qwen-27b.gguf", Context: 131072}},
		{"old llama.cpp", llamaOld + "/v1", serverProbe{Kind: "llama.cpp", Model: "local", Context: 32768}},
		{"ollama", ollama, serverProbe{Kind: "ollama"}},
		{"endpoint without /v1", vllm, serverProbe{Kind: "vllm", Model: "qwen (Qwen-27B-NVFP4)", Context: 204800}},
		{"unknown", unknown + "/v1", serverProbe{}},
		{"unreachable", "http://127.0.0.1:1/v1", serverProbe{}},
	} {
		if got := probeModelServer(t.Context(), "llama.cpp", c.endpoint, ""); got != c.want {
			t.Errorf("%s: probe = %+v, want %+v", c.name, got, c.want)
		}
	}
}

// What the test says: the server, and what in the form does not fit it. The
// mismatch it was written for is a llama.cpp provider pointed at vLLM.
func TestProbeAdvice(t *testing.T) {
	vllm := serverProbe{Kind: "vllm", Model: "qwen", Context: 204800}

	msg := vllm.describe("llama.cpp", 0)
	for _, want := range []string{
		"Server: vLLM, serving qwen, context 204,800 tokens.",
		"The provider is set to llama.cpp, but this endpoint is vLLM: pick vLLM",
		"vLLM ignores the thinking budget",
		"Context size is blank, so oddjob works within 65,536 tokens; the server allows 204,800.",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("advice missing %q:\n%s", want, msg)
		}
	}

	// Matching provider and a context that fits: just the facts.
	if msg := vllm.describe("vllm", 131072); strings.Contains(msg, "pick") || strings.Contains(msg, "Context size") {
		t.Errorf("nothing to fix, but the advice says:\n%s", msg)
	}
	// A context larger than the server allows is called out.
	if msg := vllm.describe("vllm", 262144); !strings.Contains(msg, "more than the server's 204,800") {
		t.Errorf("an oversized context went unmentioned:\n%s", msg)
	}
	// A server that would not say produces nothing, rather than a guess.
	if msg := (serverProbe{}).describe("llama.cpp", 0); msg != "" {
		t.Errorf("unknown server described as %q", msg)
	}
}
