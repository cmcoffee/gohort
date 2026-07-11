package core

import "testing"

// TestSamplerResolution locks in the -1-sentinel override chain: a per-call
// value always wins; an unset global (default -1) defers to the server (nil); a
// global >= 0 applies, and 0 is a legal explicit value (not "unset").
func TestSamplerResolution(t *testing.T) {
	// Per-call always wins.
	pc := 0.3
	if got := resolveFloatSampler(&pc, TunableSamplingTemperature); got == nil || *got != 0.3 {
		t.Fatalf("per-call should win, got %v", got)
	}
	// Unset per-call + unset global (default -1) => defer to server (nil).
	if got := resolveFloatSampler(nil, TunableSamplingTemperature); got != nil {
		t.Fatalf("default -1 should defer to server (nil), got %v", *got)
	}

	// Global set applies when per-call is unset. Temperature 0 must be honored
	// as an explicit value, which is the whole point of the -1 sentinel.
	db := memDB(t)
	db.Set(WebTable, TunableSamplingTemperature, float64(0))
	db.Set(WebTable, TunableSamplingTopK, float64(40))
	SetTunablesDB(db)
	defer SetTunablesDB(nil)
	if got := resolveFloatSampler(nil, TunableSamplingTemperature); got == nil || *got != 0 {
		t.Fatalf("global temperature 0 should apply as explicit, got %v", got)
	}
	if got := resolveIntSampler(nil, TunableSamplingTopK); got == nil || *got != 40 {
		t.Fatalf("global top-k 40 should apply, got %v", got)
	}

	// applySamplers threads resolved values onto the request; min_p is per-call
	// only (no global tunable), so it passes straight through.
	mp := 0.05
	var p oaiRequest
	(&openAIClient{}).applySamplers(&p, ChatConfig{MinP: &mp})
	if p.TopK == nil || *p.TopK != 40 {
		t.Fatalf("applySamplers should pull global top-k, got %v", p.TopK)
	}
	if p.MinP == nil || *p.MinP != 0.05 {
		t.Fatalf("min_p per-call should thread through, got %v", p.MinP)
	}
	if p.TopP != nil {
		t.Fatalf("top-p unset globally and per-call should stay nil, got %v", *p.TopP)
	}
}
