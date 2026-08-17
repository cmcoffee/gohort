// Structural edits to a stored pipeline: renaming a stage, and removing
// one.
//
// Both exist because a pipeline's references are CHECKED. Validate
// refuses a {stage:NAME} that resolves to nothing, which is what makes a
// typo a save-time error instead of a literal brace expression reaching
// a model — and it also means an editor that changes a name without
// rewriting the references produces a definition nothing will store.
// Machines have the same shape in RenameStep, for the same reason.
//
// Removal is the case where the two diverge, and deliberately. A
// machine's RemoveStep drops every reference to the deleted step,
// because a machine's references live in fields (next, choices, keep)
// and dropping one is unambiguous. A pipeline's live in PROSE — a
// prompt reading "answer from {stage:plan.queries}" — and rewriting
// somebody's sentence to make a delete succeed is a worse answer than
// saying which sentences are in the way.

package core

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// bareRefPattern matches a reference written without the {stage:…}
// wrapper, which is how fan_over, until, when and skip_to address one
// ("critic.satisfied == true").
var bareRefPattern = regexp.MustCompile(`[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+`)

// RenameStage renames a stage and rewrites every reference to it,
// returning the stages it touched (excluding the renamed one).
//
// Walks nested bodies too: a body stage can read what ran before the
// loop, so a top-level rename has to reach inside.
func (d *PipelineDef) RenameStage(from, to string) []string {
	from, to = strings.TrimSpace(from), strings.TrimSpace(to)
	if d == nil || from == "" || to == "" || from == to {
		return nil
	}
	touched := map[string]bool{}
	d.Stages = renameInStages(d.Stages, from, to, "", touched)
	names := make([]string, 0, len(touched))
	for n := range touched {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

func renameInStages(stages []PipelineStage, from, to, prefix string, touched map[string]bool) []PipelineStage {
	out := append([]PipelineStage(nil), stages...)
	for i := range out {
		s := &out[i]
		label := prefix + strings.TrimSpace(s.Name)
		changed := false
		if strings.TrimSpace(s.Name) == from && prefix == "" {
			s.Name = to
		}
		if v, ok := renameTemplate(s.Prompt, from, to); ok {
			s.Prompt, changed = v, true
		}
		// fan_over, until, when and skip_to address a stage WITHOUT the
		// {stage:…} wrapper, so they need the bare rule as well.
		if v, ok := renameBare(s.FanOver, from, to); ok {
			s.FanOver, changed = v, true
		}
		if v, ok := renameBare(s.Until, from, to); ok {
			s.Until, changed = v, true
		}
		if v, ok := renameBare(s.When, from, to); ok {
			s.When, changed = v, true
		}
		if strings.TrimSpace(s.SkipTo) == from {
			s.SkipTo, changed = to, true
		}
		if len(s.Args) > 0 {
			args := make(map[string]string, len(s.Args))
			for k, v := range s.Args {
				nv, ok := renameTemplate(v, from, to)
				if ok {
					changed = true
				}
				args[k] = nv
			}
			s.Args = args
		}
		if len(s.Output) > 0 {
			fields := append([]PipelineField(nil), s.Output...)
			for j := range fields {
				if v, ok := renameTemplate(fields[j].From, from, to); ok {
					fields[j].From, changed = v, true
				}
			}
			s.Output = fields
		}
		if len(s.Body) > 0 {
			s.Body = renameInStages(s.Body, from, to, label+" › ", touched)
		}
		if changed {
			touched[label] = true
		}
	}
	return out
}

// renameTemplate rewrites {stage:from} and {stage:from.field}.
func renameTemplate(tmpl, from, to string) (string, bool) {
	if tmpl == "" || !strings.Contains(tmpl, "{stage:") {
		return tmpl, false
	}
	var b strings.Builder
	rest, hit := tmpl, false
	for {
		i := strings.Index(rest, "{stage:")
		if i < 0 {
			b.WriteString(rest)
			return b.String(), hit
		}
		b.WriteString(rest[:i+len("{stage:")])
		rest = rest[i+len("{stage:"):]
		j := strings.Index(rest, "}")
		if j < 0 {
			b.WriteString(rest)
			return b.String(), hit
		}
		ref := rest[:j]
		name, field := SplitStageRef(ref)
		if strings.TrimSpace(name) == from {
			hit = true
			ref = to
			if field != "" {
				ref += "." + field
			}
		}
		b.WriteString(ref)
		b.WriteString("}")
		rest = rest[j+1:]
	}
}

// renameBare rewrites a reference written without the wrapper, as
// fan_over, until, when and skip_to are. Only the STAGE half is
// rewritten and only when it is the whole leading token, so a condition
// like "plan.count > 2" keeps its comparison and a stage called "plan"
// does not rewrite a field called "plan".
func renameBare(ref, from, to string) (string, bool) {
	trimmed := strings.TrimSpace(ref)
	if trimmed == "" {
		return ref, false
	}
	// Split on the first character that cannot be part of a reference.
	end := len(trimmed)
	for i, r := range trimmed {
		if !(r == '.' || r == '_' || r == '-' ||
			(r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')) {
			end = i
			break
		}
	}
	head, tail := trimmed[:end], trimmed[end:]
	name, field := SplitStageRef(head)
	if strings.TrimSpace(name) != from {
		return ref, false
	}
	out := to
	if field != "" {
		out += "." + field
	}
	return out + tail, true
}

// StageReferences returns the stages that reference name, with the
// place each reference lives, so a refusal can say WHERE to look.
func (d PipelineDef) StageReferences(name string) []string {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil
	}
	var out []string
	var walk func(stages []PipelineStage, prefix string)
	walk = func(stages []PipelineStage, prefix string) {
		for _, s := range stages {
			label := prefix + strings.TrimSpace(s.Name)
			if label == name {
				walk(s.Body, label+" › ")
				continue // its own references go with it
			}
			add := func(where string) { out = append(out, label+" ("+where+")") }
			for _, ref := range stageRefs(s.Prompt) {
				if n, _ := SplitStageRef(ref); strings.TrimSpace(n) == name {
					add("prompt")
					break
				}
			}
			for _, pair := range []struct{ where, raw string }{
				{"fan_over", s.FanOver}, {"until", s.Until}, {"when", s.When}, {"skip_to", s.SkipTo},
			} {
				if bareNames(pair.raw)[name] {
					add(pair.where)
				}
			}
			for k, v := range s.Args {
				for _, ref := range stageRefs(v) {
					if n, _ := SplitStageRef(ref); strings.TrimSpace(n) == name {
						add("args." + k)
						break
					}
				}
			}
			for _, f := range s.Output {
				for _, ref := range stageRefs(f.From) {
					if n, _ := SplitStageRef(ref); strings.TrimSpace(n) == name {
						add("output." + f.Name)
						break
					}
				}
			}
			walk(s.Body, label+" › ")
		}
	}
	walk(d.Stages, "")
	sort.Strings(out)
	return out
}

// bareNames is the set of stage names a bare reference field mentions.
func bareNames(raw string) map[string]bool {
	out := map[string]bool{}
	for _, m := range bareRefPattern.FindAllString(raw, -1) {
		if n, _ := SplitStageRef(m); strings.TrimSpace(n) != "" {
			out[strings.TrimSpace(n)] = true
		}
	}
	if t := strings.TrimSpace(raw); t != "" && !strings.ContainsAny(t, " .{") {
		out[t] = true // a whole-stage reference, as fan_over and skip_to allow
	}
	return out
}

// RemoveStage deletes a stage, refusing while anything still reads it.
//
// The refusal is the feature. A pipeline's references live in prose, and
// rewriting somebody's sentence so a delete can succeed is a worse
// answer than naming the sentences that are in the way.
func (d *PipelineDef) RemoveStage(name string) error {
	name = strings.TrimSpace(name)
	if d == nil || name == "" {
		return Error("no stage named")
	}
	if refs := d.StageReferences(name); len(refs) > 0 {
		return Error("cannot remove " + strconv.Quote(name) + " while it is read by " +
			strings.Join(refs, ", ") + " — a pipeline's references live in its prompts, so removing " +
			"one silently would mean rewriting what you wrote. Change those first.")
	}
	out := d.Stages[:0:0]
	found := false
	for _, s := range d.Stages {
		if strings.TrimSpace(s.Name) == name {
			found = true
			continue
		}
		out = append(out, s)
	}
	if !found {
		return Error("no stage called " + strconv.Quote(name) + " in this pipeline")
	}
	d.Stages = out
	return nil
}
