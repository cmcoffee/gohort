package anvil

import (
	"slices"
	"strings"
	"testing"

	"github.com/cmcoffee/gohort/core/outbound"
)

// What names a project is what its searches are checked for: its name, its
// app, its repository, a host that is not a public forge, a directory's
// folder. And the agent is told, since nothing else says the work is private.
func TestAProjectsNamesAreKeptOutOfItsSearches(t *testing.T) {
	cases := []struct {
		p    Project
		want []string
		not  []string
	}{
		{Project{Name: "Kestrel", Kind: KindClone, RepoURL: "https://github.com/acme/kestrel-control.git"},
			[]string{"Kestrel", "acme/kestrel-control", "kestrel-control"}, []string{"github.com"}},
		{Project{Name: "Ops", Kind: KindClone, RepoURL: "git@git.acme.example:infra/osprey.git", PushURL: "https://code.acme.example/infra/osprey-pub"},
			[]string{"infra/osprey", "osprey", "git.acme.example", "osprey-pub", "code.acme.example"}, nil},
		{Project{Name: "Tool", Kind: KindClone, Source: sourceApp, AppSlug: "harrier-intake"},
			[]string{"harrier-intake"}, nil},
		{Project{Name: "Live", Kind: KindRoot, Root: "/srv/merlin-gateway/"},
			[]string{"merlin-gateway"}, nil},
	}
	for _, c := range cases {
		got := searchPrivateTerms(c.p)
		for _, w := range c.want {
			if !slices.Contains(got, w) {
				t.Errorf("%s: terms %q lack %q", c.p.Name, got, w)
			}
		}
		for _, n := range c.not {
			if slices.Contains(got, n) {
				t.Errorf("%s: terms %q name the public forge %q", c.p.Name, got, n)
			}
		}
	}
	// End to end with the check the search makes.
	terms := searchPrivateTerms(cases[0].p)
	if outbound.Leak("kestrel-control reconcile loop", terms) == "" || outbound.Leak("kubernetes reconcile loop", terms) != "" {
		t.Error("the project's names and the general concept are not told apart")
	}
	T := &Anvil{}
	if text := T.projectContext(cases[0].p, t.TempDir()); !strings.Contains(text, "never this project's own names") {
		t.Error("the agent is not told to keep the project's names out of its searches")
	}
}

// An app project's agent is told what window.app offers, and that the page's
// localStorage already follows the person: one run searched the web for the
// page API, guessed app.records.get, and rebuilt per-person storage the page
// already had.
func TestAnAppProjectIsToldWhatItsPageCanCall(t *testing.T) {
	T := &Anvil{}
	text := T.projectContext(Project{Name: "Tool", Kind: KindClone, Source: sourceApp, AppSlug: "harrier-intake"}, t.TempDir())
	for _, want := range []string{"app.records.list()", "app.records.remove(id)", "a fresh run", "app.state.get"} {
		if !strings.Contains(text, want) {
			t.Errorf("the app project's context lacks %q", want)
		}
	}
	if text := T.projectContext(Project{Name: "Repo", Kind: KindClone, RepoURL: "https://example.com/r.git"}, t.TempDir()); strings.Contains(text, "window.app") {
		t.Error("a repository that is not an app was told about window.app")
	}
}
