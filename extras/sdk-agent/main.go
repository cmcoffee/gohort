// Example: using gohort's core as an agent SDK — no server, no boot, no
// database. Build an LLM, define a tool, run one agentic turn. This whole file
// is the "getting started" surface: import core, NewAgent, RunOnce.
//
// Run it against your own model by editing the LLMProviderConfig below. It
// compiles as-is (that's the point — it verifies the SDK API), but running it
// needs a reachable endpoint or an API key.
package main

import (
	"context"
	"fmt"
	"strconv"

	gohort "github.com/cmcoffee/gohort/core"
)

func main() {
	// A local model on llama.cpp / Ollama:
	agent, err := gohort.NewAgent(gohort.LLMProviderConfig{
		Provider: "llama.cpp",
		Endpoint: "http://localhost:8080/v1",
		Model:    "your-model",
	})
	// ...or a hosted one:
	//   gohort.NewAgent(gohort.LLMProviderConfig{
	//       Provider: "anthropic", APIKey: os.Getenv("ANTHROPIC_API_KEY"),
	//       Model: "claude-sonnet-5",
	//   })
	if err != nil {
		panic(err)
	}

	// Tools are plain structs: a schema plus a Go handler. No registration,
	// no framework, the handler is whatever code you want.
	tools := []gohort.AgentToolDef{{
		Tool: gohort.Tool{
			Name:        "add",
			Description: "Add two integers and return the sum.",
			Parameters: map[string]gohort.ToolParam{
				"a": {Type: "integer", Description: "first addend"},
				"b": {Type: "integer", Description: "second addend"},
			},
			Required: []string{"a", "b"},
		},
		Handler: func(args map[string]any) (string, error) {
			a, _ := args["a"].(float64)
			b, _ := args["b"].(float64)
			return strconv.Itoa(int(a) + int(b)), nil
		},
	}}

	reply, err := agent.RunOnce(context.Background(),
		"What is 21 plus 21? Use your add tool, then tell me the result.", tools)
	if err != nil {
		panic(err)
	}
	fmt.Println(reply)
}
