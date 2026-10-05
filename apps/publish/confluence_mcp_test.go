package publish

import (
	"context"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/docs"
	"github.com/cmcoffee/snugforge/kvlite"
)

// With no API credential and an MCP server named, Confluence publishes through
// that server: one target that asks for the space, a run that holds only the
// server's tools, the page's address kept so a republish updates it. A named
// credential still wins, and the MCP server is then never used.
func TestConfluencePublishesThroughAnMCPServer(t *testing.T) {
	app := &PublishApp{}
	app.DB = &DBase{Store: kvlite.MemStore()}
	app.saveConfig(PublishConfig{ConfluenceMCP: "atlassian"})
	d := &confluenceDest{app: app}
	if d.viaMCP() != "atlassian" {
		t.Fatalf("viaMCP = %q", d.viaMCP())
	}
	if ok, why := d.Available("alice"); ok || !strings.Contains(why, "atlassian") {
		t.Errorf("an MCP server that is not set up reads as available: %v %q", ok, why)
	}
	targets, err := d.Targets(context.Background(), "alice")
	if err != nil || len(targets) != 1 || targets[0].ID != confluenceMCPTargetID {
		t.Fatalf("targets = %+v, %v", targets, err)
	}
	specs := d.TargetSpecs(context.Background(), "alice")
	if len(specs) != 1 || specs[0].Kind != ConfluenceKind || len(specs[0].Fields) != 2 || specs[0].Fields[0].Name != "space" || !specs[0].Fields[0].Required {
		t.Fatalf("specs = %+v", specs)
	}

	var gotCred, gotInstr string
	docs.RegisterCredentialPublisher(func(_ context.Context, user, cred, instr string) (string, string, error) {
		gotCred, gotInstr = cred, instr
		return "Page created.", "https://acme.atlassian.net/wiki/spaces/DOCS/pages/42", nil
	})
	req := docs.PublishRequest{Target: confluenceMCPTargetID, Title: "Runbook", Doc: docs.PublishDoc{Markdown: "## Steps\n\n1. Do it."}}
	if _, err := d.Publish(context.Background(), "alice", req); err == nil || !strings.Contains(err.Error(), "space") {
		t.Fatalf("a publish with no space went ahead: %v", err)
	}
	req.Answers = map[string]string{"space": "DOCS", "parent": "Operations"}
	res, err := d.Publish(context.Background(), "alice", req)
	if err != nil {
		t.Fatal(err)
	}
	if gotCred != docs.MCPIntegrationPrefix+"atlassian" || res.URL != "https://acme.atlassian.net/wiki/spaces/DOCS/pages/42" || res.ExternalID != res.URL || res.Updated {
		t.Fatalf("cred %q, result %+v", gotCred, res)
	}
	for _, want := range []string{"Title: Runbook", "Space: DOCS", "Under page: Operations", "Create a new page", "## Steps"} {
		if !strings.Contains(gotInstr, want) {
			t.Errorf("the run was not handed %q:\n%s", want, gotInstr)
		}
	}
	// A republish updates the page it made, even without the answers.
	req.Answers, req.ExternalID = nil, res.URL
	res, err = d.Publish(context.Background(), "alice", req)
	if err != nil || !res.Updated || !strings.Contains(gotInstr, "Update that page") || strings.Contains(gotInstr, "Create a new page") {
		t.Fatalf("republish = %+v, %v:\n%s", res, err, gotInstr)
	}

	// A credential is the direct path and wins.
	app.saveConfig(PublishConfig{ConfluenceCredential: "confluence-api", ConfluenceMCP: "atlassian"})
	if d.viaMCP() != "" || d.TargetSpecs(context.Background(), "alice") != nil {
		t.Fatal("the MCP server was used with a credential named")
	}
}
