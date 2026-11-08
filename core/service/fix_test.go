package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

func draftOf(t *testing.T, m map[string]any) string {
	t.Helper()
	b, _ := json.Marshal(m)
	return string(b)
}

// A fix may change how the service is reached and how replies are read,
// never what the connector provides, its prices or its test budget.
func TestADraftKeepsWhatTheAdminDecides(t *testing.T) {
	world(t)
	install(t, "acme-images", imageSpec("https://api.example.test"))
	m := imageSpec("https://api2.example.test")
	if _, err := CheckDraft("acme-images", draftOf(t, m)); err != nil {
		t.Errorf("a new address is a fix: %v", err)
	}
	for name, edit := range map[string]func(map[string]any){
		"a price":       func(m map[string]any) { binding(m)["cost"] = map[string]any{"per_call": 0.01} },
		"the budget":    func(m map[string]any) { m["test_budget"] = 50 },
		"a capability":  func(m map[string]any) { binding(m)["capability"] = "image.edit" },
		"a broken spec": func(m map[string]any) { provider(m)["transport"] = "ftp" },
	} {
		m := imageSpec("https://api.example.test")
		edit(m)
		if _, err := CheckDraft("acme-images", draftOf(t, m)); err == nil {
			t.Errorf("a draft changing %s passed", name)
		}
	}
}

// Asking the free health operation costs nothing; any other request draws
// on the budget and says what is left. A corrected draft passes its contract
// test where the stored spec fails.
func TestAFixSeesTheServiceAndTriesADraft(t *testing.T) {
	world(t)
	p := newProvider(t)
	broken := imageSpec(p.srv.URL)
	op(broken, "submit")["path"] = "/v2/jobs" // the service moved it back
	install(t, "acme-images", broken)
	if reps, _ := Test(context.Background(), "acme-images"); len(reps) != 1 || reps[0].OK {
		t.Fatalf("the broken spec passes: %+v", reps)
	}
	draft, err := CheckDraft("acme-images", draftOf(t, broken))
	if err != nil {
		t.Fatal(err)
	}
	out, err := RequestDraft(context.Background(), "acme-images", draft, "models", nil)
	if err != nil || !strings.Contains(out, "HTTP 200") || strings.Contains(out, "paid calls left") {
		t.Errorf("the health operation: %q %v", out, err)
	}
	out, err = RequestDraft(context.Background(), "acme-images", draft, "submit", map[string]any{"prompt": "x", "width": 8, "height": 8})
	if err != nil || !strings.Contains(out, "HTTP 404") || !strings.Contains(out, "paid calls left today") {
		t.Errorf("a paid request: %q %v", out, err)
	}
	fixed := imageSpec(p.srv.URL)
	draft, err = CheckDraft("acme-images", draftOf(t, fixed))
	if err != nil {
		t.Fatal(err)
	}
	p.polls = 0
	reps, err := TryDraft(context.Background(), "acme-images", draft)
	if err != nil || len(reps) != 1 || !reps[0].OK {
		t.Errorf("the fixed draft: %+v %v", reps, err)
	}
	// The stored spec is untouched until someone saves.
	if c, _, _, _ := ForFix("acme-images"); c.Provider.Operations["submit"].Path != "/v2/jobs" {
		t.Error("trying a draft changed the stored spec")
	}
}

// A connector test's outcome is in the server's log: a failure with the
// connector, the job and the provider's reason, since the answer otherwise
// reaches only the page that ran it.
func TestATestsOutcomeIsLogged(t *testing.T) {
	world(t)
	var logged []string
	prevLog, prevWarn := Log, Warn
	capture := func(level string) func(...any) {
		return func(v ...any) {
			if f, ok := v[0].(string); ok {
				logged = append(logged, level+" "+fmt.Sprintf(f, v[1:]...))
			}
		}
	}
	Log, Warn = capture("LOG"), capture("WARN")
	t.Cleanup(func() { Log, Warn = prevLog, prevWarn })
	fail := true
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fail {
			http.Error(w, `{"error":{"message":"model not found"}}`, http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "audio/mpeg")
		w.Write(mp3ish)
	}))
	defer srv.Close()
	install(t, "tunes", map[string]any{
		"provider": map[string]any{"transport": "http", "base_url": srv.URL, "auth": map[string]any{"none": true},
			"operations": map[string]any{"song": map[string]any{"method": "POST", "path": "/songs", "body": map[string]any{"prompt": "{prompt}"}, "read": "file"}}},
		"provides": []any{map[string]any{"capability": "music.generate", "steps": []any{map[string]any{"call": "song"}},
			"output": map[string]any{"audio": map[string]any{"from": "body"}}}},
	})
	if _, err := Test(context.Background(), "tunes"); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(logged, "\n"); !strings.Contains(got, "WARN [connectors] test tunes music.generate failed:") || !strings.Contains(got, "model not found") {
		t.Errorf("a failed test is not logged with its reason:\n%s", got)
	}
	logged, fail = nil, false
	if _, err := Test(context.Background(), "tunes"); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(logged, "\n"); !strings.Contains(got, "LOG [connectors] test tunes music.generate: passed") {
		t.Errorf("a passed test is not logged:\n%s", got)
	}
}
