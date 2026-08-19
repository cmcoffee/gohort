// The working set: state that MANY phases contribute to.
//
// MachineState is keyed by phase, one entry each, which is the right shape
// for "what did decompose decide" and the wrong shape for "the answers so
// far". A run that marks some answers empty, refills them, promotes two and
// merges a child's findings into the same list is describing an
// ACCUMULATOR, and phase-keyed state cannot hold one: every phase that
// touched it would overwrite its own entry and nothing would add up.
//
// So a phase may declare what it contributes and how. The contribution
// lands in MachineState under the ACCUMULATOR's name rather than the
// phase's, which is what lets six phases write one list.
//
// Declared rather than free-form, for the reason everything else here is
// declared: a phase that changes the working set says so on its own record,
// the editor can show which phases feed which list, and Validate can refuse
// a reference to a list nothing writes. A free-form "{state:answers} +="
// would be shorter to build and impossible to read.

package core

import (
	"fmt"
	"strconv"
	"strings"
)

// AccumulatorItemsField and AccumulatorCountField are the two fields an
// accumulator exposes, so {state:answers.items} and {state:answers.count}
// resolve like any other declared reference.
const (
	AccumulatorItemsField = "items"
	AccumulatorCountField = "count"
)

// Accumulate modes.
const (
	AccumAppend  = "append"  // add to the end (the default)
	AccumReplace = "replace" // this phase's value becomes the whole list
	AccumUnion   = "union"   // add only what is not already there
)

// MachineAccumulator is one phase's contribution to a run-scoped list.
type MachineAccumulator struct {
	// Name is the list, and the MachineState key it lands under. It shares
	// a namespace with phase names, so Validate refuses one that collides:
	// a list called "answers" and a step called "answers" would be the same
	// blackboard entry, and whichever wrote last would win.
	Name string `json:"name"`

	// From is the declared output field of THIS phase whose value is
	// contributed. A list contributes its elements, one level flattened,
	// because a phase that produces three answers is adding three things
	// and not one thing shaped like three.
	From string `json:"from"`

	// Mode is append (default), replace, or union.
	Mode string `json:"mode,omitempty"`

	// By keys a union on one field of each element, for object elements
	// ("id", "question"). Empty unions on the whole rendered value, which
	// is what a list of strings wants.
	By string `json:"by,omitempty"`
}

// accumulatorFields is the shape every accumulator entry carries, for the
// validator's declared-field map.
func accumulatorFields() map[string]PipelineFieldType {
	return map[string]PipelineFieldType{
		AccumulatorItemsField: FieldList,
		AccumulatorCountField: FieldNumber,
	}
}

// Accumulators returns every list this machine writes, in the order they
// are first declared, so a rendering of the working set is stable between
// turns rather than reordered by a map walk.
func (d MachineDef) Accumulators() []string {
	var out []string
	seen := map[string]bool{}
	for _, p := range d.Phases {
		for _, a := range p.Accumulates {
			name := strings.TrimSpace(a.Name)
			if name == "" || seen[name] {
				continue
			}
			seen[name] = true
			out = append(out, name)
		}
	}
	return out
}

// accumulatorNames is Accumulators as a set, for the callers that ask
// "is this one" rather than "list them".
func (d MachineDef) accumulatorNames() map[string]bool {
	out := map[string]bool{}
	for _, n := range d.Accumulators() {
		out[n] = true
	}
	return out
}

// accumulate applies one phase's declared contributions to the blackboard.
//
// Runs immediately after the phase's own result is stored, so a later
// phase reading {state:answers} sees everything written so far including
// this phase's own contribution.
func (d MachineDef) accumulate(ph MachinePhase, fields map[string]any, st MachineState, note func(kind, detail string)) {
	for _, acc := range ph.Accumulates {
		name := strings.TrimSpace(acc.Name)
		from := strings.TrimSpace(acc.From)
		if name == "" || from == "" {
			continue
		}
		v, present := fields[from]
		if !present {
			// The phase answered without the field it was to contribute.
			// Silence here would read as "nothing to add" and be
			// indistinguishable from a list that is genuinely empty.
			note("machine_accumulator_empty", "step "+ph.Name+" contributes "+from+" to "+name+
				", but its reply carried no such field; nothing was added")
			continue
		}
		add := accumItems(v)
		if len(add) == 0 {
			continue
		}
		have := accumCurrent(st, name)
		switch strings.ToLower(strings.TrimSpace(acc.Mode)) {
		case AccumReplace:
			have = add
		case AccumUnion:
			seen := make(map[string]bool, len(have))
			for _, it := range have {
				seen[accumKey(it, acc.By)] = true
			}
			for _, it := range add {
				k := accumKey(it, acc.By)
				if seen[k] {
					continue
				}
				seen[k] = true
				have = append(have, it)
			}
		default:
			have = append(have, add...)
		}
		st[name] = PhaseResult{
			Text: renderAccumulator(name, have),
			Fields: map[string]any{
				AccumulatorItemsField: have,
				AccumulatorCountField: len(have),
			},
		}
	}
}

// accumCurrent reads the list an accumulator holds today.
func accumCurrent(st MachineState, name string) []any {
	res, ok := st[name]
	if !ok {
		return nil
	}
	list, _ := res.Fields[AccumulatorItemsField].([]any)
	out := make([]any, len(list))
	copy(out, list)
	return out
}

// accumItems turns one contributed value into the elements it adds. A list
// contributes its elements; anything else contributes itself. Empty text
// contributes nothing, since a field the model left blank is not a finding.
func accumItems(v any) []any {
	switch t := v.(type) {
	case nil:
		return nil
	case []any:
		out := make([]any, 0, len(t))
		for _, it := range t {
			if s, ok := it.(string); ok && strings.TrimSpace(s) == "" {
				continue
			}
			out = append(out, it)
		}
		return out
	case string:
		if strings.TrimSpace(t) == "" {
			return nil
		}
		return []any{t}
	default:
		return []any{v}
	}
}

// accumKey is what a union compares on: one named field of an object
// element, else the element's own rendering.
func accumKey(item any, by string) string {
	by = strings.TrimSpace(by)
	if by != "" {
		if m, ok := item.(map[string]any); ok {
			if v, found := m[by]; found {
				return strings.TrimSpace(renderFieldValue(v))
			}
		}
	}
	return strings.TrimSpace(renderFieldValue(item))
}

// renderAccumulator is what {state:NAME} reads and what the established
// block shows: a numbered list, because the working set is read by a model
// that has to refer to individual entries.
func renderAccumulator(name string, items []any) string {
	if len(items) == 0 {
		return ""
	}
	var b strings.Builder
	for i, it := range items {
		fmt.Fprintf(&b, "%s. %s\n", strconv.Itoa(i+1), strings.TrimSpace(renderFieldValue(it)))
	}
	return strings.TrimRight(b.String(), "\n")
}
