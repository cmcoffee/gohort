package orchestrate

// Memory modes: what an agent's memory is for. One field (AgentRecord.
// MemoryMode), two values, and the questions the rest of the package asks of
// it answered here, so a reader finds every meaning of the setting in one
// place.
//
//	agent    lessons for doing its job (the default wording)
//	chatbot  lessons, plus the people it talks with
//
// Servitor's investigator uses a fourth wording, "shortcuts", for its pinned
// commands (memoryModeCopy); no agent here is set to it.

import "strings"

const (
	memoryModeAgent   = "agent"
	memoryModeChatbot = "chatbot"
)

// memoryModeOf is a stored mode as these functions compare it.
func memoryModeOf(mode string) string { return strings.ToLower(strings.TrimSpace(mode)) }

// memoryForPeople reports whether an agent's memory is set for the people it
// talks with: Chatbot. Unset is not, so an agent nobody chose a mode for
// leaves session recall off (sessionRecallOn).
func memoryForPeople(mode string) bool { return memoryModeOf(mode) == memoryModeChatbot }

// memoryForAJob reports whether an agent's memory is set for a job: Agent.
// Unset is not this either: an agent Builder creates without
// naming a mode is a chat assistant and reads its Cortex
// (newAgentReadsCortex). The two questions differ only on the unset mode,
// which is why there are two.
func memoryForAJob(mode string) bool {
	return memoryModeOf(mode) == memoryModeAgent
}
