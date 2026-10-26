package admin

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// What the LLM panels get back, through the real routes and the gate in front
// of them: the worker and lead configs read and saved (the key never sent
// back, a save the running process could not take said as such), each tier's
// model list, the routing table and its per-stage tier and thinking budget,
// the all-models-private switch that decides whether a private stage may
// reach the lead, the worker's thinking defaults, and the local scheduler.
// Each as an administrator, as a user who is not one, by the wrong method and
// with a broken or missing body, and then what the activity log says
// happened. Compared with testdata/llm_contract.golden.
//
// Run with UPDATE_GOLDEN=1 to rewrite the golden after a change that is meant.
func TestLLMRoutesContract(t *testing.T) {
	adb := &DBase{Store: kvlite.MemStore()}
	adb.Set(AuthTable, "user:alice", AuthUser{Username: "alice"})
	adb.Set(AuthTable, "user:root", AuthUser{Username: "root", Admin: true})
	prevAuth, prevRoot := AuthDB, RootDB
	prevW, prevL := SharedWorkerLLM(), SharedLeadLLM()
	AuthDB = func() Database { return adb }
	RootDB = &DBase{Store: kvlite.MemStore()}
	SetSharedLLMs(nil, nil)
	// A rebuild that fails, for the model named "breaks": what a save the
	// process could not apply answers.
	RegisterLLMReloader(func() error {
		var model string
		adb.Get(LLMTable, "model", &model)
		if model == "breaks" {
			return errors.New("the provider refused the config")
		}
		return nil
	})
	t.Cleanup(func() {
		AuthDB, RootDB = prevAuth, prevRoot
		SetSharedLLMs(prevW, prevL)
		RegisterLLMReloader(nil)
	})
	// Stages of this test's own, so the table's rows do not move when an app
	// registers another.
	RegisterRouteStage(RouteStage{Key: "zz_contract.open", Label: "Open stage", Default: "worker", App: "/zz_contract"})
	RegisterRouteStage(RouteStage{Key: "zz_contract.private", Label: "Private stage", Default: "worker", Private: true, App: "/zz_contract"})

	// An Ollama standing in for a provider's model list.
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"models":[{"name":"qwen3:8b"},{"name":"llama3.2"}],"data":[{"id":"qwen3:8b"},{"id":"llama3.2"}]}`))
	}))
	defer stub.Close()

	ct := &contract{
		t:       t,
		secrets: []string{"s3cr3t-llm-w", "s3cr3t-llm-l"},
		rewrite: strings.NewReplacer(stub.URL, "<stub>"),
		keep: func(row string) bool {
			return !strings.Contains(row, `"key":`) || strings.Contains(row, `"key":"zz_contract.`)
		},
	}
	app := &AdminApp{}
	mux := http.NewServeMux()
	app.RegisterRoutes(mux, "")
	do := func(user, method, path, body string) { ct.run(mux, adb, user, method, path, body) }

	for _, p := range []string{"/api/routing", "/api/routing?app=/zz_contract", "/api/llm-privacy", "/api/worker-thinking",
		"/api/worker-llm", "/api/lead-llm", "/api/local-scheduler"} {
		do("", "GET", p, "")
		do("alice", "GET", p, "")
		do("root", "GET", p, "")
		do("root", "PUT", p, "{}")
	}
	for _, p := range []string{"/api/worker-llm/models", "/api/lead-llm/models"} {
		do("alice", "POST", p, `{"provider":"ollama"}`)
		do("root", "GET", p, "")
		do("root", "POST", p, `{}`)
		do("root", "POST", p, `{"provider":"ollama","endpoint":"`+stub.URL+`","model":"qwen3:8b"}`)
		do("root", "POST", p, `{"provider":"ollama","endpoint":"`+stub.URL+`","model":"mistral"}`)
		do("root", "POST", p, `{"provider":"ollama","endpoint":"http://127.0.0.1:1"}`)
		do("root", "POST", p, `{"provider":`)
	}

	do("alice", "POST", "/api/routing", `{"key":"zz_contract.open","value":"lead"}`)
	do("root", "POST", "/api/routing", `{"key":"zz_contract.open","value":"lead"}`)
	do("root", "POST", "/api/routing", `{"key":"zz_contract.open","think_budget":2048}`)
	do("root", "POST", "/api/routing", `{"key":"zz_contract.open","value":"sideways"}`)
	do("root", "POST", "/api/routing", `{"key":"zz_contract.open","think_budget":70000}`)
	do("root", "POST", "/api/routing", `{"key":"zz_contract.open"}`)
	do("root", "POST", "/api/routing", `{"key":" ","value":"worker"}`)
	do("root", "POST", "/api/routing", `{"key":"zz_contract.private","value":"lead (thinking)"}`)
	do("root", "POST", "/api/routing", `{"key":"zz_contract.private","value":"worker (thinking)","think_budget":512}`)
	do("root", "POST", "/api/routing", `{"key":`)
	do("root", "POST", "/api/routing", ``)
	do("root", "GET", "/api/routing?app=/zz_contract", "")

	do("alice", "POST", "/api/llm-privacy", `{"all_private":true}`)
	do("root", "POST", "/api/llm-privacy", `{"all_private":true}`)
	do("root", "GET", "/api/llm-privacy", "")
	do("root", "POST", "/api/routing", `{"key":"zz_contract.private","value":"lead"}`)
	do("root", "GET", "/api/routing?app=/zz_contract", "")
	do("root", "POST", "/api/llm-privacy", `{"all_private":false}`)
	do("root", "POST", "/api/llm-privacy", `{"all_private":`)
	do("root", "GET", "/api/routing?app=/zz_contract", "")

	do("alice", "POST", "/api/worker-thinking", `{"enabled":false}`)
	do("root", "POST", "/api/worker-thinking", `{"enabled":false,"budget":1024}`)
	do("root", "GET", "/api/worker-thinking", "")
	do("root", "POST", "/api/worker-thinking", `{"enabled":true,"budget":0}`)
	do("root", "GET", "/api/worker-thinking", "")
	do("root", "POST", "/api/worker-thinking", ``)

	do("alice", "POST", "/api/local-scheduler", `{"ollama_max_parallel":4}`)
	do("root", "POST", "/api/local-scheduler", `{"ollama_max_parallel":0,"llamacpp_max_parallel":4,"background_yield":"sometimes","image_render_yield":"maybe"}`)
	do("root", "GET", "/api/local-scheduler", "")
	do("root", "POST", "/api/local-scheduler", `{"ollama_max_parallel":2,"llamacpp_max_parallel":1,"background_yield":"never","image_render_yield":"never"}`)
	do("root", "GET", "/api/local-scheduler", "")
	do("root", "POST", "/api/local-scheduler", `{"ollama_max_parallel":`)

	do("alice", "POST", "/api/worker-llm", `{"provider":"ollama","model":"qwen3:8b"}`)
	do("root", "POST", "/api/worker-llm", `{"provider":" ollama ","model":" qwen3:8b ","endpoint":" `+stub.URL+` ","api_key":"s3cr3t-llm-w","context_size":8192,"request_timeout_seconds":120,"default_effort":" HIGH ","max_effort":"extreme","thinking_budget":2048}`)
	do("root", "GET", "/api/worker-llm", "")
	do("root", "POST", "/api/worker-llm/models", `{"provider":"ollama","endpoint":"`+stub.URL+`"}`)
	do("root", "POST", "/api/worker-llm", `{"provider":"ollama","model":"qwen3:8b","endpoint":"`+stub.URL+`"}`)
	do("root", "POST", "/api/worker-llm", `{"provider":"bedrock","model":"anthropic.claude​-v2","aws_region":"us-east-1"}`)
	do("root", "POST", "/api/worker-llm", `{"provider":"ollama","model":"breaks","endpoint":"`+stub.URL+`"}`)
	do("root", "POST", "/api/worker-llm", `{"provider":`)
	do("root", "POST", "/api/worker-llm", ``)
	do("root", "GET", "/api/worker-llm", "")
	do("root", "POST", "/api/worker-llm", `{"provider":"ollama","model":"qwen3:8b","endpoint":"`+stub.URL+`"}`)
	do("root", "GET", "/api/lead-llm", "")
	do("root", "POST", "/api/lead-llm", `{"provider":"bedrock","model":"anthropic.claude-3-5-sonnet","aws_region":"us-east-1","bedrock_api":"converse","api_key":"s3cr3t-llm-l","no_images":true}`)
	do("root", "GET", "/api/lead-llm", "")
	do("root", "GET", "/api/llm-privacy", "")

	ct.activity(mux, adb, "root")
	ct.check(t, "testdata/llm_contract.golden")
}
