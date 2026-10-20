package orchestrate

// A tool taken away from an agent cannot be loaded back by name. The agent's
// catalog left it out, but load_tool fell through to the whole pool for a
// name it did not know, so an agent that remembered the tool asked for it
// and ran it anyway.

import (
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

func TestATakenToolCannotBeLoadedBackByName(t *testing.T) {
	for _, way := range []string{"off its tool list", "on its disabled list", "scoped to another agent"} {
		df := newDispatchFixture(t,
			FakeTurn{ToolCalls: []ToolCall{{ID: "c1", Name: "load_tool", Args: map[string]any{"names": []any{"generate_song"}}}}},
			FakeTurn{ToolCalls: []ToolCall{{ID: "c2", Name: "generate_song", Args: map[string]any{"lyrics": "ran-the-song"}}}},
			FakeTurn{Content: "ok", Repeat: true})
		prevRoot := RootDB
		RootDB = df.app.DB
		song := TempTool{Name: "generate_song", Description: "Make a song.", CommandTemplate: "echo {lyrics}",
			Params: map[string]ToolParam{"lyrics": {Type: "string", Description: "The words."}}}
		if err := AdminPersistTempTool(df.udb, "u", song); err != nil {
			t.Fatal(err)
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
		df.run(AgentSyncRun{Kind: "channel", SubSessionID: "room-1", Message: "make a song", MessageSender: "u", SenderIsOwner: true})

		var loadResult, callResult string
		for i := 0; i < df.llm.Calls(); i++ {
			for _, m := range df.llm.Sent(i) {
				for _, r := range m.ToolResults {
					switch r.ID {
					case "c1":
						loadResult = r.Content
					case "c2":
						callResult = r.Content
					}
				}
			}
		}
		if strings.Contains(loadResult, "Loaded generate_song") || !strings.Contains(loadResult, "Not on your tool list") {
			t.Errorf("%s: load_tool should refuse the tool and say why: %q", way, loadResult)
		}
		if strings.Contains(callResult, "ran-the-song") {
			t.Errorf("%s: the taken tool ran: %q", way, callResult)
		}
		RootDB = prevRoot
	}
}
