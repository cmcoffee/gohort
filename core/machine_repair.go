// Mechanical repair of a machine definition.
//
// The editor reports two lists — what is still missing (Problems) and
// what the machine looks like it might not have meant (Advice). Most of
// what they report is a decision: a step that names tools AND delegates
// could lose either one, and only the author knows which.
//
// A few are not decisions at all. A reference to a step that does not
// exist has exactly one right answer, and it is the finding people got
// stuck on: the step it named is already gone, so there is nothing to
// open and nothing to correct — the reference is stranded in a field
// whose picker no longer offers it. This file is that class and only
// that class. Nothing here guesses at intent; anything with two
// defensible answers stays in the list for a person to settle.
//
// RemoveStep already knows how to drop every reference to one name (it
// runs when a step is deleted). A dangling reference is the same
// operation arriving late — the step went away without the references
// going with it — so the repairs mirror it field for field.

package core

import (
	"sort"
	"strconv"
	"strings"
)

// Repair classes. A panel fixes what it reports, so the button sits
// with the finding rather than somewhere else on the page.
const (
	RepairAll      = ""
	RepairProblems = "problems"
	RepairAdvice   = "advice"
)

// MachineRepair is one correction with exactly one right answer.
type MachineRepair struct {
	// Step it touches, or "start" for the machine's entry point.
	Step string `json:"step"`
	// What changes, in the words of the finding it settles.
	What string `json:"what"`
	// Advice marks the ones reported under "worth a look" rather than
	// as work remaining.
	Advice bool `json:"advice"`
}

// Repairs lists what can be corrected mechanically, changing nothing.
// kind is RepairAll, RepairProblems, or RepairAdvice.
func (d MachineDef) Repairs(kind string) []MachineRepair {
	c := d.clonePhases()
	return c.Repair(kind)
}

// Repair applies them and returns what it changed. The return is the
// same list Repairs previewed, so a button can be labelled with it
// before and reported with it after.
func (d *MachineDef) Repair(kind string) []MachineRepair {
	if d == nil {
		return nil
	}
	want := func(advice bool) bool {
		switch kind {
		case RepairProblems:
			return !advice
		case RepairAdvice:
			return advice
		}
		return true
	}
	var out []MachineRepair
	seen := d.stepNames()
	// Nothing to hold a reference to, and nothing to point start at:
	// an empty machine is reported, not repaired.
	if len(seen) == 0 {
		return nil
	}
	real := func(name string) bool { return seen[strings.TrimSpace(name)] }

	for i := range d.Phases {
		p := &d.Phases[i]
		name := strings.TrimSpace(p.Name)
		if name == "" {
			continue // reported as its own problem; naming it is a decision
		}
		if want(false) {
			out = append(out, repairPhaseRefs(p, name, real)...)
		}
		if want(true) {
			out = append(out, repairFilledFieldTypes(p, name)...)
		}
	}
	// The entry point. An empty Start resolves to the first step, so
	// pointing it there is what clearing it would do anyway — written
	// down rather than left implied, exactly as RemoveStep does it.
	if want(false) {
		if s := strings.TrimSpace(d.Start); s != "" && !real(s) {
			first := strings.TrimSpace(d.Phases[0].Name)
			if first != "" {
				d.Start = first
				out = append(out, MachineRepair{Step: "start",
					What: "start names " + strconv.Quote(s) + ", which is not a step — begin at " + strconv.Quote(first) + " instead"})
			}
		}
	}
	return out
}

// repairPhaseRefs drops every reference this phase makes to a step that
// is not in the machine. Field for field with RemoveStep, which is the
// same drop happening at the moment the step goes away.
func repairPhaseRefs(p *MachinePhase, name string, real func(string) bool) []MachineRepair {
	var out []MachineRepair
	add := func(what string) { out = append(out, MachineRepair{Step: name, What: "step " + name + ": " + what}) }

	if t := strings.TrimSpace(p.Next); t != "" && !real(t) {
		p.Next = ""
		add("next names " + strconv.Quote(t) + ", which is not a step — clear it")
	}
	// A guard with nowhere to send the turn falls back to the machine's
	// start, which is what an empty guard_to means.
	if t := strings.TrimSpace(p.GuardTo); t != "" && !real(t) {
		p.GuardTo = ""
		add("guard_to names " + strconv.Quote(t) + ", which is not a step — fall back to the start")
	}
	for _, l := range []struct {
		list *[]string
		what string
	}{
		{&p.Choices, "may choose"},
		{&p.Keep, "keeps what was established in"},
		{&p.ExitsTo, "may exit to"},
	} {
		for _, gone := range danglingIn(*l.list, real) {
			dropFromList(l.list, gone)
			add(strconv.Quote(gone) + " is not a step in this machine — drop it from what it " + l.what)
		}
	}
	// The targets a routing field may return. Same rule: a name the
	// model could pick that resolves to nothing at run time.
	fields := append([]PipelineField(nil), p.Output...)
	changed := false
	for i := range fields {
		for _, gone := range danglingIn(fields[i].Enum, real) {
			dropFromList(&fields[i].Enum, gone)
			changed = true
			add(fields[i].Name + " may return " + strconv.Quote(gone) + ", which is not a step — drop it")
		}
	}
	if changed {
		p.Output = fields
	}
	return out
}

// repairFilledFieldTypes settles the one piece of advice that is not a
// judgement call: a field filled FROM a variable holds text, because
// everything a variable holds is text. DeclaredOutput already treats it
// as text rather than storing a lie, so the declaration is the only
// thing out of step with what runs.
func repairFilledFieldTypes(p *MachinePhase, name string) []MachineRepair {
	var out []MachineRepair
	fields := append([]PipelineField(nil), p.Output...)
	changed := false
	for i := range fields {
		f := &fields[i]
		if strings.TrimSpace(f.From) == "" || f.Type == "" || f.Type == FieldString {
			continue
		}
		was := string(f.Type)
		f.Type = FieldString
		changed = true
		out = append(out, MachineRepair{Step: name, Advice: true,
			What: "step " + name + ": " + f.Name + " is filled from " + f.From +
				" and declared " + was + " — everything a variable holds is text, so declare it text"})
	}
	if changed {
		p.Output = fields
	}
	return out
}

// danglingIn returns the entries of a list that name no step, in list
// order and without repeats, so dropping them is stable.
func danglingIn(list []string, real func(string) bool) []string {
	var out []string
	seen := map[string]bool{}
	for _, v := range list {
		v = strings.TrimSpace(v)
		if v == "" || seen[v] || real(v) {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	return out
}

// stepNames is the set every reference is checked against.
func (d MachineDef) stepNames() map[string]bool {
	seen := make(map[string]bool, len(d.Phases))
	for _, p := range d.Phases {
		if n := strings.TrimSpace(p.Name); n != "" {
			seen[n] = true
		}
	}
	return seen
}

// clonePhases copies deeply enough that a preview cannot write through
// to the stored def: the phase slice, and every list a repair edits.
func (d MachineDef) clonePhases() MachineDef {
	phases := append([]MachinePhase(nil), d.Phases...)
	for i := range phases {
		phases[i].Choices = append([]string(nil), phases[i].Choices...)
		phases[i].Keep = append([]string(nil), phases[i].Keep...)
		phases[i].ExitsTo = append([]string(nil), phases[i].ExitsTo...)
		fields := append([]PipelineField(nil), phases[i].Output...)
		for j := range fields {
			fields[j].Enum = append([]string(nil), fields[j].Enum...)
		}
		phases[i].Output = fields
	}
	d.Phases = phases
	return d
}

// RepairLines renders repairs for display, sorted by step so the list
// reads in the same order as the page.
func RepairLines(rs []MachineRepair) []string {
	out := make([]string, 0, len(rs))
	for _, r := range rs {
		out = append(out, r.What)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
