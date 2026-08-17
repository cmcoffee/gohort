// Advice for a pipeline definition: what is worth fixing but does not
// stop it running.
//
// The mirror of MachineDef.Advice, and it exists because the two share a
// mechanism. Declaring output fields IS the structured-output feature in
// both: the framework asks for those keys, encodes them, and validates
// what comes back. So both have the same failure — a prompt that ALSO
// specifies a format, leaving two sets of formatting rules and, usually,
// a JSON string nested inside a JSON field.
//
// Kept apart from Validate for the same reason machines keep it apart
// from Problems: this reads prompt WORDING, which is a guess about
// intent, and a guess with the power to reject somebody's work is worse
// than no rule at all.
//
// Only the rule that genuinely transfers is here. A machine also warns
// about a step told to go looking with no tools — that one does NOT
// transfer, because a worker stage inherits the calling agent's whole
// catalog unless it narrows, so an empty Tools list means everything
// rather than nothing.

package core

import "strings"

// Advice returns the soft findings for a pipeline, in stage order.
// Nested loop bodies are walked, since a stage inside a loop is written
// the same way and fails the same way.
func (d PipelineDef) Advice() []string {
	return adviceForStages(d.Stages, "")
}

func adviceForStages(stages []PipelineStage, prefix string) []string {
	var out []string
	for _, s := range stages {
		name := strings.TrimSpace(s.Name)
		if name == "" {
			continue
		}
		// The ONE the model asked for and the framework already
		// provides. ModelOutput rather than Output: a field filled from
		// a variable is never asked of the model, so a stage whose only
		// declarations are fills has no contract to collide with.
		if len(s.ModelOutput()) > 0 && AsksForRawJSON(s.Prompt) {
			out = append(out, DeclaredOutputPromptAdvice("stage", prefix+name))
		}
		if len(s.Body) > 0 {
			out = append(out, adviceForStages(s.Body, prefix+name+" › ")...)
		}
	}
	return out
}
