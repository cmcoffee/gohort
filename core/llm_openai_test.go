package core

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"os"
	"strings"
	"testing"

	"github.com/cmcoffee/gohort/core/flows"
)

// An image the model cannot read is left out with a note in its place, not
// sent as a JPEG it is not: vLLM fails the whole turn over it.
func TestAnUnreadableImageIsLeftOutAndNamed(t *testing.T) {
	var pngBuf bytes.Buffer
	png.Encode(&pngBuf, image.NewRGBA(image.Rect(0, 0, 4, 4)))
	heic := append([]byte{0, 0, 0, 24}, []byte("ftypheic\x00\x00\x00\x00mif1heic")...)
	heic = append(heic, make([]byte, 64)...)

	var parts []struct {
		Type     string `json:"type"`
		Text     string `json:"text"`
		ImageURL *struct {
			URL string `json:"url"`
		} `json:"image_url"`
	}
	if err := json.Unmarshal(oaiVisionContent("what is this?", [][]byte{heic, pngBuf.Bytes()}), &parts); err != nil {
		t.Fatal(err)
	}
	if len(parts) != 2 {
		t.Fatalf("want the text and the one readable image, got %d parts", len(parts))
	}
	if !strings.Contains(parts[0].Text, "HEIC") || !strings.HasSuffix(parts[0].Text, "what is this?") {
		t.Errorf("the note should name the format ahead of the message: %q", parts[0].Text)
	}
	if !strings.Contains(parts[0].Text, "System Hooks") {
		t.Errorf("the note should say where an administrator fixes it: %q", parts[0].Text)
	}
	if parts[1].ImageURL == nil || !strings.HasPrefix(parts[1].ImageURL.URL, "data:image/png;base64,") {
		t.Errorf("the readable image should go as itself: %+v", parts[1])
	}

	// All readable: no note.
	json.Unmarshal(oaiVisionContent("hi", [][]byte{pngBuf.Bytes()}), &parts)
	if parts[0].Text != "hi" {
		t.Errorf("no note without an unreadable image: %q", parts[0].Text)
	}
}

// adaptImages gives conversions a folder to run in, outside /tmp (which the
// sandbox replaces), and an adapted image conversion under test; both go
// when the test ends.
func adaptImages(t *testing.T, name, command string) {
	t.Helper()
	cache, err := os.UserCacheDir()
	if err != nil {
		t.Skip("no folder outside /tmp to run a conversion in")
	}
	base, err := os.MkdirTemp(cache, "gohort-core-convert-test-")
	if err != nil {
		t.Skip(err)
	}
	flows.SetWorkBase(base)
	if err := flows.SetAdapted(name, flows.Adapted{Takes: []string{flows.Any}, Becomes: flows.KindImage,
		Command: command, TimeoutSecs: 20}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		flows.RemoveAdapted(name)
		flows.SetWorkBase("")
		os.RemoveAll(base)
	})
}

// An image the model cannot read is converted when the admin has adapted a
// conversion for it, and reaches the model as what the conversion gave.
func TestAnUnreadableImageIsConverted(t *testing.T) {
	var pngBuf bytes.Buffer
	png.Encode(&pngBuf, image.NewRGBA(image.Rect(0, 0, 4, 4)))
	src := append([]byte("GOHORTX!"), pngBuf.Bytes()...)
	adaptImages(t, "test-dd", "dd if={in} of={out} bs=8 skip=1")
	if _, err := flows.Convert(context.Background(), src, "", flows.KindImage); err != nil && strings.Contains(err.Error(), "sandbox") {
		t.Skipf("no sandbox here: %v", err)
	}

	var parts []struct {
		Text     string `json:"text"`
		ImageURL *struct {
			URL string `json:"url"`
		} `json:"image_url"`
	}
	if err := json.Unmarshal(oaiVisionContent("look", [][]byte{src}), &parts); err != nil {
		t.Fatal(err)
	}
	if len(parts) != 2 || parts[1].ImageURL == nil || parts[1].ImageURL.URL != "data:image/png;base64,"+base64.StdEncoding.EncodeToString(pngBuf.Bytes()) {
		t.Fatalf("want the text and the hook's PNG, got %+v", parts)
	}
	if parts[0].Text != "look" {
		t.Errorf("no note when it is converted: %q", parts[0].Text)
	}
}

// A conversion that fails is kept as failed, so the loop's later rounds do
// not run a broken command again.
func TestAFailedConversionIsNotRunEveryRound(t *testing.T) {
	src := append([]byte("GOHORTY!"), make([]byte, 64)...)
	adaptImages(t, "test-fails", "sh -c 'exit 3' {in} {out}")
	resizeImage(src)
	key := fmt.Sprintf("%x:%d:%d", sha256.Sum256(src), visionMaxDim(), visionJQQual())
	if _, ok := resizedImages.get(key); !ok {
		t.Error("the failed run should be kept")
	}
}

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
