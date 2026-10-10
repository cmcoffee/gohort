package orchestrate

import (
	"encoding/json"
	"strings"
	"testing"

	. "github.com/cmcoffee/oddjob/core"
)

// The page's calls against the app's endpoints: a call to one the app does
// not have fails; an endpoint nothing reaches, and JSON.stringify written
// into the page, are worth fixing.
func TestThePageAndItsEndpointsAgree(t *testing.T) {
	html := `<!DOCTYPE html><html><body><div id=w></div><script>
app.data("weather", {city: c}).then(function(d){ w.textContent = JSON.stringify(d); });
app.action('save_city', {city: c});
fetch("data/forecast_days");
</script></body></html>`
	secs, _ := json.Marshal([]any{map[string]any{"kind": "html", "html": html}})
	spec := AppSpec{Slug: "wx", Sections: secs,
		DataSources: []AppDataSource{{Name: "weather"}, {Name: "old-stuff"}},
		Actions:     []AppAction{{Name: "save-city"}, {Name: "nightly", Schedule: &AppSchedule{Cron: "* 03:00"}}}}
	probs := appContractProblems(spec)
	if len(probs) != 1 || !strings.Contains(probs[0], `"forecast_days"`) || !strings.Contains(probs[0], "it has: weather, old-stuff") {
		t.Fatalf("contract: %q", probs)
	}
	unused := strings.Join(appUnusedEndpoints(spec), "\n")
	if !strings.Contains(unused, `data source "old-stuff" is never called`) || strings.Contains(unused, "weather") || strings.Contains(unused, "nightly") || strings.Contains(unused, "save-city") {
		t.Fatalf("unused: %s", unused)
	}
	if !strings.Contains(unused, "JSON.stringify") {
		t.Fatalf("stringify into the page not noted: %s", unused)
	}
}

// What a person sees: raw JSON or [object Object] reads as data, not a page.
// A sentence with braces in it does not.
func TestRawDataOnThePage(t *testing.T) {
	cases := map[string]string{
		"Weather\n{\"location\": \"Santa Cruz, CA\", \"current\": {\"temp\": 71}}":              "raw JSON",
		"Forecast [ {\"date\": \"2026-10-08\", \"high\": 84}, {\"date\": \"x\", \"high\": 1} ]": "raw JSON",
		"Current: [object Object]": "[object Object]",
	}
	for text, want := range cases {
		if got, _ := pageShowsRawData([]string{"", text}); got != want {
			t.Errorf("%q -> %q, want %q", text, got, want)
		}
	}
	for _, ok := range []string{"Santa Cruz, CA 71°F Clear sky", "Use {name} in the template", `Say "hi" {to} me`} {
		if got, _ := pageShowsRawData([]string{ok}); got != "" {
			t.Errorf("%q flagged as %s", ok, got)
		}
	}
	if !appShowsJSONOnPurpose(AppSpec{Name: "JSON Formatter"}) || appShowsJSONOnPurpose(AppSpec{Name: "Weather"}) {
		t.Error("a JSON tool is exempt, a weather app is not")
	}
}

// A page that loads Chart.js from a CDN and calls new Chart(...) is not
// calling something it never defines; a page with no external script that
// calls an undefined function still is.
func TestLibraryConstructorsAreNotDangling(t *testing.T) {
	withCDN := `<!DOCTYPE html><html><head><script src="https://cdn.jsdelivr.net/npm/chart.js@4.4.1"></script></head><body><canvas id=c></canvas><script>
function draw(){ chart = new Chart(document.getElementById('c'), {}); Plotly.newPlot('x', []); }
draw(); missingHelper();
</script></body></html>`
	got := jsDanglingCalls(withCDN)
	if len(got) != 1 || got[0] != "missingHelper" {
		t.Fatalf("dangling with a CDN script = %v, want only missingHelper", got)
	}
	noCDN := `<html><body><script>function draw(){ c = new Chart(x, {}); } draw();</script></body></html>`
	if got := jsDanglingCalls(noCDN); len(got) != 1 || got[0] != "Chart" {
		t.Fatalf("without any library loaded, Chart is still undefined: %v", got)
	}
}
