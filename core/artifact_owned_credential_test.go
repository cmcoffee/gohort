package core

import (
	"encoding/json"
	"testing"
)

// A tool exported with its credential carries the credential the tool
// actually uses: its owner's own when they have one, which shadows the
// deployment's at run time. By name alone the export reached only the
// deployment's, so a tool on its owner's key either failed to export or
// carried a same-named deployment credential with another address.
func TestAToolExportsTheCredentialItActuallyUses(t *testing.T) {
	db := memDB(t)
	secureAPITestStore(t)
	if err := Secure().Save(SecureCredential{Name: "acme", Type: SecureCredBearer, BaseURL: "https://deployment.acme.example"}, "k1"); err != nil {
		t.Fatal(err)
	}
	if err := Secure().Save(SecureCredential{Name: "acme", Owner: "alice", Type: SecureCredBearer, BaseURL: "https://alice.acme.example",
		SharedReadOnly: []string{"bob"}}, "k2"); err != nil {
		t.Fatal(err)
	}
	for _, u := range []string{"alice", "carol"} {
		tool := TempTool{Name: "acme_orders", Description: "Orders.", Mode: TempToolModeAPI, Credential: "acme", Method: "GET",
			CommandTemplate: "https://x.acme.example/v1/orders"}
		if err := QueuePendingTempTool(db, u, tool, "test"); err != nil {
			t.Fatal(err)
		}
		if err := ApprovePendingTempTool(db, u, "acme_orders"); err != nil {
			t.Fatal(err)
		}
	}

	credOf := func(owner string) SecureCredential {
		t.Helper()
		b, err := ExportArtifactBundle(db, []ArtifactSel{{Type: "tool", Name: "acme_orders", Owner: owner}})
		if err != nil {
			t.Fatal(err)
		}
		var found []SecureCredential
		for _, a := range b.Artifacts {
			if a.Type == "credential" {
				var c SecureCredential
				json.Unmarshal(a.Recipe, &c)
				found = append(found, c)
			}
		}
		if len(found) != 1 {
			t.Fatalf("%s's tool exported %d credentials", owner, len(found))
		}
		return found[0]
	}

	// Alice's tool runs on Alice's key, so that is what travels: its shape,
	// with nothing about Alice or who she lent it to.
	c := credOf("alice")
	if c.BaseURL != "https://alice.acme.example" {
		t.Errorf("alice's tool carried %q, not the credential it uses", c.BaseURL)
	}
	if c.Owner != "" || len(c.SharedReadOnly) != 0 {
		t.Errorf("the credential carried its owner or its lends: owner=%q shared=%v", c.Owner, c.SharedReadOnly)
	}
	// Carol has no key of her own, so hers runs on the deployment's.
	if c := credOf("carol"); c.BaseURL != "https://deployment.acme.example" {
		t.Errorf("carol's tool carried %q, not the deployment credential it uses", c.BaseURL)
	}
	// The deployment-wide listing is unchanged: a person's credential is not
	// listed on its own, it travels with the tool that uses it.
	for _, s := range ArtifactSelectionForTypes(db, "credential") {
		if s.Owner != "" {
			t.Errorf("a person's credential is listed deployment-wide: %+v", s)
		}
	}
}
