package service

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
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
