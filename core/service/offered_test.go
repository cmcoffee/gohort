package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/media"
)

// offeredTestRuns numbers the runs of a test that registers choices.
var offeredTestRuns atomic.Int64

// A value in use that its provider no longer offers is listed once per
// value with every use, an app's uses beside the source's own; a provider
// with no list is not judged; mapping takes only a value now offered and
// changes every use of the old one.
func TestAStaleChoiceIsListedAndMappedEverywhere(t *testing.T) {
	forgetStaleChoices(t)
	// Registered for good, so its uses are held only while this test runs.
	var live atomic.Bool
	live.Store(true)
	t.Cleanup(func() { live.Store(false) })
	// Its own ids each run, since a registration outlives the test.
	n := strconv.FormatInt(offeredTestRuns.Add(1), 10)
	listed, unlisted := "test.listed."+n, "test.unlisted."+n
	offered := []string{"a", "b"}
	values := map[string]string{"one": "a", "two": "x", "three": "x"}
	use := func(where string) ChoiceUse {
		return ChoiceUse{Where: where, Value: values[where], Set: func(to string) error { values[where] = to; return nil }}
	}
	uses := func(u ...ChoiceUse) []ChoiceUse {
		if !live.Load() {
			return nil
		}
		return u
	}
	RegisterOfferedChoices(func() []OfferedChoice {
		return []OfferedChoice{
			{ID: listed, Label: "Test choice", Page: "Test",
				Offered: func(context.Context) ([]string, bool, error) { return offered, true, nil },
				Uses:    func() []ChoiceUse { return uses(use("one"), use("two")) }},
			{ID: unlisted, Label: "Unlisted",
				Offered: func(context.Context) ([]string, bool, error) { return nil, false, nil },
				Uses:    func() []ChoiceUse { return uses(ChoiceUse{Where: "w", Value: "anything"}) }},
		}
	})
	RegisterChoiceUses(listed, func() []ChoiceUse { return uses(use("three")) })
	mine := func() []StaleChoice {
		var out []StaleChoice
		for _, r := range StaleChoices(context.Background()) {
			if r.Choice == listed || r.Choice == unlisted {
				out = append(out, r)
			}
		}
		return out
	}
	rows := mine()
	if len(rows) != 1 || rows[0].Value != "x" || len(rows[0].Uses) != 2 || rows[0].Choice != listed {
		t.Fatalf("stale rows: %+v", rows)
	}
	if _, err := MapStaleChoice(context.Background(), rows[0].Key, "c"); err == nil {
		t.Error("mapped to a value the provider does not offer")
	}
	if n, err := MapStaleChoice(context.Background(), rows[0].Key, "b"); err != nil || n != 2 || values["two"] != "b" || values["three"] != "b" || values["one"] != "a" {
		t.Fatalf("map: %d %v %v", n, err, values)
	}
	if rows := mine(); len(rows) != 0 {
		t.Errorf("still stale after mapping: %+v", rows)
	}
	// Changed by hand, not by a mapping: the card drops it at once, without
	// asking the provider again.
	values["one"] = "y"
	StaleChoices(context.Background())
	values["one"] = "a"
	for _, r := range CachedStaleChoices() {
		if r.Choice == listed {
			t.Errorf("a value nothing holds any more is still on the card: %+v", r)
		}
	}
}

// A mix of voices (Kokoro's af_bella(2)+af_sky) is offered when every voice
// in it is; a mix with one the server dropped is listed like any other name.
func TestAVoiceMixIsOfferedWhenEachVoiceIs(t *testing.T) {
	forgetStaleChoices(t)
	// Registered for good, so it holds its uses only while this test runs:
	// a later test's check (StaleSettings starts one) must not find them.
	var live atomic.Bool
	live.Store(true)
	t.Cleanup(func() { live.Store(false) })
	RegisterOfferedChoices(func() []OfferedChoice {
		return []OfferedChoice{{ID: "test.mix", Label: "Mix", Parts: media.SpeechVoiceParts,
			Offered: func(context.Context) ([]string, bool, error) { return []string{"af_bella", "af_sky"}, true, nil },
			Uses: func() []ChoiceUse {
				if !live.Load() {
					return nil
				}
				return []ChoiceUse{
					{Where: "even", Value: "af_bella+af_sky"},
					{Where: "weighted", Value: "af_bella(2) + af_sky(1)"},
					{Where: "dropped", Value: "af_bella+af_gone"},
				}
			}}}
	})
	var got []string
	for _, r := range StaleChoices(context.Background()) {
		if r.Choice == "test.mix" {
			got = append(got, r.Value)
		}
	}
	if len(got) != 1 || got[0] != "af_bella+af_gone" {
		t.Errorf("stale mixes = %q, want only the one with a dropped voice", got)
	}
}

// Moving the speech connector to a Chatterbox-like server: its Model lists
// the server's models (the lookup), the template's Kokoro defaults read as
// stale, and mapping the model keeps it on the connection.
func TestAConnectionSettingThatListsOptionsIsCheckedAndMapped(t *testing.T) {
	forgetStaleChoices(t)
	world(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			w.Write([]byte(`{"object":"list","data":[{"id":"chatterbox","object":"model"}]}`))
		case "/v1/audio/voices":
			w.Write([]byte(`{"voices":["narrator"]}`))
		case "/v1/audio/speech":
			w.Write(ttsClip)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	install(t, "local-speech", templateSpec(t, "openai-compatible-speech", srv.URL+"/v1"))

	if models, err := ListOptions(context.Background(), "local-speech", "model"); err != nil || len(models) != 1 || models[0] != "chatterbox" {
		t.Fatalf("the model lookup: %v %v", models, err)
	}
	stale := map[string]StaleChoice{}
	for _, r := range StaleChoices(context.Background()) {
		if r.Choice == "setting/local-speech/model" || r.Choice == "setting/local-speech/voice" {
			stale[r.Value] = r
		}
	}
	if stale["kokoro"].Key == "" || stale["af_heart"].Key == "" {
		t.Fatalf("the Kokoro defaults should read as stale on this server: %+v", stale)
	}
	if n, err := MapStaleChoice(context.Background(), stale["kokoro"].Key, "chatterbox"); err != nil || n != 1 {
		t.Fatalf("map the model: %d %v", n, err)
	}
	c, _ := GetConnector(RootDB, "local-speech")
	if got := ConnectionConfig(RootDB, c)["model"]; got != "chatterbox" {
		t.Errorf("the model kept: %q", got)
	}
	for _, r := range StaleChoices(context.Background()) {
		if r.Choice == "setting/local-speech/model" {
			t.Errorf("the model is still stale: %+v", r)
		}
	}
}

// forgetStaleChoices empties the last check when the test ends: what it found
// is the dashboard's, and must not read as stale in a later test.
func forgetStaleChoices(t *testing.T) {
	t.Cleanup(func() {
		staleCache.Lock()
		staleCache.rows, staleCache.at = nil, time.Time{}
		staleCache.Unlock()
	})
}
