// Skills — LLM-callable prompt injections with optional tool
// inclusion.
//
// A skill is a markdown body + frontmatter. The "Available skills"
// block in each agent's system prompt lists every skill the agent
// can reach (name + 1-line description). When the host LLM judges
// that one fits the current conversation, it calls
// activate_skill(name) — the skill's body returns as the tool
// result (carried in conversation history for subsequent rounds)
// and its declared tools join the catalog. Same shape as the
// "Available agents" block + agents(action="run") dispatch.
//
// Conceptually: a catalog of inert capability bundles that the LLM
// pulls in when relevant. No classifier, no triggers, no embedding
// gatekeeper — every activation is a tool call the LLM explicitly
// made, visible in the activity log alongside everything else.
//
// Compared to agents (which have their own dispatch loop, memory,
// facts, etc.), skills are the lightest possible capability bundle.

package core

import (
	"context"
	"fmt"

	"github.com/cmcoffee/gohort/core/revisions"

	"github.com/cmcoffee/gohort/core/notices"
	"github.com/cmcoffee/gohort/core/peershare"
	"github.com/cmcoffee/gohort/core/promotion"
	"github.com/cmcoffee/gohort/core/shareledger"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

const skillsTable = "skills"

// skillStore returns the canonical DB for skill persistence: the
// process-level RootDB. Skills MUST live in one shared store so
// writes from Builder (via orchestrate's user-scoped sess.DB) and
// reads from the admin app (via AuthDB) land at the same key.
// Without this, each surface would write to a different sub-DB
// and the data would be invisible across them. Mirrors temp tools'
// tempToolStore pattern.
func skillStore(fallback Database) Database {
	if RootDB != nil {
		return RootDB
	}
	return fallback
}

// SkillRecord is one authored skill. Stored per-user; activated by
// the classifier when its triggers/description match the current
// turn's user message.
type SkillRecord struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	// Triggers are substring patterns matched against the user
	// message (and attachment filenames). Glob-style `*.pdf` matches
	// any attachment ending in .pdf; everything else is a plain
	// case-insensitive substring. Empty triggers = embedding-only
	// activation (slower but useful when triggers are hard to
	// enumerate).
	Triggers []string `json:"triggers,omitempty"`
	// AllowedTools is the union of tool names this skill brings to
	// the catalog while active. Same shape as AgentRecord's
	// AllowedTools — resolved against the registered ChatTools pool.
	// Tools missing from the registry are silently skipped (e.g. a
	// skill that names create_agent on a non-Builder agent just
	// doesn't surface it; the Builder-exclusivity invariant holds).
	AllowedTools []string `json:"allowed_tools,omitempty"`
	// Instructions is the markdown body appended to the host LLM's
	// system prompt when the skill is active. Plain markdown; no
	// templating. The skill's name is automatically added as an H2
	// header above the instructions on injection.
	Instructions string `json:"instructions"`
	// Owner is the user who authored the skill (or the seed marker
	// for framework-provided skills, when we add those).
	Owner string `json:"owner,omitempty"`
	// AllowedUsers is the peer-share recipient set: which other users may USE
	// this skill besides its Owner. Empty = private to the owner, which is the
	// default and what almost every skill is.
	//
	// The same ACL concept AgentRecord, SecureCredential and PersistentTempTool
	// carry, so an owner learns one control and an admin audits one shape. A
	// recipient gets the skill's behaviour, not its authorship: it activates
	// for them and they cannot edit or delete it.
	//
	// A share carries BEHAVIOUR, and nothing else. Neither the skill's attached
	// collections (the owner's documents) nor its bundled Tools (the owner's
	// executable code) travel with it: each of those is shared on its own
	// terms, through its own gate. A recipient gets the instructions and the
	// tool NAMES, which resolve in their own namespace like every other
	// dependency of a shared thing. See sharedSkillsFor.
	AllowedUsers []string `json:"allowed_users,omitempty"`

	// SharedFrom / SharedOmitted are set on the COPY a recipient sees, never
	// stored: sharedSkillsFor fills them in as it strips what cannot travel.
	//
	// They exist so the absence is speakable. A skill whose instructions say
	// "use check_inventory" on a machine with no such tool is the exact shape
	// that produces an improvised answer instead of an error, and the model
	// finding out is what prevents it.
	SharedFrom    string   `json:"shared_from,omitempty"`
	SharedOmitted []string `json:"shared_omitted,omitempty"`

	// Disabled mutes the skill — classifier skips it entirely as if
	// it didn't exist. Use to pause a skill without losing its
	// definition (admin can re-enable later instead of re-authoring).
	Disabled bool `json:"disabled,omitempty"`

	// AttachedCollections lists collection IDs whose corpus becomes
	// searchable when this skill is active. Admin-curated only — the
	// derived "SelfTraining" path (paraphrased self-corpus from the
	// skill's own work) stays removed because it compounded drift,
	// but admin-attached collections are stable reference material
	// that pairs naturally with a skill's Instructions. Active path
	// only: when the classifier doesn't pick this skill, its
	// collections stay out of scope, so a heavy reference corpus
	// doesn't leak into unrelated turns. Empty by default — most
	// skills are pure behavior packets and don't carry docs.
	AttachedCollections []string `json:"attached_collections,omitempty"`
	// Tools are shell/script tools BUNDLED with the skill — its own executable
	// code, shipped inline (mirrors AgentRecord.Tools). When the skill is
	// consulted this turn they join the catalog so the LLM can run them
	// deterministically without spending context tokens (a calculator, a
	// screener, a formatter). Self-contained: the skill carries its scripts, so
	// it stays portable across export/import instead of referencing a separately
	// registered tool by name. Active-path only — bundled tools surface ONLY once
	// the skill is delivered (read_skill / skill_knowledge_search), never in
	// unrelated turns. Empty by default; most skills carry no code.
	Tools []TempTool `json:"tools,omitempty"`
	// Playbook is the skill's conditional behaviour, declared: rules of the
	// form "establish Y, then Z if it holds, U if not". A rule's fact is
	// ESTABLISHED by the framework when the skill is consulted — a
	// one-phase run with the skill's tools and a declared output — and only
	// the arm that applies is handed to the model, so Y is settled before Z
	// or U can start and the model never sees the branch it did not earn.
	// Instructions carry the prose that does not branch; this carries what
	// does. Validated on save (PlaybookProblems); empty for most skills.
	Playbook []PlaybookRule `json:"playbook,omitempty"`
	// Embedding is cached at save time so the classifier doesn't
	// have to re-embed on every turn. Re-computed in SaveSkill from
	// the current Description. Persisted with the record so reloads
	// don't lose the cache.
	Embedding []float32 `json:"embedding,omitempty"`
	Created   time.Time `json:"created"`
	Updated   time.Time `json:"updated"`
}

// PlaybookRule is one conditional in a skill's playbook.
//
// The rule an author would otherwise write as a sentence — "when asked about
// X, establish Y; if Y then Z, else U" — as data the framework can enforce.
// When names the turns it applies to, matched like the skill's triggers;
// empty means every time the skill is consulted. Fact is what to establish
// and How is the instruction for establishing it; the framework runs that as
// a step with the skill's tools and a declared output of Type: "bool" (the
// default) or "choice" over Values. Then and Else are the arms of a bool;
// Cases maps each value of a choice to its arm. An arm is prose the model is
// then told to follow, or — through ThenRule, ElseRule and CaseRules —
// another rule, which establishes its own fact first. Two levels deep at
// most: beyond that it is a machine, and the author should write one.
type PlaybookRule struct {
	When      []string                 `json:"when,omitempty"`
	Fact      string                   `json:"fact"`
	How       string                   `json:"how"`
	Type      string                   `json:"type,omitempty"`
	Values    []string                 `json:"values,omitempty"`
	Then      string                   `json:"then,omitempty"`
	Else      string                   `json:"else,omitempty"`
	Cases     map[string]string        `json:"cases,omitempty"`
	ThenRule  *PlaybookRule            `json:"then_rule,omitempty"`
	ElseRule  *PlaybookRule            `json:"else_rule,omitempty"`
	CaseRules map[string]*PlaybookRule `json:"case_rules,omitempty"`
}

// playbookMaxDepth bounds nesting: a rule inside a rule is allowed, a rule
// inside that is a machine wearing a playbook's clothes.
const playbookMaxDepth = 2

// The two fact types.
const (
	playbookBool   = "bool"
	playbookChoice = "choice"
)

// playbookEstablishPhase is the name of the one phase a rule compiles to,
// and the MachineState key its fact lands under.
const playbookEstablishPhase = "establish"

// PlaybookEvidenceField is the second output the establishing step declares:
// the line of what it saw that decided the fact. Shown beside the fact so
// the model (and the person reading the export) sees WHY, and so an arm that
// needs the number the check found — "give the free space" — has it without
// running the check again.
const PlaybookEvidenceField = "evidence"

// kind normalizes Type: empty is bool.
func (r PlaybookRule) kind() string {
	if strings.TrimSpace(r.Type) == "" {
		return playbookBool
	}
	return strings.ToLower(strings.TrimSpace(r.Type))
}

// Problems lists what is wrong with the rule, each prefixed with path so a
// nested rule's problem says where it is. Empty when the rule is sound.
func (r PlaybookRule) Problems(path string, depth int) []string {
	var probs []string
	at := func(msg string) { probs = append(probs, path+": "+msg) }
	if depth > playbookMaxDepth {
		at("nested more than " + strconv.Itoa(playbookMaxDepth) + " levels deep, past that it is a machine, and the author should write one")
		return probs
	}
	fact := strings.TrimSpace(r.Fact)
	switch {
	case fact == "":
		at("fact is required: the name of what the rule establishes")
	case strings.ContainsAny(fact, " ."):
		at("fact must be one word with no spaces or dots (it is a field name), got " + strconv.Quote(fact))
	}
	if strings.TrimSpace(r.How) == "" {
		at("how is required: the instruction for establishing the fact")
	}
	armText := func(arm string) bool { return strings.TrimSpace(arm) != "" }
	switch r.kind() {
	case playbookBool:
		if len(r.Values) > 0 || len(r.Cases) > 0 || len(r.CaseRules) > 0 {
			at("values and cases belong to a choice rule; a bool rule has then and else")
		}
		if armText(r.Then) && r.ThenRule != nil {
			at("then is both prose and a rule: an arm is one or the other")
		}
		if armText(r.Else) && r.ElseRule != nil {
			at("else is both prose and a rule: an arm is one or the other")
		}
		if !armText(r.Then) && r.ThenRule == nil && !armText(r.Else) && r.ElseRule == nil {
			at("a bool rule needs at least one arm: then, else, then_rule or else_rule")
		}
		if r.ThenRule != nil {
			probs = append(probs, r.ThenRule.Problems(path+".then_rule", depth+1)...)
		}
		if r.ElseRule != nil {
			probs = append(probs, r.ElseRule.Problems(path+".else_rule", depth+1)...)
		}
	case playbookChoice:
		if len(r.Values) < 2 {
			at("a choice rule needs at least two values")
		}
		if armText(r.Then) || armText(r.Else) || r.ThenRule != nil || r.ElseRule != nil {
			at("then and else belong to a bool rule; a choice rule has cases")
		}
		seen := map[string]bool{}
		for _, v := range r.Values {
			v = strings.ToLower(strings.TrimSpace(v))
			if v == "" || seen[v] {
				at("values must be distinct and non-empty")
				break
			}
			seen[v] = true
		}
		covered := 0
		for _, v := range r.Values {
			text, hasText := r.Cases[v]
			rule, hasRule := r.CaseRules[v]
			hasText = hasText && strings.TrimSpace(text) != ""
			hasRule = hasRule && rule != nil
			if hasText && hasRule {
				at("case " + strconv.Quote(v) + " is both prose and a rule: an arm is one or the other")
			}
			if hasText || hasRule {
				covered++
			}
			if hasRule {
				probs = append(probs, rule.Problems(path+".case_rules."+v, depth+1)...)
			}
		}
		if covered == 0 {
			at("a choice rule needs an arm for at least one of its values (cases or case_rules)")
		}
		for v := range r.Cases {
			if !seen[strings.ToLower(strings.TrimSpace(v))] {
				at("cases names " + strconv.Quote(v) + ", which is not one of the values")
			}
		}
	default:
		at("type must be \"bool\" (the default) or \"choice\", got " + strconv.Quote(r.Type))
	}
	return probs
}

// PlaybookProblems validates every rule of the skill's playbook.
func (s SkillRecord) PlaybookProblems() []string {
	var probs []string
	for i, r := range s.Playbook {
		probs = append(probs, r.Problems("rule "+strconv.Itoa(i+1), 1)...)
	}
	return probs
}

// Machine compiles the rule's establishing step to a one-phase unattended
// machine: the skill's tools, thinking on, and the fact as a declared,
// required output. Running it through the ordinary machine host is what
// makes the fact a decoded field rather than a claim in prose.
func (r PlaybookRule) Machine(skill SkillRecord) MachineDef {
	fact := strings.TrimSpace(r.Fact)
	field := PipelineField{Name: fact, Required: true}
	evidence := PipelineField{Name: PlaybookEvidenceField, Type: FieldString, Required: true,
		Desc: "the one line of what you saw that decided it (a command's output line, a number, a status), quoted, not described"}
	prompt := "Establish ONE thing and report it; do not answer the person's question here, and do not go past what is asked.\n\n" +
		"What to establish: " + fact + "\n\nHow: " + strings.TrimSpace(r.How) + "\n\n"
	switch r.kind() {
	case playbookChoice:
		field.Type = FieldString
		field.Desc = "exactly one of: " + strings.Join(r.Values, ", ")
		prompt += "Use the tools you have to check, then report " + fact + " as exactly one of: " + strings.Join(r.Values, ", ") + ". If what you find fits none of them, say which is closest and why in your text, and report the closest."
	default:
		field.Type = FieldBool
		field.Desc = "true or false"
		prompt += "Use the tools you have to check, then report " + fact + " as true or false. Report what the evidence shows, not what would be convenient; if you could not check, say so in your text and report false."
	}
	prompt += " Report as " + PlaybookEvidenceField + " the one line of output that decided it, quoted."
	prompt += "\n\nThe person's message, for context:\n\n{input}"
	return MachineDef{
		Name:        skill.Name + " playbook: " + fact,
		Description: "Establishes " + fact + " for the " + skill.Name + " skill.",
		Start:       playbookEstablishPhase,
		Unattended:  true,
		Phases: []MachinePhase{{
			Name:   playbookEstablishPhase,
			Desc:   "Establishing " + fact,
			Prompt: prompt,
			Think:  "on",
			// It goes and finds the fact, so it reaches the catalog. Said
			// outright: an unset reach on a step like this one reaches
			// nothing whenever the skill allows no tools by name.
			Reach:  ReachAll,
			Tools:  append([]string(nil), skill.AllowedTools...),
			Output: []PipelineField{field, evidence},
		}},
	}
}

// Applies reports whether the skill's playbook should fire on this turn
// without waiting for the model to consult the skill: the skill's own
// triggers match, or any rule's When does. Firing is the framework's call
// here on purpose — "when asked about X, establish Y" is a rule about the
// turn, and a rule the model may decline to look up is a suggestion.
func (s SkillRecord) PlaybookApplies(message string, attachmentNames []string) bool {
	if len(s.Playbook) == 0 || s.Disabled {
		return false
	}
	if SkillTriggersMatch(s, message, attachmentNames) {
		return true
	}
	for _, r := range s.Playbook {
		if len(r.When) > 0 && TriggersMatch(r.When, message, attachmentNames) {
			return true
		}
	}
	return false
}

// Decide reads the established value and picks the arm. value is the
// decoded field (a bool, or a string for either kind — a model that writes
// "yes" has still answered). Returns the value as it will be shown, the arm's
// prose, the nested rule if the arm is one, and ok=false when the value does
// not decide anything (not a bool, not one of the values).
func (r PlaybookRule) Decide(value any) (shown, arm string, next *PlaybookRule, ok bool) {
	switch r.kind() {
	case playbookChoice:
		s := strings.ToLower(strings.TrimSpace(fmt.Sprint(value)))
		for _, v := range r.Values {
			if strings.EqualFold(strings.TrimSpace(v), s) {
				return v, r.Cases[v], r.CaseRules[v], true
			}
		}
		return s, "", nil, false
	default:
		var b bool
		switch v := value.(type) {
		case bool:
			b = v
		case string:
			switch strings.ToLower(strings.TrimSpace(v)) {
			case "true", "yes", "y":
				b = true
			case "false", "no", "n":
				b = false
			default:
				return v, "", nil, false
			}
		default:
			return fmt.Sprint(value), "", nil, false
		}
		if b {
			return "true", r.Then, r.ThenRule, true
		}
		return "false", r.Else, r.ElseRule, true
	}
}

// Fallback renders the rule as prose for when the fact could not be
// established by the framework: the model is told to establish it itself,
// and is given every arm with its condition. Less than enforcement, more
// than silence.
func (r PlaybookRule) Fallback() string {
	var b strings.Builder
	fmt.Fprintf(&b, "Establish %s first: %s\n", strings.TrimSpace(r.Fact), strings.TrimSpace(r.How))
	arm := func(label, text string, rule *PlaybookRule) {
		if rule != nil {
			fmt.Fprintf(&b, "- %s: then %s", label, rule.Fallback())
			return
		}
		if strings.TrimSpace(text) != "" {
			fmt.Fprintf(&b, "- %s: %s\n", label, strings.TrimSpace(text))
		}
	}
	switch r.kind() {
	case playbookChoice:
		for _, v := range r.Values {
			arm("if "+v, r.Cases[v], r.CaseRules[v])
		}
	default:
		arm("if true", r.Then, r.ThenRule)
		arm("if false", r.Else, r.ElseRule)
	}
	return b.String()
}

// Sentence reads the rule back as the sentence its author would have
// written, so an editor can show that the fields mean what they think.
func (r PlaybookRule) Sentence() string {
	var b strings.Builder
	if len(r.When) > 0 {
		b.WriteString("When the message mentions " + strings.Join(r.When, " or ") + ", ")
	} else {
		b.WriteString("Whenever this skill is consulted, ")
	}
	fact := strings.TrimSpace(r.Fact)
	if fact == "" {
		fact = "(unnamed fact)"
	}
	b.WriteString("establish " + fact)
	arm := func(label, text string, rule *PlaybookRule) {
		switch {
		case rule != nil:
			b.WriteString("; " + label + ", " + lowerFirst(rule.Sentence()))
		case strings.TrimSpace(text) != "":
			b.WriteString("; " + label + ", " + strings.TrimRight(strings.TrimSpace(text), "."))
		}
	}
	switch r.kind() {
	case playbookChoice:
		for _, v := range r.Values {
			arm("if "+v, r.Cases[v], r.CaseRules[v])
		}
	default:
		arm("if yes", r.Then, r.ThenRule)
		arm("if no", r.Else, r.ElseRule)
	}
	b.WriteString(".")
	return b.String()
}

func lowerFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToLower(s[:1]) + s[1:]
}

// LoadSkills returns every skill in the user's pool, ordered by
// most-recently-updated first (a stable, human-meaningful order for
// admin views). Empty username returns nil.
//
// Storage shape: one row per user keyed by username, value is a
// []SkillRecord. Same pattern as PersistentTempTools — fewer DB
// keys, atomic per-user updates, and the row count stays small.
// sharedSkillsTable indexes peer shares: recipient -> (owner, skill id).
//
// Unexported, with sharedSkillsFor below it: nothing outside core resolves a
// skill share. Callers ask AvailableSkills what a user may use and get every
// tier at once, which is the question they actually have.
const sharedSkillsTable = "shared_skills"

// sharedSkillsFor returns the skills other people have shared WITH this user.
//
// A share carries BEHAVIOUR. Bundled tools are stripped (see below), and the
// attached collections travel as IDS that resolve — or do not — in the
// recipient's own namespace, exactly as a shared agent's do. Disabled ones are
// skipped, since an owner who muted a skill has muted it for everybody, not
// just themselves.
func sharedSkillsFor(db Database, username string) []SkillRecord {
	store := skillStore(db)
	if store == nil || strings.TrimSpace(username) == "" {
		return nil
	}
	var out []SkillRecord
	for _, ref := range peershare.List(store, sharedSkillsTable, username) {
		for _, s := range LoadSkills(db, ref.Owner) {
			if s.ID != ref.ID || s.Disabled {
				continue
			}
			// Re-checked against the record, not trusted from the index. The
			// list on the skill is what the owner edits and an admin audits;
			// the index is a derived lookup, and a derived thing that can
			// outvote its source is how a revoked share keeps working.
			if !skillSharedWith(s, username) {
				continue
			}
			s.SharedFrom = ref.Owner
			s.SharedOmitted = omittedFromShare(s)
			noteSkillShareGaps(ref.Owner, username, s)
			// Bundled tools do not travel. A skill can carry its own executable
			// scripts so it stays portable, and attaching those for a recipient
			// would run another person's code in their session, under their
			// credentials, with no approval anywhere — which is precisely the
			// gate a tool has to pass to reach even one other user. A skill
			// share would be the way around it. Tools are shared as tools.
			//
			// The attached COLLECTIONS stay, and are gated where they are read
			// rather than here. They used to be stripped, which meant a skill
			// and a collection deliberately shared with the same person still
			// could not work together. The retrieval path resolves every
			// collection id in the RUNTIME user's namespace, so a recipient
			// gets the ones they were actually given, a deployment corpus
			// resolves for everybody, and anything else is withheld and
			// reported. An id is not a grant, and it is not treated as one.
			s.Tools = nil
			out = append(out, s)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Updated.After(out[j].Updated) })
	return out
}

// omittedFromShare names what a share cannot carry, in the reader's terms.
// Counts rather than names, because a recipient has no use for the owner's
// internal tool names and every use for knowing the skill expects capabilities
// they may not have.
//
// Only the tools. The attached collections are no longer stripped here — they
// are resolved, or withheld, per runtime user at retrieval time, which reports
// itself. Claiming them as missing up front would be wrong for the recipient
// who was given them.
func omittedFromShare(s SkillRecord) []string {
	if n := len(s.Tools); n > 0 {
		return []string{fmt.Sprintf("%d bundled tool(s)", n)}
	}
	return nil
}

func skillSharedWith(s SkillRecord, user string) bool {
	for _, u := range s.AllowedUsers {
		if u == user {
			return true
		}
	}
	return false
}

// AvailableSkills is every skill this user may use: their own, plus the ones
// shared with them. The order puts the user's own first, because a name
// collision should resolve to the skill they wrote.
func AvailableSkills(db Database, username string) []SkillRecord {
	out := LoadSkills(db, username)
	seen := make(map[string]bool, len(out))
	for _, s := range out {
		seen[s.ID] = true
	}
	// Then the ones a colleague gave them, then the ones the deployment
	// publishes. Three tiers, most specific first: a skill they wrote beats one
	// handed to them, and a colleague handing you something is a more specific
	// answer than a skill every account in the building has.
	for _, tier := range [][]SkillRecord{sharedSkillsFor(db, username), DeploymentSkills(db)} {
		for _, s := range tier {
			if seen[s.ID] {
				continue
			}
			seen[s.ID] = true
			out = append(out, s)
		}
	}
	return out
}

func LoadSkills(db Database, username string) []SkillRecord {
	store := skillStore(db)
	if store == nil || username == "" {
		return nil
	}
	var out []SkillRecord
	if !store.Get(skillsTable, username, &out) {
		return nil
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].Updated.After(out[j].Updated)
	})
	return out
}

// FindSkillByName looks up a skill by case-insensitive name match.
// Used by Builder's update / delete paths and by the classifier when
// trigger matches reference a skill by its declared name.
func FindSkillByName(db Database, username, name string) (SkillRecord, bool) {
	if name == "" {
		return SkillRecord{}, false
	}
	lower := strings.ToLower(strings.TrimSpace(name))
	for _, s := range LoadSkills(db, username) {
		if strings.ToLower(s.Name) == lower {
			return s, true
		}
	}
	return SkillRecord{}, false
}

// SaveSkill upserts a skill record in the user's pool. Assigns ID
// on create. Returns the saved record with ID + timestamps filled.
// Same-ID skills replace; matched-by-ID upserts atomically rewrite
// the per-user slice.
func SaveSkill(db Database, username string, s SkillRecord) (SkillRecord, error) {
	return SaveSkillAs(db, username, s, "update")
}

// SaveSkillAs is SaveSkill with a note about what is doing the writing, filed
// against the version being REPLACED so an owner reading the history sees what
// happened next to each entry instead of a column of timestamps. Pass
// revisions.NoHistory to suppress the snapshot.
//
// Two functions rather than a reason on SaveSkill, for the same reason
// SaveAppSpec and SaveAppSpecAs are two: the callers that just want to store a
// skill should not each have to have an opinion about history.
func SaveSkillAs(db Database, username string, s SkillRecord, reason string) (SkillRecord, error) {
	store := skillStore(db)
	if store == nil || username == "" {
		return SkillRecord{}, errString("save skill requires user")
	}
	if s.ID == "" {
		s.ID = "skill-" + UUIDv4()
	}
	if s.Created.IsZero() {
		s.Created = time.Now()
	}
	s.Owner = username
	s.Updated = time.Now()
	// The playbook is NOT validated here. Storage stores; the doors validate
	// (the Builder tool, the admin save) and the visual editor saves a rule
	// half-built on purpose — refusing to store the third field until the
	// tenth exists is how an editor becomes a puzzle. The resolver skips a
	// rule with problems, so a half-built one never runs.
	// Description-embedding removed. Was used by the cosine
	// gatekeeper / fuzzy classifier that auto-fired skills; with
	// activation now exclusively LLM-driven via activate_skill, the
	// vector serves no purpose. Existing records' Embedding field
	// stays populated until they're saved again; the field is just
	// dead weight on disk.
	existing := LoadSkills(db, username)
	rest := existing[:0]
	var prior SkillRecord
	hadPrior := false
	for i := range existing {
		if existing[i].ID == s.ID {
			// Copied by value before the rewrite below can reach this index.
			prior, hadPrior = existing[i], true
			continue
		}
		rest = append(rest, existing[i])
	}
	// Keep the version this save replaces. The record being dropped from the
	// slice IS the stored row, so there is nothing rawer to read.
	if hadPrior && reason != revisions.NoHistory && revisions.Differs(prior, s, "updated") {
		revisions.Push(store, revisions.KindSkill, skillRingKey(username, s.ID), prior, prior.Updated, reason)
	}
	rest = append(rest, s)
	store.Set(skillsTable, username, rest)
	// The index follows the record, always and in the same write path. A share
	// that updated one without the other would either strand a recipient or
	// keep one who had been removed, and which of those you got would depend on
	// which half ran.
	peershare.SetRecipients(store, sharedSkillsTable, username, s.ID, s.AllowedUsers)
	return s, nil
}

// SkillRevisionRing names where a skill's kept versions live: the store the
// skills themselves are in, and the ring key.
//
// Exported because a skill's history is served from another package, and both
// halves are things that package must not have to work out for itself. The
// store is not the handle the caller passes (skillStore prefers RootDB), and
// the key is not the skill id (see skillRingKey). Getting either wrong reads
// as "this skill has no history" rather than as an error.
func SkillRevisionRing(db Database, username, id string) (Database, string) {
	return skillStore(db), skillRingKey(username, id)
}

// skillRingKey names one skill's history.
//
// The username is IN THE KEY because skills are the odd one out: agents,
// machines and pipelines live in a per-user store, where the database is
// already the tenancy boundary and a bare id is enough. Skills live in RootDB
// keyed by username, so a bare skill id would put two users' histories in the
// same ring and hand one of them the other's definitions.
func skillRingKey(username, id string) string { return username + ":" + id }

// RollbackSkill restores a kept version. ref is a revision id ("4" or "#4"),
// a stamp, or empty for the most recent.
//
// The restore is an ordinary save, so the version it REPLACES is filed like
// any other and going back is itself reversible.
func RollbackSkill(db Database, username, id, ref string) (SkillRecord, error) {
	store := skillStore(db)
	if store == nil || username == "" || id == "" {
		return SkillRecord{}, errString("rollback needs a user and a skill")
	}
	key := skillRingKey(username, id)
	rev, ok := revisions.Find(store, revisions.KindSkill, key, ref)
	if !ok {
		return SkillRecord{}, errString("this skill has no such kept version")
	}
	var restored SkillRecord
	if !revisions.Load(store, revisions.KindSkill, key, ref, &restored) {
		return SkillRecord{}, errString("that kept version could not be read")
	}
	// Trust the arguments over the payload: the ring is keyed by owner AND id,
	// so a body naming another skill or another owner must not become a write
	// to it.
	restored.ID = id
	restored.Owner = username
	return SaveSkillAs(db, username, restored, fmt.Sprintf("rolled back to #%d", rev.Seq))
}

// DeleteSkill removes a skill by ID. Returns true when an entry was
// removed. Also drops any corpus chunks stored under the skill's
// source prefix — orphan chunks would otherwise survive the skill's
// deletion and silently bloat the vector store.
func DeleteSkill(db Database, username, id string) bool {
	store := skillStore(db)
	if store == nil || username == "" || id == "" {
		return false
	}
	existing := LoadSkills(db, username)
	rest := existing[:0]
	removed := false
	for i := range existing {
		if existing[i].ID == id {
			removed = true
			continue
		}
		rest = append(rest, existing[i])
	}
	if !removed {
		return false
	}
	if len(rest) == 0 {
		store.Unset(skillsTable, username)
	} else {
		store.Set(skillsTable, username, rest)
	}
	// The shares go with it. An index entry outliving its record points at
	// nothing, which reads to a recipient as access they lost rather than a
	// skill that is gone.
	peershare.DropAll(store, sharedSkillsTable, username, id)
	// The history goes with the skill, the way a deleted pipeline's does.
	revisions.Delete(store, revisions.KindSkill, skillRingKey(username, id))
	// Drop the skill's corpus chunks from its dedicated store.
	if chunksDB := skillChunksDB(username); chunksDB != nil {
		if n := WipeChunksBySourcePrefix(chunksDB, skillSource(id)); n > 0 {
			Log("[skills] dropped %d chunk(s) for deleted skill %s", n, id)
		}
	}
	return true
}

// skillSource returns the source-prefix used by the vector store
// for this skill's corpus. Centralized so the ingest path, the
// activation search, and the delete-cleanup all derive the same
// namespace from the skill ID.
func skillSource(skillID string) string {
	return "skill:" + skillID
}

// skillChunksDB returns the database the skill's knowledge chunks
// live in: a dedicated per-user sub-store of RootDB. Separate from
// any app's own per-(user, agent) knowledge store — skills are
// user-scoped, not agent-scoped, so they get their own home.
// Returns nil when RootDB isn't initialized; callers should treat
// that as "no corpus available" and skip both ingest and search.
func skillChunksDB(username string) Database {
	if username == "" {
		return nil
	}
	// Skill corpus now lives in the shared, dedicated vector store
	// alongside all other knowledge, scoped logically by the
	// skillSource("skill:<id>") tag rather than by a per-user sub-store.
	// Skill IDs are globally unique, so any future skill search MUST
	// scope to the requesting user's own skill IDs (the source tag does
	// not by itself partition by user). No skill chunks are ingested or
	// searched today; the only consumer is delete-cleanup.
	return VectorDB
}

// --- shared skill runtime (orchestrate + phantom) ---
//
// A skill is a domain pack: instructions + (optionally) searchable
// knowledge sources (attached collections and/or source-hooks). The LLM
// reaches a skill three ways, none of which tracks state across turns:
//   - read_skill(skill) — pull the skill's instructions to apply now;
//   - skill_knowledge_search(skill, query) — search the skill's sources
//     (collections + source-hooks, merged), with the instructions
//     attached the first time the skill is touched this turn;
//   - skill_knowledge_fetch_doc(skill, doc_id) — a full doc.
// Plus, when a skill's Triggers match the turn, the framework surfaces a
// soft HINT nudging the LLM to consult it (renderSkillTriggerHints) — a
// signal, not a forced injection; consulting via the tools is what loads
// the instructions. A per-turn `delivered` set dedupes them so the LLM
// sees a consulted skill's instructions once.

// RenderAvailableSkills builds the "## Available skills" prompt block from
// the skills an agent/conversation can reach, so the LLM knows what
// read_skill / skill_knowledge_search can draw on. Empty when none.
func RenderAvailableSkills(skills []SkillRecord) string {
	if len(skills) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\n## Available skills\n\n")
	b.WriteString("Domain packs you can draw on in your own context: read_skill(skill) returns its approach/instructions; skill_knowledge_search(skill, query) searches its sources (and attaches its approach the first time); skill_knowledge_fetch_doc(skill, doc_id) pulls a full document.\n\nRULE: when a listed skill covers the subject in front of you, consult it FIRST, call skill_knowledge_search (or read_skill) before web_search and before answering from memory. Its sources are authoritative for its domain and override your priors, so answering a covered question without it is a mistake even when you're confident. This fires on what you DISCOVER mid-task, not just the opening request: a repo that turns out to be Go → the Go skill, a tax-law doc → the tax skill, a PDF → the PDF skill, even if the user never named the domain. On FOLLOW-UPS the skill's instructions and what you already retrieved stay in your context: answer from that skill content, not your priors, and search the skill again only if the follow-up needs material you didn't pull. Skip a skill only for what it plainly doesn't cover or fast-changing facts (current events, latest figures). When a skill's trigger matches the turn you'll see a \"Likely relevant\" hint: treat it as a strong nudge to consult that skill, not a guarantee. Format: **name**, purpose.\n\n")
	for _, s := range skills {
		// Full description — descriptions are model-facing; show it
		// un-truncated so the whole activation cue is visible.
		desc := strings.TrimSpace(s.Description)
		if desc == "" {
			desc = "(no description)"
		}
		b.WriteString("- **")
		b.WriteString(s.Name)
		b.WriteString("** ")
		b.WriteString(desc)
		if trig := strings.TrimSpace(strings.Join(s.Triggers, ", ")); trig != "" {
			b.WriteString(" (triggers: ")
			b.WriteString(trig)
			b.WriteString(")")
		}
		if facts := s.playbookFacts(); len(facts) > 0 {
			// A playbook skill is worth consulting EARLY: consulting it runs
			// the checks and hands back only what applies, which is cheaper
			// than working the same thing out by hand and then being told.
			b.WriteString(" (playbook: consulting it establishes ")
			b.WriteString(strings.Join(facts, ", "))
			b.WriteString(" for you and tells you what follows: consult before working it out yourself)")
		}
		b.WriteString("\n")
	}
	return b.String()
}

// playbookFacts lists the top-level facts a skill's playbook establishes.
func (s SkillRecord) playbookFacts() []string {
	var out []string
	for _, r := range s.Playbook {
		if f := strings.TrimSpace(r.Fact); f != "" {
			out = append(out, f)
		}
	}
	return out
}

// SkillTriggersMatch reports whether a skill's Triggers match this turn: a
// glob trigger (contains '*' or '?') matches against the attachment
// filenames; any other trigger is a case-insensitive substring test against
// the message text. A skill with NO triggers never matches. A match is a
// relevance SIGNAL, not a command — it surfaces a HINT nudging the LLM to
// consult the skill (see renderSkillTriggerHints), it does NOT force-inject
// the skill's instructions. Deterministic and framework-owned.
func SkillTriggersMatch(s SkillRecord, message string, attachmentNames []string) bool {
	return TriggersMatch(s.Triggers, message, attachmentNames)
}

// TriggersMatch reports whether any of the given triggers match this turn:
// a glob trigger (contains '*' or '?') matches against the attachment
// filenames; any other trigger is a case-insensitive substring test
// against the message. Empty triggers never match. Generic + framework-
// owned — shared by skills (SkillTriggersMatch) and agents (the per-turn
// dispatch hint) so trigger semantics read identically across both.
func TriggersMatch(triggers []string, message string, attachmentNames []string) bool {
	if len(triggers) == 0 {
		return false
	}
	lowerMsg := strings.ToLower(message)
	for _, t := range triggers {
		t = strings.TrimSpace(t)
		if t == "" {
			continue
		}
		if strings.ContainsAny(t, "*?") {
			pat := strings.ToLower(t)
			for _, name := range attachmentNames {
				if ok, _ := filepath.Match(pat, strings.ToLower(filepath.Base(name))); ok {
					return true
				}
			}
			continue
		}
		if strings.Contains(lowerMsg, strings.ToLower(t)) {
			return true
		}
	}
	return false
}

// renderSkillTriggerHints returns a soft HINT block naming the allowed,
// enabled skills whose Triggers match this turn — a per-turn nudge to
// consult them, NOT the full instruction injection. A matched trigger is a
// relevance signal; the LLM still decides to consult (read_skill /
// skill_knowledge_search), and consulting is what actually loads the
// skill's approach. We hint instead of force-inject because a wrong hint is
// cheap (one line the LLM ignores) while a wrong injection is expensive (a
// wall of off-topic instructions steering the whole reply). Returns "" when
// nothing matches.
func renderSkillTriggerHints(db Database, owner string, allowed []string, message string, attachmentNames []string) string {
	if owner == "" || len(allowed) == 0 {
		return ""
	}
	allowSet := make(map[string]bool, len(allowed))
	for _, id := range allowed {
		allowSet[id] = true
	}
	var names []string
	for _, s := range LoadSkills(db, owner) {
		if s.Disabled || !allowSet[s.ID] {
			continue
		}
		if SkillTriggersMatch(s, message, attachmentNames) {
			names = append(names, s.Name)
		}
	}
	return SkillTriggerHintBlock(names)
}

// SkillTriggerHintBlock formats the trigger-match hint line for the given
// skill names. Empty names → "". Shared by orchestrate + phantom so the
// nudge reads identically on both surfaces.
func SkillTriggerHintBlock(names []string) string {
	if len(names) == 0 {
		return ""
	}
	quoted := make([]string, len(names))
	for i, n := range names {
		quoted[i] = "**" + n + "**"
	}
	return "\n\n[Likely relevant this turn (triggers matched): " + strings.Join(quoted, ", ") + ", consult the fitting one FIRST via skill_knowledge_search / read_skill before answering. A trigger match is a hint, not a guarantee: skip a skill that doesn't actually fit.]\n\n"
}

// resolveAllowedSkill looks up a skill by name (case-insensitive),
// gated on the allowed-ID set. Shared by the three skill tools.
func resolveAllowedSkill(db Database, owner string, allowed []string, name string) (*SkillRecord, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, fmt.Errorf("skill name is required")
	}
	allowSet := make(map[string]bool, len(allowed))
	for _, id := range allowed {
		allowSet[id] = true
	}
	for _, s := range LoadSkills(db, owner) {
		if s.Disabled || !strings.EqualFold(s.Name, name) {
			continue
		}
		if !allowSet[s.ID] {
			return nil, fmt.Errorf("skill %q is not enabled here", s.Name)
		}
		sc := s
		return &sc, nil
	}
	return nil, fmt.Errorf("no skill named %q (check the 'Available skills' block)", name)
}

// allowedSkillNames returns the names of the existing, non-disabled skills
// in the allowed set, sorted. Used to close the skill-tool `skill`
// parameter to an enum so the model can only name a skill that actually
// exists — it can't invent one or address an agent (e.g. a former skill
// that's now an agent) through the skill tools. Empty (e.g. all allowed
// IDs are orphans) yields no enum, leaving the handler's rejection as the
// backstop.
func allowedSkillNames(db Database, owner string, allowed []string) []string {
	allowSet := make(map[string]bool, len(allowed))
	for _, id := range allowed {
		allowSet[id] = true
	}
	var names []string
	for _, s := range LoadSkills(db, owner) {
		if s.Disabled || !allowSet[s.ID] {
			continue
		}
		names = append(names, s.Name)
	}
	sort.Strings(names)
	return names
}

// querySkillSourceHooks queries each source-hook the skill names in its
// AllowedTools (e.g. "courtlistener_search") for the query and returns
// their combined text, or "" when the skill has none / they're empty.
// Reuses the registered source-hook tool's own handler so the adapter
// logic lives in one place.
func querySkillSourceHooks(skill SkillRecord, query string) string {
	var b strings.Builder
	for _, name := range skill.AllowedTools {
		def, ok := SourceHookToolDefByName(name)
		if !ok || def.Handler == nil {
			continue
		}
		res, err := def.Handler(context.Background(), map[string]any{"query": query})
		if err != nil || strings.TrimSpace(res) == "" {
			continue
		}
		b.WriteString("\n\n, from ")
		b.WriteString(name)
		b.WriteString(", \n")
		b.WriteString(strings.TrimSpace(res))
	}
	return b.String()
}

// skillInstructionsBlock formats a skill's instructions as the lens to
// apply to its knowledge, marking it delivered. Returns "" when the
// instructions were already delivered this turn (via read_skill, a prior
// search, or trigger-injection) or the skill has no body.
func skillInstructionsBlock(skill SkillRecord, delivered map[string]bool) string {
	if delivered != nil && delivered[skill.ID] {
		return ""
	}
	body := strings.TrimSpace(skill.Instructions)
	if delivered != nil {
		delivered[skill.ID] = true
	}
	if body == "" {
		return ""
	}
	out := "Apply the \"" + skill.Name + "\" approach for the REST of this turn, it governs how you read these results AND how you reply, not just this one result:\n\n" + body
	// A shared skill arrives without whatever it could not bring. Said here,
	// with the instructions, because this is the moment the model would
	// otherwise go looking for a tool that is not in its catalog and invent a
	// way around it.
	if skill.SharedFrom != "" && len(skill.SharedOmitted) > 0 {
		out += "\n\n(This skill was shared with you by " + skill.SharedFrom + ", and " +
			strings.Join(skill.SharedOmitted, " and ") + " did not come with it. " +
			"Follow the approach with the tools and documents you actually have; if it calls for something you cannot reach, say so plainly rather than working around it.)"
	}
	return out + "\n\n---\n"
}

// AttachDeliveredSkillTools loads the bundled Tools of every skill consulted
// this turn (delivered[id] true) into the session pool, so a skill's shipped
// scripts become callable once it's active and never before. Idempotent.
// Returns the names attached/present this call so a per-round caller can surface
// exactly the skill-bundled tools (bypassing any default-deny on shell tools —
// consulting an allowed skill IS the opt-in, the same trust boundary that lets
// AgentRecord.Tools skip the allowlist). Shared by orchestrate + phantom so the
// behavior is identical on both surfaces.
func AttachDeliveredSkillTools(sess *ToolSession, db Database, user string, delivered map[string]bool, privateMode bool) []string {
	if sess == nil || len(delivered) == 0 {
		return nil
	}
	var attached []string
	for _, s := range LoadSkills(db, user) {
		if s.Disabled || !delivered[s.ID] || len(s.Tools) == 0 {
			continue
		}
		for i := range s.Tools {
			tool := s.Tools[i]
			// Private mode drops network-capable api-mode tools (local shell
			// tools still run); same rule the agent-kit load path uses.
			if privateMode && tool.Mode == TempToolModeAPI {
				continue
			}
			if sess.HasTempTool(tool.Name) {
				attached = append(attached, tool.Name) // already loaded — still report it for per-round surfacing
				continue
			}
			if err := sess.AppendTempTool(&tool); err != nil {
				Log("[skills] skill %q bundled tool %q failed to load: %v", s.Name, tool.Name, err)
				continue
			}
			attached = append(attached, tool.Name)
		}
	}
	return attached
}

// BuildReadSkillTool builds read_skill(skill): returns the named skill's
// instructions to apply this turn. One-shot, no state — for when the LLM
// just wants the skill's approach (a PDF-handling method, an output
// format). Marks the skill delivered so the search tool won't repeat the
// instructions.
//
// playbook, when set, resolves a skill's playbook for this turn — runs each
// rule's establishing step and renders only the arm that applies — and its
// result is appended to the instructions. The app supplies it because
// establishing a fact runs a step through the turn's machine host, which
// core does not have; nil leaves playbooks unresolved.
func BuildReadSkillTool(db Database, owner string, allowed []string, delivered map[string]bool, playbook func(SkillRecord) string) AgentToolDef {
	return AgentToolDef{
		Tool: Tool{
			Name:        "read_skill",
			Description: "Pull a named skill's instructions/approach into this turn and apply them now. Use when you want the skill's METHOD itself (how to handle a PDF, an output format, a voice): not to search its knowledge (that's skill_knowledge_search). One-shot: it returns the instructions; there's nothing to activate or turn off. A skill with a playbook also ESTABLISHES its facts when read: the reply tells you what was found and what follows from it, so read it before working those out yourself.",
			Parameters: map[string]ToolParam{
				"skill": {Type: "string", Description: "Exact skill name from the 'Available skills' block (case-insensitive).", Enum: allowedSkillNames(db, owner, allowed)},
			},
			Required: []string{"skill"},
			Caps:     []Capability{CapRead},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			found, err := resolveAllowedSkill(db, owner, allowed, stringArgSkill(args, "skill"))
			if err != nil {
				return "", err
			}
			body := strings.TrimSpace(found.Instructions)
			if delivered != nil {
				delivered[found.ID] = true
			}
			resolved := ""
			if playbook != nil && len(found.Playbook) > 0 {
				resolved = strings.TrimSpace(playbook(*found))
			}
			if body == "" && resolved == "" {
				return fmt.Sprintf("Skill %q has no instructions body: its value is its knowledge sources (use skill_knowledge_search).", found.Name), nil
			}
			out := fmt.Sprintf("Skill %q, apply this approach for the REST of this turn, including your reply:\n\n%s", found.Name, body)
			if resolved != "" {
				out += "\n\n" + resolved
			}
			return out, nil
		},
	}
}

// BuildSkillKnowledgeSearchTool builds skill_knowledge_search(skill, query):
// searches the named skill's sources — its attached collections (via the
// app-provided searchCollections, which may be nil) AND its source-hooks,
// merged. The skill's instructions are attached the FIRST time the skill is
// touched this turn (deduped via delivered) so the LLM gets the lens with
// the evidence even if it skipped read_skill.
func BuildSkillKnowledgeSearchTool(db Database, owner string, allowed []string, delivered map[string]bool, searchCollections func(SkillRecord, string) string) AgentToolDef {
	return AgentToolDef{
		Tool: Tool{
			Name:        "skill_knowledge_search",
			Description: "Search a named skill's reference sources (its document collections and live source APIs, merged and ranked) for material relevant to your query. Returns excerpts plus (the first time you touch the skill this turn), the skill's approach for interpreting them. Use when the request is in the skill's domain and you need grounded evidence. Pass a doc_id from the results to skill_knowledge_fetch_doc for the full document.",
			Parameters: map[string]ToolParam{
				"skill": {Type: "string", Description: "Exact skill name from the 'Available skills' block (case-insensitive).", Enum: allowedSkillNames(db, owner, allowed)},
				"query": {Type: "string", Description: "Natural-language search query: phrase like a web search."},
			},
			Required: []string{"skill", "query"},
			Caps:     []Capability{CapRead, CapNetwork},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			found, err := resolveAllowedSkill(db, owner, allowed, stringArgSkill(args, "skill"))
			if err != nil {
				return "", err
			}
			query := strings.TrimSpace(stringArgSkill(args, "query"))
			if query == "" {
				return "", fmt.Errorf("query is required")
			}
			var out strings.Builder
			out.WriteString(skillInstructionsBlock(*found, delivered))
			var collHits string
			if searchCollections != nil {
				collHits = strings.TrimSpace(searchCollections(*found, query))
			}
			hookHits := querySkillSourceHooks(*found, query)
			if collHits == "" && hookHits == "" {
				out.WriteString(fmt.Sprintf("No matches in the %q skill's sources for that query.", found.Name))
				return out.String(), nil
			}
			if collHits != "" {
				out.WriteString(collHits)
			}
			if hookHits != "" {
				out.WriteString(hookHits)
			}
			// Grounding reminder at the point of results (the global
			// grounding rule covers the same ground from the system prompt;
			// this keeps it salient right where the citations are).
			out.WriteString("\n\n[Grounding: cite only what appears in the results above, if a specific citation, number, name, or quote isn't here, say the sources don't specify it rather than supplying one from memory.]")
			return out.String(), nil
		},
	}
}

// BuildSkillKnowledgeFetchDocTool builds skill_knowledge_fetch_doc(skill,
// doc_id): pulls a full document from the skill's corpus via the app-
// provided fetchDoc (nil → unsupported). Mirrors fetch_knowledge_doc.
// Like the other skill tools, it attaches the skill's instructions on the
// first touch this turn (deduped via delivered) — so even if the LLM jumps
// straight to a fetch without read_skill / skill_knowledge_search first,
// it still gets the skill's lens.
func BuildSkillKnowledgeFetchDocTool(db Database, owner string, allowed []string, delivered map[string]bool, fetchDoc func(SkillRecord, string) (string, error)) AgentToolDef {
	return AgentToolDef{
		Tool: Tool{
			Name:        "skill_knowledge_fetch_doc",
			Description: "Fetch the full text of a document from a skill's corpus, by the doc_id returned in a skill_knowledge_search result. Use when an excerpt isn't enough.",
			Parameters: map[string]ToolParam{
				"skill":  {Type: "string", Description: "Exact skill name (case-insensitive).", Enum: allowedSkillNames(db, owner, allowed)},
				"doc_id": {Type: "string", Description: "The doc_id from a skill_knowledge_search hit."},
			},
			Required: []string{"skill", "doc_id"},
			Caps:     []Capability{CapRead},
		},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			found, err := resolveAllowedSkill(db, owner, allowed, stringArgSkill(args, "skill"))
			if err != nil {
				return "", err
			}
			docID := strings.TrimSpace(stringArgSkill(args, "doc_id"))
			if docID == "" {
				return "", fmt.Errorf("doc_id is required")
			}
			if fetchDoc == nil {
				return "", fmt.Errorf("document fetch isn't available for skill %q here", found.Name)
			}
			doc, err := fetchDoc(*found, docID)
			if err != nil {
				return "", err
			}
			return skillInstructionsBlock(*found, delivered) + doc, nil
		},
	}
}

// stringArgSkill extracts a string arg with a case-insensitive fallback.
func stringArgSkill(args map[string]any, key string) string {
	if v, ok := args[key].(string); ok {
		return v
	}
	lower := strings.ToLower(key)
	for k, v := range args {
		if strings.ToLower(k) == lower {
			if s, ok := v.(string); ok {
				return s
			}
		}
	}
	return ""
}

// SkillPromptSection returns the skill's instructions formatted for
// injection into a system prompt — an H2 header with the skill name
// followed by the body, separated by blank lines.
func SkillPromptSection(s SkillRecord) string {
	body := strings.TrimSpace(s.Instructions)
	if body == "" {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\n## Skill: ")
	b.WriteString(s.Name)
	b.WriteString("\n\n")
	b.WriteString(body)
	return b.String()
}

// noteSkillShareGaps tells a skill's OWNER that what they shared arrived
// incomplete.
//
// To the owner, because they are the only one who can do anything: share the
// tool as a tool, share the collection as a collection, or reword the skill so
// it does not depend on either. The recipient cannot, and telling them would be
// reporting somebody else's configuration at them.
//
// Folded by the notice's own fingerprint, so a skill that activates fifty times
// a day is one row with a count rather than a stream.
func noteSkillShareGaps(owner, recipient string, s SkillRecord) {
	if RootDB == nil || len(s.SharedOmitted) == 0 || owner == "" || owner == recipient {
		return
	}
	notices.Record(RootDB, notices.Notice{
		Owner: owner,
		Kind:  notices.KindStopped,
		Title: "\"" + s.Name + "\" reaches other people without " + strings.Join(s.SharedOmitted, " or "),
		Body: "A skill share carries the behaviour, not the owner's code: a bundled tool would run in somebody else's session, under their credentials, with no approval anywhere. " +
			"Tools are shared as tools instead. If this skill needs one, share it from Extensions; otherwise the people you shared this with are following instructions that reference something they cannot reach. " +
			"Attached collections are not stripped — each resolves for whoever was given it, and a run that cannot reach one says so on its own.",
	})
}

// ----------------------------------------------------------------------
// The deployment rung
// ----------------------------------------------------------------------

// deploymentSkillsTable holds the skills the deployment publishes: one slice,
// in the same store the per-user pools live in.
//
// A table of its own rather than a reserved key in skillsTable, which is keyed
// by username — a deployment skill sharing that keyspace would be one
// unfortunate account name away from being somebody's personal pool.
const deploymentSkillsTable = "deployment_skills"

// SkillPromotionKind is the promotion kind for publishing a skill
// deployment-wide.
const SkillPromotionKind = "skill"

func init() {
	promotion.RegisterApprover(SkillPromotionKind, func(owner, id string) error {
		return promoteSkillToDeployment(owner, id)
	})
}

// DeploymentSkills returns the skills published to everybody.
//
// They carry no bundled tools, by construction: the promotion strips them for
// the reason a peer share does, only more so, since this reaches every user
// rather than one. Attached collection ids travel and resolve per runtime user
// at retrieval time, so a skill published alongside a deployment collection
// works for everybody and one pointing at the author's private corpus quietly
// reaches nobody and says so.
func DeploymentSkills(db Database) []SkillRecord {
	store := skillStore(db)
	if store == nil {
		return nil
	}
	var out []SkillRecord
	if !store.Get(deploymentSkillsTable, "all", &out) {
		return nil
	}
	live := out[:0]
	for _, s := range out {
		// Disabled is the author's own mute and it still counts here: an admin
		// approved publishing a skill, not publishing it regardless of what its
		// author later decided about it.
		if !s.Disabled {
			live = append(live, s)
		}
	}
	sort.SliceStable(live, func(i, j int) bool { return live[i].Updated.After(live[j].Updated) })
	return live
}

// promoteSkillToDeployment moves a user's skill into the deployment pool, where
// it activates on everybody's turns.
//
// Unexported and reached only through the registered approver: publishing to
// the whole deployment is an administrator's decision, not a call another
// package makes on its own.
//
// The record MOVES rather than being copied, as a collection's does. One copy
// means an edit later is an edit everybody gets, and it means there is exactly
// one answer to "whose is this" — the author's name stays on it, because a
// published skill nobody is answerable for is worse than none.
//
// The bundled tools do NOT come. Everything true of that for one recipient is
// more true for every user at once: it would run the author's scripts in every
// session in the deployment, under each person's own credentials, with the tool
// rung skipped entirely.
func promoteSkillToDeployment(owner, id string) error {
	owner, id = strings.TrimSpace(owner), strings.TrimSpace(id)
	if owner == "" || id == "" {
		return errString("owner and skill id are required")
	}
	store := skillStore(nil)
	if store == nil {
		return errString("no skill store")
	}
	// By id, then by name. The request is FILED under the skill's name, because
	// an administrator deciding whether the deployment should publish something
	// is reading that row and "sk-9f2a1c" tells them nothing. The id still
	// resolves, for any caller that has one.
	var found *SkillRecord
	for _, s := range LoadSkills(nil, owner) {
		if s.ID == id || strings.EqualFold(s.Name, id) {
			c := s
			found = &c
			break
		}
	}
	if found == nil {
		return errString("no skill " + id + " owned by " + owner +
			" (a skill renamed after the request was filed no longer answers to the name on it)")
	}
	for _, s := range DeploymentSkills(nil) {
		if strings.EqualFold(s.Name, found.Name) {
			return errString("the deployment already publishes a skill called " + found.Name +
				"; rename yours before publishing it, so a turn matching that name has one answer")
		}
	}

	published := *found
	published.Owner = owner
	stripped := len(published.Tools)
	published.Tools = nil
	// The peer shares go: everybody has it now, so a list naming three people
	// decides nothing, and leaving it would have a later narrowing silently
	// restore an ACL the owner had forgotten.
	published.AllowedUsers = nil

	existing := DeploymentSkills(nil)
	store.Set(deploymentSkillsTable, "all", append(existing, published))
	DeleteSkill(nil, owner, found.ID)
	Log("[skills] %q published %q deployment-wide (%d bundled tool(s) not carried)", owner, published.Name, stripped)
	noteSkillPublished(owner, published, stripped)
	return nil
}

// NarrowSkillToOwner is the way back, and it is the OWNER's: nobody needs
// permission to stop publishing something they wrote.
//
// It returns to their own pool with no recipients, because the alternative is
// guessing which of the deployment's users they meant to keep. The bundled
// tools do not come back — they were dropped at publication, and the record
// that exists now is the one everybody has been using.
func NarrowSkillToOwner(db Database, owner, id string) error {
	owner, id = strings.TrimSpace(owner), strings.TrimSpace(id)
	store := skillStore(db)
	if store == nil || owner == "" || id == "" {
		return errString("owner and skill id are required")
	}
	all := DeploymentSkills(db)
	var taken *SkillRecord
	rest := make([]SkillRecord, 0, len(all))
	for _, s := range all {
		if s.ID == id {
			c := s
			taken = &c
			continue
		}
		rest = append(rest, s)
	}
	if taken == nil {
		return errString("no deployment skill " + id)
	}
	if taken.Owner != owner {
		return errString("skill " + id + " was published by " + taken.Owner + ", not " + owner)
	}
	store.Set(deploymentSkillsTable, "all", rest)
	taken.AllowedUsers = nil
	if _, err := SaveSkillAs(db, owner, *taken, "unpublished"); err != nil {
		// Put it back rather than losing it between two tables.
		store.Set(deploymentSkillsTable, "all", all)
		return err
	}
	Log("[skills] %q took %q back from the deployment", owner, taken.Name)
	return nil
}

// noteSkillPublished tells the author what reaching everybody cost them.
//
// They asked for one thing and two follow: the skill left their pool, and its
// bundled tools did not come. A publication that reports success and says
// neither leaves somebody wondering where their skill went and why the
// deployment's copy does half of what theirs did.
func noteSkillPublished(owner string, s SkillRecord, strippedTools int) {
	if RootDB == nil || owner == "" {
		return
	}
	body := "It has moved out of your own skills and into the deployment's, where it activates on everybody's turns. It is still yours: your name is on it, you edit it, and you can take it back at any time without asking."
	if strippedTools > 0 {
		body += " Its " + strconv.Itoa(strippedTools) + " bundled tool(s) did not come with it — that would run your scripts in every session in the deployment, under each person's own credentials. Share those from Extensions if the skill needs them."
	}
	if len(s.AttachedCollections) > 0 {
		body += " Its attached collections travel as references and resolve for whoever can already read them, so promote the collection too if everybody is meant to have it."
	}
	notices.Record(RootDB, notices.Notice{
		Owner: owner,
		Kind:  notices.KindStopped,
		Title: "\"" + s.Name + "\" is now a deployment skill",
		Body:  body,
	})
}

func init() {
	shareledger.Register("skill", shareledger.Provider{
		Label: "Skill",
		Candidates: func(owner string) []shareledger.Grant {
			var out []shareledger.Grant
			for _, s := range LoadSkills(nil, owner) {
				out = append(out, shareledger.Grant{ID: s.ID, Name: s.Name})
			}
			return out
		},
		Share: func(owner, id string, recipients []string, _ map[string]string) []string {
			for _, s := range LoadSkills(nil, owner) {
				if s.ID != id {
					continue
				}
				s.AllowedUsers = addRecipients(s.AllowedUsers, recipients)
				if _, err := SaveSkillAs(nil, owner, s, "shared"); err != nil {
					return []string{"Skill " + s.Name + ": " + err.Error()}
				}
				out := []string{"Skill " + s.Name + " → " + strings.Join(recipients, ", ")}
				if d := skillShareDetail(s); d != "" {
					out = append(out, "Skill "+s.Name+": "+d)
				}
				return out
			}
			return []string{"That skill is not yours to share."}
		},
		Mine: func(owner string) []shareledger.Grant {
			var out []shareledger.Grant
			for _, s := range LoadSkills(nil, owner) {
				if len(s.AllowedUsers) == 0 {
					continue
				}
				out = append(out, shareledger.Grant{
					ID: s.ID, Name: s.Name, Recipients: s.AllowedUsers,
					Reach:     "Shared with " + strings.Join(s.AllowedUsers, ", "),
					Detail:    skillShareDetail(s),
					Revocable: true,
				})
			}
			// A published one reaches everybody, and taking it back is the
			// author's — but through the skill's own door, not this one, since
			// un-publishing is not the same act as dropping one recipient.
			for _, s := range DeploymentSkills(nil) {
				if s.Owner == owner {
					out = append(out, shareledger.Grant{
						ID: s.ID, Name: s.Name, Reach: "Deployment-wide", Wide: true,
						Detail: skillShareDetail(s),
					})
				}
			}
			return out
		},
		ToMe: func(user string) []shareledger.Grant {
			var out []shareledger.Grant
			for _, s := range sharedSkillsFor(nil, user) {
				out = append(out, shareledger.Grant{
					ID: s.ID, Name: s.Name, Owner: s.SharedFrom,
					Reach: "Activates on your turns", Detail: skillShareDetail(s),
				})
			}
			for _, s := range DeploymentSkills(nil) {
				if s.Owner != user {
					out = append(out, shareledger.Grant{
						ID: s.ID, Name: s.Name, Owner: s.Owner,
						Reach: "Published to everybody", Wide: true,
					})
				}
			}
			return out
		},
		Carries: func(owner, id, viewer string) []string {
			for _, s := range LoadSkills(nil, owner) {
				if s.ID != id {
					continue
				}
				var out []string
				for _, cid := range s.AttachedCollections {
					if c, ok := LoadCollection(UserDB(CollectionsDB(), owner), owner, cid); ok {
						out = append(out, "Reads "+owner+"'s \""+c.Name+"\" while it is active")
					}
				}
				return out
			}
			return nil
		},
		Manifest: func(owner, id, recipient string) []string {
			for _, s := range LoadSkills(nil, owner) {
				if s.ID != id {
					continue
				}
				var out []string
				if n := len(s.Tools); n > 0 {
					out = append(out, "It expects "+strconv.Itoa(n)+" tool(s) that stay with "+owner+
						". If its steps call for one you do not have, say so rather than working around it.")
				}
				for _, cid := range s.AttachedCollections {
					if _, ok := LoadCollection(UserDB(CollectionsDB(), recipient), recipient, cid); !ok {
						out = append(out, "It reads a document collection you cannot reach. Ask "+owner+" to share it.")
						break
					}
				}
				return out
			}
			return nil
		},
		Revoke: func(owner, id, recipient string) error {
			for _, s := range LoadSkills(nil, owner) {
				if s.ID != id {
					continue
				}
				if recipient == "" {
					s.AllowedUsers = nil
				} else {
					s.AllowedUsers = dropRecipient(s.AllowedUsers, recipient)
				}
				_, err := SaveSkillAs(nil, owner, s, "share taken back")
				return err
			}
			return errString("no skill " + id + " owned by " + owner)
		},
	})
}

// skillShareDetail is the manifest line: what a recipient has to bring for
// this skill to do what it says. Its bundled tools never travel, and its
// collections travel as references that answer only for whoever can read them.
func skillShareDetail(s SkillRecord) string {
	var parts []string
	if n := len(s.Tools); n > 0 {
		parts = append(parts, strconv.Itoa(n)+" bundled tool(s) stay with the author")
	}
	if n := len(s.AttachedCollections); n > 0 {
		parts = append(parts, strconv.Itoa(n)+" attached collection(s) answer only for whoever can already read them")
	}
	return strings.Join(parts, "; ")
}

// dropRecipient removes one name, leaving the rest as they were.
func dropRecipient(list []string, drop string) []string {
	out := []string{}
	for _, u := range list {
		if u != drop {
			out = append(out, u)
		}
	}
	return out
}

// addRecipients appends the ones not already there, leaving the rest as they
// were: a share is additive, and rewriting the whole list would drop anybody
// granted for a reason this share knows nothing about.
func addRecipients(have, add []string) []string {
	seen := map[string]bool{}
	out := append([]string{}, have...)
	for _, u := range have {
		seen[u] = true
	}
	for _, u := range add {
		if u = strings.TrimSpace(u); u != "" && !seen[u] {
			seen[u] = true
			out = append(out, u)
		}
	}
	return out
}
