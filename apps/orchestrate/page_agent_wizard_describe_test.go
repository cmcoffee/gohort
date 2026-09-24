package orchestrate

// "Describe it" created the agent and then opened a 404.
//
// Every URL in the dialog is relative to the page it runs on,
// /orchestrate/agent/wizard. The two fetches were written for that page; the
// navigation after a successful create said agent/<id>, which from there is
// /orchestrate/agent/agent/<id>. handleAgentPage reads that as an agent called
// "agent" with a sub-page, and answers NotFound.

import (
	"net/url"
	"os"
	"regexp"
	"strings"
	"testing"
)

const wizardPageURL = "https://example.test/orchestrate/agent/wizard"

func resolveFromWizard(t *testing.T, ref string) string {
	t.Helper()
	base, _ := url.Parse(wizardPageURL)
	u, err := base.Parse(ref)
	if err != nil {
		t.Fatalf("resolving %q: %v", ref, err)
	}
	return u.Path
}

func TestDescribeItOpensTheAgentItCreated(t *testing.T) {
	js := wizardDescribeHTML()
	m := regexp.MustCompile(`location\.href=([^;]*)\+?encodeURIComponent\(rec\.id\)`).FindStringSubmatch(js)
	if m == nil {
		t.Fatal("the dialog no longer navigates to the agent it created")
	}
	prefix := strings.Trim(strings.TrimSuffix(strings.TrimSpace(m[1]), "+"), "'")
	got := resolveFromWizard(t, prefix+"abc123")
	if got != "/orchestrate/agent/abc123" {
		t.Errorf("after creating, the dialog opens %s; the agent's page is /orchestrate/agent/abc123", got)
	}
}

func TestDescribeItPostsToRoutesThatExist(t *testing.T) {
	raw, err := os.ReadFile("orchestrate.go")
	if err != nil {
		t.Fatalf("reading the routes: %v", err)
	}
	routes := string(raw)
	fetches := regexp.MustCompile(`fetch\('([^']+)'`).FindAllStringSubmatch(wizardDescribeHTML(), -1)
	if len(fetches) == 0 {
		t.Fatal("the dialog makes no calls; the test is reading the wrong thing")
	}
	for _, f := range fetches {
		path := resolveFromWizard(t, f[1])
		route := strings.TrimPrefix(path, "/orchestrate")
		if route == path {
			t.Errorf("%q resolves outside the app: %s", f[1], path)
			continue
		}
		if !strings.Contains(routes, `HandleFunc("`+route+`"`) {
			t.Errorf("%q resolves to %s, which is not a registered route", f[1], path)
		}
	}
}
