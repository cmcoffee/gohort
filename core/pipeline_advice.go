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
// Only the rules that genuinely transfer are here. A machine also warns
// about a step told to go looking with no tools — that one does NOT
// transfer, because a worker stage inherits the calling agent's whole
// catalog unless it narrows, so an empty Tools list means everything
// rather than nothing.
//
// One rule was built, tested against the pipelines this repo ships, and
// REMOVED: "nothing reads this declared field". It fired on all three,
// and was wrong all three times. See pipeline-structured-outputs.md —
// the short version is that {prev} renders the previous stage's whole
// JSON, so the default way stages chain already reads every field, and
// a field that genuinely is unreferenced is usually a rationale beside
// a decision or the unread half of a deliberate pair. A rule that tells
// somebody to delete work that is doing its job is worse than no rule.

package core

import (
	"strconv"
	"strings"
)

// Advice returns the soft findings for a pipeline, in stage order.
// Nested loop bodies are walked, since a stage inside a loop is written
// the same way and fails the same way.
// fanoutCostAdviceThreshold is where a fan stops being ordinary and starts
// being worth a sentence. Twelve items (the cap) times four body stages is
// forty-eight and passes without comment; a fifth stage does not.
const fanoutCostAdviceThreshold = 50

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
		// A fanout body multiplies: N branches x K stages of model calls,
		// paid every run. Advice rather than a problem, because a
		// deliberate twenty-four-call fan is a legitimate thing to author
		// and nothing here knows which this is.
		if s.Kind == StageFanout && len(s.Body) > 0 {
			if calls := FanoutMaxItems * len(s.Body); calls > fanoutCostAdviceThreshold {
				out = append(out, "stage "+prefix+name+" fans a "+strconv.Itoa(len(s.Body))+
					"-stage body over up to "+strconv.Itoa(FanoutMaxItems)+" items, which is up to "+
					strconv.Itoa(calls)+" model calls in one run. That may be exactly what you want; "+
					"if it is not, narrow what it fans over or move work out of the body.")
			}
		}
		if len(s.Body) > 0 {
			out = append(out, adviceForStages(s.Body, prefix+name+" › ")...)
		}
	}
	return out
}
