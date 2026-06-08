package core

import (
	"testing"
	"time"
)

func TestBuildTemplateCleanSucceeds(t *testing.T) {
	src := sampleBundle()
	b, err := BuildTemplate(
		TemplateManifest{Name: "github", Title: "GitHub", Description: "GitHub API."},
		src.Credentials, src.Tools, nil,
	)
	if err != nil {
		t.Fatalf("clean export should succeed: %v", err)
	}
	// requires_secrets auto-derived for the github credential.
	if len(b.Manifest.RequiresSecrets) != 1 || b.Manifest.RequiresSecrets[0].Credential != "github" {
		t.Fatalf("expected one auto-derived secret for github, got %+v", b.Manifest.RequiresSecrets)
	}
}

func TestBuildTemplateClearsInstanceState(t *testing.T) {
	cred := SecureCredential{
		Name:       "x",
		Type:       "bearer",
		CreatedAt:  time.Now(),
		LastUsedAt: time.Now(),
		Pending:    true,
		Disabled:   true,
	}
	b, err := BuildTemplate(TemplateManifest{Name: "x"}, []SecureCredential{cred}, nil, nil)
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	got := b.Credentials[0]
	if !got.CreatedAt.IsZero() || !got.LastUsedAt.IsZero() || got.Pending || got.Disabled {
		t.Fatalf("instance state not cleared: %+v", got)
	}
}

func TestBuildTemplateRefusesHardcodedSecret(t *testing.T) {
	tool := TempTool{
		Name:            "leaky",
		Mode:            "api",
		Credential:      "x",
		CommandTemplate: "https://api.example.com/data?api_key=abcd1234efgh5678",
	}
	_, err := BuildTemplate(TemplateManifest{Name: "x"},
		[]SecureCredential{{Name: "x", Type: "bearer"}}, []TempTool{tool}, nil)
	if err == nil {
		t.Fatal("expected export to refuse a tool with a hardcoded secret")
	}
}

func TestScanForEmbeddedSecret(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"https://api.github.com/repos/{owner}/{repo}/issues", false}, // placeholders only
		{"https://api.x.com/data?api_key={key}", false},               // placeholder value
		{"curl -H 'Authorization: Bearer {token}' https://x", false},  // placeholder bearer
		{"https://api.x.com/data?api_key=abcd1234efgh", true},         // literal key
		{"Authorization: Bearer ghp_realtokenvalue1234", true},        // literal bearer
		{"token=sk-livesecretvalue99", true},                          // literal token
		{"", false},
	}
	for _, c := range cases {
		if got := scanForEmbeddedSecret(c.in); got != c.want {
			t.Errorf("scanForEmbeddedSecret(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}
