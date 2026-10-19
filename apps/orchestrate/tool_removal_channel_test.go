package orchestrate

// A tool taken away from an agent is gone from its channel too, on the very
// next message in a thread where the agent had it.

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// offered reports whether the call put the tool in front of the model, as a
// schema or as a name it can load.
func offered(llm *FakeLLM, i int, tool string) bool {
	for _, tl := range llm.Config(i).Tools {
		if tl.Name == tool {
			return true
		}
	}
	return strings.Contains(llm.Prompt(i)+llm.Config(i).SystemPrompt, tool)
}

func TestATakenToolIsGoneFromTheChannelThread(t *testing.T) {
	for _, way := range []string{"off its tool list", "on its disabled list", "scoped to another agent"} {
		df := newDispatchFixture(t, FakeTurn{Content: "ok", Repeat: true})
		prevRoot := RootDB
		RootDB = df.app.DB
		song := TempTool{Name: "generate_song", Description: "Make a song.", CommandTemplate: "echo {lyrics}",
			Params: map[string]ToolParam{"lyrics": {Type: "string", Description: "The words."}}}
		if err := AdminPersistTempTool(df.udb, "u", song); err != nil {
			t.Fatal(err)
		}
		if way == "off its tool list" {
			df.agent.AllowedTools = []string{"generate_song", "calculate"}
			if _, err := saveAgent(df.udb, df.agent); err != nil {
				t.Fatal(err)
			}
		}
		df.run(AgentSyncRun{Kind: "channel", SubSessionID: "room-1", Message: "make a song", MessageSender: "u", SenderIsOwner: true})
		before := df.llm.Calls()
		had := false
		for i := 0; i < before; i++ {
			had = had || offered(df.llm, i, "generate_song")
		}
		if !had {
			t.Fatalf("%s: the agent should have the tool to begin with", way)
		}

		switch way {
		case "off its tool list":
			df.agent.AllowedTools = []string{"calculate"}
		case "on its disabled list":
			df.agent.DisabledPersistentTools = []string{"generate_song"}
		case "scoped to another agent":
			if !SetUserToolScopeAgents(df.udb, "u", "generate_song", []string{"someone-else"}) {
				t.Fatal("could not scope the tool")
			}
		}
		if _, err := saveAgent(df.udb, df.agent); err != nil {
			t.Fatal(err)
		}
		df.run(AgentSyncRun{Kind: "channel", SubSessionID: "room-1", Message: "another song", MessageSender: "u", SenderIsOwner: true})
		for i := before; i < df.llm.Calls(); i++ {
			if offered(df.llm, i, "generate_song") {
				t.Errorf("%s: the next message in the same thread was still offered the tool (call %d)", way, i)
				break
			}
		}
		RootDB = prevRoot
	}
}
