package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/cmcoffee/gohort/apps/orchestrate"
	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/service"
	"github.com/cmcoffee/snugforge/kvlite"
)

// The report gives what an instance keeps as the values to type back in,
// read from records stored as gohort stores them today: each tier's provider
// and the connection it names with its settings, the embeddings space marked
// to keep exactly, the searches, the image backend and its spec, mail, and
// the agents naming a connection, a provided one marked as going. A key is
// only ever said to be kept.
func TestTheConfigReportGivesTheValuesBackNeverAKey(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}
	savedRoot, savedAuth := RootDB, AuthDB
	RootDB, AuthDB = db, func() Database { return db }
	t.Cleanup(func() { RootDB, AuthDB = savedRoot, savedAuth })

	keyed := []byte(`{"config":[{"name":"api_key","type":"secret"}]}`)
	put := func(c Connector, settings map[string]string, key string) {
		t.Helper()
		db.Set("connectors", c.Name, c)
		if settings != nil {
			if err := SetConnectionConfig(db, c, settings); err != nil {
				t.Fatal(err)
			}
		}
		if key != "" {
			if err := SetConnectionSecret(db, c, "api_key", key, []string{"https://api.example"}); err != nil {
				t.Fatal(err)
			}
		}
	}
	put(Connector{Name: "anthropic", ID: "id-a", Kind: "service", Provided: true, Approved: true, Spec: keyed},
		map[string]string{"model": "model-large"}, "sk-SECRETVALUE-anthropic-0123")
	put(Connector{Name: "local-embeddings", ID: "id-e", Kind: "service", Provided: true, Approved: true, Spec: []byte(`{}`)},
		map[string]string{"base_url": "http://embed.lan:8080"}, "")
	put(Connector{Name: "serper-search", ID: "id-s", Kind: "service", DisplayName: "Serper", Template: "serper-search", Approved: true, Spec: keyed},
		nil, "SECRETVALUE-serper-key-4567")
	put(Connector{Name: "pubmed", ID: "id-p", Kind: "service", Approved: true, Spec: []byte(`{}`)}, nil, "")
	put(Connector{Name: "studio", ID: "id-c", Kind: "service", Template: "comfyui", Approved: true,
		Spec: []byte(`{"provider":{"operations":{"render":{"builtin":"comfyui","options":{"workflow":"{\"9\":{}}"}}}}}`)}, nil, "")

	db.Set(LLMTable, "provider", "llm:anthropic")
	db.Set(LLMTable, "context_size", 200000)
	db.Set(LeadLLMTable, "provider", "llm:anthropic")
	db.Set(EmbeddingTable, "current", EmbeddingConfig{Enabled: true, Provider: "embed:local-embeddings", Endpoint: "embed:local-embeddings", Model: "embed-small"})
	db.Set(SearchTable, "provider", "search:serper-search")
	db.Set(service.ResearchSourcesTable, "pubmed", service.ResearchSource{Connector: "pubmed", Topics: []string{"medical"}, ToolName: "medical_search"})
	db.Set(ImageTable, "provider", "studio")
	db.Set(ImageTable, "agents", []string{"studio"})
	db.Set(MailTable, "provider", "smtp")
	db.Set(MailTable, "server", "mail.example:587")
	db.Set(MailTable, "password", "SECRETVALUE-mail")

	db.Set(AuthTable, "user:alice", AuthUser{Username: "alice"})
	base := db.Bucket("orchestrate")
	UserDB(base, "alice").Set("orchestrate_agents", "a1", orchestrate.AgentRecord{ID: "a1", Owner: "alice", Name: "Illustrator",
		AllowedTools: []string{"generate_image_studio", "medical_search", "web_search"}, DisabledCredentials: []string{"connection:anthropic"}})
	base.Set("orchestrate_agents", "a2", orchestrate.AgentRecord{ID: "a2", Name: "Plain", AllowedTools: []string{"web_search"}})

	var out bytes.Buffer
	writeConfigReport(db, &out)
	got := out.String()
	for _, want := range []string{
		"== LLM: worker tier ==\n  provider: llm:anthropic\n    provider is the connection: anthropic: service, provided by gohort, and gone after the rework",
		"      model: model-large",
		"      api_key: a key is kept, given ",
		"  context_size: 200000",
		"== LLM: lead tier ==\n  provider: llm:anthropic",
		"  endpoint: embed:local-embeddings   <- keep this exactly",
		"    so: add its connection back under the name local-embeddings, or every collection is embedded again",
		"== Music ==\n  nothing set\n",
		"== Transcription ==\n  nothing set\n",
		"      base_url: http://embed.lan:8080",
		"  model: embed-small",
		"provider is the connection: Serper (serper-search): service, from template serper-search",
		"  pubmed: on medical; agents' tool medical_search",
		"  default: studio",
		"  server: mail.example:587",
		"  password: a key is kept",
		"  Illustrator [alice]: tool generate_image_studio; tool medical_search; kept from anthropic (gone after the rework)",
		`"builtin": "comfyui"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the report lacks %q", want)
		}
	}
	if strings.Contains(got, "SECRETVALUE") {
		t.Errorf("the report shows a key:\n%s", got)
	}
	if strings.Contains(got, "Plain") {
		t.Errorf("an agent naming no connection is listed")
	}
}

// The report reads only a copy, taken while gohort is not running: a database
// a running gohort holds is refused, and an idle one is copied whole.
func TestTheConfigReportCopiesOnlyAStoppedDatabase(t *testing.T) {
	dir := t.TempDir()
	src, dst := dir+"/gohort.db", dir+"/copy.db"
	held, err := kvlite.Open(src)
	if err != nil {
		t.Fatal(err)
	}
	held.Set("t", "k", "v")
	if err := copyStoppedDB(src, dst); err == nil || !strings.Contains(err.Error(), "stop gohort") {
		t.Errorf("a database in use: %v", err)
	}
	held.Close()
	if err := copyStoppedDB(src, dst); err != nil {
		t.Fatal(err)
	}
	copied, err := kvlite.Open(dst)
	if err != nil {
		t.Fatal(err)
	}
	defer copied.Close()
	var v string
	if found, _ := copied.Get("t", "k", &v); !found || v != "v" {
		t.Errorf("the copy holds %q", v)
	}
}
