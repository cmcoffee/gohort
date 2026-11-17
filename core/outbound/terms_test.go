package outbound

import (
	"context"
	"strings"
	"testing"
)

// The check matches names, never subjects: the general concept a private
// project is built on is a fine search, and its own name is not.
func TestASearchMayNameTheConceptButNotThePrivateThing(t *testing.T) {
	terms := []string{"Kestrel Control", "acme/kestrel-control", "git.acme.example", "Demo service", "api"}
	cases := []struct {
		query string
		want  string // "" for a query that may go out
	}{
		{"kubernetes operator reconcile loop backoff", ""},
		{"kestrel control reconcile backoff", `names "Kestrel Control"`},
		{"kestrel_control crash on start", `names "Kestrel Control"`},
		{"acme/kestrel-control issues", `names "Kestrel Control"`},
		{"clone from git.acme.example failing", `names "git.acme.example"`},
		// A word inside another is not the name.
		{"kestrelcontrolled valves", ""},
		// Names only of generic words, or too short, identify nothing.
		{"demo service template", ""},
		{"rest api pagination", ""},
		{"PROJ-4412 request smuggling", "ticket ID (PROJ-4412)"},
		{"CVE-2024-3094 xz backdoor", ""},
		{"UTF-8 vs ISO-8601 parsing", ""},
		{"build01.corp unreachable", "internal host (build01.corp)"},
		{"connection refused 10.0.4.12 port 5432", "internal address (10.0.4.12)"},
		{"8.8.8.8 dns timeout", ""},
	}
	for _, c := range cases {
		got := Leak(c.query, terms)
		if c.want == "" && got != "" || c.want != "" && !strings.Contains(got, c.want) {
			t.Errorf("Leak(%q) = %q, want %q", c.query, got, c.want)
		}
	}
}

// Terms ride the context, add to what is there, and survive a run detached
// from the request that named them.
func TestPrivateTermsFollowTheWork(t *testing.T) {
	ctx := WithPrivateTerms(context.Background(), "Kestrel", " ")
	ctx = WithPrivateTerms(ctx, "kestrel", "Osprey")
	if got := PrivateTerms(ctx); len(got) != 2 || got[0] != "Kestrel" || got[1] != "Osprey" {
		t.Fatalf("terms = %q, want Kestrel and Osprey once each", got)
	}
	detached := CarryPrivateTerms(context.Background(), ctx)
	if got := PrivateTerms(detached); len(got) != 2 {
		t.Fatalf("a detached run lost the terms: %q", got)
	}
	if PrivateTerms(CarryPrivateTerms(context.Background(), context.Background())) != nil {
		t.Fatal("a run with none gained some")
	}
}
