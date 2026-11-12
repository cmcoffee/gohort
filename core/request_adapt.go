package core

// Request adaptation (docs/design/request-adaptation.md), stage 1: ONE
// registry for the parts of a model request a model may refuse, where there
// were four learners each keeping its own map, its own matching and its own
// log line.
//
// A field starts every subject (a model id, or for images the client that
// learned it) on its top step. A refusal of the field AS SENT moves the
// subject one step down, by the field's own rule (Next), once; the client
// then rebuilds the call from the step it is on, and retryLLM retries it
// (adaptedRetry). Each move is stored (adaptTable) with the refusal that made
// it, so a restart does not pay for it again; it stands adaptRecheckAfter,
// then the field is tried as configured again; and the Models page lists
// them with Forget (ListRequestAdaptations, ForgetRequestAdaptation).

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/png"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// requestField is one part of a request a model may refuse.
type requestField struct {
	key string
	// top is the step every subject starts on.
	top string
	// next is the step a subject on current moves to on refusal err; ok
	// false when err is not a refusal of this field as sent.
	next func(current string, err error) (string, bool)
	// said logs a move, once, in the field's own words.
	said func(subject, from, to string, err error)
	// label is the field as the Models page names it; sends says what a
	// step below the top sends instead.
	label string
	sends func(step string) string
}

var (
	requestFieldsMu sync.RWMutex
	requestFields   = map[string]requestField{}
)

// registerRequestField adds f to the registry. Called from init by the
// client file that builds the field.
func registerRequestField(f requestField) {
	requestFieldsMu.Lock()
	requestFields[f.key] = f
	requestFieldsMu.Unlock()
}

func requestFieldFor(key string) (requestField, bool) {
	requestFieldsMu.RLock()
	defer requestFieldsMu.RUnlock()
	f, ok := requestFields[key]
	return f, ok
}

// RequestAdaptation is one subject (a model) moved below a field's top step:
// what it sends now, since when, and the refusal that moved it. Stored, so a
// restart does not pay for it again, and shown on the Models page with
// Forget.
type RequestAdaptation struct {
	Field   string    `json:"field"`
	Subject string    `json:"subject"`
	Step    string    `json:"step"`
	Since   time.Time `json:"since"`
	Refusal string    `json:"refusal,omitempty"`
	// What and Sends are the field and its step in words, filled when listed.
	What  string `json:"what,omitempty"`
	Sends string `json:"sends,omitempty"`
	// Unmapped marks a refusal gohort cannot adapt to yet (fieldUnmapped).
	Unmapped bool `json:"unmapped,omitempty"`
}

// adaptTable keeps the adaptations, keyed field NUL subject.
const adaptTable = "request_adaptations"

// adaptRecheckAfter is how long a step below the top stands: after it, the
// next call goes back to the top, since providers relax limits as well as
// add them, and a fallback kept for ever runs worse than it needs to.
const adaptRecheckAfter = 30 * 24 * time.Hour

// adaptRefusalMax is how much of a refusal is kept beside its step.
const adaptRefusalMax = 300

// adaptState is the stored adaptations, read once from RootDB and kept in
// memory, since a step is read on every request. Read again when RootDB is
// another store (a test's own).
var adaptState struct {
	sync.RWMutex
	db     Database
	loaded bool
	steps  map[string]map[string]RequestAdaptation // field -> subject -> record
}

func adaptKey(field, subject string) string { return field + "\x00" + subject }

// adaptEnsure loads the stored adaptations when they are not loaded from the
// store RootDB is now.
func adaptEnsure() {
	adaptState.RLock()
	fresh := adaptState.loaded && adaptState.db == RootDB
	adaptState.RUnlock()
	if fresh {
		return
	}
	adaptState.Lock()
	defer adaptState.Unlock()
	if adaptState.loaded && adaptState.db == RootDB {
		return
	}
	steps := map[string]map[string]RequestAdaptation{}
	if RootDB != nil {
		for _, k := range RootDB.Keys(adaptTable) {
			var a RequestAdaptation
			if RootDB.Get(adaptTable, k, &a) && a.Field != "" && a.Subject != "" {
				if steps[a.Field] == nil {
					steps[a.Field] = map[string]RequestAdaptation{}
				}
				steps[a.Field][a.Subject] = a
			}
		}
	}
	adaptState.steps, adaptState.db, adaptState.loaded = steps, RootDB, true
}

// adaptedStep is the step subject is on for field: the field's top step
// unless the subject was moved, within adaptRecheckAfter.
func adaptedStep(subject, field string) string {
	adaptEnsure()
	adaptState.RLock()
	a, ok := adaptState.steps[field][subject]
	adaptState.RUnlock()
	f, _ := requestFieldFor(field)
	if !ok || a.Step == "" {
		return f.top
	}
	if time.Since(a.Since) > adaptRecheckAfter {
		dropAdaptation(field, subject)
		Log("[llm] %s: trying %s as configured again, %s after it was refused", subject, chFirstNonEmpty(f.label, field), adaptRecheckAfter)
		return f.top
	}
	return a.Step
}

// setAdaptedStep puts subject on step for field, saying so when it moved, and
// reports whether it did. Stored, with the refusal that moved it.
func setAdaptedStep(subject, field, step string, err error) bool {
	if subject == "" || step == "" {
		return false
	}
	from := adaptedStep(subject, field)
	if from == step {
		return false
	}
	a := RequestAdaptation{Field: field, Subject: subject, Step: step, Since: time.Now()}
	if err != nil {
		a.Refusal = truncateRunes(err.Error(), adaptRefusalMax)
	}
	adaptState.Lock()
	if adaptState.steps == nil {
		adaptState.steps = map[string]map[string]RequestAdaptation{}
	}
	if adaptState.steps[field] == nil {
		adaptState.steps[field] = map[string]RequestAdaptation{}
	}
	f, _ := requestFieldFor(field)
	if step == f.top {
		delete(adaptState.steps[field], subject)
	} else {
		adaptState.steps[field][subject] = a
	}
	db := adaptState.db
	adaptState.Unlock()
	if db != nil {
		if step == f.top {
			db.Unset(adaptTable, adaptKey(field, subject))
		} else {
			db.Set(adaptTable, adaptKey(field, subject), a)
		}
	}
	if f.said != nil {
		f.said(subject, from, step, err)
	}
	return true
}

// adaptFrom moves subject down field's ladder when err is a refusal of the
// field as sent, and reports whether it moved.
func adaptFrom(subject, field string, err error) bool {
	f, ok := requestFieldFor(field)
	if !ok || err == nil || subject == "" {
		return false
	}
	next, refused := f.next(adaptedStep(subject, field), err)
	if !refused {
		return false
	}
	return setAdaptedStep(subject, field, next, err)
}

// dropAdaptation puts subject back on field's top step, stored and in memory.
func dropAdaptation(field, subject string) bool {
	adaptEnsure()
	adaptState.Lock()
	_, had := adaptState.steps[field][subject]
	delete(adaptState.steps[field], subject)
	db := adaptState.db
	adaptState.Unlock()
	if db != nil {
		db.Unset(adaptTable, adaptKey(field, subject))
	}
	return had
}

// forgetAdaptations puts every subject back on field's top step: a test's
// clean start.
func forgetAdaptations(field string) {
	adaptEnsure()
	adaptState.Lock()
	subjects := adaptState.steps[field]
	delete(adaptState.steps, field)
	db := adaptState.db
	adaptState.Unlock()
	if db != nil {
		for subject := range subjects {
			db.Unset(adaptTable, adaptKey(field, subject))
		}
	}
}

// ListRequestAdaptations is every subject moved below a field's top step and
// still standing, newest first, with the field and step in words.
func ListRequestAdaptations() []RequestAdaptation {
	adaptEnsure()
	adaptState.RLock()
	var out []RequestAdaptation
	for _, bySubject := range adaptState.steps {
		for _, a := range bySubject {
			out = append(out, a)
		}
	}
	adaptState.RUnlock()
	kept := out[:0]
	for _, a := range out {
		if time.Since(a.Since) > adaptRecheckAfter {
			continue
		}
		f, _ := requestFieldFor(a.Field)
		a.What = chFirstNonEmpty(f.label, a.Field)
		a.Unmapped = a.Field == fieldUnmapped
		a.Sends = a.Step
		if f.sends != nil {
			a.Sends = chFirstNonEmpty(f.sends(a.Step), a.Step)
		}
		kept = append(kept, a)
	}
	sort.Slice(kept, func(i, j int) bool { return kept[i].Since.After(kept[j].Since) })
	return kept
}

// ForgetRequestAdaptation puts subject back on field's top step: the next
// call sends the field as configured and learns again if it is refused.
func ForgetRequestAdaptation(field, subject string) bool {
	if !dropAdaptation(field, subject) {
		return false
	}
	Log("[llm] %s: %s forgotten, sending it as configured again", subject, field)
	return true
}

// adaptedRetry marks a refusal a field moved on: the rebuilt call differs
// from the refused one, so retryLLM may try again without looping on an
// identical request.
type adaptedRetry struct {
	field string
	error
}

func (e adaptedRetry) Unwrap() error { return e.error }

// adaptedField is the field a refusal moved, when it moved one.
func adaptedField(err error) (string, bool) {
	var r adaptedRetry
	if errors.As(err, &r) {
		return r.field, true
	}
	return "", false
}

// --- fields every provider's client can take ---------------------------------

// fieldMaxTokens is the most output a model takes in one call: as the call
// asks ("asked") until a refusal names a maximum, then that maximum. Its step
// is the number, so a lower maximum named later lowers it again.
const fieldMaxTokens = "max_tokens"

// fieldSampling is temperature, top_p and top_k: sent as configured until a
// model refuses them, then left out ("omit").
const fieldSampling = "sampling"

// maxTokensLimitREs read the maximum a refusal of max_tokens names, in each
// provider's words: Anthropic's "max_tokens: 64000 > 32000, which is the
// maximum allowed", OpenAI's "supports at most 16384 completion tokens",
// Bedrock's "#/max_tokens: 200000 is not less or equal to 128000".
var maxTokensLimitREs = []*regexp.Regexp{
	regexp.MustCompile(`max_tokens:?\s*\d+\s*>\s*(\d+)`),
	regexp.MustCompile(`at most (\d+) (?:completion |output )?tokens`),
	regexp.MustCompile(`not less (?:than )?or equal to (\d+)`),
	regexp.MustCompile(`maximum (?:allowed )?(?:number of )?(?:output |completion )?tokens(?: allowed)?(?: is| of|:)? (\d+)`),
}

// maxTokensLimitFrom is the output maximum a 400 refusing max_tokens names,
// or 0.
func maxTokensLimitFrom(err error) int {
	var api *APIError
	if !errors.As(err, &api) || api.StatusCode != http.StatusBadRequest {
		return 0
	}
	msg := strings.ToLower(api.Message)
	if !strings.Contains(msg, "max_tokens") && !strings.Contains(msg, "max_completion_tokens") &&
		!strings.Contains(msg, "maxtokens") && !strings.Contains(msg, "output tokens") {
		return 0
	}
	for _, re := range maxTokensLimitREs {
		if m := re.FindStringSubmatch(msg); m != nil {
			if n, _ := strconv.Atoi(m[1]); n > 0 {
				return n
			}
		}
	}
	return 0
}

// samplingRefusedRE is a refusal of a sampling setting: the setting named,
// and refused rather than out of range.
var samplingRefusedRE = regexp.MustCompile(`(?i)(temperature|top_p|top_k)\W.{0,80}?(not supported|unsupported|does not support|only the default|is not allowed)|(not supported|unsupported|does not support)\W.{0,80}?(temperature|top_p|top_k)`)

func init() {
	registerRequestField(requestField{
		key: fieldMaxTokens,
		top: "asked",
		next: func(current string, err error) (string, bool) {
			n := maxTokensLimitFrom(err)
			if n <= 0 {
				return "", false
			}
			if was, _ := strconv.Atoi(current); current != "asked" && was <= n {
				return "", false
			}
			return strconv.Itoa(n), true
		},
		said: func(model, _, to string, _ error) {
			Log("[llm] %s takes at most %s output tokens: asking for no more from now on", model, to)
		},
		label: "Output tokens asked for",
		sends: func(step string) string { return "at most " + step + ", the maximum it names" },
	})
	registerRequestField(requestField{
		key: fieldSampling,
		top: "send",
		next: func(current string, err error) (string, bool) {
			var api *APIError
			if current == "send" && errors.As(err, &api) && api.StatusCode == http.StatusBadRequest && samplingRefusedRE.MatchString(api.Message) {
				return "omit", true
			}
			return "", false
		},
		said: func(model, _, _ string, _ error) {
			Log("[llm] %s does not take sampling settings (temperature, top_p, top_k): leaving them out for this model", model)
		},
		label: "Sampling (temperature, top_p, top_k)",
		sends: func(string) string { return "nothing: the settings are left out, the model's own defaults" },
	})
}

// clampMaxTokens is n, or the most model takes when it named a lower maximum.
func clampMaxTokens(model string, n int) int {
	if limit, _ := strconv.Atoi(adaptedStep(model, fieldMaxTokens)); limit > 0 && n > limit {
		return limit
	}
	return n
}

// samplingOmitted reports whether model refused sampling settings.
func samplingOmitted(model string) bool {
	return adaptedStep(model, fieldSampling) == "omit"
}

// fieldUnmapped is a refusal no field above reads: a 400 that names a
// setting the model will not take, which gohort cannot adapt to yet. Kept as
// a row ("seen") so the Models page shows it, with the model's words, and the
// dashboard says one arrived; nothing is sent differently for it. Forget
// clears it.
const fieldUnmapped = "unmapped"

// settingRefusedRE is a 400 that refuses a setting of the request, as
// against one that refuses its content or size: what is worth a row.
var settingRefusedRE = regexp.MustCompile(`(?i)(not supported|unsupported (parameter|value|field|argument)|unknown (parameter|field|argument)|extra inputs are not permitted|is not allowed|unrecognized (request )?(argument|parameter|field)|invalid (value|parameter) for|does not support)`)

func init() {
	registerRequestField(requestField{
		key:  fieldUnmapped,
		top:  "none",
		next: func(string, error) (string, bool) { return "", false },
		said: func(model, _, _ string, err error) {
			Warn("[llm] %s refused a request in a way gohort cannot adapt to yet: %v. Calls carrying it fail until the request changes (Admin > Models, Learned from model refusals)", model, err)
		},
		label: "Something gohort cannot adapt to yet",
		sends: func(string) string { return "nothing different: calls carrying it fail until the request changes" },
	})
}

// noteUnmappedRefusal records err when a model refused a setting of the
// request and no field moved on it: a 400 naming a setting, not one the
// fields read, not a context overflow.
func (r *retryLLM) noteUnmappedRefusal(err error) {
	var api *APIError
	if err == nil || r.model == "" || !errors.As(err, &api) || api.StatusCode != http.StatusBadRequest {
		return
	}
	if _, moved := adaptedField(err); moved || IsContextExceededError(err) || !settingRefusedRE.MatchString(api.Message) {
		return
	}
	for _, f := range []string{fieldThinkingOff, fieldThinkingShape, fieldReasoningEffort, fieldMaxTokens, fieldSampling, fieldImages, fieldImageLimit} {
		if fl, ok := requestFieldFor(f); ok {
			if _, reads := fl.next(fl.top, err); reads {
				return
			}
		}
	}
	setAdaptedStep(r.model, fieldUnmapped, "seen", err)
}

// --- probes: learning a model's limits before real work meets them ------------

// requestProbes are one short call each, carrying one request field a model
// may refuse. A refusal is learned by the client as any refusal is, so after
// them the model's ladders are where its first real call would have put them.
var requestProbes = []struct {
	name   string
	images bool
	opts   []ChatOption
}{
	{name: "thinking off", opts: []ChatOption{WithThink(false), WithMaxTokens(16)}},
	{name: "an effort level", opts: []ChatOption{WithEffort(effortLow), WithMaxTokens(16)}},
	{name: "a sampling setting", opts: []ChatOption{WithTemperature(0.2), WithMaxTokens(16)}},
	{name: "a large output allowance", opts: []ChatOption{WithThink(false), WithMaxTokens(200000)}},
	{name: "a picture", images: true, opts: []ChatOption{WithThink(false), WithMaxTokens(16)}},
}

// probePicture is a one-pixel PNG, the least a picture probe can carry.
func probePicture() []byte {
	var b bytes.Buffer
	png.Encode(&b, image.NewGray(image.Rect(0, 0, 1, 1)))
	return b.Bytes()
}

// ProbeRequestFields asks llm the probes, one at a time, reporting each as it
// starts (step, 1-based, of n), and returns what was adapted to: one line per
// field a refusal moved, in words ("Thinking off: thinking between_tools,
// its lowest setting"). Empty when the model took every probe as configured.
// A probe that fails for another reason (the server down) is passed over: the
// caller's own check reports reachability.
func ProbeRequestFields(ctx context.Context, llm LLM, step func(i, n int, name string)) []string {
	before := map[string]string{}
	for _, a := range ListRequestAdaptations() {
		before[a.Field+"\x00"+a.Subject] = a.Step
	}
	for i, p := range requestProbes {
		if ctx.Err() != nil {
			break
		}
		if step != nil {
			step(i+1, len(requestProbes), p.name)
		}
		msg := Message{Role: "user", Content: "Reply with the single word: ok"}
		if p.images {
			msg.Content, msg.Images = "What colour is this picture? One word.", [][]byte{probePicture()}
		}
		llm.Chat(ctx, []Message{msg}, p.opts...)
	}
	var out []string
	for _, a := range ListRequestAdaptations() {
		if before[a.Field+"\x00"+a.Subject] != a.Step {
			out = append(out, a.What+": "+a.Sends)
		}
	}
	sort.Strings(out)
	return out
}

var probed struct {
	sync.Mutex
	done map[string]bool
}

// ProbeOnce runs ProbeRequestFields on llm in the background, once per
// identity per process: a tier's model just saved, whose fields are saved one
// at a time, so the same model is not probed on every save.
func ProbeOnce(identity string, llm LLM) {
	if identity == "" || llm == nil {
		return
	}
	probed.Lock()
	if probed.done == nil {
		probed.done = map[string]bool{}
	}
	if probed.done[identity] {
		probed.Unlock()
		return
	}
	probed.done[identity] = true
	probed.Unlock()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		if lines := ProbeRequestFields(ctx, llm, nil); len(lines) > 0 {
			Log("[llm] probing %s adapted to: %s", identity, strings.Join(lines, "; "))
		} else {
			Debug("[llm] probing %s: it took every request as configured", identity)
		}
	}()
}
