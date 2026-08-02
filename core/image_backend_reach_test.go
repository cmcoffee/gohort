package core

// Which image backends a caller may render through. The grouped `image` tool
// turned the backend from a tool NAME into a parameter, so this list is what
// stands between a caller and every rest_image connector on the box.

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/cmcoffee/snugforge/kvlite"
)

// dropImageBackend removes a registered backend so one test's connectors don't
// leak into the next (the registry is process-global and append-only in prod —
// Teardown deliberately leaves entries, since the tool live-resolves).
func dropImageBackend(name string) {
	imageBackendMu.Lock()
	delete(imageBackends, name)
	imageBackendMu.Unlock()
}

func backendTestDB(t *testing.T) {
	t.Helper()
	savedDB, savedProvider := RootDB, ImageProviderFunc
	RootDB = &DBase{Store: kvlite.MemStore()}
	ImageProviderFunc = func() string { return "none" }
	t.Cleanup(func() {
		RootDB, ImageProviderFunc = savedDB, savedProvider
		for _, n := range []string{"comfy_lan", "comfy_edit", "hosted_flux"} {
			dropImageBackend(n)
		}
	})
}

// putImageConnector stores a rest_image connector and, when approved, registers
// the backend closure the way Materialize would.
func putImageConnector(t *testing.T, name, credential string, approved bool) {
	t.Helper()
	spec, err := json.Marshal(RestImageSpec{
		Credential:     credential,
		SubmitURL:      "http://localhost:8188/prompt",
		PollURL:        "http://localhost:8188/history/{id}",
		SubmitIDPath:   "prompt_id",
		PromptGuidance: "guidance for " + name,
		ComfyWorkflow:  comfyDefaultGraph,
		ComfyMap:       ComfyNodeMap{OutputNode: "9", PromptNodes: []string{"6"}},
	})
	if err != nil {
		t.Fatalf("marshal spec: %v", err)
	}
	c := Connector{Name: name, Kind: RestImageConnectorKind, Approved: approved, Spec: spec}
	RootDB.Set(connectorsTable, name, &c)
	if approved {
		RegisterImageBackend(name, func(_ context.Context, _ string, _ bool) (*ImageGenResult, error) {
			return &ImageGenResult{}, nil
		})
	}
}

func backendNames(cs []ImageBackendChoice) []string {
	out := make([]string, 0, len(cs))
	for _, c := range cs {
		out = append(out, c.Name)
	}
	return out
}

func TestUnapprovedConnectorIsNotReachable(t *testing.T) {
	// Approval is the admin gate on an unattended outbound call. A pending
	// connector must not become reachable just because the grouped tool lists
	// backends by name.
	backendTestDB(t)
	putImageConnector(t, "comfy_lan", "no_auth", false)
	got := ReachableImageBackends(&ToolSession{})
	if len(got) != 0 {
		t.Errorf("reachable = %v, want none — the connector is not approved", backendNames(got))
	}
	if ImageBackendReachable(&ToolSession{}, "comfy_lan") {
		t.Error("an unapproved connector must be refused at the enforcement check too")
	}
}

func TestApprovedConnectorIsReachableWithItsGuidance(t *testing.T) {
	backendTestDB(t)
	putImageConnector(t, "comfy_lan", "no_auth", true)
	got := ReachableImageBackends(&ToolSession{})
	if len(got) != 1 || got[0].Name != "comfy_lan" {
		t.Fatalf("reachable = %v, want [comfy_lan]", backendNames(got))
	}
	if got[0].Guidance != "guidance for comfy_lan" {
		t.Errorf("guidance = %q, want the spec's PromptGuidance", got[0].Guidance)
	}
	if !ImageBackendReachable(&ToolSession{}, "comfy_lan") {
		t.Error("an approved, registered connector must pass the enforcement check")
	}
}

func TestDeniedCredentialHidesTheBackend(t *testing.T) {
	// A rest_image backend dispatches through its credential, so the per-agent
	// credential deny list is the gate that already governs it. Offering a
	// backend the agent can't actually dispatch through is a guaranteed refusal.
	backendTestDB(t)
	putImageConnector(t, "comfy_lan", "no_auth", true)
	putImageConnector(t, "hosted_flux", "flux_key", true)

	open := ReachableImageBackends(&ToolSession{})
	if len(open) != 2 {
		t.Fatalf("reachable = %v, want both", backendNames(open))
	}
	denied := &ToolSession{DeniedCredentials: map[string]bool{"flux_key": true}}
	got := ReachableImageBackends(denied)
	if len(got) != 1 || got[0].Name != "comfy_lan" {
		t.Errorf("reachable = %v, want only comfy_lan", backendNames(got))
	}
	if ImageBackendReachable(denied, "hosted_flux") {
		t.Error("a denied credential must be refused at the enforcement check too")
	}
}

func TestBackendListIsSortedAndMemoized(t *testing.T) {
	backendTestDB(t)
	putImageConnector(t, "hosted_flux", "no_auth", true)
	putImageConnector(t, "comfy_lan", "no_auth", true)

	sess := &ToolSession{}
	got := backendNames(ReachableImageBackends(sess))
	if len(got) != 2 || got[0] != "comfy_lan" || got[1] != "hosted_flux" {
		t.Fatalf("reachable = %v, want sorted [comfy_lan hosted_flux]", got)
	}
	// Memoized for the turn: a connector approved mid-conversation lands on the
	// NEXT turn's session, not this one. That keeps the schema stable across the
	// repeated catalog builds within a turn.
	putImageConnector(t, "comfy_edit", "no_auth", true)
	if again := backendNames(ReachableImageBackends(sess)); len(again) != 2 {
		t.Errorf("reachable = %v, want the memoized 2 — a mid-turn change must not shift the advertised schema", again)
	}
	if fresh := backendNames(ReachableImageBackends(&ToolSession{})); len(fresh) != 3 {
		t.Errorf("a fresh session = %v, want all 3", fresh)
	}
}

func TestBuiltInProviderOfferedOnlyWhenItIsTheDefault(t *testing.T) {
	// A leftover Gemini key must not quietly re-open a provider the admin moved
	// off of. The built-in joins the list only when it IS the configured default.
	backendTestDB(t)
	putImageConnector(t, "comfy_lan", "no_auth", true)

	ImageProviderFunc = func() string { return "comfy_lan" }
	got := ReachableImageBackends(&ToolSession{})
	if len(got) != 1 || got[0].Name != "comfy_lan" || !got[0].Default {
		t.Errorf("reachable = %+v, want comfy_lan marked default and no built-in", got)
	}

	savedKey := GeminiKeyFunc
	GeminiKeyFunc = func() string { return "key" }
	ImageProviderFunc = func() string { return "gemini" }
	t.Cleanup(func() { GeminiKeyFunc = savedKey })
	got = ReachableImageBackends(&ToolSession{})
	names := backendNames(got)
	if len(names) != 2 || names[0] != "comfy_lan" || names[1] != "gemini" {
		t.Fatalf("reachable = %v, want [comfy_lan gemini]", names)
	}
	for _, c := range got {
		if c.Default != (c.Name == "gemini") {
			t.Errorf("%s default = %v, want the configured provider marked", c.Name, c.Default)
		}
	}
}

func TestApprovedButUnmaterializedIsNotReachable(t *testing.T) {
	// Approved with an unparseable spec means Materialize never registered a
	// backend closure. Advertising the name would hand the model a value that
	// cannot dispatch.
	backendTestDB(t)
	c := Connector{Name: "comfy_lan", Kind: RestImageConnectorKind, Approved: true, Spec: json.RawMessage(`{"submit_url":"http://x/prompt"}`)}
	RootDB.Set(connectorsTable, "comfy_lan", &c)
	if got := ReachableImageBackends(&ToolSession{}); len(got) != 0 {
		t.Errorf("reachable = %v, want none — no backend closure is registered", backendNames(got))
	}
}
