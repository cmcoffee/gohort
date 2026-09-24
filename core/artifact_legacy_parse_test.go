package core

// Old files and form uploads reach the bundle importer too: a type's bare
// recipe (from a per-app Export button) is recognized by the type itself, a
// form's {"recipe": "<file text>"} wrapper is unwrapped, and an ordinary
// user's import takes only the types they may import.

import (
	"encoding/json"
	"strings"
	"testing"
)

// sniffingFake is a fake type whose bare recipe is any object with "stages".
type sniffingFake struct{ fakeArtifact }

func (*sniffingFake) SniffsRecipe(fields map[string]json.RawMessage) bool {
	_, ok := fields["stages"]
	return ok
}
func (*sniffingFake) UserImportable() bool { return true }

func TestABareRecipeIsLiftedByTheTypeThatClaimsIt(t *testing.T) {
	withFakeTypes(t, &sniffingFake{fakeArtifact{typ: "pipeline"}}, &fakeArtifact{typ: "connector"})

	b, err := ParseArtifactBundle([]byte(`{"name": "Nightly", "stages": []}`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(b.Artifacts) != 1 || b.Artifacts[0].Type != "pipeline" || b.Artifacts[0].Name != "Nightly" {
		t.Fatalf("a bare pipeline recipe should lift to one pipeline artifact, got %+v", b.Artifacts)
	}

	// Nothing claims it: the legacy connector reading still applies.
	b, err = ParseArtifactBundle([]byte(`{"name": "hook", "kind": "rest_poll"}`))
	if err != nil || len(b.Artifacts) != 1 || b.Artifacts[0].Type != "connector" {
		t.Fatalf("an unclaimed object should still read as a legacy connector: %+v %v", b.Artifacts, err)
	}
}

func TestAFormUploadWrapperIsUnwrapped(t *testing.T) {
	withFakeTypes(t, &sniffingFake{fakeArtifact{typ: "pipeline"}})
	inner := `{"name": "Nightly", "stages": []}`
	for _, key := range []string{"recipe", "pack"} {
		wrapped, _ := json.Marshal(map[string]string{key: inner})
		b, err := ParseArtifactBundle(wrapped)
		if err != nil || len(b.Artifacts) != 1 || b.Artifacts[0].Type != "pipeline" {
			t.Errorf("%s wrapper: %+v %v", key, b.Artifacts, err)
		}
		if got := string(UnwrapArtifactUpload(wrapped)); got != inner {
			t.Errorf("%s unwrap = %q", key, got)
		}
	}
	// A bare artifact's {"type", "recipe": {...}} is not a wrapper.
	bare := []byte(`{"type": "pipeline", "recipe": {"name": "X", "stages": []}}`)
	if string(UnwrapArtifactUpload(bare)) != string(bare) {
		t.Error("a bare artifact must not be unwrapped")
	}
}

func TestIsArtifactEnvelope(t *testing.T) {
	yes := []string{
		`{"bundle": "` + ArtifactBundleFormat + `", "artifacts": []}`,
		`{"artifacts": [{"type": "x", "recipe": {}}]}`,
		`{"type": "pipeline", "recipe": {"name": "X"}}`,
		`{"recipe": "{\"bundle\": \"` + ArtifactBundleFormat + `\", \"artifacts\": []}"}`,
	}
	no := []string{
		`{"name": "X", "stages": []}`,
		`{"recipe": "{\"name\": \"X\", \"stages\": []}"}`,
		`[]`, ``, `not json`,
	}
	for _, s := range yes {
		if !IsArtifactEnvelope([]byte(s)) {
			t.Errorf("should be an envelope: %s", s)
		}
	}
	for _, s := range no {
		if IsArtifactEnvelope([]byte(s)) {
			t.Errorf("should not be an envelope: %s", s)
		}
	}
}

// An ordinary user's bundle import takes their own kinds of artifact and
// reports the rest as needing an administrator, both in the import and in its
// preview.
func TestAUserImportTakesOnlyUserImportableTypes(t *testing.T) {
	userType := &sniffingFake{fakeArtifact{typ: "pipeline"}}
	adminType := &fakeArtifact{typ: "credential"}
	withFakeTypes(t, userType, adminType)
	bundle := []byte(`{"bundle": "` + ArtifactBundleFormat + `", "artifacts": [
		{"type": "pipeline", "name": "p", "recipe": "p"},
		{"type": "credential", "name": "c", "recipe": "c"}]}`)

	pv, err := PreviewArtifactBundleAsUser(nil, bundle, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if pv.WouldImport != 1 || pv.WouldSkip != 1 || pv.Items[1].Detail != adminOnlyArtifactDetail {
		t.Fatalf("preview: %+v", pv)
	}
	res, err := ImportArtifactBundleAsUser(nil, bundle, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if res.Imported != 1 || res.Skipped != 1 {
		t.Fatalf("import: %+v", res)
	}
	if _, ok := adminType.recipes["c"]; ok {
		t.Error("an admin-only type was imported by a user")
	}
	if _, ok := userType.recipes["p"]; !ok {
		t.Error("the user's own type did not import")
	}
	// The admin importer takes both.
	adminType.recipes = nil
	userType.recipes = nil
	if res, _ := ImportArtifactBundle(nil, bundle, "root"); res.Imported != 2 {
		t.Errorf("admin import: %+v", res)
	}
}

// A user's export is their own artifacts and nothing deployment-wide: an admin
// kind is refused outright, every selector resolves in the user's namespace
// whatever owner it named, and the dependency closure leaves out
// deployment-wide dependencies.
func TestAUserExportStaysInsideTheUsersOwnNamespace(t *testing.T) {
	agentLike := &ownerFake{sniffingFake: sniffingFake{fakeArtifact{typ: "pipeline"}}}
	agentLike.byOwner = map[string]map[string]bool{"alice": {"mine": true, "helper": true}, "bob": {"theirs": true}}
	agentLike.deps = map[string][]ArtifactSel{
		"mine": {{Type: "credential", Name: "crm"}, {Type: "pipeline", Name: "helper", Owner: "bob"}},
	}
	cred := &fakeArtifact{typ: "credential", recipes: map[string]string{"crm": "crm"}}
	withFakeTypes(t, agentLike, cred)

	if _, err := ExportArtifactBundleAsUser(nil, "alice", []ArtifactSel{{Type: "credential", Name: "crm"}}, UserExportOptions{IncludeDeps: true}); err == nil {
		t.Error("a user exported an administrator's kind of artifact")
	}
	// Named with bob as owner: still resolves as alice's, and alice has no
	// "theirs", so the export fails rather than reading bob's store.
	if _, err := ExportArtifactBundleAsUser(nil, "alice", []ArtifactSel{{Type: "pipeline", Name: "theirs", Owner: "bob"}}, UserExportOptions{IncludeDeps: true}); err == nil {
		t.Error("a user exported another user's artifact by naming its owner")
	}
	b, err := ExportArtifactBundleAsUser(nil, "alice", []ArtifactSel{{Type: "pipeline", Name: "mine"}}, UserExportOptions{IncludeDeps: true})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, a := range b.Artifacts {
		got = append(got, a.Type+":"+a.Name)
	}
	// The pipeline dependency was addressed to bob; it resolves in ALICE'S
	// namespace, where she has her own "helper". The credential stays out.
	if len(got) != 2 || got[0] != "pipeline:mine" || got[1] != "pipeline:helper" {
		t.Fatalf("got %v", got)
	}
	for _, o := range agentLike.resolvedFor {
		if o != "alice" {
			t.Fatalf("an export read %q's store", o)
		}
	}
}

// ownerFake resolves names per owner and records whose store each export read.
type ownerFake struct {
	sniffingFake
	byOwner     map[string]map[string]bool
	resolvedFor []string
}

func (o *ownerFake) ExportArtifact(_ Database, name, owner string) (json.RawMessage, error) {
	o.resolvedFor = append(o.resolvedFor, owner)
	if !o.byOwner[owner][name] {
		return nil, Error("no such pipeline")
	}
	return json.Marshal(name)
}

func (o *ownerFake) Dependencies(_ Database, name, _ string) []ArtifactSel { return o.deps[name] }

// The export dialog offers what an item depends on, by kind, and the ticks it
// sends back narrow the closure to those kinds.
func TestTheExportPlanAndTheKindsTheDialogTicks(t *testing.T) {
	pipes := &ownerFake{sniffingFake: sniffingFake{fakeArtifact{typ: "pipeline"}}}
	pipes.byOwner = map[string]map[string]bool{"alice": {"main": true}}
	pipes.deps = map[string][]ArtifactSel{"main": {{Type: "skill", Name: "triage"}, {Type: "machine", Name: "intake"}}}
	skills := &ownerFake{sniffingFake: sniffingFake{fakeArtifact{typ: "skill"}}}
	skills.byOwner = map[string]map[string]bool{"alice": {"triage": true}}
	machines := &ownerFake{sniffingFake: sniffingFake{fakeArtifact{typ: "machine"}}}
	machines.byOwner = map[string]map[string]bool{"alice": {"intake": true}}
	withFakeTypes(t, pipes, skills, machines)

	plan, err := ArtifactExportPlanAsUser(nil, "alice", []ArtifactSel{{Type: "pipeline", Name: "main"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan) != 2 {
		t.Fatalf("the plan should list both dependencies and not the item itself: %+v", plan)
	}

	b, err := ExportArtifactBundleAsUser(nil, "alice", []ArtifactSel{{Type: "pipeline", Name: "main"}},
		UserExportOptions{IncludeDeps: true, DepTypes: []string{"skill"}})
	if err != nil {
		t.Fatal(err)
	}
	var kinds []string
	for _, a := range b.Artifacts {
		kinds = append(kinds, a.Type)
	}
	if len(kinds) != 2 || kinds[0] != "pipeline" || kinds[1] != "skill" {
		t.Fatalf("only the ticked kind should travel: %v", kinds)
	}
}

// Opt-in data kinds ride a closure only when named; attachments import after
// everything they could attach to; an item selected by id is not exported a
// second time when a dependency points back at it by name.
type dataFake struct{ ownerFake }

// canonFake names its recipes canonically, so an id and a name can reach the
// same recipe.
type canonFake struct {
	ownerFake
	canon map[string]string
}

func (c *canonFake) ExportArtifact(db Database, name, owner string) (json.RawMessage, error) {
	if _, err := c.ownerFake.ExportArtifact(db, name, owner); err != nil {
		return nil, err
	}
	return json.Marshal(map[string]string{"name": c.canon[name]})
}

func (*dataFake) OptInDependency() bool { return true }
func (*dataFake) ImportsLate() bool     { return true }

func TestOptInDataLateImportAndBackReferences(t *testing.T) {
	agents := &canonFake{ownerFake: ownerFake{sniffingFake: sniffingFake{fakeArtifact{typ: "agent"}}},
		canon: map[string]string{"id-1": "Helper", "Helper": "Helper"}}
	agents.byOwner = map[string]map[string]bool{"alice": {"id-1": true, "Helper": true}}
	agents.deps = map[string][]ArtifactSel{"id-1": {{Type: "memory", Name: "Helper"}, {Type: "agent", Name: "Helper"}}}
	mem := &dataFake{ownerFake{sniffingFake: sniffingFake{fakeArtifact{typ: "memory"}}}}
	mem.byOwner = map[string]map[string]bool{"alice": {"Helper": true}}
	withFakeTypes(t, agents, mem)

	// Not named: the memory stays home, even though every other kind travels.
	b, err := ExportArtifactBundleAsUser(nil, "alice", []ArtifactSel{{Type: "agent", Name: "id-1"}}, UserExportOptions{IncludeDeps: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range b.Artifacts {
		if a.Type == "memory" {
			t.Fatal("opt-in data travelled without being asked for")
		}
	}
	// Selected by id, pointed back at by name: one agent in the bundle.
	b, err = ExportArtifactBundleAsUser(nil, "alice", []ArtifactSel{{Type: "agent", Name: "id-1"}},
		UserExportOptions{IncludeDeps: true, DepTypes: []string{"memory", "agent"}})
	if err != nil {
		t.Fatal(err)
	}
	agentsOut := 0
	for _, a := range b.Artifacts {
		if a.Type == "agent" {
			agentsOut++
		}
	}
	if agentsOut != 1 {
		t.Errorf("the agent travelled %d times", agentsOut)
	}

	// Import order: the memory is listed first, lands last.
	bundle := []byte(`{"bundle": "` + ArtifactBundleFormat + `", "artifacts": [
		{"type": "memory", "name": "m", "recipe": "m"},
		{"type": "agent", "name": "a", "recipe": "a"}]}`)
	res, err := ImportArtifactBundle(nil, bundle, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Outcomes) != 2 || res.Outcomes[0].Type != "agent" || res.Outcomes[1].Type != "memory" {
		t.Errorf("attachments should import after what they attach to: %+v", res.Outcomes)
	}
}

// A recipe with a key baked into it is refused at export, whether it was
// selected or pulled in as a dependency; content kinds are not scanned.
type secretFake struct {
	fakeArtifact
	recipe  map[string]string
	content bool
}

func (f *secretFake) ExportArtifact(_ Database, name, _ string) (json.RawMessage, error) {
	r, ok := f.recipe[name]
	if !ok {
		return nil, Error("no such")
	}
	return json.Marshal(map[string]string{"name": name, "body": r})
}
func (f *secretFake) UserImportable() bool { return true }
func (f *secretFake) ContentKind() bool    { return f.content }

func TestExportRefusesAHardcodedSecretButNotInContent(t *testing.T) {
	tools := &secretFake{fakeArtifact: fakeArtifact{typ: "tool"}, recipe: map[string]string{
		"clean": "curl {url}", "leaky": "curl -H 'Authorization: Bearer sk_live_abcdefghijklmnop' {url}",
	}}
	tools.deps = map[string][]ArtifactSel{"clean": {{Type: "tool", Name: "leaky"}}}
	docs := &secretFake{fakeArtifact: fakeArtifact{typ: "guide"}, content: true, recipe: map[string]string{
		"howto": "Set api_key=abcdefghijklmnop in your shell",
	}}
	withFakeTypes(t, &dependingSecretFake{tools}, docs)

	_, err := ExportArtifactBundleAsUser(nil, "alice", []ArtifactSel{{Type: "tool", Name: "leaky"}}, UserExportOptions{})
	if err == nil || !strings.Contains(err.Error(), "hardcoded secret (bearer)") || strings.Contains(err.Error(), "sk_live") {
		t.Fatalf("a baked-in key should be refused without repeating it: %v", err)
	}
	_, err = ExportArtifactBundleAsUser(nil, "alice", []ArtifactSel{{Type: "tool", Name: "clean"}}, UserExportOptions{IncludeDeps: true})
	if err == nil || !strings.Contains(err.Error(), "depends on") {
		t.Fatalf("a dependency with a baked-in key should be refused and named: %v", err)
	}
	// Prose that merely names a key is not one.
	tools.recipe["prose"] = "Pass the token: provided by the caller, and the password: whatever they chose."
	if _, err := ExportArtifactBundleAsUser(nil, "alice", []ArtifactSel{{Type: "tool", Name: "prose"}}, UserExportOptions{}); err != nil {
		t.Fatalf("prose naming a key was refused: %v", err)
	}
	tools.recipe["kv"] = "X-Api-Key: api_key=a1b2c3d4e5f6g7h8"
	if _, err := ExportArtifactBundleAsUser(nil, "alice", []ArtifactSel{{Type: "tool", Name: "kv"}}, UserExportOptions{}); err == nil {
		t.Fatal("a key=value with a key-shaped value should be refused")
	}
	if _, err := ExportArtifactBundleAsUser(nil, "alice", []ArtifactSel{{Type: "guide", Name: "howto"}}, UserExportOptions{}); err != nil {
		t.Fatalf("a document quoting an example token is content, not a leak: %v", err)
	}
}

type dependingSecretFake struct{ *secretFake }

func (d *dependingSecretFake) Dependencies(_ Database, name, _ string) []ArtifactSel {
	return d.deps[name]
}

// An import says what is left to do: each inert thing's next step, and each
// reference the install does not have.
type followUpFake struct{ fakeArtifact }

func (*followUpFake) ImportFollowUp() string { return "Switched off. Turn it on." }

func TestAnImportReportsWhatIsLeftToDo(t *testing.T) {
	skills := &followUpFake{fakeArtifact{typ: "skill", deps: map[string][]ArtifactSel{"triage": {{Type: "tool", Name: "absent"}}}}}
	withFakeTypes(t, skills, &fakeArtifact{typ: "tool"})
	bundle := []byte(`{"bundle": "` + ArtifactBundleFormat + `", "artifacts": [{"type": "skill", "name": "triage", "recipe": "triage"}]}`)
	res, err := ImportArtifactBundle(nil, bundle, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Checklist) != 2 {
		t.Fatalf("want the skill's next step and the missing tool: %+v", res.Checklist)
	}
	if res.Checklist[0].Action != "Switched off. Turn it on." || res.Checklist[0].Missing {
		t.Errorf("follow-up: %+v", res.Checklist[0])
	}
	if !res.Checklist[1].Missing || res.Checklist[1].Name != "absent" {
		t.Errorf("missing reference: %+v", res.Checklist[1])
	}
	if !strings.Contains(res.Summary(), "What is left to do") {
		t.Errorf("summary: %s", res.Summary())
	}
}
