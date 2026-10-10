package ollama_proxy

import (
	"strings"
	"testing"
)

// A client configured before the rename asks for the old virtual model, with
// or without a tag; the request reads as one for the current name.
func TestTheOldVirtualModelNameIsStillServed(t *testing.T) {
	for in, want := range map[string]string{
		`{"model":"gohort","prompt":"hi"}`:        `"model":"oddjob"`,
		`{"model":"gohort:latest","prompt":"hi"}`: `"model":"oddjob:latest"`,
		`{"model":"llama3","prompt":"hi"}`:        `"model":"llama3"`,
	} {
		if got := string(normalizeLegacyModel([]byte(in))); !strings.Contains(got, want) {
			t.Errorf("%s -> %s, want %s", in, got, want)
		}
	}
	if _, ok := holdToLentModel(normalizeLegacyModel([]byte(`{"model":"gohort"}`)), "qwen"); !ok {
		t.Error("the old name should pass the lent-model check after normalising")
	}
}
