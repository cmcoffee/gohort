package main

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/service"
	"github.com/cmcoffee/snugforge/kvlite"
)

// A tier on a connection is described by the connection's name, its model
// and, for a local server, its address, however its setting spells the
// choice: an llm: tier read as a provider named directly lost the model and
// the address.
func TestATierOnAConnectionIsDescribedByItsName(t *testing.T) {
	saved := RootDB
	RootDB = &DBase{Store: kvlite.MemStore()}
	defer func() { RootDB = saved }()
	ProvideConnectors(RootDB)
	c, ok := GetConnector(RootDB, "vllm")
	if !ok {
		t.Fatal("vllm is not provided")
	}
	if err := service.SaveSettings(c, "llm.chat", map[string]string{"base_url": "http://gpu.example:8000/v1"}, nil); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"llm:vllm", "service:vllm"} {
		// What saving the LLM page does: the tier names it, and it is settled in use.
		RootDB.Set(LLMTable, "provider", p)
		service.Settle(RootDB, "vllm")
		if _, err := ResolveModelProvider(LLMProviderConfig{Provider: p}, p); err != nil {
			t.Fatalf("%s does not resolve: %v", p, err)
		}
		if got := describeLLMConfig(LLMProviderConfig{Provider: p, Model: "qwen"}); got != "vLLM/qwen at http://gpu.example:8000/v1" {
			t.Errorf("%s described as %q", p, got)
		}
	}
	if got := describeLLMConfig(LLMProviderConfig{Provider: "peer:den", Model: "m"}); !strings.HasPrefix(got, "Peer den/m") {
		t.Errorf("a peer described as %q", got)
	}
}
