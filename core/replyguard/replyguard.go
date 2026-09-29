// Package replyguard is the registry of the agent loop's reply guards: the
// checks that catch a model's final reply going wrong in a known way (a
// double answer, a sentence cut off, a call announced and never made) and
// send it back to try again.
//
// They were code and nothing else: tuned against the models this deployment
// runs most, with no way to see how often each one fired, on which model, or
// to turn one down where it misfires. Here each guard has a name, a line
// saying what it catches, a mode per model (on, off, or shadow, which records
// what it would have done and changes nothing), and a tally of its firings
// with the last few replies it caught.
//
// A leaf package: it knows no agent loop and imports nothing of core. The
// loop registers its guards and asks ModeFor at each decision point; the
// admin page reads Guards, Stats and SetMode.
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

// Mode is what a guard does on a model.
type Mode string

const (
	On     Mode = "on"     // catches and corrects (the default)
	Shadow Mode = "shadow" // records what it would have done, changes nothing
	Off    Mode = "off"    // does not run
)

// AllModels is the model key for a guard's default across every model.
const AllModels = "*"

const (
	modesTable = "reply_guard_modes"
	statsTable = "reply_guard_stats"
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
	// Authored marks a guard drafted from flagged replies (authored.go)
	// rather than one written into the loop.
	Authored bool `json:"authored,omitempty"`
}

// Sample is one reply a guard caught.
type Sample struct {
	At     time.Time `json:"at"`
	Text   string    `json:"text"`
	Shadow bool      `json:"shadow,omitempty"` // recorded in shadow mode: nothing was changed
}

// Stat is one guard's firings on one model.
type Stat struct {
	ID       string    `json:"id"`
	Model    string    `json:"model"`
	Acted    int       `json:"acted"`
	Shadowed int       `json:"shadowed"`
	Last     time.Time `json:"last"`
	Samples  []Sample  `json:"samples,omitempty"`
}

var (
	mu     sync.Mutex
	store  Store
	guards []Guard
	byID   = map[string]int{}
	modes  map[string]Mode // "<id>|<model>" -> mode; nil until loaded
)

// SetStore wires persistence. Until it is called, every guard is on and
// nothing is recorded.
func SetStore(s Store) {
	mu.Lock()
	for id := range authored {
		unregisterLocked(id)
	}
	store, modes, authored = s, nil, nil
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

// Known reports whether a guard id is registered.
func Known(id string) bool {
	mu.Lock()
	defer mu.Unlock()
	loadAuthoredLocked()
	_, ok := byID[id]
	return ok
}

// NormalizeModel is the key a model is filed under: lower case, without a
// provider's "models/" prefix, "unknown" when empty.
func NormalizeModel(model string) string {
	m := strings.ToLower(strings.TrimSpace(model))
	m = strings.TrimPrefix(m, "models/")
	if m == "" {
		return "unknown"
	}
	return m
}

func modeKey(id, model string) string { return id + "|" + model }

func loadModesLocked() {
	if modes != nil {
		return
	}
	modes = map[string]Mode{}
	if store == nil {
		return
	}
	for _, k := range store.Keys(modesTable) {
		var m Mode
		if store.Get(modesTable, k, &m) && m != "" {
			modes[k] = m
		}
	}
}

// ModeFor is what a guard does on a model: the model's own setting, else the
// guard's default for all models, else On.
func ModeFor(id, model string) Mode {
	mu.Lock()
	defer mu.Unlock()
	loadModesLocked()
	if m, ok := modes[modeKey(id, NormalizeModel(model))]; ok {
		return m
	}
	if m, ok := modes[modeKey(id, AllModels)]; ok {
		return m
	}
	return On
}

// DefaultMode is a guard's setting for all models: On unless changed.
func DefaultMode(id string) Mode {
	mu.Lock()
	defer mu.Unlock()
	loadModesLocked()
	if m, ok := modes[modeKey(id, AllModels)]; ok {
		return m
	}
	return On
}

// Setting is one stored mode: a guard's default (Model AllModels) or its
// setting on one model.
type Setting struct {
	ID    string `json:"id"`
	Model string `json:"model"`
	Mode  Mode   `json:"mode"`
}

// Settings lists every stored mode.
func Settings() []Setting {
	mu.Lock()
	defer mu.Unlock()
	loadModesLocked()
	var out []Setting
	for k, m := range modes {
		id, model, _ := strings.Cut(k, "|")
		out = append(out, Setting{ID: id, Model: model, Mode: m})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].ID != out[j].ID {
			return out[i].ID < out[j].ID
		}
		return out[i].Model < out[j].Model
	})
	return out
}

// SetMode sets a guard's mode on a model, or its default with AllModels. An
// empty mode clears the setting, so the model follows the default again.
func SetMode(id, model string, m Mode) error {
	if !Known(id) {
		return fmt.Errorf("no reply guard %q", id)
	}
	switch m {
	case "", On, Off, Shadow:
	default:
		return fmt.Errorf("mode must be on, off or shadow, not %q", m)
	}
	if model != AllModels {
		model = NormalizeModel(model)
	}
	mu.Lock()
	defer mu.Unlock()
	loadModesLocked()
	k := modeKey(id, model)
	if m == "" {
		delete(modes, k)
	} else {
		modes[k] = m
	}
	if store != nil {
		store.Set(modesTable, k, m)
	}
	return nil
}

// Record tallies one firing: acted when the guard corrected the reply, not
// when it only recorded it in shadow mode. The reply's tail is kept as a
// sample.
func Record(id, model, reply string, acted bool) {
	model = NormalizeModel(model)
	mu.Lock()
	defer mu.Unlock()
	if store == nil {
		return
	}
	k := modeKey(id, model)
	var st Stat
	store.Get(statsTable, k, &st)
	st.ID, st.Model = id, model
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
