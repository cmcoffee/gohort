package orchestrate

// Memory modes: what an agent's memory is for. One field (AgentRecord.
// MemoryMode), three values, and the questions the rest of the package asks
// of it answered here, so a reader finds every meaning of the setting in one
// place.
//
//	agent         lessons for doing its job (the default wording)
//	chatbot       lessons, plus the people it talks with
//	cartographer  lessons like agent, plus a map of what it works on, kept in
//	              graph memory ("## Your map", renderMapPromptSection)
//
// Servitor's investigator uses a fourth wording, "shortcuts", for its pinned
// commands (memoryModeCopy); no agent here is set to it.

import "strings"

const (
	memoryModeAgent        = "agent"
	memoryModeChatbot      = "chatbot"
	memoryModeCartographer = "cartographer"
)

// memoryModeOf is a stored mode as these functions compare it.
func memoryModeOf(mode string) string { return strings.ToLower(strings.TrimSpace(mode)) }

// memoryForPeople reports whether an agent's memory is set for the people it
// talks with: Chatbot. Unset is not, so an agent nobody chose a mode for
// leaves session recall off (sessionRecallOn).
func memoryForPeople(mode string) bool { return memoryModeOf(mode) == memoryModeChatbot }

// memoryForAJob reports whether an agent's memory is set for a job: Agent or
// Cartographer. Unset is not this either: an agent Builder creates without
// naming a mode is a chat assistant and reads its Cortex
// (newAgentReadsCortex). The two questions differ only on the unset mode,
// which is why there are two.
func memoryForAJob(mode string) bool {
	m := memoryModeOf(mode)
	return m == memoryModeAgent || m == memoryModeCartographer
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
	if memoryModeOf(agent.MemoryMode) != memoryModeCartographer || agent.DisableExplicit {
		return ""
	}
	return "## Your map\n\n" +
		"Your job includes building a map, and your graph memory is where it lives: the parts of what you are mapping (modules, services, databases, files that matter) and how they connect. " +
		"Record each connection with link_entities as soon as you find it, one relationship per call (\"api\" calls \"auth\"; \"orders\" writes to \"orders_db\"), with paths, versions and ports as attributes. " +
		"Before exploring a part, look it up with recall_about: what an earlier run mapped does not need finding again. " +
		"Keep it true as well as full: nothing in it expires, so when a part turns out to be gone, remove it with forget_graph, and when a connection has changed, record the new one with replace=true rather than adding a second. " +
		"The map is what carries over to the next conversation; a finding you only state in a reply is gone after it.\n\n"
}
