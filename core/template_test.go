package core

import (
	"bytes"
	"strings"
	"testing"
)

// sampleBundle is a GitHub-shaped integration template: a bearer credential
// (config only, no secret) plus an api-mode tool that dispatches through it,
// with the manifest declaring the secret the importer must collect.
func sampleBundle() TemplateBundle {
	return TemplateBundle{
		Manifest: TemplateManifest{
			Name:        "github",
			Title:       "GitHub",
			Description: "Read repos, issues, and PRs via the GitHub API.",
			Category:    "dev",
			RequiresSecrets: []TemplateSecret{{
				Credential: "github",
				Label:      "GitHub Personal Access Token",
				Help:       "Create at github.com/settings/tokens with repo scope.",
			}},
			SetupNotes: "Paste a PAT, then run Test.",
		},
		Credentials: []SecureCredential{{
			Name:              "github",
			Type:              "bearer",
			AllowedURLPattern: "https://api.github.com/**",
			Description:       "GitHub REST API",
			AllowedMethods:    []string{"GET"},
		}},
		Tools: []TempTool{{
			Name:            "github_list_issues",
			Description:     "List open issues for a repo.",
			Mode:            "api",
			Credential:      "github",
			Method:          "GET",
			CommandTemplate: "https://api.github.com/repos/{owner}/{repo}/issues",
			Params: map[string]ToolParam{
				"owner": {Type: "string", Description: "Repo owner"},
				"repo":  {Type: "string", Description: "Repo name"},
			},
			Required: []string{"owner", "repo"},
		}},
	}
}

// TestTemplateRoundTrip proves marshal -> parse -> marshal is byte-stable,
// so a template checked into the repo imports back to exactly what was
// exported (no field loss, no reordering surprises).
func TestTemplateRoundTrip(t *testing.T) {
	data, err := MarshalTemplate(sampleBundle())
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	parsed, err := ParseTemplate(data)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	again, err := MarshalTemplate(parsed)
	if err != nil {
		t.Fatalf("re-marshal: %v", err)
	}
	if !bytes.Equal(data, again) {
		t.Fatalf("round trip not stable:\n--- first ---\n%s\n--- second ---\n%s", data, again)
	}
	if parsed.Manifest.SchemaVersion != TemplateSchemaVersion {
		t.Fatalf("schema version not stamped: got %d", parsed.Manifest.SchemaVersion)
	}
}

// TestTemplateNoSecretLeak guards the core invariant: a serialized template
// must never carry a secret. SecureCredential has no secret field today, so
// this is a regression tripwire for the day someone adds one and forgets to
// keep it out of the bundle.
func TestTemplateNoSecretLeak(t *testing.T) {
	data, err := MarshalTemplate(sampleBundle())
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	lower := strings.ToLower(string(data))
	for _, banned := range []string{"\"secret\"", "client_secret", "private_key", "refresh_token", "password", "access_token"} {
		if strings.Contains(lower, banned) {
			t.Fatalf("template JSON leaked a secret-shaped field %q:\n%s", banned, data)
		}
	}
}

// TestTemplateRejectsDanglingSecret: a RequiresSecrets entry pointing at a
// credential the bundle does not ship is a bundle authoring error; the
// importer would otherwise prompt for a secret with nowhere to land it.
func TestTemplateRejectsDanglingSecret(t *testing.T) {
	b := sampleBundle()
	b.Manifest.RequiresSecrets[0].Credential = "nonexistent"
	data, err := MarshalTemplate(b)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if _, err := ParseTemplate(data); err == nil {
		t.Fatal("expected ParseTemplate to reject a dangling secret reference")
	}
}

// TestTemplateRejectsFutureSchema: an old binary must refuse a bundle
// authored against a newer schema rather than silently mis-import it.
func TestTemplateRejectsFutureSchema(t *testing.T) {
	b := sampleBundle()
	b.Manifest.SchemaVersion = TemplateSchemaVersion + 1
	data, err := MarshalTemplate(b)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if _, err := ParseTemplate(data); err == nil {
		t.Fatal("expected ParseTemplate to reject a future schema version")
	}
}
