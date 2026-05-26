// skill_def — Builder's authoring surface for skills (conditional
// prompt injection bundles). NOT globally registered; reaches
// catalogs only via builderInternalTools when the active agent IS
// Builder. Same exclusivity model as create_agent / add_tool / tool_def.
//
// Actions: list (read), get (read one), create (upsert), delete,
// help. Skills authored here land in the calling user's per-user
// skill pool and become eligible for the global classifier to
// activate on future turns.

package orchestrate

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

func skillDefTool() ChatTool { return skillDefImpl{} }

type skillDefImpl struct{}

func (skillDefImpl) Name() string { return "skill_def" }
func (skillDefImpl) Desc() string {
	return "Manage skills — conditional prompt addendums that auto-activate based on the user's message. Actions: list (every skill in the user's pool), get (one skill by name), create (upsert a skill with description, triggers, instructions, optional allowed_tools), delete (drop a skill), help (full usage). Skills are like dynamic personas: their instructions get appended to the active agent's system prompt only when the classifier matches the user's message against the skill's description + triggers."
}
func (skillDefImpl) Params() map[string]ToolParam {
	return map[string]ToolParam{
		"action": {Type: "string", Description: "list | get | create | delete | help"},
		"name":   {Type: "string", Description: "(get / create / delete) Skill name. Human-readable; doubles as the lookup key for get / delete."},
		"description": {
			Type:        "string",
			Description: "(create) One-sentence \"when to use this skill\" hint. The classifier embeds this and matches it against the user's message — write it as if completing the sentence \"Use when the user…\". Specific descriptions match precisely; generic ones over-activate.",
		},
		"triggers": {
			Type:        "array",
			Description: "(create) Optional explicit-match patterns. Plain substrings, case-insensitive, matched against the user message (and any inlined attachment header). Use for high-precision activation like `.pdf`, `gh pr`, `SELECT `. Empty triggers = embedding-only activation. Adding triggers is a precision boost; the embedding still runs as fallback for fuzzy variants.",
			Items:       &ToolParam{Type: "string"},
		},
		"allowed_tools": {
			Type:        "array",
			Description: "(create) Optional tools the skill brings to the active agent's catalog while it's active. Resolved against the registered tool pool — names not in the pool are silently skipped (e.g. authoring tools won't surface on non-Builder agents). Same shape as agent AllowedTools.",
			Items:       &ToolParam{Type: "string"},
		},
		"instructions": {
			Type:        "string",
			Description: "(create) Markdown body that gets appended to the active agent's system prompt when this skill activates. Write it as additive guidance — \"when this kind of task comes up, also do X, Y, Z.\" The framework prepends an `## Skill: <name>` H2 header automatically.",
		},
	}
}

func (s skillDefImpl) Run(map[string]any) (string, error) {
	return "", errors.New("skill_def requires a session context")
}

func (s skillDefImpl) RunWithSession(args map[string]any, sess *ToolSession) (string, error) {
	if sess == nil || sess.DB == nil || sess.Username == "" {
		return "", errors.New("skill_def requires authenticated session")
	}
	action := strings.TrimSpace(stringArg(args, "action"))
	switch action {
	case "", "help":
		return skillDefHelpText(), nil
	case "list":
		return skillDefList(sess)
	case "get":
		return skillDefGet(args, sess)
	case "create":
		return skillDefCreate(args, sess)
	case "delete":
		return skillDefDelete(args, sess)
	default:
		return "", fmt.Errorf("unknown action %q. valid: list, get, create, delete, help", action)
	}
}

func skillDefHelpText() string {
	return `skill_def — usage

action="list"
  Return every skill in the user's pool as JSON
  [{id, name, description, triggers, allowed_tools, updated}].

action="get", name="<skill name>"
  Fetch one skill's full record (incl. instructions body) by name.

action="create", name=..., description=..., instructions=...,
                 triggers=[...]?, allowed_tools=[...]?
  Upsert a skill. If a skill with this name already exists in the
  user's pool, it gets replaced (same record, new content). Skill is
  embedded automatically for the fuzzy-match classifier.

action="delete", name=...
  Drop a skill from the pool by name.

action="help"
  This text.

What skills do: at the start of every turn (for non-Builder agents),
the classifier scans the user's message against every skill's triggers
and embedded description. Matches above threshold (capped at 3 active
skills per turn) get their instructions appended to the active agent's
system prompt, and their allowed_tools get unioned into the catalog
for that turn only.

Think of skills as dynamic personas: "if the user is doing X, also
know Y." Keep them focused — one skill, one capability. Multiple small
skills compose better than one giant catch-all.`
}

func skillDefList(sess *ToolSession) (string, error) {
	skills := LoadSkills(sess.DB, sess.Username)
	type row struct {
		ID           string   `json:"id"`
		Name         string   `json:"name"`
		Description  string   `json:"description,omitempty"`
		Triggers     []string `json:"triggers,omitempty"`
		AllowedTools []string `json:"allowed_tools,omitempty"`
		Updated      string   `json:"updated"`
	}
	out := make([]row, 0, len(skills))
	for _, s := range skills {
		out = append(out, row{
			ID:           s.ID,
			Name:         s.Name,
			Description:  s.Description,
			Triggers:     s.Triggers,
			AllowedTools: s.AllowedTools,
			Updated:      s.Updated.Format("2006-01-02 15:04:05"),
		})
	}
	b, _ := json.Marshal(out)
	return string(b), nil
}

func skillDefGet(args map[string]any, sess *ToolSession) (string, error) {
	name := strings.TrimSpace(stringArg(args, "name"))
	if name == "" {
		return "", errors.New("name is required for action=get")
	}
	s, ok := FindSkillByName(sess.DB, sess.Username, name)
	if !ok {
		return "", fmt.Errorf("skill %q not found", name)
	}
	b, _ := json.Marshal(s)
	return string(b), nil
}

func skillDefCreate(args map[string]any, sess *ToolSession) (string, error) {
	name := strings.TrimSpace(stringArg(args, "name"))
	if name == "" {
		return "", errors.New("name is required for action=create")
	}
	description := strings.TrimSpace(stringArg(args, "description"))
	if description == "" {
		return "", errors.New("description is required — the classifier embeds it to match against user messages")
	}
	instructions := stringArg(args, "instructions")
	if strings.TrimSpace(instructions) == "" {
		return "", errors.New("instructions is required — that's the markdown body that gets injected when the skill activates")
	}
	triggers := stringSliceFromArgs(args, "triggers")
	allowedTools := stringSliceFromArgs(args, "allowed_tools")

	// Upsert by name. If an existing skill matches, reuse its ID +
	// Created so the record's identity is preserved across edits.
	existing, hadPrior := FindSkillByName(sess.DB, sess.Username, name)
	rec := SkillRecord{
		Name:         name,
		Description:  description,
		Triggers:     triggers,
		AllowedTools: allowedTools,
		Instructions: instructions,
	}
	if hadPrior {
		rec.ID = existing.ID
		rec.Created = existing.Created
	}
	saved, err := SaveSkill(sess.DB, sess.Username, rec)
	if err != nil {
		return "", err
	}
	verb := "created"
	if hadPrior {
		verb = "updated"
	}
	embedNote := ""
	if len(saved.Embedding) == 0 {
		embedNote = " (embedding unavailable — only trigger-match activation will fire until next re-save)"
	}
	return fmt.Sprintf("Skill %q %s%s. Active when triggers match OR the embedded description scores above 0.55 against the user message.", saved.Name, verb, embedNote), nil
}

func skillDefDelete(args map[string]any, sess *ToolSession) (string, error) {
	name := strings.TrimSpace(stringArg(args, "name"))
	if name == "" {
		return "", errors.New("name is required for action=delete")
	}
	s, ok := FindSkillByName(sess.DB, sess.Username, name)
	if !ok {
		return "", fmt.Errorf("skill %q not found", name)
	}
	if !DeleteSkill(sess.DB, sess.Username, s.ID) {
		return "", fmt.Errorf("delete skill %q failed", name)
	}
	return fmt.Sprintf("Skill %q deleted.", name), nil
}
