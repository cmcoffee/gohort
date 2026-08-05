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
	// The detailed description, not the label: this text is read by an agent
	// that cannot see the picture and may need to work FROM it — write a
	// prompt in the same style, brief someone else — and a one-line label
	// forces it back into a vision call for every such question.
	if k.Description != "" {
		fmt.Fprintf(&b, "It shows: %s\n", k.Description)
	} else if k.Caption != "" {
		fmt.Fprintf(&b, "It shows: %s\n", k.Caption)
	}
	if k.Note != "" {
		fmt.Fprintf(&b, "Kept because: %s\n", k.Note)
	}
	fmt.Fprintf(&b, "To use it, pass %s anywhere an image reference is accepted (for example the images list of an image edit). "+
		"This name does not expire and does not shift the way recent-image numbers do. "+
		"The picture itself is still stored, so if this description leaves out a detail you need, look at %s directly rather than guessing.", k.Ref, k.Ref)
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
