// skill_def — Builder's authoring surface for skills (conditional
// prompt injection bundles). NOT globally registered; reaches
// catalogs only via builderAuthoringTools when the active agent IS
// Builder. Same exclusivity model as create_agent / add_tool / tool_def.
//
// Actions: list (read), get (read one), create, update, delete, help.
// Skills authored here land in the calling user's per-user skill pool;
// update and delete also reach the ones the author published. Host
// agents activate them by reading the description in their skill list
// (a trigger match adds a hint, nothing more).

package orchestrate

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

func skillDefTool() ChatTool { return skillDefImpl{} }

type skillDefImpl struct{}

func (skillDefImpl) Name() string { return "skill_def" }
func (skillDefImpl) Desc() string {
	return "Manage skills: saved domain packs (instructions + optional knowledge + tools) a host agent can draw on. Actions: list (every skill in the user's pool), get (one skill by name), create (author a NEW skill with description, triggers, instructions, optional allowed_tools; refused when you already have a skill by that name), update (patch an existing skill, only the fields you pass change, the rest are preserved), delete (drop a skill), help (full usage). Activation is model-driven: the host LLM reads each skill's description in its available-skills list and decides whether to consult the skill (via read_skill / skill_knowledge_search), so the description is the activation signal. Triggers, when set, are an optional precision nudge (substring/glob match on the message/attachments): a match surfaces a 'likely relevant this turn' hint to the host LLM, which still decides whether to consult."
}
func (skillDefImpl) Params() map[string]ToolParam {
	return map[string]ToolParam{
		"action": {Type: "string", Description: "list | get | create | update | delete | help"},
		"name":   {Type: "string", Description: "(get / create / update / delete) Skill name. Human-readable; doubles as the lookup key for get / update / delete (a skill's id also works there, for telling two same-named skills apart)."},
		"description": {
			Type:        "string",
			Description: "(create / update) The activation cue: the host LLM reads this in its skill list and decides whether to consult the skill, so it's the primary activation signal. Write it as if completing \"Use when the user…\", naming the situations it should fire on. Specific descriptions get picked at the right time; generic ones get skipped or over-fire.",
		},
		"triggers": {
			Type:        "array",
			Description: "(create / update) Optional precision nudge. Plain substrings, case-insensitive, matched against the user message (and inlined attachment header); a glob like *.pdf matches attachment filenames. A match surfaces a 'likely relevant this turn' hint to the host LLM: it does NOT force injection; the LLM still decides. Use disambiguating phrases (gh pr, SELECT ), not standalone words. Empty triggers = the skill activates purely when the LLM picks it from the description. Triggers supplement the description; they don't replace it.",
			Items:       &ToolParam{Type: "string"},
		},
		"allowed_tools": {
			Type:        "array",
			Description: "(create / update) Optional tool names tied to the skill. A name that matches a tool you authored this session (or the user's persistent pool) is SNAPSHOTTED into the skill: it ships with the skill and becomes callable whenever the skill is consulted (the skill's own executable code, e.g. a calculator or screener). A name that matches a source hook is used by skill_knowledge_search to query that source. Author the script first with tool_def(mode=\"shell\"), then list its name here to bundle it.",
			Items:       &ToolParam{Type: "string"},
		},
		"attached_collections": {
			Type:        "array",
			Description: rewriteMemoryToolNames("(create / update) Optional collection IDs whose corpus becomes searchable via knowledge_search when this skill is active. Use to ship domain reference material with the skill: e.g. a Kubernetes skill carries the k8s reference + an instructions section about \"in k8s contexts, prefer X.\" Active path only: when the skill isn't in use this turn, its collections stay out of scope, so heavy reference docs don't leak into unrelated turns. Pass collection IDs from collections(action=list)."),
			Items:       &ToolParam{Type: "string"},
		},
		"attach_to_agents": {
			Type:        "array",
			Description: "(create / update) Agent names or IDs to add this skill to, by appending it to each one's allowed_skills. A skill an agent does not allow is invisible to it: creating one without attaching it leaves it in the user's pool doing nothing. Same argument the machine and pipeline tools take.",
			Items:       &ToolParam{Type: "string"},
		},
		"create_collection": {
			Type:        "boolean",
			Description: "(create) When true, mint a NEW empty knowledge collection named after the skill and auto-attach it (added to attached_collections). Use when the skill needs its own reference corpus and one doesn't exist yet: you get back the collection ID; tell the user to populate it via the Knowledge surface (upload docs or Auto-fill). To link an EXISTING collection instead, pass its ID in attached_collections and leave this off.",
		},
		"instructions": {
			Type:        "string",
			Description: "(create / update) Markdown body that gets appended to the active agent's system prompt when this skill activates. Write it as additive guidance, \"when this kind of task comes up, also do X, Y, Z.\" The framework prepends an `## Skill: <name>` H2 header automatically.",
		},
		"playbook": {
			Type:        "string",
			Description: "(create / update) Optional. The skill's CONDITIONAL behaviour as a JSON array of rules, each \"establish Y; if Y then Z, else U\". When the skill is consulted the framework ESTABLISHES each rule's fact itself (a step with the skill's tools and a declared output), and hands the host agent only the arm that applies, so the condition is settled before either branch can start. Rule shape: {\"fact\": \"queue_draining\", \"how\": \"Read the consumer lag for the orders queue over the last five minutes.\", \"then\": \"Look at the consumer: its log, restart count, lag trend.\", \"else\": \"Look at the broker: connectivity from the consumer host, partition state, disk.\"}. Optional: \"when\": [triggers] to apply the rule only on matching turns; \"type\": \"choice\" with \"values\": [...] and \"cases\": {value: arm} for a many-way branch; \"then_rule\" / \"else_rule\" / \"case_rules\" to nest another rule (two levels max). A playbook skill FIRES ON ITS OWN when the skill's triggers or a rule's when match the message (the facts are established before the agent's first round), so give a playbook skill triggers; without them it runs only when the agent chooses to consult the skill. Put prose that does not branch in instructions, not here. Pass \"[]\" to clear.",
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
	return `skill_def: usage

action="list"
  Return every skill in the user's pool as JSON
  [{id, name, description, triggers, allowed_tools, attached_collections, updated}].

action="get", name="<skill name>"
  Fetch one skill's full record (incl. instructions body) by name.

action="create", name=..., description=..., instructions=...,
                 triggers=[...]?, allowed_tools=[...]?,
                 attached_collections=[...]?, create_collection=true?
  Create a NEW skill. Refused when the user already has a skill with
  this name, in their own pool or one they published: use
  action="update" to change that one instead.
  attached_collections ships domain corpus alongside the skill
  searchable via knowledge_search only when the skill is active.
  create_collection=true mints a fresh empty collection named
  "<skill> Knowledge" and auto-links it; the user then fills it via
  the Knowledge surface. Link an existing collection instead by
  passing its ID in attached_collections.

action="update", name=..., [description / instructions / triggers /
                 allowed_tools / attached_collections]
  PATCH an existing skill: only the fields you pass are overwritten;
  everything else is preserved. Use to tweak one thing (e.g. add "war"
  to a geopolitics skill's description) without re-supplying the whole
  record. Errors if no skill with that name exists.

action="delete", name=...
  Drop a skill by name. A skill the user published deployment-wide is
  taken back from everybody and then deleted.

action="help"
  This text.

What skills do: every allowed skill's name + description is listed in
the host agent's prompt. The LLM reads that list and, when a skill's
domain fits the task, consults it (read_skill / skill_knowledge_search)
- activation is the model's call, and the description is what it judges
against. If a skill has triggers, a substring/glob match on the message
or an attachment filename surfaces a "likely relevant this turn" hint to
the host LLM: a nudge toward consulting, not a forced injection.
A consulted skill's BUNDLED tools (scripts snapshotted from allowed_tools at
author time) join the catalog for that turn: its own executable code, callable
without spending context tokens.

Think of skills as dynamic personas: "if the user is doing X, also
know Y." Keep them focused: one skill, one capability. Multiple small
skills compose better than one giant catch-all.`
}

func skillDefList(sess *ToolSession) (string, error) {
	skills := AvailableSkills(sess.DB, sess.Username)
	type row struct {
		ID                  string   `json:"id"`
		Name                string   `json:"name"`
		Description         string   `json:"description,omitempty"`
		Triggers            []string `json:"triggers,omitempty"`
		AllowedTools        []string `json:"allowed_tools,omitempty"`
		AttachedCollections []string `json:"attached_collections,omitempty"`
		// Whose it is. The list spans the user's own skills, the ones shared
		// with them and the deployment's, and two of those can share a name.
		From    string `json:"from,omitempty"`
		Updated string `json:"updated"`
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
			From:                chFirst(s.SharedFrom, s.Owner),
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
	s, _, ok, err := authoredSkill(sess, name)
	if err != nil {
		return "", err
	}
	if !ok {
		// Not one of theirs: one shared with them or the deployment's, which
		// list shows too and get should be able to open.
		var hits []SkillRecord
		for _, a := range AvailableSkills(sess.DB, sess.Username) {
			if a.ID == name || strings.EqualFold(strings.TrimSpace(a.Name), name) {
				hits = append(hits, a)
			}
		}
		switch len(hits) {
		case 0:
			return "", fmt.Errorf("skill %q not found", name)
		case 1:
			s = hits[0]
		default:
			return "", fmt.Errorf("more than one skill available to you is called %q - pass the id as name: %s", name, skillIDList(hits))
		}
	}
	b, _ := json.Marshal(s)
	return string(b), nil
}

// authoredSkill finds one of the caller's OWN skills by id or name, in both
// pools an author writes to: their own, and the deployment's for one they
// published. Publishing moves a skill out of the author's pool, so a lookup
// that stopped there could neither update nor delete it, and a create by the
// same name landed a private duplicate beside it.
//
// Two of their skills answering to one name is an error naming the ids, not a
// pick: update and delete are writes, and the wrong one is not undone by
// noticing afterwards.
func authoredSkill(sess *ToolSession, key string) (s SkillRecord, published, ok bool, err error) {
	key = strings.TrimSpace(key)
	if key == "" {
		return SkillRecord{}, false, false, nil
	}
	own := LoadSkills(sess.DB, sess.Username)
	pub := PublishedSkillsBy(sess.DB, sess.Username)
	for _, r := range own {
		if r.ID == key {
			return r, false, true, nil
		}
	}
	for _, r := range pub {
		if r.ID == key {
			return r, true, true, nil
		}
	}
	var hits []SkillRecord
	var hitPublished []bool
	for _, r := range own {
		if strings.EqualFold(strings.TrimSpace(r.Name), key) {
			hits, hitPublished = append(hits, r), append(hitPublished, false)
		}
	}
	for _, r := range pub {
		if strings.EqualFold(strings.TrimSpace(r.Name), key) {
			hits, hitPublished = append(hits, r), append(hitPublished, true)
		}
	}
	switch len(hits) {
	case 0:
		return SkillRecord{}, false, false, nil
	case 1:
		return hits[0], hitPublished[0], true, nil
	}
	return SkillRecord{}, false, false, fmt.Errorf("you have more than one skill called %q - pass the id as name to pick one: %s", key, skillIDList(hits))
}

// skillIDList renders same-named skills so the caller can pick one by id.
func skillIDList(skills []SkillRecord) string {
	parts := make([]string, len(skills))
	for i, s := range skills {
		parts[i] = s.ID
		if who := chFirst(s.SharedFrom, s.Owner); who != "" {
			parts[i] += " (from " + who + ")"
		}
	}
	return strings.Join(parts, ", ")
}

func skillDefCreate(args map[string]any, sess *ToolSession) (string, error) {
	name := strings.TrimSpace(stringArg(args, "name"))
	if name == "" {
		return "", errors.New("name is required for action=create")
	}
	description := strings.TrimSpace(stringArg(args, "description"))
	if description == "" {
		return "", errors.New("description is required: the host LLM reads it to decide whether to consult this skill")
	}
	instructions := stringArg(args, "instructions")
	if strings.TrimSpace(instructions) == "" {
		return "", errors.New("instructions is required: that's the markdown body that gets injected when the skill activates")
	}
	triggers := stringSliceFromArgs(args, "triggers")
	allowedTools := stringSliceFromArgs(args, "allowed_tools")
	attachedCollections := stringSliceFromArgs(args, "attached_collections")
	playbook, hasPlaybook, err := playbookFromArgs(args)
	if err != nil {
		return "", err
	}
	// A name the author already has is refused, and before anything is
	// minted. Create used to upsert by name against the private pool only, so
	// after the author published "Triage", creating "Triage" landed a second,
	// private one beside it: two skills, one name, and every agent naming it
	// resolving whichever came first. Changing an existing skill is update.
	if prior, published, found, err := authoredSkill(sess, name); err != nil {
		return "", err
	} else if found {
		where := "in your skills"
		if published {
			where = "published deployment-wide"
		}
		return "", fmt.Errorf("you already have a skill called %q (%s, id %s) - use action=update to change it, or pick another name", prior.Name, where, prior.ID)
	}

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
		// Checked before anything is saved. A collection by this name is most
		// likely this skill's corpus from before, and a second one of the same
		// name is how a later attach by name picks the wrong one.
		if clash := duplicateCollectionName(sess.DB, sess.Username, name+" Knowledge", ""); clash != "" {
			return "", fmt.Errorf("%s - pass its id in attached_collections instead of create_collection=true", clash)
		}
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

	rec := SkillRecord{
		Name:                name,
		Description:         description,
		Triggers:            triggers,
		AllowedTools:        allowedTools,
		AttachedCollections: attachedCollections,
		Instructions:        instructions,
	}
	if hasPlaybook {
		rec.Playbook = playbook
	}
	// Snapshot any allowed_tools that name a local session/persistent tool INTO
	// the skill so it ships its own executable code (portable, self-contained).
	copiedTools := autoCopySessionToolsForSkill(sess, &rec)
	saved, err := SaveSkill(sess.DB, sess.Username, rec)
	if err != nil {
		return "", err
	}
	attached, unknown := attachSkillToAgents(sess, args["attach_to_agents"], saved.ID)
	collNote := ""
	if mintedCollection != "" {
		collNote = fmt.Sprintf(" Created and linked an empty knowledge collection %q Knowledge (id=%s): it has no documents yet, so tell the user to populate it via the Knowledge surface (upload docs or Auto-fill) before the skill's knowledge_search returns anything.", saved.Name, mintedCollection)
	}
	toolNote := ""
	if copiedTools > 0 {
		toolNote = fmt.Sprintf(" Bundled %d tool(s) INTO the skill: they ship with it and become callable whenever the skill is consulted.", copiedTools)
	}
	return fmt.Sprintf("Skill %q created.%s%s %s%s", saved.Name, toolNote,
		attachNote(attached, unknown), activationNote(saved), collNote), nil
}

// attachNote says what attaching did, in the words the caller needs to hear:
// a skill no agent allows is invisible, and the tool used to answer a create
// with a sentence about how agents activate skills, which read as
// confirmation that this one was attached to something. It was not.
func attachNote(attached, unknown []string) string {
	var b strings.Builder
	switch {
	case len(attached) > 0:
		fmt.Fprintf(&b, " Attached to %s.", strings.Join(attached, ", "))
	case len(unknown) == 0:
		b.WriteString(" NOT attached to any agent, so no agent can see it: pass attach_to_agents, or add it to an agent's allowed_skills.")
	}
	if len(unknown) > 0 {
		fmt.Fprintf(&b, " No agent found named: %s.", strings.Join(unknown, ", "))
	}
	return b.String()
}

// activationNote says how the skill comes into a turn — which differs once it
// carries a playbook, because a playbook fires on a match rather than waiting
// to be consulted.
func activationNote(s SkillRecord) string {
	if len(s.Playbook) > 0 {
		if len(s.Triggers) > 0 {
			return "It carries a playbook, so on a turn matching its triggers the framework establishes its facts BEFORE the agent's first round and hands the agent only the arm that applies."
		}
		return "It carries a playbook but NO triggers, so it fires only when the agent chooses to consult it: give it triggers if it should fire on its own."
	}
	return "Host agents activate it by reading its description in their skill list; a trigger match adds a \"likely relevant\" hint on matching turns."
}

// attachSkillToAgents appends the skill to each named agent's allowed_skills.
func attachSkillToAgents(sess *ToolSession, raw any, skillID string) (attached, unknown []string) {
	var names []string
	switch v := raw.(type) {
	case []any:
		for _, n := range v {
			names = append(names, fmt.Sprint(n))
		}
	case []string:
		names = v
	case string:
		if strings.TrimSpace(v) != "" {
			names = []string{v}
		}
	}
	for _, key := range names {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		ag, ok := findAgentByNameOrID(sess.DB, sess.Username, key)
		if !ok {
			unknown = append(unknown, key)
			continue
		}
		if msg := agentEditRefusal(ag, sess.Username); msg != "" {
			unknown = append(unknown, key+" ("+msg+")")
			continue
		}
		label := chFirst(ag.Name, ag.ID)
		if slices.Contains(ag.AllowedSkills, skillID) {
			attached = append(attached, label)
			continue
		}
		ag.AllowedSkills = append(ag.AllowedSkills, skillID)
		if _, err := saveAgent(sess.DB, ag); err != nil {
			unknown = append(unknown, key+" (save failed: "+err.Error()+")")
			continue
		}
		attached = append(attached, label)
	}
	return attached, unknown
}

// autoCopySessionToolsForSkill snapshots any tool named in the skill's
// AllowedTools that matches a session draft or the user's persistent pool INTO
// the skill's bundled Tools, so the skill ships its own executable code and
// stays portable (mirrors autoCopySessionToolsForAgent). Names that don't match
// a local tool — source-hook names used for knowledge queries, registered-pool
// tools — are left alone. Returns how many were copied.
func autoCopySessionToolsForSkill(sess *ToolSession, rec *SkillRecord) int {
	if sess == nil || len(rec.AllowedTools) == 0 {
		return 0
	}
	byName := make(map[string]*TempTool)
	if sess.Username != "" {
		for _, p := range LoadPersistentTempTools(sess.DB, sess.Username) {
			t := p.Tool
			byName[t.Name] = &t
		}
	}
	if sess.ChatSessionID != "" {
		for _, draft := range LoadSessionTempTools(sess.DB, sess.ChatSessionID) {
			t := draft
			byName[t.Name] = &t
		}
	}
	if len(byName) == 0 {
		return 0
	}
	already := make(map[string]bool, len(rec.Tools))
	for _, t := range rec.Tools {
		already[t.Name] = true
	}
	copied := 0
	for _, name := range rec.AllowedTools {
		if already[name] {
			continue
		}
		t, ok := byName[name]
		if !ok {
			continue // not a local tool — leave the name for the knowledge/pool path
		}
		rec.Tools = append(rec.Tools, *t)
		already[name] = true
		copied++
		Log("[orchestrate.skill_def] bundled tool %q into skill %q", name, rec.Name)
		// Now owned by the skill record — drop it from the admin pending-review
		// queue (no-op when it wasn't queued, e.g. came from the persistent pool).
		if sess.Username != "" {
			DequeuePendingTempTool(sess.DB, sess.Username, name)
		}
	}
	return copied
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
	// Their own pool or the deployment's: SaveSkill sends a published id back
	// to the deployment copy, so the edit reaches the record everybody uses.
	existing, published, ok, err := authoredSkill(sess, name)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", fmt.Errorf("skill %q not found: use action=create to make a new one", name)
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
		// Re-snapshot: a newly-named local tool gets bundled into the skill.
		// Not into a published one: the deployment copy carries no bundled
		// tools (the save strips them), and reporting "bundled_tools" for a
		// copy that was about to be dropped would be a lie.
		if !published && autoCopySessionToolsForSkill(sess, &rec) > 0 {
			changed = append(changed, "bundled_tools")
		}
	}
	if _, has := args["attached_collections"]; has {
		rec.AttachedCollections = stringSliceFromArgs(args, "attached_collections")
		changed = append(changed, "attached_collections")
	}
	if playbook, has, err := playbookFromArgs(args); err != nil {
		return "", err
	} else if has {
		rec.Playbook = playbook
		changed = append(changed, "playbook")
	}
	attached, unknown := attachSkillToAgents(sess, args["attach_to_agents"], rec.ID)
	if len(changed) == 0 && len(attached) == 0 && len(unknown) == 0 {
		return "", errors.New("nothing to update: pass at least one of description, instructions, triggers, allowed_tools, attached_collections, playbook, attach_to_agents")
	}
	saved, err := SaveSkill(sess.DB, sess.Username, rec)
	if err != nil {
		return "", err
	}
	if len(changed) == 0 {
		return fmt.Sprintf("Skill %q unchanged.%s", saved.Name, attachNote(attached, unknown)), nil
	}
	return fmt.Sprintf("Skill %q updated (%s). All other fields preserved.%s", saved.Name, strings.Join(changed, ", "), attachNote(attached, unknown)), nil
}

func skillDefDelete(args map[string]any, sess *ToolSession) (string, error) {
	name := strings.TrimSpace(stringArg(args, "name"))
	if name == "" {
		return "", errors.New("name is required for action=delete")
	}
	s, published, ok, err := authoredSkill(sess, name)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", fmt.Errorf("skill %q not found", name)
	}
	// A published skill is still its author's, and taking it back needs
	// nobody's permission, so deleting it is taking it back and then deleting
	// it. Through the one door that moves it, rather than reaching into the
	// deployment pool from here.
	if published {
		if err := NarrowSkillToOwner(sess.DB, sess.Username, s.ID); err != nil {
			return "", fmt.Errorf("delete skill %q: %w", s.Name, err)
		}
	}
	if !DeleteSkill(sess.DB, sess.Username, s.ID) {
		return "", fmt.Errorf("delete skill %q failed", name)
	}
	if published {
		return fmt.Sprintf("Skill %q deleted. It was published deployment-wide, so nobody has it now.", s.Name), nil
	}
	return fmt.Sprintf("Skill %q deleted.", s.Name), nil
}

// playbookFromArgs reads the playbook argument: a JSON array of rules as a
// string (the shape the tool declares), or an already-decoded array. has is
// false when the argument was not passed; an empty array clears. The rules
// are validated here so the error names the rule, not the save.
func playbookFromArgs(args map[string]any) (rules []PlaybookRule, has bool, err error) {
	raw, present := args["playbook"]
	if !present {
		return nil, false, nil
	}
	var data []byte
	switch v := raw.(type) {
	case string:
		if strings.TrimSpace(v) == "" {
			return nil, true, nil
		}
		data = []byte(v)
	default:
		data, _ = json.Marshal(v)
	}
	if err := json.Unmarshal(data, &rules); err != nil {
		return nil, true, fmt.Errorf("playbook must be a JSON array of rules: %w", err)
	}
	if probs := (SkillRecord{Playbook: rules}).PlaybookProblems(); len(probs) > 0 {
		return nil, true, errors.New("playbook: " + strings.Join(probs, "; "))
	}
	return rules, true, nil
}
