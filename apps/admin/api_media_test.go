package admin

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/jsonapi"
	"github.com/cmcoffee/gohort/core/ui"
	"github.com/cmcoffee/snugforge/kvlite"
)

// A speech or transcription setting may name a service connector, but only an
// approved one that provides it; the settings offer exactly those.
func TestSpeechSettingsTakeAServiceThatSpeaks(t *testing.T) {
	saved := RootDB
	RootDB = &DBase{Store: kvlite.MemStore()}
	t.Cleanup(func() { RootDB = saved })
	spec := `{"provider":{"transport":"http","base_url":"https://api.example.test","auth":{"credential":"k"},
		"operations":{"speak":{"method":"POST","path":"/v1/speak","body":{"text":"{text}"},"read":"file"}}},
		"provides":[{"capability":"speech.synthesize","steps":[{"call":"speak"}],"output":{"audio":{"from":"body"}}}]}`
	SaveConnector(RootDB, Connector{Name: "voice", Kind: "service", Spec: json.RawMessage(spec)})
	if err := serviceProviderOK("service:voice", "speech.synthesize"); err == nil || !strings.Contains(err.Error(), "not approved") {
		t.Errorf("an unapproved service: %v", err)
	}
	ApproveConnector(RootDB, "voice")
	if err := serviceProviderOK("service:voice", "speech.synthesize"); err != nil {
		t.Errorf("an approved service that speaks: %v", err)
	}
	if err := serviceProviderOK("service:voice", "speech.transcribe"); err == nil {
		t.Error("a service that only speaks was taken for transcription")
	}
	if err := serviceProviderOK("service:nothing", "speech.synthesize"); err == nil {
		t.Error("a missing connector was taken")
	}
	if err := serviceProviderOK("local", "speech.synthesize"); err != nil {
		t.Errorf("this instance: %v", err)
	}
	opts := serviceProviderOptions("speech.synthesize", "Speak")
	if len(opts) != 1 || opts[0].Value != "service:voice" {
		t.Errorf("speech choices: %+v", opts)
	}
	if len(serviceProviderOptions("speech.transcribe", "Transcribe")) != 0 {
		t.Error("a service that only speaks is offered for transcription")
	}
}

// Embeddings may be made by an approved service that provides them. Picking
// one stores the service as the endpoint and the model its spec declares,
// whatever model the hidden local field still held, so the stored config
// names the space its vectors are in.
func TestEmbeddingSettingsTakeAServiceThatEmbeds(t *testing.T) {
	saved := RootDB
	RootDB = &DBase{Store: kvlite.MemStore()}
	savedCfg := GetEmbeddingConfig()
	t.Cleanup(func() { RootDB = saved; SetEmbeddingConfig(savedCfg) })
	spec := `{"provider":{"transport":"http","base_url":"https://api.example.test","auth":{"credential":"k"},
		"operations":{"embed":{"method":"POST","path":"/v1/embeddings","body":{"model":"{model}","input":"{texts}"}}}},
		"provides":[{"capability":"embed.text","steps":[{"call":"embed"}],"output":{"vectors":{"path":"data","each":"embedding"}},
		"defaults":{"model":"embed-small"}}]}`
	SaveConnector(RootDB, Connector{Name: "vec", Kind: "service", Spec: json.RawMessage(spec)})
	req := EmbeddingConfig{Enabled: true, Provider: "service:vec", Endpoint: "http://localhost:11434/api", Model: "nomic-embed-text", APIKey: "x"}
	if _, err := resolveEmbedding(req); err == nil || !strings.Contains(err.Error(), "not approved") {
		t.Errorf("an unapproved service: %v", err)
	}
	ApproveConnector(RootDB, "vec")
	got, err := resolveEmbedding(req)
	if err != nil || got.Endpoint != "service:vec" || got.Model != "embed-small" || got.APIKey != "" {
		t.Errorf("resolved: %+v %v", got, err)
	}
	if _, err := resolveEmbedding(EmbeddingConfig{Enabled: true, Provider: "service:nothing"}); err == nil {
		t.Error("a missing connector was taken")
	}
	found := false
	for _, f := range embeddingFormFields() {
		if f.Field != "provider" {
			continue
		}
		for _, o := range f.Options {
			found = found || o.Value == "service:vec"
		}
	}
	if !found {
		t.Error("the Embed on choices do not offer the service")
	}
}

// While this instance is the only thing that can embed, transcribe or speak,
// each page says where to get something else instead of offering a choice of
// one; once a connector is approved, the choice replaces the pointer.
func TestCapabilityPagesPointToAnAlternative(t *testing.T) {
	saved := RootDB
	RootDB = &DBase{Store: kvlite.MemStore()}
	t.Cleanup(func() { RootDB = saved })
	read := func(fields []ui.FormField) (help string, choice bool) {
		for _, f := range fields {
			if f.Field == "enabled" {
				help = f.Help
			}
			choice = choice || f.Field == "provider"
		}
		return
	}
	for name, fields := range map[string]func() []ui.FormField{"embeddings": embeddingFormFields, "transcription": transcribeFormFields, "speech": speechFormFields} {
		if help, choice := read(fields()); choice || !strings.Contains(help, "Admin > Extensions > Templates") || !strings.Contains(help, "Admin > Capabilities > Peers") {
			t.Errorf("%s with nothing else: %q, choice %v", name, help, choice)
		}
	}
	spec := `{"provider":{"transport":"http","base_url":"https://api.example.test","auth":{"none":true},
		"operations":{"embed":{"method":"POST","path":"/v1/embeddings","body":{"input":"{texts}"}}}},
		"provides":[{"capability":"embed.text","steps":[{"call":"embed"}],"output":{"vectors":{"path":"data","each":"embedding"}}}]}`
	SaveConnector(RootDB, Connector{Name: "vectors", Kind: "service", Spec: json.RawMessage(spec)})
	ApproveConnector(RootDB, "vectors")
	if help, choice := read(embeddingFormFields()); !choice || help != "" {
		t.Errorf("embeddings with a connector to choose: %q, choice %v", help, choice)
	}
	if _, choice := read(speechFormFields()); choice {
		t.Error("a connector that embeds made speech a choice")
	}
}

// Web Search offers an approved connector that searches as a provider, and
// refuses to save one that is not approved.
func TestWebSearchTakesAConnectorThatSearches(t *testing.T) {
	saved := RootDB
	RootDB = &DBase{Store: kvlite.MemStore()}
	t.Cleanup(func() { RootDB = saved })
	spec := `{"provider":{"transport":"http","base_url":"https://search.example.test","auth":{"none":true},
		"operations":{"search":{"url":"/find?q={query}"}}},
		"provides":[{"capability":"web.search","steps":[{"call":"search"}],"output":{"results":{"path":"hits","fields":{"title":"name","url":"href"}}}}]}`
	SaveConnector(RootDB, Connector{Name: "finder", Kind: "service", Spec: json.RawMessage(spec)})
	offered := func() bool {
		for _, f := range webSearchFormFields() {
			for _, o := range f.Options {
				if f.Field == "provider" && o.Value == "service:finder" {
					return true
				}
			}
		}
		return false
	}
	a := &AdminApp{db: &DBase{Store: kvlite.MemStore()}}
	if offered() {
		t.Error("an unapproved connector is offered")
	}
	if _, err := a.handleWebSearchSave(&jsonapi.Call{R: httptest.NewRequest("POST", "/api/web-search", nil)}, WebSearchConfig{Provider: "service:finder"}); err == nil || !strings.Contains(err.Error(), "not approved") {
		t.Errorf("saving an unapproved connector: %v", err)
	}
	ApproveConnector(RootDB, "finder")
	if !offered() {
		t.Error("an approved connector that searches is not offered")
	}
	if _, err := a.handleWebSearchSave(&jsonapi.Call{R: httptest.NewRequest("POST", "/api/web-search", nil)}, WebSearchConfig{Provider: "service:finder"}); err != nil {
		t.Errorf("saving it: %v", err)
	}
	if got := a.storedString(SearchTable, "provider"); got != "service:finder" {
		t.Errorf("stored provider %q", got)
	}
}

// Page Rendering offers an approved connector that renders pages, refuses to
// save one that is not approved, and routes browsing to the one saved.
func TestPageRenderingTakesAConnectorThatRenders(t *testing.T) {
	saved, prevCfg := RootDB, LoadBrowseConfig()
	RootDB = &DBase{Store: kvlite.MemStore()}
	t.Cleanup(func() { RootDB = saved; SetBrowseConfig(prevCfg) })
	spec := `{"provider":{"transport":"http","base_url":"https://reader.example.test","auth":{"none":true},
		"operations":{"read":{"method":"POST","path":"/","body":{"url":"{url}"}}}},
		"provides":[{"capability":"page.render","steps":[{"call":"read"}],"output":{"text":{"path":"data.content"}}}]}`
	SaveConnector(RootDB, Connector{Name: "reader", Kind: "service", Spec: json.RawMessage(spec)})
	offered := func() bool {
		for _, o := range browseFormFields()[0].Options {
			if o.Value == "service:reader" {
				return true
			}
		}
		return false
	}
	a := &AdminApp{db: &DBase{Store: kvlite.MemStore()}}
	call := &jsonapi.Call{R: httptest.NewRequest("POST", "/api/browse", nil)}
	if offered() {
		t.Error("an unapproved connector is offered")
	}
	if _, err := a.handleBrowseSave(call, BrowseConfig{Source: "service:reader"}); err == nil || !strings.Contains(err.Error(), "not approved") {
		t.Errorf("saving an unapproved connector: %v", err)
	}
	ApproveConnector(RootDB, "reader")
	if !offered() {
		t.Error("an approved connector that renders is not offered")
	}
	if _, err := a.handleBrowseSave(call, BrowseConfig{Source: "service:reader"}); err != nil || LoadBrowseConfig().Source != "service:reader" {
		t.Errorf("saving it: %v %+v", err, LoadBrowseConfig())
	}
}

// Mail offers an approved connector that sends mail beside SMTP, keeping the
// SMTP fields to SMTP, and points to Templates while SMTP is all there is.
// An unapproved connector is refused; the one saved is what sends.
func TestMailTakesAConnectorThatSends(t *testing.T) {
	saved := RootDB
	RootDB = &DBase{Store: kvlite.MemStore()}
	t.Cleanup(func() { RootDB = saved })
	field := func(name string) (ui.FormField, bool) {
		for _, f := range mailFormFields() {
			if f.Field == name {
				return f, true
			}
		}
		return ui.FormField{}, false
	}
	if _, ok := field("provider"); ok {
		t.Error("a choice of one: SMTP alone")
	}
	if f, _ := field("server"); f.ShowWhen != "" || !strings.Contains(f.Help, "Admin > Extensions > Templates") {
		t.Errorf("SMTP alone: %+v", f)
	}
	spec := `{"provider":{"transport":"http","base_url":"https://mail.example.test","auth":{"none":true},
		"operations":{"send":{"method":"POST","path":"/emails","body":{"to":"{to}","subject":"{subject}","text":"{body}"}}}},
		"provides":[{"capability":"mail.send","steps":[{"call":"send"}],"output":{"id":{"path":"id"}}}]}`
	SaveConnector(RootDB, Connector{Name: "postbox", Kind: "service", Spec: json.RawMessage(spec)})
	a := &AdminApp{db: &DBase{Store: kvlite.MemStore()}}
	call := &jsonapi.Call{R: httptest.NewRequest("POST", "/api/mail", nil)}
	if _, err := a.handleMailSave(call, MailConfig{Provider: "service:postbox"}); err == nil || !strings.Contains(err.Error(), "not approved") {
		t.Errorf("saving an unapproved connector: %v", err)
	}
	ApproveConnector(RootDB, "postbox")
	if f, ok := field("provider"); !ok || len(f.Options) != 2 || f.Options[1].Value != "service:postbox" {
		t.Errorf("the choice: %+v", f)
	}
	if f, _ := field("server"); f.ShowWhen != "provider:smtp" || f.Help != "" {
		t.Errorf("SMTP's server with a choice: %+v", f)
	}
	if f, _ := field("from"); f.ShowWhen != "" {
		t.Error("From is hidden, though a connector uses it too")
	}
	got, _ := a.handleMail(call)
	if got.(MailConfig).Provider != "smtp" {
		t.Errorf("nothing saved reads as SMTP: %+v", got)
	}
	if _, err := a.handleMailSave(call, MailConfig{Provider: "service:postbox", From: "alerts@mail.example"}); err != nil {
		t.Fatal(err)
	}
	if got, _ := a.handleMail(call); got.(MailConfig).Provider != "service:postbox" {
		t.Errorf("saved: %+v", got)
	}
}

// The LLM tiers offer an approved model connector as a provider, and refuse
// to save one that is not approved.
func TestLLMTiersTakeAModelConnector(t *testing.T) {
	saved := RootDB
	RootDB = &DBase{Store: kvlite.MemStore()}
	t.Cleanup(func() { RootDB = saved })
	spec := `{"provider":{"transport":"http","base_url":"https://llm.example.test/v1","auth":{"none":true},
		"operations":{"models":{"path":"/models"}}},
		"provides":[{"capability":"llm.chat","dialect":"openai","defaults":{"model":"fast-model"}}]}`
	SaveConnector(RootDB, Connector{Name: "hosted", Kind: "service", Spec: json.RawMessage(spec)})
	offered := func() bool {
		for _, o := range LLMProviderOptions(false) {
			if o.Value == "service:hosted" {
				return true
			}
		}
		return false
	}
	a := &AdminApp{db: &DBase{Store: kvlite.MemStore()}}
	call := &jsonapi.Call{R: httptest.NewRequest("POST", "/api/worker-llm", nil)}
	if offered() {
		t.Error("an unapproved model connector is offered")
	}
	if _, err := a.handleLLMConfigSave(LLMTable, true)(call, llmConfigSave{Provider: "service:hosted"}); err == nil || !strings.Contains(err.Error(), "not approved") {
		t.Errorf("saving an unapproved one: %v", err)
	}
	ApproveConnector(RootDB, "hosted")
	if !offered() {
		t.Error("an approved model connector is not offered")
	}
}
