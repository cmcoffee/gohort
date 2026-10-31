package admin

import (
	"encoding/json"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
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
