package orchestrate

// The Cartographer memory mode: an agent whose memory is for a map of what it
// works on (a codebase, a system, how a project's parts connect). It keeps
// Agent mode's lessons-only pinned notes, and is told to build and keep the
// map in its graph memory.

import "strings"

// memoryModeCartographer is the third MemoryMode, beside "agent" and
// "chatbot". Everywhere else it reads as "agent": lessons, not people.
const memoryModeCartographer = "cartographer"

// migrateGraphSize reads the retired GraphSize "large" as the Cartographer
// mode, which is what it had come to mean: a setting that both enlarged an
// agent's graph and told it to keep a map there. The graph is one size again,
// and the map instruction is the mode. On READ, like migrateExposedFlag, so an
// agent saved with it keeps mapping without being saved again; a Chatbot one
// stays a Chatbot. Idempotent: the field is cleared once read.
func migrateGraphSize(a AgentRecord) AgentRecord {
	if a.GraphSize == "" {
		return a
	}
	if a.GraphSize == "large" && (a.MemoryMode == "" || a.MemoryMode == "agent") {
		a.MemoryMode = memoryModeCartographer
	}
	a.GraphSize = ""
	return a
}

// focusedMemoryMode reports whether an agent's memory is framed for a job
// rather than for people: Agent and Cartographer.
func focusedMemoryMode(mode string) bool {
	m := strings.ToLower(strings.TrimSpace(mode))
	return m == "agent" || m == memoryModeCartographer
}

// renderMapPromptSection tells a Cartographer that building a map is its job
// and that graph memory is where the map lives. The graph tools alone did not
// say so: their descriptions are about people and organisations, and an agent
// mapping a codebase reported what it found in replies and never filed it,
// until somebody reminded it. A graph does not age by date (the cap only
// crowds out what is least used), so it also says to remove a part that is
// gone and to correct a changed connection. Fixed text, so the prompt stays
// cacheable; empty without the graph tools (Explicit Memory off).
func renderMapPromptSection(agent AgentRecord) string {
	if agent.MemoryMode != memoryModeCartographer || agent.DisableExplicit {
		return ""
	}
	return "## Your map\n\n" +
		"Your job includes building a map, and your graph memory is where it lives: the parts of what you are mapping (modules, services, databases, files that matter) and how they connect. " +
		"Record each connection with link_entities as soon as you find it, one relationship per call (\"api\" calls \"auth\"; \"orders\" writes to \"orders_db\"), with paths, versions and ports as attributes. " +
		"Before exploring a part, look it up with recall_about: what an earlier run mapped does not need finding again. " +
		"Keep it true as well as full: nothing in it expires, so when a part turns out to be gone, remove it with forget_graph, and when a connection has changed, record the new one with replace=true rather than adding a second. " +
		"The map is what carries over to the next conversation; a finding you only state in a reply is gone after it.\n\n"
}
