package orchestrate

import (
	"sort"
	"strings"
	"testing"

	. "github.com/cmcoffee/oddjob/core"
	"github.com/cmcoffee/oddjob/core/prompts"
	"github.com/cmcoffee/snugforge/kvlite"
)

// Every tool Builder authors with has an editable description, and the list
// names nothing it does not hand out: a new authoring tool is a block from
// the start, and a removed one does not linger as one.
func TestEveryAuthoringToolIsABlock(t *testing.T) {
	root := &DBase{Store: kvlite.MemStore()}
	udb := UserDB(root, "u")
	app := &OrchestrateApp{}
	app.DB = root
	turn := &chatTurn{app: app, user: "u", udb: udb, agent: AgentRecord{ID: "a1", Name: "Wren", Owner: "u"}}
	sess := &ToolSession{DB: udb}
	// Per-credential tools are made from whatever credentials exist, not
	// shipped, and are never blocks.
	perCredential := map[string]bool{}
	for _, td := range Secure().BuildTools(sess) {
		perCredential[td.Tool.Name] = true
	}
	var got []string
	for _, td := range builderAuthoringTools(sess, turn) {
		if perCredential[td.Tool.Name] {
			if prompts.TunableToolGroup(td.Tool.Name) != "" {
				t.Errorf("%s is made from a credential, and must not be a block", td.Tool.Name)
			}
			continue
		}
		got = append(got, td.Tool.Name)
		if prompts.TunableToolGroup(td.Tool.Name) != prompts.ToolGroupAuthoring {
			t.Errorf("%s is an authoring tool without an editable description: add it to authoringToolNames", td.Tool.Name)
		}
	}
	want := append([]string{}, authoringToolNames...)
	sort.Strings(got)
	sort.Strings(want)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("authoringToolNames lists %v; builderAuthoringTools hands out %v", want, got)
	}
}
