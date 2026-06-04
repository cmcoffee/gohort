// Skills in phantom — mirrors orchestrate's stateless model. A conversation
// opts in via Conversation.AllowedSkills. The shared core runtime does the
// work: a trigger match surfaces a soft HINT via RenderSkillTriggerHints
// (a nudge, not an injection); the LLM consults a skill via read_skill /
// skill_knowledge_search (BuildReadSkillTool / BuildSkillKnowledgeSearchTool),
// which is what loads its instructions. No activation, no sub-agents. This
// file only computes phantom's available-skill set for the prompt block.
package phantom

import (
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// formatSkillCollectionHits renders collection search hits for a skill's
// skill_knowledge_search result — one labeled excerpt per hit. Empty when
// there are no hits. The core tool prepends the skill's instructions and
// appends any source-hook results.
func formatSkillCollectionHits(hits []SearchHit) string {
	if len(hits) == 0 {
		return ""
	}
	var b strings.Builder
	for i, h := range hits {
		if i > 0 {
			b.WriteString("\n\n")
		}
		label := strings.TrimSpace(h.Section)
		if label == "" {
			label = strings.TrimSpace(h.Title)
		}
		if label == "" {
			label = h.Source
		}
		b.WriteString("--- ")
		b.WriteString(label)
		b.WriteString(" ---\n")
		b.WriteString(strings.TrimSpace(h.Text))
	}
	return b.String()
}

// phantomAvailableSkillsBlock advertises the allowed skills the LLM can
// draw on via read_skill / skill_knowledge_search. Empty when skills are
// disabled or none are allowed.
func (T *Phantom) phantomAvailableSkillsBlock(conv Conversation) string {
	if conv.DisableSkills || len(conv.AllowedSkills) == 0 {
		return ""
	}
	owner := phantomAgentOwner(T.DB)
	if owner == "" {
		return ""
	}
	allowed := make(map[string]bool, len(conv.AllowedSkills))
	for _, id := range conv.AllowedSkills {
		allowed[id] = true
	}
	var avail []SkillRecord
	for _, s := range LoadSkills(T.DB, owner) {
		if s.Disabled || !allowed[s.ID] {
			continue
		}
		avail = append(avail, s)
	}
	return RenderAvailableSkills(avail)
}
