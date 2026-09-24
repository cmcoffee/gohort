package orchestrate

// An agent's memory travels only on request, carries only the owner's own
// facts, and attaches to the agent of that name, and only to one with no
// memory yet.

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

func TestAgentMemoryTravelsOnRequestAndAttachesByName(t *testing.T) {
	T, udb, user := newTestOrchestrate(t)
	pinRootDB(t)
	savedVec := VectorDB
	VectorDB = &DBase{Store: kvlite.MemStore()}
	InvalidateChunkCache()
	t.Cleanup(func() { VectorDB = savedVec; InvalidateChunkCache() })
	RegisterAgentArtifactType(T)
	RegisterAgentMemoryArtifactType(T)
	mem := &agentMemoryArtifact{app: T}

	a, err := saveAgent(udb, AgentRecord{Owner: user, Name: "Helper", OrchestratorPrompt: "help"})
	if err != nil {
		t.Fatal(err)
	}
	ns := factsNamespace(a.ID)
	StoreMemoryFactP(udb, ns, "prefers metric units", FactWritePolicy{Source: MemSourceImported})
	SaveOperatingNotes(udb, ns, "check the calendar before booking")
	// A fact a channel contact stated: theirs, not the owner's.
	var other MemoryFact
	other.Namespace, other.ID, other.Note, other.Created = ns, "contact-1", "my birthday is in May", time.Now()
	other.Speaker, other.SpeakerHandle = "Contact", "+15550100"
	udb.Set(MemoryFactsTable, ns+"/contact-1", other)
	// One derived finding, one upload.
	prefix := agentKnowledgePrefix(user, a.ID)
	VectorDB.Set(EmbeddedChunks, "k1", EmbeddedChunk{ID: "k1", Source: prefix + ":travel", ReportID: "orch-know-x-1", Title: "Trains", Text: "Night trains save a hotel night.", Ord: 1})
	VectorDB.Set(EmbeddedChunks, "u1", EmbeddedChunk{ID: "u1", Source: prefix, ReportID: "orch-upload-y", Text: "passport scan"})
	InvalidateChunkCache()

	raw, err := mem.ExportArtifact(nil, a.ID, user)
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)
	for _, want := range []string{"prefers metric units", "check the calendar", "Night trains"} {
		if !strings.Contains(body, want) {
			t.Errorf("the export is missing %q", want)
		}
	}
	for _, not := range []string{"birthday", "passport"} {
		if strings.Contains(body, not) {
			t.Errorf("the export carries %q, which should stay home", not)
		}
	}

	// Not asked for: not in the agent's bundle. Asked for: there.
	b, _ := ExportArtifactBundleAsUser(RootDB, user, []ArtifactSel{{Type: "agent", Name: a.ID}}, UserExportOptions{IncludeDeps: true})
	for _, x := range b.Artifacts {
		if x.Type == "agent_memory" {
			t.Fatal("memory travelled without being asked for")
		}
	}
	b, _ = ExportArtifactBundleAsUser(RootDB, user, []ArtifactSel{{Type: "agent", Name: a.ID}},
		UserExportOptions{IncludeDeps: true, DepTypes: []string{"agent_memory"}})
	found := false
	for _, x := range b.Artifacts {
		found = found || x.Type == "agent_memory"
	}
	if !found {
		t.Fatal("memory did not travel when asked for")
	}

	// A fresh user imports the bundle: the agent lands, then its memory.
	adb := AuthDB()
	adb.Set(AuthTable, "user:bob", AuthUser{Username: "bob"})
	data, _ := json.Marshal(b)
	res, err := ImportArtifactBundleAsUser(RootDB, data, "bob")
	if err != nil || res.Imported != 2 {
		t.Fatalf("import: %v %+v", err, res.Outcomes)
	}
	budb := UserDB(T.DB, "bob")
	nb, ok := topAgent(budb, "bob", "Helper")
	if !ok {
		t.Fatal("the agent did not land")
	}
	if got := LoadOperatingNotes(budb, factsNamespace(nb.ID)).Text; got != "check the calendar before booking" {
		t.Errorf("notes: %q", got)
	}
	deadline := time.Now().Add(5 * time.Second)
	for len(ListMemoryFacts(budb, factsNamespace(nb.ID))) == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if len(ListMemoryFacts(budb, factsNamespace(nb.ID))) != 1 {
		t.Error("the owner's fact did not land on the imported agent")
	}

	// A second import cannot graft onto an agent that already remembers.
	res, _ = ImportArtifactBundleAsUser(RootDB, data, "bob")
	for _, o := range res.Outcomes {
		if o.Type == "agent_memory" && o.Status != "skipped" {
			t.Errorf("memory merged into an agent that already had some: %+v", o)
		}
	}
}
