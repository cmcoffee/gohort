package service

// Offered choices: a stored value that has to be one of what a provider
// offers. A voice is one (Kokoro's af_heart), a model another (kokoro, a
// Gemini image model). Swap the provider behind it, Kokoro for Chatterbox,
// and the name means nothing to the new one: a call naming it fails, and
// nothing said so. A connection that is gone is already found (StaleWhy);
// this is the level below it, a name inside one that is still there.
//
// One pattern for every kind of it:
//
//   - Declared once. A source (RegisterOfferedChoices) says, for each choice,
//     what the provider offers now (Offered) and where the value is used
//     (Uses), each use with a way to change it. Built in here: every
//     connection setting that lists its options (options_from, a model
//     field's List), and the speech voice. An app adds the uses it keeps
//     (an agent's own voice) with RegisterChoiceUses.
//   - Checked against what is offered now. StaleChoices lists each used
//     value the provider no longer offers. Asking a provider is a network
//     call, so what was found is kept for a while (CachedStaleChoices), for
//     the dashboard's card to read without asking.
//   - Mapped when it is swapped. MapStaleChoice changes every use of an old
//     value to one the provider offers: what the owner picks, never done
//     for them, so nothing is migrated on start.
//   - A call does not depend on it: speech falls back to the default voice
//     when its voice is refused (media.SpeakWith), so a stale name costs the
//     voice, not the reply.

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/media"
)

// ChoiceUse is one place a choice's value is used.
type ChoiceUse struct {
	Where string                // what a person reads it as: "the Speech (TTS) voice", "agent Briefer's voice"
	Value string                // the value it holds
	Set   func(to string) error // changes it
}

// OfferedChoice is one kind of value a provider offers, and its uses.
type OfferedChoice struct {
	ID    string // stable: "speech.voice", "setting/<connection>/<key>"
	Label string // "Speech voice", "local-speech's model"
	Page  string // the admin page it is chosen on, for the card
	// Offered is what the provider offers now; listed false when it offers
	// no list, and then nothing can be judged stale.
	Offered func(ctx context.Context) (values []string, listed bool, err error)
	Uses    func() []ChoiceUse
	// Parts, when set, is the names a value is made of, each of which must
	// be offered: a mix of voices is offered when every voice in it is.
	Parts func(value string) []string
}

// offers reports whether a value is offered: listed whole, or made only of
// names that are.
func (ch OfferedChoice) offers(have map[string]bool, v string) bool {
	if have[v] {
		return true
	}
	if ch.Parts == nil {
		return false
	}
	parts := ch.Parts(v)
	for _, p := range parts {
		if !have[p] {
			return false
		}
	}
	return len(parts) > 0
}

var (
	offeredMu      sync.Mutex
	offeredSources []func() []OfferedChoice
	extraUses      = map[string][]func() []ChoiceUse{}
)

// RegisterOfferedChoices adds a source of choices, read whenever they are
// checked (so it may list a different set each time).
func RegisterOfferedChoices(src func() []OfferedChoice) {
	offeredMu.Lock()
	defer offeredMu.Unlock()
	offeredSources = append(offeredSources, src)
}

// RegisterChoiceUses adds uses an app keeps to the choice named id: an
// agent's own voice, checked against the voices speech offers.
func RegisterChoiceUses(id string, uses func() []ChoiceUse) {
	offeredMu.Lock()
	defer offeredMu.Unlock()
	extraUses[id] = append(extraUses[id], uses)
}

// offeredChoices is every choice, with the uses apps added.
func offeredChoices() []OfferedChoice {
	offeredMu.Lock()
	srcs := append([]func() []OfferedChoice(nil), offeredSources...)
	extra := map[string][]func() []ChoiceUse{}
	for k, v := range extraUses {
		extra[k] = append([]func() []ChoiceUse(nil), v...)
	}
	offeredMu.Unlock()
	var out []OfferedChoice
	for _, src := range srcs {
		for _, ch := range src() {
			if adds := extra[ch.ID]; len(adds) > 0 {
				own := ch.Uses
				ch.Uses = func() []ChoiceUse {
					var all []ChoiceUse
					if own != nil {
						all = own()
					}
					for _, f := range adds {
						all = append(all, f()...)
					}
					return all
				}
			}
			out = append(out, ch)
		}
	}
	return out
}

// StaleChoice is one value in use that its provider no longer offers.
type StaleChoice struct {
	Key     string   `json:"_id"` // names this row: the choice and the value
	Choice  string   `json:"choice"`
	Label   string   `json:"label"`
	Page    string   `json:"page"`
	Value   string   `json:"value"`
	Uses    []string `json:"uses"`
	Offered []string `json:"-"`
}

// staleKey names a stale value of a choice, safe in a URL.
func staleKey(choice, value string) string {
	h := sha1.Sum([]byte(choice + "\x00" + value))
	return hex.EncodeToString(h[:8])
}

// offeredTimeout bounds asking one provider what it offers.
const offeredTimeout = 8 * time.Second

// StaleChoices asks each provider what it offers now and lists every value
// in use it does not, one row per choice and value with every use. A
// provider that offers no list, or cannot be reached, is not judged: a name
// it would refuse is caught when a call is refused, not guessed at here.
func StaleChoices(ctx context.Context) []StaleChoice {
	var out []StaleChoice
	for _, ch := range offeredChoices() {
		if ch.Uses == nil || ch.Offered == nil {
			continue
		}
		uses := ch.Uses()
		if len(uses) == 0 {
			continue
		}
		octx, cancel := context.WithTimeout(ctx, offeredTimeout)
		values, listed, err := ch.Offered(octx)
		cancel()
		if err != nil || !listed {
			if err != nil {
				Debug("[offered] what %s offers: %v", ch.Label, err)
			}
			continue
		}
		have := map[string]bool{}
		for _, v := range values {
			have[strings.TrimSpace(v)] = true
		}
		byValue := map[string]*StaleChoice{}
		for _, u := range uses {
			v := strings.TrimSpace(u.Value)
			if v == "" || ch.offers(have, v) {
				continue
			}
			row := byValue[v]
			if row == nil {
				row = &StaleChoice{Key: staleKey(ch.ID, v), Choice: ch.ID, Label: ch.Label, Page: ch.Page, Value: v, Offered: values}
				byValue[v] = row
			}
			row.Uses = append(row.Uses, u.Where)
		}
		for _, row := range byValue {
			out = append(out, *row)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Label != out[j].Label {
			return out[i].Label < out[j].Label
		}
		return out[i].Value < out[j].Value
	})
	staleCache.Lock()
	staleCache.rows, staleCache.at = out, time.Now()
	staleCache.Unlock()
	return out
}

// staleCache keeps the last StaleChoices, for a page that must not ask every
// provider each time it loads.
var staleCache struct {
	sync.Mutex
	rows    []StaleChoice
	at      time.Time
	running bool
}

// staleCacheFresh is how long a check stands before CachedStaleChoices asks
// again, in the background.
const staleCacheFresh = 30 * time.Minute

// CachedStaleChoices is the last check's rows, at once, and starts a new
// check in the background when that one is old (or there has been none).
//
// A row is shown only while something still holds its value: where each is
// used is read again here, which asks no provider, so a name changed by hand
// (a voice picked again on the Speech page) leaves the card at once rather
// than when the next check runs.
func CachedStaleChoices() []StaleChoice {
	rows := stillHeld(cachedStaleRows())
	return rows
}

// cachedStaleRows is the last check's rows, starting a new check when that
// one is old.
func cachedStaleRows() []StaleChoice {
	staleCache.Lock()
	rows, at, running := staleCache.rows, staleCache.at, staleCache.running
	refresh := !running && time.Since(at) > staleCacheFresh
	if refresh {
		staleCache.running = true
	}
	staleCache.Unlock()
	if refresh {
		go func() {
			defer func() {
				staleCache.Lock()
				staleCache.running = false
				staleCache.Unlock()
			}()
			StaleChoices(context.Background())
		}()
	}
	return rows
}

// stillHeld keeps the rows whose value something still holds, each with the
// uses that hold it now.
func stillHeld(rows []StaleChoice) []StaleChoice {
	if len(rows) == 0 {
		return nil
	}
	uses := map[string][]ChoiceUse{}
	for _, ch := range offeredChoices() {
		if ch.Uses != nil {
			uses[ch.ID] = ch.Uses()
		}
	}
	var out []StaleChoice
	for _, r := range rows {
		var where []string
		for _, u := range uses[r.Choice] {
			if strings.TrimSpace(u.Value) == r.Value {
				where = append(where, u.Where)
			}
		}
		if len(where) > 0 {
			r.Uses = where
			out = append(out, r)
		}
	}
	return out
}

// StaleChoiceOptions is what a stale row's provider offers now, to map it to.
func StaleChoiceOptions(ctx context.Context, key string) ([]string, error) {
	for _, row := range StaleChoices(ctx) {
		if row.Key == key {
			return row.Offered, nil
		}
	}
	return nil, fmt.Errorf("that name is no longer stale: it is offered again, or nothing uses it")
}

// MapStaleChoice changes every use of the stale value key names to to, which
// the provider has to offer, and says how many it changed.
func MapStaleChoice(ctx context.Context, key, to string) (int, error) {
	to = strings.TrimSpace(to)
	if to == "" {
		return 0, fmt.Errorf("choose what to use instead")
	}
	var row *StaleChoice
	for _, r := range StaleChoices(ctx) {
		if r.Key == key {
			r := r
			row = &r
			break
		}
	}
	if row == nil {
		return 0, fmt.Errorf("that name is no longer stale: it is offered again, or nothing uses it")
	}
	offered := false
	for _, v := range row.Offered {
		if strings.TrimSpace(v) == to {
			offered = true
		}
	}
	if !offered {
		return 0, fmt.Errorf("%s is not one of what it offers now", to)
	}
	n := 0
	for _, ch := range offeredChoices() {
		if ch.ID != row.Choice || ch.Uses == nil {
			continue
		}
		for _, u := range ch.Uses() {
			if strings.TrimSpace(u.Value) != row.Value || u.Set == nil {
				continue
			}
			if err := u.Set(to); err != nil {
				return n, fmt.Errorf("%s: %v", u.Where, err)
			}
			n++
		}
	}
	Log("[offered] %s: %s -> %s in %d place(s)", row.Label, row.Value, to, n)
	StaleChoices(ctx)
	return n, nil
}

func init() {
	RegisterOfferedChoices(settingChoices)
	RegisterOfferedChoices(speechVoiceChoice)
}

// settingChoices are the connection settings that list their options
// (options_from): each one's value, its spec's default when none is kept,
// against what the connection lists now.
func settingChoices() []OfferedChoice {
	var out []OfferedChoice
	for _, c := range ListConnectors(RootDB) {
		if c.Kind != Kind {
			continue
		}
		for _, st := range SettingsFor(c, "") {
			if st.Field.OptionsFrom == "" || st.Field.IsSecret() {
				continue
			}
			c, st := c, st
			out = append(out, OfferedChoice{
				ID:    "setting/" + c.Name + "/" + st.Key,
				Label: c.Name + "'s " + strings.ToLower(st.Field.Title()),
				Page:  "Connectors",
				Offered: func(ctx context.Context) ([]string, bool, error) {
					values, err := ListOptions(ctx, c.Name, st.Key)
					return values, err == nil && len(values) > 0, err
				},
				Uses: func() []ChoiceUse {
					now, ok := GetConnector(RootDB, c.Name)
					if !ok {
						return nil
					}
					v := ConnectionConfig(RootDB, now)[st.Key]
					if strings.TrimSpace(v) == "" {
						v = st.Field.Default
					}
					return []ChoiceUse{{
						Where: "the " + c.Name + " connection's " + strings.ToLower(st.Field.Title()),
						Value: v,
						Set: func(to string) error {
							cur, ok := GetConnector(RootDB, c.Name)
							if !ok {
								return fmt.Errorf("the connection %s is gone", c.Name)
							}
							values := ConnectionConfig(RootDB, cur)
							if values == nil {
								values = map[string]string{}
							}
							values[st.Key] = to
							return SaveSettings(cur, "", values, nil)
						},
					}}
				},
			})
		}
	}
	return out
}

// StaleChoicesPage is the admin section the stale names are listed and
// mapped on, and the page the dashboard's card names for them.
const StaleChoicesPage = "Names no longer offered"

// SpeechVoiceChoice is the choice id of the voice speech speaks in: the
// deployment's, and any an app adds (an agent's own).
const SpeechVoiceChoice = "speech.voice"

// speechVoiceChoice is the voice speech speaks in, against the voices the
// speech connector or peer offers.
func speechVoiceChoice() []OfferedChoice {
	return []OfferedChoice{{
		ID:    SpeechVoiceChoice,
		Label: "Speech voice",
		Page:  "Speech (TTS)",
		Offered: func(ctx context.Context) ([]string, bool, error) {
			cfg := media.GetSpeechConfig()
			if !cfg.Enabled {
				return nil, false, nil
			}
			var ids []string
			for _, v := range media.SpeechVoices(ctx, cfg) {
				ids = append(ids, v.ID)
			}
			return ids, len(ids) > 0, nil
		},
		Parts: media.SpeechVoiceParts,
		Uses: func() []ChoiceUse {
			cfg := media.GetSpeechConfig()
			if !cfg.Enabled {
				return nil
			}
			return []ChoiceUse{{
				Where: "the Speech (TTS) voice",
				Value: cfg.Voice,
				Set: func(to string) error {
					// The stored record, as the Speech page saves it, not
					// the resolved one a read hands out.
					var stored media.SpeechConfig
					if RootDB == nil || !RootDB.Get(media.SpeechTable, "current", &stored) {
						return fmt.Errorf("speech has no saved settings")
					}
					stored.Voice = to
					if err := RootDB.TrySet(media.SpeechTable, "current", stored); err != nil {
						return err
					}
					media.SetSpeechConfig(stored)
					return nil
				},
			}}
		},
	}}
}
