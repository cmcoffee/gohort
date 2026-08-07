// Kept images in the agent's memory.
//
// core owns the image library (core/image_keep.go) and deliberately knows
// nothing about memory; this file supplies the two hooks it exposes.
//
// WHICH LAYER. Not store_fact: that block is injected into every system prompt
// forever, and its own tool description says quantity there costs tokens on
// every turn. A kept reference image is the other thing that description names
// — material you MIGHT need for a specific question later — so it belongs in
// the pull-only layer behind memory(search), exactly where a saved finding
// goes. Fifty kept images then cost nothing until one is actually relevant.
//
// WHAT IS STORED is a sentence, never the picture: the ref, the caption from
// the one vision pass at keep time, and why the agent kept it. Recall surfaces
// that sentence; the agent then passes image#<name> to a tool if it wants the
// pixels. This is the whole reason captions exist, and it's what lets images
// participate in a text-only vector space without an image embedding model.

package orchestrate

import (
	"context"
	"fmt"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

func init() {
	KeptImageRemember = rememberKeptImage
	KeptImageForgetMemory = forgetKeptImageMemory
	KeptImageParentAgent = keptImageParent
}

// keptImageTopic groups these under one retrievable topic, so "what images do
// I have saved" retrieves as a set rather than as scattered findings.
const keptImageTopic = "kept_images"

// keptImageReportID is DERIVED, not generated. Two consequences, both wanted:
// re-keeping a name replaces its memory entry (IngestReport overwrites every
// chunk under a reportID) instead of leaving a stale caption behind, and
// forgetting can delete the entry without core having to persist an id.
func keptImageReportID(user, agentID, name string) string {
	return fmt.Sprintf("orch-know-%s-%s-keptimg-%s", agentID, user, name)
}

// keptImageMemoryBody is the text an agent will READ months from now, with no
// memory of the turn that wrote it: what the picture is, how to point at it,
// and that the name won't expire. Separate from the ingest call because this
// sentence IS the feature — the vector store is just where it sits.
func keptImageMemoryBody(k KeptImage) string {
	var b strings.Builder
	fmt.Fprintf(&b, "A reference image is saved under the name %q and is addressed as %s.\n", k.Name, k.Ref)
	// The subject goes in FIRST and in plain words, because it is what a later
	// question will be phrased around. A vector store asked "what does Rory look
	// like" has to hit something, and "a reference image named rory_2" is not a
	// sentence that contains the question.
	if subject := SubjectLabel(k.Subject); subject != "" {
		if k.Subject.Person {
			fmt.Fprintf(&b, "It is a picture of %s — this is what %s looks like, and it is the picture to use when a request names them.\n", subject, subject)
		} else {
			fmt.Fprintf(&b, "It is a picture of %s.\n", subject)
		}
	}
	// The description is here so the entry can be FOUND — a vector store
	// matches on text, and "the dog on the sofa" has to hit something. It is
	// not here to be rendered from.
	//
	// That distinction was missing and the description quietly became a
	// substitute for the picture: recall put a vivid paragraph in context while
	// the actual reference needed a tool parameter, so the model wrote a prompt
	// from the prose and generated a NEW picture that merely matched the words.
	// The earlier wording invited exactly that ("work FROM it — write a prompt
	// in the same style"), which is the same mistake as treating a caption of a
	// person as a picture of them.
	if k.Description != "" {
		fmt.Fprintf(&b, "It shows: %s\n", k.Description)
	} else if k.Caption != "" {
		fmt.Fprintf(&b, "It shows: %s\n", k.Caption)
	}
	if k.Note != "" {
		fmt.Fprintf(&b, "Kept because: %s\n", k.Note)
	}
	fmt.Fprintf(&b, "To USE it, pass %s in the images list of an image call. That description is how you find this entry, NOT how you reproduce the picture: "+
		"writing a prompt from those words renders something that merely matches the description, which for a person is a different face and for a logo is a different logo. "+
		"The picture itself is still stored and passing %s costs nothing, so pass it rather than describing it. "+
		"This name does not expire and does not shift the way recent-image numbers do.", k.Ref, k.Ref)
	return b.String()
}

// rememberKeptImage records a kept image in the agent's pull-only memory.
// Best-effort throughout: this runs inside a keep that has already succeeded on
// disk, and no memory failure should turn a saved image into an error.
func rememberKeptImage(sess *ToolSession, k KeptImage) {
	if sess == nil || VectorDB == nil || k.Name == "" {
		return
	}
	user, agentID := strings.TrimSpace(sess.Username), strings.TrimSpace(sess.AgentID)
	if user == "" || agentID == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), knowledgeIngestTimeout())
	defer cancel()
	IngestReport(ctx, VectorDB,
		knowledgeSource(user, agentID, keptImageTopic),
		keptImageReportID(user, agentID, k.Name),
		"## Kept image: "+k.Name+"\n\n"+keptImageMemoryBody(k))
	if sess.DB != nil {
		recordAgentTopic(sess.DB, user, agentID, keptImageTopic)
	}
}

// forgetKeptImageMemory drops the entry when the image itself is forgotten. A
// memory that outlives its image is worse than no memory: recall would keep
// offering a ref that resolves to nothing.
func forgetKeptImageMemory(sess *ToolSession, name string) {
	if sess == nil || VectorDB == nil || name == "" {
		return
	}
	user, agentID := strings.TrimSpace(sess.Username), strings.TrimSpace(sess.AgentID)
	if user == "" || agentID == "" {
		return
	}
	DeleteReportChunks(VectorDB, keptImageReportID(user, agentID, name))
}

// --- inheritance -------------------------------------------------------------

// keptImageParent resolves a sub-agent's parent so core's kept-image walk can
// read the parent's library. Reference images inherit where TOOLS deliberately
// do not (InheritParentTools is opt-in): a tool is a capability, and handing a
// sub-agent one it wasn't granted is escalation. A kept image is read-only
// reference material owned by the same user, the sub-agent cannot modify or
// delete it, and a fleet that can't see its own parent's brand mark is the
// problem this feature exists to solve.
func keptImageParent(sess *ToolSession, agentID string) string {
	if sess == nil || sess.DB == nil || agentID == "" {
		return ""
	}
	user := strings.TrimSpace(sess.Username)
	if user == "" {
		return ""
	}
	udb := UserDB(sess.DB, user)
	if udb == nil {
		return ""
	}
	a, ok := loadAgent(udb, agentID)
	if !ok {
		return ""
	}
	return strings.TrimSpace(a.OwnedBy)
}
