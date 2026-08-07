// Naming the reference library where the model will actually read it.
//
// A kept image was reachable only through image(action="help") — and nothing
// gave the model a reason to call it. So a user who had supplied a reference
// and then asked for "an image of x doing y" got a freshly invented x that
// looked like somebody else, with the reference sitting unused under a stable
// id the model never learned about.
//
// The names go in the tool schema, which is read before the action is picked.
// That is affordable HERE and not for the recent ring: kept ids change only
// when something is kept or forgotten, so the schema is stable across turns and
// across image operations, and the prefix cache survives.
//
// Labels are truncated hard and the list is capped. This exists so a request
// can be MATCHED to a reference, not so the library can be read in full — help
// still does that, and the note says so once the list is short of the whole.
package imagefetch

import (
	"fmt"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// maxSchemaKeptRefs is how many references are named before the list defers to
// help. Enough to cover a working library; short enough that a big one cannot
// crowd out the decision rule underneath it.
const maxSchemaKeptRefs = 12

// maxKeptLabelChars keeps a label to roughly a few words. The label is a
// matching aid, not a description — Description is what an agent works FROM,
// and it is deliberately not here.
const maxKeptLabelChars = 44

// keptRef is one reference as the schema presents it: an id to pass and just
// enough words to recognize what it is.
type keptRef struct {
	Ref   string
	Label string
}

// keptRefsFor reads the session's library and keeps only what may serve as a
// REFERENCE. The agent's own output is excluded: a generated subject was
// invented, so offering it as the reference for that subject compounds the
// invention on every reuse and the thing depicted drifts from the real one.
// Those entries remain kept, resolvable and listed by help — they are simply
// not advertised as evidence of what anything looks like.
//
// Unknown origin is kept in, not filtered out. Same direction as the workspace
// reaper: act only on what is POSITIVELY recognized. A stale reference in this
// list is visible and correctable; dropping one somebody deliberately kept is
// silent, and they find out when the agent stops using it.
func keptRefsFor(sess *ToolSession) []keptRef {
	all := KeptImages(sess)
	if len(all) == 0 {
		return nil
	}
	out := make([]keptRef, 0, len(all))
	for _, k := range all {
		if k.Origin.AgentMade() {
			continue
		}
		out = append(out, keptRef{Ref: k.Ref, Label: keptLabel(k)})
	}
	return out
}

// keptLabel prefers the agent's own words for WHY it kept the picture, since
// that is what a later request is most likely to echo. The caption — written by
// a vision pass, describing what is in frame — backs it up.
//
// A SUBJECT BEATS BOTH. "Rory" is the token a request naming Rory contains; the
// note ("sent me a selfie") and the caption ("man with a beard outdoors") are
// what the agent thought at the time, and neither matches on the word that
// actually arrives. This list exists to be matched against, so it leads with
// the thing most likely to be matched.
func keptLabel(k KeptImage) string {
	if subject := SubjectLabel(k.Subject); subject != "" {
		if k.Subject.Person {
			label := subject
			if k.Origin == ImageOriginUnknown {
				// Marked in the matching list too, not only in the manifest.
				// This list is read at action-choosing time and the manifest
				// often is not, so an entry that reads as a confirmed likeness
				// here is one the model will present as a photograph later.
				label += " (unverified)"
			}
			return truncateLabel(collapseSpace(label), maxKeptLabelChars)
		}
		// A thing keeps its description alongside the subject: "the office" is
		// less self-explanatory than a person's name and the caption earns its
		// place next to it.
		if extra := collapseSpace(strings.TrimSpace(k.Caption)); extra != "" {
			return truncateLabel(subject+" — "+extra, maxKeptLabelChars)
		}
		return truncateLabel(collapseSpace(subject), maxKeptLabelChars)
	}
	label := strings.TrimSpace(k.Note)
	if label == "" {
		label = strings.TrimSpace(k.Caption)
	}
	return truncateLabel(collapseSpace(label), maxKeptLabelChars)
}

// collapseSpace flattens newlines: this goes into a single schema sentence, and
// a caption that wrapped would otherwise break the line it sits in.
func collapseSpace(s string) string { return strings.Join(strings.Fields(s), " ") }

func truncateLabel(s string, max int) string {
	if len(s) <= max {
		return s
	}
	if cut := strings.LastIndex(s[:max], " "); cut > max/2 {
		return s[:cut] + "…"
	}
	return s[:max] + "…"
}

// describeKeptRefs renders the list for the schema. An unlabelled reference
// still appears — its id is the part that has to be passable — and a truncated
// list says so, because a model that believes it has seen the whole library
// will not call help to find the rest.
func describeKeptRefs(refs []keptRef) string {
	var parts []string
	for i, r := range refs {
		if i >= maxSchemaKeptRefs {
			parts = append(parts, fmt.Sprintf("…and %d more (call action=\"help\" for the full library)", len(refs)-maxSchemaKeptRefs))
			break
		}
		if r.Label == "" {
			parts = append(parts, r.Ref)
			continue
		}
		parts = append(parts, r.Ref+" ("+r.Label+")")
	}
	return strings.Join(parts, ", ")
}
