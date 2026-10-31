package admin

import (
	"encoding/json"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
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
