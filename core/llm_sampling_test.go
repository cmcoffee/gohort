package core

import "testing"

// TestSamplerResolution locks in the -1-sentinel override chain: a per-call
// value always wins; an unset global (default -1) defers to the server (nil); a
// global >= 0 applies, and 0 is a legal explicit value (not "unset").
func TestSamplerResolution(t *testing.T) {
	thinkTemp := samplerKey(TunableSamplingTemperature, true)
	thinkTopK := samplerKey(TunableSamplingTopK, true)

	// Per-call always wins.
	pc := 0.3
	if got := resolveFloatSampler(&pc, thinkTemp); got == nil || *got != 0.3 {
		t.Fatalf("per-call should win, got %v", got)
	}
	// Unset per-call + unset global (default -1) => defer to server (nil).
	if got := resolveFloatSampler(nil, thinkTemp); got != nil {
		t.Fatalf("default -1 should defer to server (nil), got %v", *got)
	}

	// Global set applies when per-call is unset. Temperature 0 must be honored
	// as an explicit value, which is the whole point of the -1 sentinel.
	db := memDB(t)
	db.Set(WebTable, thinkTemp, float64(0))
	db.Set(WebTable, thinkTopK, float64(40))
	SetTunablesDB(db)
	defer SetTunablesDB(nil)
	if got := resolveFloatSampler(nil, thinkTemp); got == nil || *got != 0 {
		t.Fatalf("global temperature 0 should apply as explicit, got %v", got)
	}
	if got := resolveIntSampler(nil, thinkTopK); got == nil || *got != 40 {
		t.Fatalf("global top-k 40 should apply, got %v", got)
	}

	// applySamplers threads resolved values onto the request; min_p is per-call
	// only (no global tunable), so it passes straight through. A defer (Think
	// nil, no master override) resolves to the thinking profile.
	mp := 0.05
	var p oaiRequest
	(&openAIClient{}).applySamplers(&p, ChatConfig{MinP: &mp})
	if p.TopK == nil || *p.TopK != 40 {
		t.Fatalf("applySamplers should pull the thinking-profile top-k, got %v", p.TopK)
	}
	if p.MinP == nil || *p.MinP != 0.05 {
		t.Fatalf("min_p per-call should thread through, got %v", p.MinP)
	}
	if p.TopP != nil {
		t.Fatalf("top-p unset globally and per-call should stay nil, got %v", *p.TopP)
	}
}

// TestSamplerThinkNoThinkSplit: the two profiles are independent, and the call's
// effective think state selects which one applies. A thinking call reads the
// _think keys; a no-think call reads _nothink; and the master no-think override
// forces the non-think profile even when the caller asked to think.
func TestSamplerThinkNoThinkSplit(t *testing.T) {
	db := memDB(t)
	db.Set(WebTable, samplerKey(TunableSamplingTemperature, true), float64(0.6))
	db.Set(WebTable, samplerKey(TunableSamplingTemperature, false), float64(0.7))
	SetTunablesDB(db)
	defer SetTunablesDB(nil)

	tru, fal := true, false

	// Explicit thinking call -> thinking profile (0.6).
	var pt oaiRequest
	(&openAIClient{}).applySamplers(&pt, ChatConfig{Think: &tru})
	if pt.Temperature == nil || *pt.Temperature != 0.6 {
		t.Fatalf("thinking call should use thinking temp 0.6, got %v", pt.Temperature)
	}

	// Explicit no-think call -> non-thinking profile (0.7).
	var pn oaiRequest
	(&openAIClient{}).applySamplers(&pn, ChatConfig{Think: &fal})
	if pn.Temperature == nil || *pn.Temperature != 0.7 {
		t.Fatalf("no-think call should use non-thinking temp 0.7, got %v", pn.Temperature)
	}

	// Master no-think override forces the non-thinking profile even when the
	// caller asked to think.
	var pm oaiRequest
	(&openAIClient{disableThinking: true}).applySamplers(&pm, ChatConfig{Think: &tru})
	if pm.Temperature == nil || *pm.Temperature != 0.7 {
		t.Fatalf("master no-think override should force non-thinking temp 0.7, got %v", pm.Temperature)
	}

	// A defer (Think nil) takes the thinking profile.
	var pd oaiRequest
	(&openAIClient{}).applySamplers(&pd, ChatConfig{})
	if pd.Temperature == nil || *pd.Temperature != 0.6 {
		t.Fatalf("deferred think state should use thinking temp 0.6, got %v", pd.Temperature)
	}
}
