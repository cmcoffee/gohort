package admin

import (
	"encoding/json"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/jsonapi"
	"github.com/cmcoffee/snugforge/kvlite"
)

// A peer is a connector read from its record and the settings that name it:
// what it lends, the instance and its key, what here uses each thing it
// lends and what nothing uses, and where each is served. Its row cannot be
// approved, edited or deleted here; Test asks it again what it lends.
func TestPeersAreConnectors(t *testing.T) {
	savedRoot, savedAuth := RootDB, AuthDB
	RootDB = &DBase{Store: kvlite.MemStore()}
	auth := &DBase{Store: kvlite.MemStore()}
	AuthDB = func() Database { return auth }
	t.Cleanup(func() { RootDB, AuthDB = savedRoot, savedAuth })

	RootDB.Set("remote_peers", "gpu-box", RemotePeer{Name: "gpu-box", BaseURL: "https://gpu.example.test", Instance: "gpu-box",
		Caps:   []string{PeerCapSpeech, PeerCapEmbeddings, PeerCapImages, PeerCapModels},
		Models: []PeerModelInfo{{Tier: "lead", Model: "big-model", Provider: "vllm"}}, EmbedModel: "nomic-embed-text",
		ImageConnectors: []string{"gpu-box-flux"}, LastChecked: time.Now().Format(time.RFC3339)})
	auth.Set(LeadLLMTable, "provider", "peer:gpu-box")
	a := &AdminApp{db: &DBase{Store: kvlite.MemStore()}}
	a.db.Set(EmbeddingTable, "current", EmbeddingConfig{Enabled: true, Provider: "peer:gpu-box"})
	a.db.Set(ImageTable, "provider", "gpu-box-flux")

	pcs := a.peerConnectors()
	if len(pcs) != 1 {
		t.Fatalf("peers: %+v", pcs)
	}
	pc := pcs[0]
	if pc.name != "peer:gpu-box" || pc.wrapper != "Inference, Embeddings, Image generation, Speech ⇐ another gohort, gpu-box at gpu.example.test (its peer key)" {
		t.Errorf("line: %q %q", pc.name, pc.wrapper)
	}
	if want := "In use for the lead LLM; Embeddings; Image Generation, through gpu-box-flux. Lent, not used here: Speech. Checked just now. Set up under Admin > Capabilities > Peers."; pc.status != want {
		t.Errorf("status:\n%s\nwant\n%s", pc.status, want)
	}
	eps := peerEndpoints(pc)
	if len(eps) != 4 || eps[0].Address != "/api/peer/v1/chat/completions" || eps[0].UsedBy != "the lead LLM" ||
		!strings.HasSuffix(eps[0].Sends, "(lead: big-model on vllm)") || eps[1].Reads != "one vector per text, from nomic-embed-text" ||
		eps[3].UsedBy != "lent, not used here" {
		t.Errorf("endpoints: %+v", eps)
	}

	rowsAny, _ := a.handleConnectors(&jsonapi.Call{R: httptest.NewRequest("GET", "/api/connectors", nil)})
	b, _ := json.Marshal(rowsAny)
	var rows []map[string]any
	json.Unmarshal(b, &rows)
	// Rows arrive in their bands' order, so each band is drawn once.
	last := 0
	for _, r := range rows {
		i := slices.Index(connectorGroups, r["group"].(string))
		if i < last {
			t.Errorf("%v is in the band %v, after a later one", r["name"], r["group"])
		}
		last = i
	}
	found := false
	for _, r := range rows {
		if r["name"] == "peer:gpu-box" {
			found = true
			if r["stored"] != false || r["state"] != "peer" || r["testable"] != true || r["endpoints"] != true || r["group"] != groupPeers {
				t.Errorf("a peer's row: %v", r)
			}
		}
	}
	if !found {
		t.Error("the peer has no row")
	}

	// Test asks the peer again, through the same probe as Re-check.
	transcribePeer(t, PeerCapTranscribe)
	if got := peerTest(t.Context(), "peer:gpu-box"); !strings.HasPrefix(got["message"].(string), "Checked: ") || !strings.HasSuffix(got["message"].(string), "lends Transcription.") || got["warnings"] != nil {
		t.Errorf("test: %v", got)
	}
	if got := peerTest(t.Context(), "peer:gone"); got["warnings"] == nil {
		t.Errorf("testing a peer that is not registered: %v", got)
	}
}
