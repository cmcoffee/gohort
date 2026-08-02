package core

// How long a render gets, and what happens when it never finishes.
//
// The deadline used to be a hardcoded 120s fallback with 180s baked into the
// ComfyUI preset, reachable only by editing spec JSON. An edit needs far more
// than that — a large edit checkpoint loads before the first step runs, and on
// a GPU shared with a resident LLM it loads slowly or in low-VRAM mode.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestEditsGetTheLongerDeadline(t *testing.T) {
	gen, _, err := NewComfyImageSpec("http://localhost:8188", "no_auth", ComfyStarterGraph(ComfyTypeGenerate), "")
	if err != nil {
		t.Fatalf("generate spec: %v", err)
	}
	edit, _, err := NewComfyImageSpec("http://localhost:8188", "no_auth", ComfyStarterGraph(ComfyTypeEdit), "")
	if err != nil {
		t.Fatalf("edit spec: %v", err)
	}
	if gen.PollMaxSecs != 0 || edit.PollMaxSecs != 0 {
		t.Fatal("neither preset should hardcode a deadline any more — the tunable decides")
	}
	if edit.pollDeadline() <= gen.pollDeadline() {
		t.Errorf("edit deadline %s must exceed generate's %s", edit.pollDeadline(), gen.pollDeadline())
	}
	if want := TuneDuration("tune_image_poll_max_secs"); gen.pollDeadline() != want {
		t.Errorf("generate deadline = %s, want the tunable's %s", gen.pollDeadline(), want)
	}
	if want := TuneDuration("tune_image_edit_poll_max_secs"); edit.pollDeadline() != want {
		t.Errorf("edit deadline = %s, want the tunable's %s", edit.pollDeadline(), want)
	}
}

func TestPerConnectorDeadlineOverridesTheTunable(t *testing.T) {
	// One slow workflow shouldn't force the global up for every backend.
	s := RestImageSpec{PollMaxSecs: 45}
	if got := s.pollDeadline(); got != 45*time.Second {
		t.Errorf("deadline = %s, want the connector's 45s", got)
	}
}

func TestBothDeadlinesAreTunable(t *testing.T) {
	// The question that started this: is it a knob an operator can turn without
	// editing JSON? It has to be, in the same Timeouts group as the rest.
	for _, key := range []string{"tune_image_poll_max_secs", "tune_image_edit_poll_max_secs"} {
		spec, ok := LookupTunable(key)
		if !ok {
			t.Errorf("%s is not registered — it can only be changed by hand-editing a spec", key)
			continue
		}
		if spec.Category != "Timeouts" {
			t.Errorf("%s category = %q, want Timeouts", key, spec.Category)
		}
		if spec.Kind != KindSeconds {
			t.Errorf("%s should be entered in seconds", key)
		}
	}
}

func TestReportedFailureBeatsWaitingOutTheClock(t *testing.T) {
	// A job that DIED looks exactly like one still running: the ready path stays
	// empty either way. The loop used to poll it until the deadline and report a
	// timeout, which reads as "too slow" and sends everyone to raise the
	// deadline instead of at the actual error.
	secureAPITestStore(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/prompt") {
			_, _ = w.Write([]byte(`{"prompt_id":"abc"}`))
			return
		}
		_, _ = w.Write([]byte(`{"abc":{"status":{"status_str":"error","messages":[]}}}`))
	}))
	defer srv.Close()

	spec, _, err := NewComfyImageSpec(srv.URL, "no_auth", ComfyStarterGraph(ComfyTypeGenerate), "")
	if err != nil {
		t.Fatalf("NewComfyImageSpec: %v", err)
	}
	spec.PollMaxSecs = 30 // would be a 30s wait if the failure went unnoticed

	start := time.Now()
	_, err = spec.generate(&ToolSession{}, restImageParams{prompt: "x", seed: 1})
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("a backend-reported error must fail the call")
	}
	if strings.Contains(err.Error(), "timed out") {
		t.Errorf("a reported failure must not surface as a timeout: %v", err)
	}
	if !strings.Contains(err.Error(), "failed to run this workflow") {
		t.Errorf("error should name the backend failure: %v", err)
	}
	if elapsed > 15*time.Second {
		t.Errorf("took %s — the failure should be noticed on the first poll, not waited out", elapsed)
	}
}

func TestTimeoutSaysWhereToRaiseIt(t *testing.T) {
	// A timeout that doesn't name the setting leaves the operator guessing at
	// which of several timeouts applied.
	secureAPITestStore(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/prompt") {
			_, _ = w.Write([]byte(`{"prompt_id":"abc"}`))
			return
		}
		_, _ = w.Write([]byte(`{"abc":{"status":{"status_str":"running"}}}`)) // never finishes
	}))
	defer srv.Close()

	spec, _, err := NewComfyImageSpec(srv.URL, "no_auth", ComfyStarterGraph(ComfyTypeGenerate), "")
	if err != nil {
		t.Fatalf("NewComfyImageSpec: %v", err)
	}
	spec.PollMaxSecs = 1
	spec.PollIntervalSecs = 1

	_, err = spec.generate(&ToolSession{}, restImageParams{prompt: "x", seed: 1})
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("want a timeout, got: %v", err)
	}
	for _, want := range []string{"render timeout", "Tunables"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("timeout should point at %q:\n%v", want, err)
		}
	}
}
