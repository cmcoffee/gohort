// Package replyguard is the registry of the agent loop's reply guards: the
// checks that catch a model's final reply going wrong in a known way (a
// double answer, a sentence cut off, a call announced and never made) and
// send it back to try again.
//
// They were code and nothing else: tuned against the models this deployment
// runs most, with no way to see how often each one fired, or to turn one down
// where it misfires. Here each guard has a name, a line saying what it
// catches, and settings for all tiers and for the lead and the worker tier
// apart: its mode (on, off, or shadow, which records what it would have done
// and changes nothing), how many times a turn it may ask again, and, where
// the note it sends is fixed text, that note. A built-in guard reverts to its
// shipped behaviour in one step.
//
// Settings are per TIER, not per model. A deployment runs one model per tier,
// a guard that fixes one model's habit is often right for the next, and "the
// worker" is how an admin thinks about it. Each tier setting remembers the
// model it was made on, so a model swapped in behind a tier is noticed rather
// than silently inheriting tuning made for another. Firings are counted per
// model all the same, so a swap does not mix two models' histories.
//
// A leaf package: it knows no agent loop and imports nothing of core. The
// loop registers its guards and asks Resolve at each decision point; the
// admin page reads Guards, Stats and Settings and writes Put, Clear, Revert.
package replyguard

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// Store is the slice of the database this package needs; core's Database
// satisfies it.
type Store interface {
	Get(table, key string, output interface{}) bool
	Set(table, key string, value interface{})
	Keys(table string) []string
}

// Mode is what a guard does on a tier.
type Mode string

const (
	On     Mode = "on"     // catches and corrects (the default)
	Shadow Mode = "shadow" // records what it would have done, changes nothing
	Off    Mode = "off"    // does not run
)

// Scopes a setting applies to: every tier, or one.
const (
	AllTiers = "*"
	Lead     = "lead"
	Worker   = "worker"
)

// Scopes lists the scopes in the order they are shown.
func Scopes() []string { return []string{AllTiers, Lead, Worker} }

// DefaultRetries is how many times a turn a guard may ask again, unless set.
const DefaultRetries = 2

// MaxRetries bounds a setting: past a few, a model that has not moved will not.
const MaxRetries = 5

const (
	settingsTable = "reply_guard_settings"
	legacyModes   = "reply_guard_modes" // per-model modes, before tiers
	statsTable    = "reply_guard_stats"
	tiersTable    = "reply_guard_tiers"
	// maxSamples is how many recent replies a guard keeps per model: enough to
	// judge whether it is catching the right thing, not a log.
	maxSamples = 8
	// maxSampleChars bounds a kept reply: the tail is where most of these
	// problems show, and a whole reply per firing would bloat the store.
	maxSampleChars = 600
)

// Guard describes one reply guard.
type Guard struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Desc  string `json:"desc"`
	Judge bool   `json:"judge,omitempty"` // costs a model call to decide
	// Note is the text the guard sends the model when it fires, when that
	// text is fixed; a setting may replace it. Empty for a guard whose note is
	// built from the reply it caught, which is not editable.
	Note string `json:"note,omitempty"`
	// Authored marks a guard drafted from flagged replies (authored.go)
	// rather than one written into the loop.
	Authored bool `json:"authored,omitempty"`
}

// Editable reports whether a setting may replace the guard's note.
func (g Guard) Editable() bool { return g.Authored || g.Note != "" }

// Setting is how a guard is set for one scope. Zero fields follow the scope
// above (a tier follows All tiers, All tiers follows the guard's default).
type Setting struct {
	ID      string  `json:"id"`
	Scope   string  `json:"scope"`
	Mode    Mode    `json:"mode,omitempty"`
	Note    string  `json:"note,omitempty"`
	Retries int     `json:"retries,omitempty"`
	Checks  []Check `json:"checks,omitempty"` // an authored guard's checks for this scope
	// SetOn is the model the scope was running when this was set, so a
	// different model behind the tier later can be pointed out.
	SetOn string    `json:"set_on,omitempty"`
	At    time.Time `json:"at"`
}

func (s Setting) empty() bool {
	return s.Mode == "" && s.Note == "" && s.Retries == 0 && len(s.Checks) == 0
}

// Effective is a guard's behaviour on one tier, after its settings.
type Effective struct {
	Mode    Mode
	Note    string  // "" = the guard's own note
	Retries int     // always set
	Checks  []Check // nil = the authored guard's own checks
}

// Sample is one reply a guard caught.
type Sample struct {
	At     time.Time `json:"at"`
	Text   string    `json:"text"`
	Shadow bool      `json:"shadow,omitempty"` // recorded in shadow mode: nothing was changed
}

// Stat is one guard's firings on one model, and the tier that model served.
type Stat struct {
	ID       string    `json:"id"`
	Model    string    `json:"model"`
	Tier     string    `json:"tier,omitempty"`
	Acted    int       `json:"acted"`
	Shadowed int       `json:"shadowed"`
	Last     time.Time `json:"last"`
	Samples  []Sample  `json:"samples,omitempty"`
}

var (
	mu       sync.Mutex
	store    Store
	guards   []Guard
	byID     = map[string]int{}
	settings map[string]Setting // "<id>|<scope>"; nil until loaded
	current  = map[string]string{}
)

// SetStore wires persistence. Until it is called, every guard is on and
// nothing is recorded.
func SetStore(s Store) {
	mu.Lock()
	for id := range authored {
		unregisterLocked(id)
	}
	store, settings, authored = s, nil, nil
	current = map[string]string{}
	loadAuthoredLocked()
	mu.Unlock()
}

// Register adds a guard. Registering an id again replaces its description.
func Register(g Guard) {
	mu.Lock()
	defer mu.Unlock()
	registerLocked(g)
}

func registerLocked(g Guard) {
	if i, ok := byID[g.ID]; ok {
		guards[i] = g
		return
	}
	byID[g.ID] = len(guards)
	guards = append(guards, g)
}

// unregisterLocked takes a guard out of the list (an authored guard switched
// off or deleted).
func unregisterLocked(id string) {
	i, ok := byID[id]
	if !ok {
		return
	}
	guards = append(guards[:i], guards[i+1:]...)
	byID = map[string]int{}
	for j, g := range guards {
		byID[g.ID] = j
	}
}

// Guards lists the registered guards in the order they were registered.
func Guards() []Guard {
	mu.Lock()
	defer mu.Unlock()
	loadAuthoredLocked()
	return append([]Guard(nil), guards...)
}

// Lookup returns one registered guard.
func Lookup(id string) (Guard, bool) {
	mu.Lock()
	defer mu.Unlock()
	loadAuthoredLocked()
	i, ok := byID[id]
	if !ok {
		return Guard{}, false
	}
	return guards[i], true
}

// Known reports whether a guard id is registered.
func Known(id string) bool {
	_, ok := Lookup(id)
	return ok
}

// NormalizeModel is the name a model is counted under: lower case, without a
// provider's "models/" prefix, "unknown" when empty.
func NormalizeModel(model string) string {
	m := strings.ToLower(strings.TrimSpace(model))
	m = strings.TrimPrefix(m, "models/")
	if m == "" {
		return "unknown"
	}
	return m
}

// NormalizeTier is a tier name as a scope: lead, worker, or "" when unknown.
func NormalizeTier(tier string) string {
	switch strings.ToLower(strings.TrimSpace(tier)) {
	case Lead:
		return Lead
	case Worker:
		return Worker
	}
	return ""
}

func key(id, scope string) string { return id + "|" + scope }

func loadSettingsLocked() {
	if settings != nil {
		return
	}
	settings = map[string]Setting{}
	if store == nil {
		return
	}
	for _, k := range store.Keys(settingsTable) {
		var s Setting
		if store.Get(settingsTable, k, &s) && s.ID != "" && !s.empty() {
			settings[k] = s
		}
	}
	// A guard's all-models mode from before tiers carries over. A per-model
	// one does not: it named a model, not a tier, and the tier it served is
	// not recorded.
	for _, k := range store.Keys(legacyModes) {
		id, scope, _ := strings.Cut(k, "|")
		if scope != AllTiers {
			continue
		}
		var m Mode
		if store.Get(legacyModes, k, &m) && m != "" {
			if _, set := settings[key(id, AllTiers)]; !set {
				settings[key(id, AllTiers)] = Setting{ID: id, Scope: AllTiers, Mode: m}
			}
		}
		store.Set(legacyModes, k, Mode(""))
	}
	for _, k := range store.Keys(tiersTable) {
		var m string
		if store.Get(tiersTable, k, &m) && m != "" {
			current[k] = m
		}
	}
}

// Resolve is a guard's behaviour on a tier: the tier's own setting, then the
// all-tiers setting, then the defaults. tier "" reads the all-tiers setting.
func Resolve(id, tier string) Effective {
	mu.Lock()
	defer mu.Unlock()
	loadSettingsLocked()
	eff := Effective{Mode: On, Retries: DefaultRetries}
	apply := func(s Setting) {
		if s.Mode != "" {
			eff.Mode = s.Mode
		}
		if s.Note != "" {
			eff.Note = s.Note
		}
		if s.Retries > 0 {
			eff.Retries = s.Retries
		}
		if len(s.Checks) > 0 {
			eff.Checks = s.Checks
		}
	}
	if s, ok := settings[key(id, AllTiers)]; ok {
		apply(s)
	}
	if t := NormalizeTier(tier); t != "" {
		if s, ok := settings[key(id, t)]; ok {
			apply(s)
		}
	}
	return eff
}

// ModeFor is what a guard does on a tier.
func ModeFor(id, tier string) Mode { return Resolve(id, tier).Mode }

// DefaultMode is a guard's mode for all tiers.
func DefaultMode(id string) Mode { return Resolve(id, "").Mode }

// SettingFor returns the stored setting for one scope, if any.
func SettingFor(id, scope string) (Setting, bool) {
	mu.Lock()
	defer mu.Unlock()
	loadSettingsLocked()
	s, ok := settings[key(id, scope)]
	return s, ok
}

// Settings lists every stored setting.
func Settings() []Setting {
	mu.Lock()
	defer mu.Unlock()
	loadSettingsLocked()
	out := make([]Setting, 0, len(settings))
	for _, s := range settings {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].ID != out[j].ID {
			return out[i].ID < out[j].ID
		}
		return out[i].Scope < out[j].Scope
	})
	return out
}

// Customized reports whether a guard has any setting at all.
func Customized(id string) bool {
	for _, s := range Scopes() {
		if _, ok := SettingFor(id, s); ok {
			return true
		}
	}
	return false
}

func validScope(scope string) bool { return scope == AllTiers || scope == Lead || scope == Worker }

// Put stores a setting, merged over what the scope already has: a zero field
// leaves that part as it was. It refuses an unknown guard or scope, a mode
// that is not one, retries out of range, a note on a guard whose note is not
// editable, and checks on a guard that was not drafted or that do not
// validate.
func Put(s Setting) error {
	g, ok := Lookup(s.ID)
	if !ok {
		return fmt.Errorf("no reply guard %q", s.ID)
	}
	if !validScope(s.Scope) {
		return fmt.Errorf("scope must be all tiers, lead or worker, not %q", s.Scope)
	}
	switch s.Mode {
	case "", On, Off, Shadow:
	default:
		return fmt.Errorf("mode must be on, off or shadow, not %q", s.Mode)
	}
	if s.Retries < 0 || s.Retries > MaxRetries {
		return fmt.Errorf("retries must be between 1 and %d", MaxRetries)
	}
	if strings.TrimSpace(s.Note) != "" && !g.Editable() {
		return fmt.Errorf("%s writes its note from the reply it caught, so the note cannot be replaced", g.Name)
	}
	if len(s.Checks) > 0 {
		if !g.Authored {
			return fmt.Errorf("%s is built in: its checks are code", g.Name)
		}
		if err := Validate(s.Checks); err != nil {
			return err
		}
	}
	mu.Lock()
	defer mu.Unlock()
	loadSettingsLocked()
	k := key(s.ID, s.Scope)
	merged := settings[k]
	merged.ID, merged.Scope = s.ID, s.Scope
	if s.Mode != "" {
		merged.Mode = s.Mode
	}
	if note := strings.TrimSpace(s.Note); note != "" {
		merged.Note = note
	}
	if s.Retries > 0 {
		merged.Retries = s.Retries
	}
	if len(s.Checks) > 0 {
		merged.Checks = s.Checks
	}
	merged.At = time.Now()
	if s.Scope != AllTiers {
		merged.SetOn = current[s.Scope]
	}
	settings[k] = merged
	if store != nil {
		store.Set(settingsTable, k, merged)
	}
	return nil
}

// ClearPart resets one part of a scope's setting ("mode", "note", "retries"
// or "checks") so it follows the scope above again.
func ClearPart(id, scope, part string) {
	mu.Lock()
	defer mu.Unlock()
	loadSettingsLocked()
	k := key(id, scope)
	s, ok := settings[k]
	if !ok {
		return
	}
	switch part {
	case "mode":
		s.Mode = ""
	case "note":
		s.Note = ""
	case "retries":
		s.Retries = 0
	case "checks":
		s.Checks = nil
	}
	if s.empty() {
		delete(settings, k)
		if store != nil {
			store.Set(settingsTable, k, Setting{})
		}
		return
	}
	settings[k] = s
	if store != nil {
		store.Set(settingsTable, k, s)
	}
}

// Clear removes a scope's setting entirely, so it follows the scope above.
func Clear(id, scope string) {
	mu.Lock()
	defer mu.Unlock()
	loadSettingsLocked()
	clearLocked(id, scope)
}

func clearLocked(id, scope string) {
	k := key(id, scope)
	if _, ok := settings[k]; !ok {
		return
	}
	delete(settings, k)
	if store != nil {
		store.Set(settingsTable, k, Setting{}) // an empty record reads as unset
	}
}

// Revert puts a guard back as it shipped: every scope's setting removed.
func Revert(id string) {
	mu.Lock()
	defer mu.Unlock()
	loadSettingsLocked()
	for _, s := range Scopes() {
		clearLocked(id, s)
	}
}

// NoteModel records which model a tier is running, as the loop sees it.
func NoteModel(tier, model string) {
	tier, model = NormalizeTier(tier), NormalizeModel(model)
	if tier == "" || model == "unknown" {
		return
	}
	mu.Lock()
	defer mu.Unlock()
	loadSettingsLocked()
	if current[tier] == model {
		return
	}
	current[tier] = model
	if store != nil {
		store.Set(tiersTable, tier, model)
	}
}

// CurrentModel is the model a tier was last seen running, "" when not yet.
func CurrentModel(tier string) string {
	mu.Lock()
	defer mu.Unlock()
	loadSettingsLocked()
	return current[NormalizeTier(tier)]
}

// Record tallies one firing: acted when the guard corrected the reply, not
// when it only recorded it in shadow mode. The reply's tail is kept as a
// sample, and the tier the model was serving is noted with it.
func Record(id, tier, model, reply string, acted bool) {
	model = NormalizeModel(model)
	mu.Lock()
	defer mu.Unlock()
	if store == nil {
		return
	}
	k := key(id, model)
	var st Stat
	store.Get(statsTable, k, &st)
	st.ID, st.Model = id, model
	if t := NormalizeTier(tier); t != "" {
		st.Tier = t
	}
	if acted {
		st.Acted++
	} else {
		st.Shadowed++
	}
	st.Last = time.Now()
	st.Samples = append([]Sample{{At: st.Last, Text: tail(reply, maxSampleChars), Shadow: !acted}}, st.Samples...)
	if len(st.Samples) > maxSamples {
		st.Samples = st.Samples[:maxSamples]
	}
	store.Set(statsTable, k, st)
}

// Stats lists every guard's firings per model, most recent first.
func Stats() []Stat {
	mu.Lock()
	defer mu.Unlock()
	if store == nil {
		return nil
	}
	var out []Stat
	for _, k := range store.Keys(statsTable) {
		var st Stat
		if store.Get(statsTable, k, &st) && st.ID != "" {
			out = append(out, st)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Last.After(out[j].Last) })
	return out
}

// tail keeps the last n characters of s, marked when cut.
func tail(s string, n int) string {
	s = strings.TrimSpace(s)
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return "…" + string(r[len(r)-n:])
}
