package core

import (
	"encoding/json"
	"testing"
)

// fakeArtifact is a registry-only ArtifactType backed by an in-memory map, so
// closure behavior can be exercised without any store. deps wires up the
// dependency edges; missing names simulate an unresolvable dependency.
type fakeArtifact struct {
	typ     string
	recipes map[string]string        // name -> recipe payload (absent = export fails)
	deps    map[string][]ArtifactSel // name -> declared dependencies
}

func (f *fakeArtifact) ArtifactType() string                 { return f.typ }
func (f *fakeArtifact) ListArtifacts(Database) []ArtifactSel { return nil }

func (f *fakeArtifact) ExportArtifact(_ Database, name, _ string) (json.RawMessage, error) {
	payload, ok := f.recipes[name]
	if !ok {
		return nil, Error("no such " + f.typ + ": " + name)
	}
	return json.Marshal(payload)
}

func (f *fakeArtifact) ImportArtifact(Database, json.RawMessage, string) (string, string, error) {
	return "", "", nil
}

func (f *fakeArtifact) Dependencies(_ Database, name, _ string) []ArtifactSel {
	return f.deps[name]
}

// withFakeTypes swaps the global artifact registry for the duration of a test,
// restoring it afterward so no other test sees the fakes.
func withFakeTypes(t *testing.T, types ...ArtifactType) {
	t.Helper()
	saved := artifactTypes
	artifactTypes = map[string]ArtifactType{}
	for _, at := range types {
		RegisterArtifactType(at)
	}
	t.Cleanup(func() { artifactTypes = saved })
}

func bundleNames(b ArtifactBundle) []string {
	out := make([]string, 0, len(b.Artifacts))
	for _, a := range b.Artifacts {
		out = append(out, a.Type+"/"+a.Name)
	}
	return out
}

func TestExportClosure_PullsTransitiveDeps(t *testing.T) {
	tool := &fakeArtifact{
		typ:     "tool",
		recipes: map[string]string{"weather": "wx"},
		deps:    map[string][]ArtifactSel{"weather": {{Type: "credential", Name: "openweather"}}},
	}
	cred := &fakeArtifact{typ: "credential", recipes: map[string]string{"openweather": "key"}}
	withFakeTypes(t, tool, cred)

	b, err := ExportArtifactBundle(nil, []ArtifactSel{{Type: "tool", Name: "weather", Owner: "u"}})
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	got := bundleNames(b)
	if len(got) != 2 || got[0] != "tool/weather" || got[1] != "credential/openweather" {
		t.Fatalf("expected tool then its credential, got %v", got)
	}
}

func TestExportClosure_TransitiveThreeLevels(t *testing.T) {
	// agent → tool → credential: the chain agent-level closure introduces. One
	// explicit agent selection must pull the tool AND the tool's credential.
	agent := &fakeArtifact{
		typ:     "agent",
		recipes: map[string]string{"scout": "a"},
		deps:    map[string][]ArtifactSel{"scout": {{Type: "tool", Name: "weather", Owner: "u"}}},
	}
	tool := &fakeArtifact{
		typ:     "tool",
		recipes: map[string]string{"weather": "wx"},
		deps:    map[string][]ArtifactSel{"weather": {{Type: "credential", Name: "openweather"}}},
	}
	cred := &fakeArtifact{typ: "credential", recipes: map[string]string{"openweather": "key"}}
	withFakeTypes(t, agent, tool, cred)

	b, err := ExportArtifactBundle(nil, []ArtifactSel{{Type: "agent", Name: "scout", Owner: "u"}})
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	got := bundleNames(b)
	want := []string{"agent/scout", "tool/weather", "credential/openweather"}
	if len(got) != len(want) {
		t.Fatalf("expected %v, got %v", want, got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("expected %v, got %v", want, got)
		}
	}
}

func TestExportClosure_DedupsAndIsIdempotent(t *testing.T) {
	// Two tools sharing one credential must not emit the credential twice.
	tool := &fakeArtifact{
		typ:     "tool",
		recipes: map[string]string{"a": "1", "b": "2"},
		deps: map[string][]ArtifactSel{
			"a": {{Type: "credential", Name: "shared"}},
			"b": {{Type: "credential", Name: "shared"}},
		},
	}
	cred := &fakeArtifact{typ: "credential", recipes: map[string]string{"shared": "k"}}
	withFakeTypes(t, tool, cred)

	b, err := ExportArtifactBundle(nil, []ArtifactSel{
		{Type: "tool", Name: "a", Owner: "u"},
		{Type: "tool", Name: "b", Owner: "u"},
	})
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	seen := 0
	for _, a := range b.Artifacts {
		if a.Type == "credential" && a.Name == "shared" {
			seen++
		}
	}
	if seen != 1 {
		t.Fatalf("shared credential should appear exactly once, saw %d (%v)", seen, bundleNames(b))
	}
}

func TestExportClosure_MissingDepSkippedNotFatal(t *testing.T) {
	// The tool references a credential that doesn't exist. The export must still
	// succeed with the tool; the unresolvable dependency is silently dropped.
	tool := &fakeArtifact{
		typ:     "tool",
		recipes: map[string]string{"t": "1"},
		deps:    map[string][]ArtifactSel{"t": {{Type: "credential", Name: "ghost"}}},
	}
	cred := &fakeArtifact{typ: "credential", recipes: map[string]string{}}
	withFakeTypes(t, tool, cred)

	b, err := ExportArtifactBundle(nil, []ArtifactSel{{Type: "tool", Name: "t", Owner: "u"}})
	if err != nil {
		t.Fatalf("a missing dependency must not fail the export: %v", err)
	}
	if got := bundleNames(b); len(got) != 1 || got[0] != "tool/t" {
		t.Fatalf("expected just the tool, got %v", got)
	}
}

func TestExportClosure_ExplicitTypoStillErrors(t *testing.T) {
	// The dependency tier is best-effort, but an explicit selection typo must
	// still be a hard error.
	withFakeTypes(t, &fakeArtifact{typ: "tool", recipes: map[string]string{}})
	if _, err := ExportArtifactBundle(nil, []ArtifactSel{{Type: "tool", Name: "nope", Owner: "u"}}); err == nil {
		t.Fatal("expected an error for a missing explicit selection")
	}
	if _, err := ExportArtifactBundle(nil, []ArtifactSel{{Type: "bogus", Name: "x"}}); err == nil {
		t.Fatal("expected an error for an unknown explicit type")
	}
}

func TestExportableCredential(t *testing.T) {
	for _, name := range []string{"", "none", "no_auth", "NO_AUTH", " none "} {
		if exportableCredential(name) {
			t.Errorf("%q should not be an exportable credential dependency", name)
		}
	}
	for _, name := range []string{"openweather", "github_api"} {
		if !exportableCredential(name) {
			t.Errorf("%q should be an exportable credential dependency", name)
		}
	}
}

func TestConnectorCredentialRefs(t *testing.T) {
	restPoll := json.RawMessage(`{"credential":"stripe","url":"https://x"}`)
	if got := connectorCredentialRefs(restPoll); len(got) != 1 || got[0] != "stripe" {
		t.Fatalf("rest_poll: expected [stripe], got %v", got)
	}
	mcp := json.RawMessage(`{"auth_mode":"secure_api","secure_cred":"atlassian"}`)
	if got := connectorCredentialRefs(mcp); len(got) != 1 || got[0] != "atlassian" {
		t.Fatalf("mcp: expected [atlassian], got %v", got)
	}
	if got := connectorCredentialRefs(json.RawMessage(`{"kind":"desktop_command"}`)); got != nil {
		t.Fatalf("no-credential spec: expected nil, got %v", got)
	}
}
