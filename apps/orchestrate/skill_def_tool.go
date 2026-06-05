// skill_def — Builder's authoring surface for skills (conditional
// prompt injection bundles). NOT globally registered; reaches
// catalogs only via builderAuthoringTools when the active agent IS
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
	"time"

	. "github.com/cmcoffee/gohort/core"
)

func skillDefTool() ChatTool { return skillDefImpl{} }

type skillDefImpl struct{}

func (skillDefImpl) Name() string { return "skill_def" }
func (skillDefImpl) Desc() string {
	return "Manage skills — saved domain packs (instructions + optional knowledge + tools) a host agent can draw on. Actions: list (every skill in the user's pool), get (one skill by name), create (author a skill with description, triggers, instructions, optional allowed_tools), update (patch an existing skill — only the fields you pass change, the rest are preserved), delete (drop a skill), help (full usage). Activation is model-driven: the host LLM reads each skill's description in its available-skills list and decides whether to consult the skill (via read_skill / skill_knowledge_search) — so the description is the activation signal. Triggers, when set, are an optional deterministic fast-path (substring/glob match on the message/attachments) that injects a skill's instructions without the LLM deciding."
}
func (skillDefImpl) Params() map[string]ToolParam {
	return map[string]ToolParam{
		"action": {Type: "string", Description: "list | get | create | update | delete | help"},
		"name":   {Type: "string", Description: "(get / create / delete) Skill name. Human-readable; doubles as the lookup key for get / delete."},
		"description": {
			Type:        "string",
			Description: "(create) The activation cue: the host LLM reads this in its skill list and decides whether to consult the skill, so it's the primary activation signal. Write it as if completing \"Use when the user…\", naming the situations it should fire on. Specific descriptions get picked at the right time; generic ones get skipped or over-fire.",
		},
		"triggers": {
			Type:        "array",
			Description: "(create) Optional deterministic fast-path. Plain substrings, case-insensitive, matched against the user message (and inlined attachment header); a glob like *.pdf matches attachment filenames. A match injects the skill's instructions WITHOUT the LLM deciding. Use disambiguating phrases (gh pr, SELECT ), not standalone words. Empty triggers = the skill activates purely when the LLM picks it from the description. Triggers supplement the description; they don't replace it.",
			Items:       &ToolParam{Type: "string"},
		},
		"allowed_tools": {
			Type:        "array",
			Description: "(create) Optional tools the skill brings to the active agent's catalog while it's active. Resolved against the registered tool pool — names not in the pool are silently skipped (e.g. authoring tools won't surface on non-Builder agents). Same shape as agent AllowedTools.",
			Items:       &ToolParam{Type: "string"},
		},
		"attached_collections": {
			Type:        "array",
			Description: "(create) Optional collection IDs whose corpus becomes searchable via knowledge_search when this skill is active. Use to ship domain reference material with the skill — e.g. a Kubernetes skill carries the k8s reference + an instructions section about \"in k8s contexts, prefer X.\" Active path only: when the skill isn't in use this turn, its collections stay out of scope, so heavy reference docs don't leak into unrelated turns. Pass collection IDs from collections(action=list).",
			Items:       &ToolParam{Type: "string"},
		},
		"create_collection": {
			Type:        "boolean",
			Description: "(create) When true, mint a NEW empty knowledge collection named after the skill and auto-attach it (added to attached_collections). Use when the skill needs its own reference corpus and one doesn't exist yet — you get back the collection ID; tell the user to populate it via the Knowledge surface (upload docs or Auto-fill). To link an EXISTING collection instead, pass its ID in attached_collections and leave this off.",
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
	case "update":
		return skillDefUpdate(args, sess)
	case "delete":
		return skillDefDelete(args, sess)
	default:
		return "", fmt.Errorf("unknown action %q. valid: list, get, create, update, delete, help", action)
	}
}

func skillDefHelpText() string {
	return `skill_def — usage

action="list"
  Return every skill in the user's pool as JSON
  [{id, name, description, triggers, allowed_tools, attached_collections, updated}].

action="get", name="<skill name>"
  Fetch one skill's full record (incl. instructions body) by name.

action="create", name=..., description=..., instructions=...,
                 triggers=[...]?, allowed_tools=[...]?,
                 attached_collections=[...]?, create_collection=true?
  Upsert a skill. If a skill with this name already exists in the
  user's pool, it gets replaced (same record, new content).
  attached_collections ships domain corpus alongside the skill —
  searchable via knowledge_search only when the skill is active.
  create_collection=true mints a fresh empty collection named
  "<skill> Knowledge" and auto-links it; the user then fills it via
  the Knowledge surface. Link an existing collection instead by
  passing its ID in attached_collections.

action="update", name=..., [description / instructions / triggers /
                 allowed_tools / attached_collections]
  PATCH an existing skill — only the fields you pass are overwritten;
  everything else is preserved. Use to tweak one thing (e.g. add "war"
  to a geopolitics skill's description) without re-supplying the whole
  record. Errors if no skill with that name exists.

action="delete", name=...
  Drop a skill from the pool by name.

action="help"
  This text.

What skills do: every allowed skill's name + description is listed in
the host agent's prompt. The LLM reads that list and, when a skill's
domain fits the task, consults it (read_skill / skill_knowledge_search)
— activation is the model's call, and the description is what it judges
against. If a skill has triggers, a substring/glob match on the message
or an attachment filename ALSO injects its instructions deterministically
(no LLM decision) — an optional precision fast-path, not a classifier.
An activated skill's allowed_tools join the catalog for that turn.

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
		Triggers            []string `json:"triggers,omitempty"`
		AllowedTools        []string `json:"allowed_tools,omitempty"`
		AttachedCollections []string `json:"attached_collections,omitempty"`
		Updated             string   `json:"updated"`
	}
	out := make([]row, 0, len(skills))
	for _, s := range skills {
		out = append(out, row{
			ID:                  s.ID,
			Name:                s.Name,
			Description:         s.Description,
			Triggers:            s.Triggers,
			AllowedTools:        s.AllowedTools,
			AttachedCollections: s.AttachedCollections,
			Updated:             s.Updated.Format("2006-01-02 15:04:05"),
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
		return "", errors.New("description is required — the host LLM reads it to decide whether to consult this skill")
	}
	instructions := stringArg(args, "instructions")
	if strings.TrimSpace(instructions) == "" {
		return "", errors.New("instructions is required — that's the markdown body that gets injected when the skill activates")
	}
	triggers := stringSliceFromArgs(args, "triggers")
	allowedTools := stringSliceFromArgs(args, "allowed_tools")
	attachedCollections := stringSliceFromArgs(args, "attached_collections")

	// create_collection: mint a fresh empty collection for this skill and
	// auto-link it, so authoring a skill + giving it a corpus is one step.
	createColl := false
	switch v := args["create_collection"].(type) {
	case bool:
		createColl = v
	case string:
		createColl = strings.EqualFold(strings.TrimSpace(v), "true")
	}
	mintedCollection := ""
	if createColl {
		c := Collection{
			ID:          UUIDv4(),
			Owner:       sess.Username,
			Name:        name + " Knowledge",
			Description: description,
			Created:     time.Now(),
		}
		saveCollection(sess.DB, c)
		attachedCollections = append(attachedCollections, c.ID)
		mintedCollection = c.ID
		Log("[orchestrate.skill_def] minted collection %q (id=%s) for skill %q user=%q", c.Name, c.ID, name, sess.Username)
	}

	// Upsert by name. If an existing skill matches, reuse its ID +
	// Created so the record's identity is preserved across edits.
	existing, hadPrior := FindSkillByName(sess.DB, sess.Username, name)
	rec := SkillRecord{
		Name:                name,
		Description:         description,
		Triggers:            triggers,
		AllowedTools:        allowedTools,
		AttachedCollections: attachedCollections,
		Instructions:        instructions,
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
	collNote := ""
	if mintedCollection != "" {
		collNote = fmt.Sprintf(" Created and linked an empty knowledge collection %q Knowledge (id=%s) — it has no documents yet, so tell the user to populate it via the Knowledge surface (upload docs or Auto-fill) before the skill's knowledge_search returns anything.", saved.Name, mintedCollection)
	}
	return fmt.Sprintf("Skill %q %s%s. Active when triggers match OR the embedded description scores above 0.55 against the user message.%s", saved.Name, verb, embedNote, collNote), nil
}

// skillDefUpdate patches an EXISTING skill in place: only the fields
// present in args are overwritten; everything else is preserved. This is
// the "tweak one thing" path (e.g. add "war" to a geopolitics skill's
// description) so callers don't have to re-supply instructions / triggers
// / collections and risk wiping them, which a full create-upsert would do.
func skillDefUpdate(args map[string]any, sess *ToolSession) (string, error) {
	name := strings.TrimSpace(stringArg(args, "name"))
	if name == "" {
		return "", errors.New("name is required for action=update")
	}
	existing, ok := FindSkillByName(sess.DB, sess.Username, name)
	if !ok {
		return "", fmt.Errorf("skill %q not found — use action=create to make a new one", name)
	}
	rec := existing
	var changed []string
	if _, has := args["description"]; has {
		if s := strings.TrimSpace(stringArg(args, "description")); s != "" {
			rec.Description = s
			changed = append(changed, "description")
		}
	}
	if _, has := args["instructions"]; has {
		if s := strings.TrimSpace(stringArg(args, "instructions")); s != "" {
			rec.Instructions = stringArg(args, "instructions")
			changed = append(changed, "instructions")
		}
	}
	if _, has := args["triggers"]; has {
		rec.Triggers = stringSliceFromArgs(args, "triggers")
		changed = append(changed, "triggers")
	}
	if _, has := args["allowed_tools"]; has {
		rec.AllowedTools = stringSliceFromArgs(args, "allowed_tools")
		changed = append(changed, "allowed_tools")
	}
	if _, has := args["attached_collections"]; has {
		rec.AttachedCollections = stringSliceFromArgs(args, "attached_collections")
		changed = append(changed, "attached_collections")
	}
	if len(changed) == 0 {
		return "", errors.New("nothing to update — pass at least one of description, instructions, triggers, allowed_tools, attached_collections")
	}
	saved, err := SaveSkill(sess.DB, sess.Username, rec)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("Skill %q updated (%s). All other fields preserved.", saved.Name, strings.Join(changed, ", ")), nil
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
