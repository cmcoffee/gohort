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
	"unicode"
	"unicode/utf8"
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
	// Triggers are word or phrase patterns matched against the user
	// message (and attachment filenames). Glob-style `*.pdf` matches
	// any attachment ending in .pdf; everything else is a
	// case-insensitive whole-word match (TriggersMatch). Empty triggers = embedding-only
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
	// AwaitingApproval is set on the copy a save of a published skill hands
	// back when the edit went to an administrator instead of to everybody
	// (holdPublishedSkillEdit), never stored. The caller says so, rather than
	// "updated" over text nobody else will see until it is approved.
	AwaitingApproval bool `json:"awaiting_approval,omitempty"`

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
	// Conditions, when set, make the skill apply only in some cases, as an
	// if / else if / else list. The first is the primary condition: when it
	// holds, the skill applies as written. An else if holds its own
	// condition and instructions, used in place of the skill's when it is the
	// first to hold; an else holds instructions for whatever the conditions
	// above it do not cover. With no else, a skill none of whose conditions
	// hold does not apply. The framework decides them before the skill is
	// handed over (DecideConditions), and the agent never sees the cases that
	// did not apply. Empty for most skills.
	Conditions []SkillCase `json:"conditions,omitempty"`
	// Embedding is cached at save time so the classifier doesn't
	// have to re-embed on every turn. Re-computed in SaveSkill from
	// the current Description. Persisted with the record so reloads
	// don't lose the cache.
	Embedding []float32 `json:"embedding,omitempty"`
	Created   time.Time `json:"created"`
	Updated   time.Time `json:"updated"`
}

// SkillCase is one entry in a skill's if / else if / else list.
//
// Kind is "if" (the first, primary condition), "else_if" or "else". A
// condition is checked one of two ways: "lookup" (the default) asks Question,
// answered yes or no by a short run with the skill's tools following How;
// "mentions" holds when the message mentions any of Mentions (words, phrases,
// or *.ext for an attachment), decided by the framework with no run at all.
// Instructions belong to an else if or an else: what the agent follows in
// place of the skill's own when that case is the one that applies.
type SkillCase struct {
	Kind         string   `json:"kind"`
	Check        string   `json:"check,omitempty"`
	Question     string   `json:"question,omitempty"`
	How          string   `json:"how,omitempty"`
	Mentions     []string `json:"mentions,omitempty"`
	Instructions string   `json:"instructions,omitempty"`
}

const (
	caseIf        = "if"
	caseElseIf    = "else_if"
	caseElse      = "else"
	checkLookup   = "lookup"
	checkMentions = "mentions"
	// conditionPhase is the one phase the lookups compile to.
	conditionPhase = "check"
)

// byMention reports whether the case is decided from the message alone.
func (c SkillCase) byMention() bool { return strings.TrimSpace(c.Check) == checkMentions }

// label is the case's condition as the agent and the person read it.
func (c SkillCase) label() string {
	if c.byMention() {
		return "the message mentions " + strings.Join(c.Mentions, " or ")
	}
	return strings.TrimSpace(c.Question)
}

// caseField is the check's output for the lookup case at index i, and the
// evidence beside it.
func caseField(i int) string    { return "case_" + strconv.Itoa(i+1) }
func caseEvidence(i int) string { return caseField(i) + "_evidence" }

// ConditionProblems says what the skill's conditions are missing, by
// position. Validated at the doors that take them (the skill forms,
// Builder); a skill whose conditions have problems is not gated by them, so
// it behaves as unconditional until they are complete.
func (s SkillRecord) ConditionProblems() []string {
	var probs []string
	// The If applies the skill as written, so with conditions and no
	// instructions the primary case would hand the agent nothing.
	if len(s.Conditions) > 0 && strings.TrimSpace(s.Instructions) == "" {
		probs = append(probs, "the If applies the skill's own Instructions, which are empty: write them")
	}
	for i, c := range s.Conditions {
		at := func(msg string) { probs = append(probs, "condition "+strconv.Itoa(i+1)+": "+msg) }
		switch strings.TrimSpace(c.Kind) {
		case caseIf:
			if i != 0 {
				at("only the first condition is an If; this one is Else if or Else")
			}
		case caseElseIf:
			if i == 0 {
				at("the first condition is the If; an Else if comes after it")
			}
			if strings.TrimSpace(c.Instructions) == "" {
				at("an Else if needs the instructions to follow when it holds")
			}
		case caseElse:
			if i == 0 {
				at("the first condition is the If; an Else comes after it")
			}
			if i != len(s.Conditions)-1 {
				at("an Else is the last one, since nothing after it can apply")
			}
			if strings.TrimSpace(c.Instructions) == "" {
				at("an Else needs the instructions to follow when nothing above holds")
			}
			continue
		default:
			at("the kind must be if, else_if or else")
			continue
		}
		switch strings.TrimSpace(c.Check) {
		case "", checkLookup:
			if strings.TrimSpace(c.Question) == "" {
				at("it needs its question (answered yes or no)")
			}
			if strings.TrimSpace(c.How) == "" {
				at("it needs how to check it")
			}
		case checkMentions:
			if len(c.Mentions) == 0 {
				at("it needs the words the message must mention")
			}
		default:
			at("the check must be lookup or mentions")
		}
	}
	return probs
}

// Conditional reports whether the skill carries complete conditions.
func (s SkillRecord) Conditional() bool {
	return len(s.Conditions) > 0 && len(s.ConditionProblems()) == 0
}

// ConditionMachine compiles every lookup condition of the skill into ONE
// check: an unattended phase with the skill's tools that answers each
// question yes or no and quotes what decided it. One run however many
// conditions there are, so an if / else if list costs a single lookup.
func (s SkillRecord) ConditionMachine() MachineDef {
	var qs strings.Builder
	var out []PipelineField
	for i, c := range s.Conditions {
		if c.Kind == caseElse || c.byMention() {
			continue
		}
		fmt.Fprintf(&qs, "%s: %s\n   How to check: %s\n", caseField(i), strings.TrimSpace(c.Question), strings.TrimSpace(c.How))
		out = append(out,
			PipelineField{Name: caseField(i), Type: FieldBool, Required: true, Desc: "true (yes) or false (no) for " + caseField(i)},
			PipelineField{Name: caseEvidence(i), Type: FieldString, Required: true,
				Desc: "the one line of what you saw that decided " + caseField(i) + " (a command's output line, a number, a status), quoted, not described"})
	}
	prompt := "Answer the questions below and report them; do not answer the person's message here, and do not go past what is asked.\n\n" +
		qs.String() + "\n" +
		"Use the tools you have to check, then report each as true (yes) or false (no), with the one line that decided it as its _evidence. " +
		"Report what the evidence shows, not what would be convenient. If you could not check one, say why in your text and report it false." +
		"\n\nThe person's message, for context:\n\n{input}"
	return MachineDef{
		Name:        s.Name + " conditions",
		Description: "Checks which case of the " + s.Name + " skill applies.",
		Start:       conditionPhase,
		Unattended:  true,
		Phases: []MachinePhase{{
			Name:   conditionPhase,
			Desc:   "Checking which case of " + s.Name + " applies",
			Prompt: prompt,
			Think:  "on",
			// It goes and finds the answers, so it reaches the catalog. Said
			// outright: an unset reach on a step like this one reaches nothing
			// whenever the skill allows no tools by name.
			Reach:  ReachAll,
			Tools:  append([]string(nil), s.AllowedTools...),
			Output: out,
		}},
	}
}

// readYesNo reads a decoded answer: a bool, or the words a model writes.
func readYesNo(v any) (answer, ok bool) {
	switch t := v.(type) {
	case bool:
		return t, true
	case string:
		switch strings.ToLower(strings.TrimSpace(t)) {
		case "true", "yes", "y":
			return true, true
		case "false", "no", "n":
			return false, true
		}
	}
	return false, false
}

// DecideConditions settles the skill's conditions for one message: the cases
// in order, the first that holds wins, an else when none does. lookup runs the
// skill's ConditionMachine; it is called at most once, and only when a lookup
// condition is reached (a message the primary mentions-condition already
// settles costs nothing). chosen is the skill as the agent gets it, its
// instructions those of the case that applied. note says what was found, so
// the agent neither follows instructions meant for another case nor goes
// looking for a skill that does not apply. A lookup that fails or gives no
// clear yes or no settles nothing: the skill does not apply, and the note says
// why, since following the wrong case is what this exists to prevent.
func (s SkillRecord) DecideConditions(message string, attachmentNames []string, lookup func(MachineDef) (fields map[string]any, text string, err error)) (chosen SkillRecord, applies bool, note string) {
	chosen = s
	if !s.Conditional() {
		return chosen, true, ""
	}
	var fields map[string]any
	var text string
	looked := false
	var seen []string
	for i, c := range s.Conditions {
		if c.Kind == caseElse {
			chosen.Instructions = c.Instructions
			return chosen, true, fmt.Sprintf("Checked before applying %q: %s. None held, so these are its instructions for every other case.", s.Name, strings.Join(seen, "; "))
		}
		held := false
		if c.byMention() {
			held = TriggersMatch(c.Mentions, message, attachmentNames)
			seen = append(seen, c.label()+": "+yesNo(held))
		} else {
			if !looked {
				looked = true
				var err error
				if lookup == nil {
					err = errString("no way to look it up here")
				} else {
					fields, text, err = lookup(s.ConditionMachine())
				}
				if err != nil {
					return chosen, false, fmt.Sprintf("The %q skill does not apply this turn: its conditions could not be checked (%s). Do not follow it; if it matters, ask the person.", s.Name, err.Error())
				}
			}
			answer, ok := readYesNo(fields[caseField(i)])
			ev := conditionEvidence(fields[caseEvidence(i)], text)
			if !ok {
				return chosen, false, fmt.Sprintf("The %q skill does not apply this turn: its condition (%s) could not be checked (the check gave no yes or no%s). Do not follow it; if it matters, ask the person.", s.Name, c.label(), ev)
			}
			held = answer
			seen = append(seen, c.label()+" "+yesNo(held)+ev)
		}
		if held {
			if c.Kind == caseElseIf {
				chosen.Instructions = c.Instructions
				return chosen, true, fmt.Sprintf("Checked before applying %q: %s. So these are its instructions for that case.", s.Name, strings.Join(seen, "; "))
			}
			return chosen, true, fmt.Sprintf("Checked before applying %q: %s.", s.Name, strings.Join(seen, "; "))
		}
	}
	return chosen, false, fmt.Sprintf("The %q skill does not apply this turn. Its conditions were checked and none holds: %s.", s.Name, strings.Join(seen, "; "))
}

func yesNo(b bool) string {
	if b {
		return "Yes"
	}
	return "No"
}

// conditionEvidence is the line that decided a lookup, as ": <line>", or ""
// when there is none. The step's text stands in for a missing evidence field,
// but not the structured output itself, which is the answer restated.
func conditionEvidence(v any, text string) string {
	ev := strings.TrimSpace(fmt.Sprint(v))
	if ev == "" || ev == "<nil>" {
		ev = strings.TrimSpace(text)
		if strings.HasPrefix(ev, "{") {
			ev = ""
		}
	}
	if len(ev) > 240 {
		ev = ev[:240] + "..."
	}
	if ev == "" {
		return ""
	}
	return ": " + ev
}

// conditionSummary is the skill's conditions as the Available skills list
// gives them: "if X; else if Y; else".
func (s SkillRecord) conditionSummary() string {
	var parts []string
	for _, c := range s.Conditions {
		switch c.Kind {
		case caseElse:
			parts = append(parts, "else")
		case caseElseIf:
			parts = append(parts, "else if "+c.label())
		default:
			parts = append(parts, "if "+c.label())
		}
	}
	return strings.Join(parts, "; ")
}

// ConditionRows is the skill's conditions as a form's "rows" field edits them:
// one row per case, its mentions as one comma-separated line.
func (s SkillRecord) ConditionRows() []map[string]any {
	out := make([]map[string]any, 0, len(s.Conditions))
	for _, c := range s.Conditions {
		check := c.Check
		if check == "" {
			check = checkLookup
		}
		out = append(out, map[string]any{
			"kind": c.Kind, "check": check, "question": c.Question, "how": c.How,
			"mentions": strings.Join(c.Mentions, ", "), "instructions": c.Instructions,
		})
	}
	return out
}

// SetConditionRows reads a form's condition rows onto the skill. A row's
// kind left unset is the If when it is first and an Else if after; its check
// left unset is a lookup; mentions split on commas. An empty list clears. The
// result is not validated here: the caller asks ConditionProblems, so the
// error reaches the person while the form is still on screen.
func (s *SkillRecord) SetConditionRows(rows []map[string]any) {
	str := func(r map[string]any, k string) string {
		v, ok := r[k]
		if !ok || v == nil {
			return ""
		}
		return strings.TrimSpace(fmt.Sprint(v))
	}
	var cases []SkillCase
	for _, r := range rows {
		c := SkillCase{Kind: str(r, "kind"), Check: str(r, "check"), Question: str(r, "question"),
			How: str(r, "how"), Instructions: str(r, "instructions")}
		for _, m := range strings.Split(str(r, "mentions"), ",") {
			if m = strings.TrimSpace(m); m != "" {
				c.Mentions = append(c.Mentions, m)
			}
		}
		if c.Kind == "" {
			if len(cases) == 0 {
				c.Kind = caseIf
			} else {
				c.Kind = caseElseIf
			}
		}
		if c.Check == "" || c.Check == checkLookup {
			c.Check = ""
		}
		// What the case does not use is not kept: an Else has no condition,
		// a lookup no mentions, a mention no question, and the If's
		// instructions are the skill's own.
		switch {
		case c.Kind == caseElse:
			c.Check, c.Question, c.How, c.Mentions = "", "", "", nil
		case c.Check == checkMentions:
			c.Question, c.How = "", ""
		default:
			c.Mentions = nil
		}
		if c.Kind == caseIf {
			c.Instructions = ""
		}
		cases = append(cases, c)
	}
	s.Conditions = cases
}

// ConditionLine is the skill's conditions in a few words, for a list: the
// primary, and how many cases follow it.
func (s SkillRecord) ConditionLine() string {
	if !s.Conditional() {
		return ""
	}
	line := "If " + s.Conditions[0].label()
	if n := len(s.Conditions) - 1; n == 1 {
		line += " (+1 case)"
	} else if n > 1 {
		line += fmt.Sprintf(" (+%d cases)", n)
	}
	return line
}

// ConditionFires reports whether a conditional skill is checked on this turn
// before the model's first round, without waiting for it to be consulted: its
// triggers match. A trigger on a conditional skill is a rule about the turn
// ("on sync problems, check the version first"), not a hint the model may
// decline, which is what the conditions are for.
func (s SkillRecord) ConditionFires(message string, attachmentNames []string) bool {
	return s.Conditional() && !s.Disabled && SkillTriggersMatch(s, message, attachmentNames)
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
	for _, ref := range SharedRefsFor(store, sharedSkillsTable, username) {
		for _, s := range LoadSkills(db, ref.Owner) {
			if s.ID != ref.ID || s.Disabled {
				continue
			}
			// Re-checked against the record, not trusted from the index. The
			// list on the skill is what the owner edits and an admin audits;
			// the index is a derived lookup, and a derived thing that can
			// outvote its source is how a revoked share keeps working.
			if !skillSharedWith(s, ref.Owner, username) {
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

// skillSharedWith says whether owner's skill reaches user: by name, or through
// a group they are both in.
func skillSharedWith(s SkillRecord, owner, user string) bool {
	return SharedWithUser(s.AllowedUsers, owner, user)
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
	s.AwaitingApproval = false
	// A skill its author published has moved to the deployment pool and is
	// still theirs. Saving it goes there, to the copy everybody uses, rather
	// than landing a second, private copy in the author's own pool. Publishing
	// MOVES a record, so an id is only ever in one of the two.
	if s.ID != "" {
		for _, p := range PublishedSkillsBy(db, username) {
			if p.ID == s.ID {
				return saveDeploymentSkill(db, username, s, reason)
			}
		}
	}
	if s.ID == "" {
		s.ID = "skill-" + UUIDv4()
	}
	if s.Created.IsZero() {
		s.Created = time.Now()
	}
	s.Owner = username
	s.Updated = time.Now()
	// The condition is NOT validated here. Storage stores; the doors validate
	// (the skill forms, the Builder tool). An incomplete condition is not
	// checked (Conditional), so it never gates anything.
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
	// Whoever this save took off the share is told, on every path that narrows
	// it (the skill's own form, the Sharing page, a rollback), because they all
	// arrive here. Their agents keep the id; without this they find out by
	// watching one answer without the skill.
	if hadPrior {
		shareledger.Withdrawn("skill", username, s.ID, s.Name, sharesLost(prior.AllowedUsers, s.AllowedUsers, username))
	}
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
	// Read before it goes: the notice below names it, and after the drop the
	// id is all anybody has.
	var gone SkillRecord
	for _, s := range LoadSkills(db, username) {
		if s.ID == id {
			gone = s
			break
		}
	}
	if !dropSkillRecord(db, username, id) {
		return false
	}
	// Here rather than in dropSkillRecord, which publishing also uses: a skill
	// moving to the deployment still reaches everybody it reached before.
	shareledger.Withdrawn("skill", username, id, gone.Name, ShareMembers(gone.AllowedUsers, username))
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

// dropSkillRecord takes a skill out of its owner's pool, with its shares, and
// nothing else. Publishing MOVES a skill, so the move uses this rather than
// DeleteSkill: the history and corpus belong to the skill, which still exists
// under the same owner and id.
func dropSkillRecord(db Database, username, id string) bool {
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
	// Handles, not bare names: two skills called the same thing are listed
	// with whose each is, and the tools' enum is built by the same rule, so
	// the name the model copies from here is one they accept.
	handles := skillHandles(skills)
	b.WriteString("\n\n## Available skills\n\n")
	b.WriteString("Domain packs you can draw on in your own context: read_skill(skill) returns its approach/instructions; skill_knowledge_search(skill, query) searches its sources (and attaches its approach the first time); skill_knowledge_fetch_doc(skill, doc_id) pulls a full document.\n\nRULE: when a listed skill covers the subject in front of you, consult it FIRST, call skill_knowledge_search (or read_skill) before web_search and before answering from memory. Its sources are authoritative for its domain and override your priors, so answering a covered question without it is a mistake even when you're confident. This fires on what you DISCOVER mid-task, not just the opening request: a repo that turns out to be Go → the Go skill, a tax-law doc → the tax skill, a PDF → the PDF skill, even if the user never named the domain. On FOLLOW-UPS the skill's instructions and what you already retrieved stay in your context: answer from that skill content, not your priors, and search the skill again only if the follow-up needs material you didn't pull. Skip a skill only for what it plainly doesn't cover or fast-changing facts (current events, latest figures). When a skill's trigger matches the turn you'll see a \"Likely relevant\" hint: treat it as a strong nudge to consult that skill, not a guarantee. Format: **name**, purpose.\n\n")
	for i, s := range skills {
		// Full description — descriptions are model-facing; show it
		// un-truncated so the whole activation cue is visible.
		desc := strings.TrimSpace(s.Description)
		if desc == "" {
			desc = "(no description)"
		}
		b.WriteString("- **")
		b.WriteString(handles[i])
		b.WriteString("** ")
		b.WriteString(desc)
		if trig := strings.TrimSpace(strings.Join(s.Triggers, ", ")); trig != "" {
			b.WriteString(" (triggers: ")
			b.WriteString(trig)
			b.WriteString(")")
		}
		if s.Conditional() {
			// Said in the listing so the model knows the skill may come back
			// as "does not apply", and that the check is done for it.
			b.WriteString(" (applies by case: ")
			b.WriteString(s.conditionSummary())
			b.WriteString("; checked for you when used)")
		}
		b.WriteString("\n")
	}
	return b.String()
}

// SkillTriggersMatch reports whether a skill's Triggers match this turn: a
// glob trigger (contains '*' or '?') matches against the attachment
// filenames; any other trigger is a case-insensitive word or phrase match
// against the message text. A skill with NO triggers never matches. A match is a
// relevance SIGNAL, not a command — it surfaces a HINT nudging the LLM to
// consult the skill (see renderSkillTriggerHints), it does NOT force-inject
// the skill's instructions. Deterministic and framework-owned.
func SkillTriggersMatch(s SkillRecord, message string, attachmentNames []string) bool {
	return TriggersMatch(s.Triggers, message, attachmentNames)
}

// TriggersMatch reports whether any of the given triggers match this turn:
// a glob trigger (contains '*' or '?') matches against the attachment
// filenames; any other trigger is a case-insensitive WORD or phrase match
// against the message (triggerInMessage). Empty triggers never match.
// Generic + framework-owned — shared by skills (SkillTriggersMatch) and
// agents (the per-turn dispatch hint) so trigger semantics read identically
// across both.
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
		if triggerInMessage(lowerMsg, strings.ToLower(t)) {
			return true
		}
	}
	return false
}

// triggerSuffixes are the endings a trigger may carry in the message and
// still be the same word: "felony" matches "felonys" style plurals,
// "sentence" matches "sentenced", "contract" matches "contracts".
var triggerSuffixes = []string{"s", "es", "ed", "d", "ing", "er", "ers", "'s"}

// triggerInMessage reports whether trigger t occurs in msg (both lowercased)
// as a word or phrase: starting at a word boundary, and ending at one or after
// one of triggerSuffixes. It was a plain substring test, so a trigger fired
// inside any word that contained it: an agent with "act" among its triggers
// was offered for "actually let's make the layout more uniform", and the
// nudge to dispatch there arrived on a turn about a web page.
func triggerInMessage(msg, t string) bool {
	if t == "" {
		return false
	}
	for from := 0; from <= len(msg)-len(t); {
		i := strings.Index(msg[from:], t)
		if i < 0 {
			return false
		}
		start, end := from+i, from+i+len(t)
		if !wordRuneBefore(msg, start) && endsWord(msg, end) {
			return true
		}
		_, size := utf8.DecodeRuneInString(msg[start:])
		from = start + size
	}
	return false
}

// endsWord reports whether a match ending at end finishes a word, directly or
// through one of triggerSuffixes.
func endsWord(msg string, end int) bool {
	if !wordRuneAt(msg, end) {
		return true
	}
	for _, suf := range triggerSuffixes {
		if strings.HasPrefix(msg[end:], suf) && !wordRuneAt(msg, end+len(suf)) {
			return true
		}
	}
	return false
}

func wordRuneAt(s string, i int) bool {
	if i >= len(s) {
		return false
	}
	r, _ := utf8.DecodeRuneInString(s[i:])
	return unicode.IsLetter(r) || unicode.IsDigit(r)
}

func wordRuneBefore(s string, i int) bool {
	if i <= 0 {
		return false
	}
	r, _ := utf8.DecodeLastRuneInString(s[:i])
	return unicode.IsLetter(r) || unicode.IsDigit(r)
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
	set := allowedSkillSet(db, owner, allowed)
	handles := skillHandles(set)
	var names []string
	for i, s := range set {
		if SkillTriggersMatch(s, message, attachmentNames) {
			names = append(names, handles[i])
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
	return "\n\n[Likely relevant this turn (triggers matched): " + strings.Join(quoted, ", ") + ", consult the fitting one FIRST via skill_knowledge_search / read_skill before answering. A trigger match is a hint, not a guarantee: skip a skill that doesn't actually fit. The person did not write this note and cannot see it, so never mention it.]\n\n"
}

// allowedSkillSet is the set the skill tools, their enum and the "Available
// skills" block all draw from: every skill owner may use (their own, the ones
// shared with them, the deployment's), narrowed to the enabled ones the
// agent's allowlist names.
//
// One function because there used to be two answers. The block listed
// AvailableSkills while the tools resolved against the owner's own pool, so a
// shared or published skill was advertised and then answered "no skill named"
// by read_skill, and a same-named skill of the owner's own was consulted in
// its place.
func allowedSkillSet(db Database, owner string, allowed []string) []SkillRecord {
	if owner == "" || len(allowed) == 0 {
		return nil
	}
	allowSet := make(map[string]bool, len(allowed))
	for _, id := range allowed {
		allowSet[id] = true
	}
	var out []SkillRecord
	for _, s := range AvailableSkills(db, owner) {
		if s.Disabled || !allowSet[s.ID] {
			continue
		}
		out = append(out, s)
	}
	return out
}

// skillHandles names each skill in set the way the model must address it: the
// bare name when it is unique in the set, the name plus whose it is when two
// share it, and the id as well when even that does not separate them (two of
// one person's skills with one name, which older data can hold).
//
// Computed over the whole set, so the listing and the tool enum, handed the
// same set, print the same handles. A name the model sees twice with nothing
// to tell them apart is a coin toss, and the old resolver always lost it the
// same way: to whichever came first.
func skillHandles(set []SkillRecord) []string {
	out := make([]string, len(set))
	names := make(map[string]int, len(set))
	for _, s := range set {
		names[strings.ToLower(strings.TrimSpace(s.Name))]++
	}
	for i, s := range set {
		name := strings.TrimSpace(s.Name)
		switch who := skillAuthor(s); {
		case names[strings.ToLower(name)] < 2:
			out[i] = name
		case who != "":
			out[i] = name + " (from " + who + ")"
		default:
			out[i] = name + " (" + s.ID + ")"
		}
	}
	handles := make(map[string]int, len(out))
	for _, h := range out {
		handles[strings.ToLower(h)]++
	}
	for i, s := range set {
		if handles[strings.ToLower(out[i])] < 2 {
			continue
		}
		name := strings.TrimSpace(s.Name)
		if who := skillAuthor(s); who != "" {
			out[i] = name + " (from " + who + ", " + s.ID + ")"
		} else {
			out[i] = name + " (" + s.ID + ")"
		}
	}
	return out
}

// skillAuthor is whose skill this is, for telling two same-named ones apart:
// the sharer on a shared copy, the owner otherwise.
func skillAuthor(s SkillRecord) string {
	if who := strings.TrimSpace(s.SharedFrom); who != "" {
		return who
	}
	return strings.TrimSpace(s.Owner)
}

// NameAmong is how this skill is named to the model when set is what the
// model can see: its bare name, or the name plus whose it is when another
// skill in set shares it. Every surface that names a skill to the model (the
// listing, the trigger hint, the tool enum) goes through the same rule, so
// the handle the model copies is one the tools accept.
func (s SkillRecord) NameAmong(set []SkillRecord) string {
	handles := skillHandles(set)
	for i := range set {
		if set[i].ID == s.ID {
			return handles[i]
		}
	}
	return strings.TrimSpace(s.Name)
}

// resolveAllowedSkill finds the skill the model named, among the ones this
// agent may use. Shared by the three skill tools.
//
// The handle from the listing first, then an id, then a bare name. A bare
// name that fits more than one skill is refused with the handles to choose
// from: guessing is how the wrong skill's instructions end up steering a
// reply with nothing to say it happened.
func resolveAllowedSkill(db Database, owner string, allowed []string, name string) (*SkillRecord, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, fmt.Errorf("skill name is required")
	}
	set := allowedSkillSet(db, owner, allowed)
	handles := skillHandles(set)
	for i := range set {
		if strings.EqualFold(handles[i], name) {
			return &set[i], nil
		}
	}
	for i := range set {
		if set[i].ID == name {
			return &set[i], nil
		}
	}
	var hits []int
	for i := range set {
		if strings.EqualFold(strings.TrimSpace(set[i].Name), name) {
			hits = append(hits, i)
		}
	}
	if len(hits) == 1 {
		return &set[hits[0]], nil
	}
	if len(hits) > 1 {
		quoted := make([]string, len(hits))
		for j, i := range hits {
			quoted[j] = strconv.Quote(handles[i])
		}
		return nil, fmt.Errorf("more than one skill here is called %q - name the one you mean: %s", name, strings.Join(quoted, ", "))
	}
	for _, s := range AvailableSkills(db, owner) {
		if !s.Disabled && strings.EqualFold(strings.TrimSpace(s.Name), name) {
			return nil, fmt.Errorf("skill %q is not enabled here", s.Name)
		}
	}
	return nil, fmt.Errorf("no skill named %q (check the 'Available skills' block)", name)
}

// allowedSkillNames returns the handles of the skills in the allowed set,
// sorted. Used to close the skill-tool `skill` parameter to an enum so the
// model can only name a skill that actually exists — it can't invent one or
// address an agent (e.g. a former skill that's now an agent) through the
// skill tools. The same handles the "Available skills" block prints, so
// two same-named skills are two distinct choices here too. Empty (e.g. all
// allowed IDs are orphans) yields no enum, leaving the handler's rejection
// as the backstop.
func allowedSkillNames(db Database, owner string, allowed []string) []string {
	names := skillHandles(allowedSkillSet(db, owner, allowed))
	if len(names) == 0 {
		return nil
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
	// The RUNTIME user's own pool, and only that, on purpose. The skill tools
	// resolve against the agent owner's wider set (shared, published), but a
	// bundled tool is code, and code runs only for the person who wrote it:
	// shared and published copies arrive with Tools stripped, and an agent
	// owner's own skill must not run its scripts in somebody else's session
	// just because they chatted the agent. So a delivered id that is not one
	// of this user's own skills attaches nothing.
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

// SkillGate settles a conditional skill for this turn: the skill as the agent
// gets it (its instructions those of the case that applied), whether it
// applies at all, and the line the agent is told either way
// (SkillRecord.DecideConditions). The app caches it per turn, so whichever
// tool asks first runs the check and the rest reuse the answer. Never called
// for a skill without conditions.
type SkillGate func(SkillRecord) (chosen SkillRecord, applies bool, note string)

// check runs the gate on a conditional skill. An unconditional skill, or no
// gate at all, applies as written with nothing to add.
func (g SkillGate) check(s SkillRecord) (SkillRecord, bool, string) {
	if g == nil || !s.Conditional() {
		return s, true, ""
	}
	return g(s)
}

// anyConditional reports whether any of the skills carries a condition.
func anyConditional(skills []SkillRecord) bool {
	for _, s := range skills {
		if s.Conditional() {
			return true
		}
	}
	return false
}

// BuildReadSkillTool builds read_skill(skill): returns the named skill's
// instructions to apply this turn. One-shot, no state — for when the LLM
// just wants the skill's approach (a PDF-handling method, an output
// format). Marks the skill delivered so the search tool won't repeat the
// instructions.
//
// owner is whose skills these are: the AGENT's owner, not necessarily the
// person running it. The tools resolve against everything owner may use (own,
// shared with them, published), narrowed to allowed, which is the same set the
// "Available skills" block lists.
//
// gate, when set, checks a conditional skill before it is handed over (see
// SkillGate). The app supplies it because the check runs a step through the
// turn's machine host, which core does not have; nil leaves conditions
// unchecked, so a conditional skill is handed over as a plain one.
func BuildReadSkillTool(db Database, owner string, allowed []string, delivered map[string]bool, gate SkillGate) AgentToolDef {
	desc := "Pull a named skill's instructions/approach into this turn and apply them now. Use when you want the skill's METHOD itself (how to handle a PDF, an output format, a voice): not to search its knowledge (that's skill_knowledge_search). One-shot: it returns the instructions; there's nothing to activate or turn off."
	if anyConditional(allowedSkillSet(db, owner, allowed)) {
		// Only where it can happen, so it costs nothing on the turns it cannot.
		desc += " A skill that applies by case is checked for you when read: the reply says which case applies, or that none does, and what the check found."
	}
	return AgentToolDef{
		Tool: Tool{
			Name:        "read_skill",
			Description: desc,
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
			chosen, applies, resolved := gate.check(*found)
			if !applies {
				return resolved, nil
			}
			body := strings.TrimSpace(chosen.Instructions)
			if delivered != nil {
				delivered[found.ID] = true
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
func BuildSkillKnowledgeSearchTool(db Database, owner string, allowed []string, delivered map[string]bool, gate SkillGate, searchCollections func(SkillRecord, string) string) AgentToolDef {
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
			// A skill that does not apply this turn brings nothing: not its
			// approach, and not its sources either.
			chosen, applies, note := gate.check(*found)
			if !applies {
				return note, nil
			}
			var out strings.Builder
			if instr := skillInstructionsBlock(chosen, delivered); instr != "" {
				if note != "" {
					out.WriteString(note + "\n\n")
				}
				out.WriteString(instr)
			}
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
func BuildSkillKnowledgeFetchDocTool(db Database, owner string, allowed []string, delivered map[string]bool, gate SkillGate, fetchDoc func(SkillRecord, string) (string, error)) AgentToolDef {
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
			chosen, applies, note := gate.check(*found)
			if !applies {
				return note, nil
			}
			doc, err := fetchDoc(*found, docID)
			if err != nil {
				return "", err
			}
			instr := skillInstructionsBlock(chosen, delivered)
			if instr != "" && note != "" {
				instr = note + "\n\n" + instr
			}
			return instr + doc, nil
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
	promotion.RegisterApprover(SkillPromotionKind, approveSkillRequest)
	promotion.RegisterRequestHook(SkillPromotionKind, snapshotSkillRequest)
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
	var live []SkillRecord
	for _, s := range allDeploymentSkills(db) {
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

// allDeploymentSkills is the deployment pool as stored, muted ones included.
//
// Every read-modify-write of the pool goes through this, never through
// DeploymentSkills. That list hides what an author disabled, and a write built
// on it writes the hidden ones out of existence: publishing any skill, or
// taking any back, used to erase every muted deployment skill along with it.
func allDeploymentSkills(db Database) []SkillRecord {
	store := skillStore(db)
	if store == nil {
		return nil
	}
	var out []SkillRecord
	store.Get(deploymentSkillsTable, "all", &out)
	return out
}

// PublishedSkillsBy is what owner has published, muted ones included, for the
// author's own list. DeploymentSkills leaves a muted one out because nobody
// should activate it, but its author still has to SEE it to turn it back on
// or take it back, or disabling a published skill is a one-way door.
func PublishedSkillsBy(db Database, owner string) []SkillRecord {
	if strings.TrimSpace(owner) == "" {
		return nil
	}
	var out []SkillRecord
	for _, s := range allDeploymentSkills(db) {
		if s.Owner == owner {
			out = append(out, s)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Updated.After(out[j].Updated) })
	return out
}

// saveDeploymentSkill writes an author's edit to a skill they published. The
// record stays in the deployment pool: an edit is an edit everybody gets,
// which is why publishing moves the record rather than copying it. Reached
// through SaveSkillAs, so every surface that saves an author's skill saves a
// published one to the right place without knowing there are two pools.
//
// Only the author may, and only to what a published skill may carry. The
// bundled tools stay off (bringing them back here would be the way around the
// administrator who approved publishing it without them) and so do peer
// recipients, since everybody already has it. Owner and Created come from the
// stored record, never the argument.
func saveDeploymentSkill(db Database, owner string, s SkillRecord, reason string) (SkillRecord, error) {
	owner = strings.TrimSpace(owner)
	store := skillStore(db)
	if store == nil || owner == "" || s.ID == "" {
		return SkillRecord{}, errString("owner and skill id are required")
	}
	all := allDeploymentSkills(db)
	at := -1
	for i := range all {
		if all[i].ID == s.ID {
			at = i
			break
		}
	}
	if at < 0 {
		return SkillRecord{}, errString("no deployment skill " + s.ID)
	}
	if all[at].Owner != owner {
		return SkillRecord{}, errString("skill " + s.ID + " was published by " + all[at].Owner + ", not " + owner)
	}
	// A rename is held to the rule publishing is: a turn matching a name has
	// one answer. Checked only on a rename, so an edit to the text of a skill
	// is never refused over a clash it did not cause.
	if !strings.EqualFold(all[at].Name, s.Name) {
		for i, o := range all {
			if i != at && strings.EqualFold(o.Name, s.Name) {
				return SkillRecord{}, errString("the deployment already publishes a skill called " + s.Name)
			}
		}
	}
	s.Owner = owner
	s.Created = all[at].Created
	s.Tools = nil
	s.AllowedUsers = nil
	s.SharedFrom, s.SharedOmitted, s.AwaitingApproval = "", nil, false
	s.Updated = time.Now()
	// An administrator approved this text going to everybody, so changing it
	// is the author's request and an administrator's decision, the way a
	// published tool's next version is. Muting or unmuting is the author's
	// at once: it only decides whether the approved text runs. An
	// administrator's own edit is its own approval.
	if !UserIsAdmin(owner) {
		live := all[at]
		changed := skillTextChanged(live, s)
		if live.Disabled != s.Disabled {
			live.Disabled = s.Disabled
			all[at] = live
			store.Set(deploymentSkillsTable, "all", all)
		}
		if !changed {
			// Back to the published text: an edit waiting on an
			// administrator is no longer asked for.
			withdrawSkillEdit(store, owner, live)
			return live, nil
		}
		return holdPublishedSkillEdit(store, owner, live, s, reason)
	}
	return writeDeploymentSkill(store, all, at, s, reason), nil
}

// writeDeploymentSkill replaces the published skill at index at with s,
// keeping the version it replaces in the skill's history.
func writeDeploymentSkill(store Database, all []SkillRecord, at int, s SkillRecord, reason string) SkillRecord {
	// Same ring as before it was published: owner and id do not change when a
	// skill moves pools, so its history is still the author's to read.
	if reason != revisions.NoHistory && revisions.Differs(all[at], s, "updated") {
		revisions.Push(store, revisions.KindSkill, skillRingKey(s.Owner, s.ID), all[at], all[at].Updated, reason)
	}
	all[at] = s
	store.Set(deploymentSkillsTable, "all", all)
	return s
}

// skillTextChanged is whether next changes what a published skill does, as
// opposed to whether it runs or when it was saved.
func skillTextChanged(prev, next SkillRecord) bool {
	return revisions.Differs(prev, next, "updated", "created", "disabled", "embedding", "awaiting_approval")
}

// skillRequestsTable holds the skill a publish or edit request asked for,
// keyed by the request's id: what approving it publishes, frozen when it was
// asked for, so an administrator approves the text they were shown and not
// whatever the skill says by the time they click.
const skillRequestsTable = "skill_requests"

type skillRequestSnapshot struct {
	Skill SkillRecord `json:"skill"`
	// Edits is the id of the published skill this request changes; empty for
	// a first publish.
	Edits  string    `json:"edits,omitempty"`
	Reason string    `json:"reason,omitempty"`
	At     time.Time `json:"at"`
}

// holdPublishedSkillEdit files an author's edit to their published skill as
// a request, under the published name, and hands back the published copy
// marked as waiting. A later edit replaces the pending one.
func holdPublishedSkillEdit(store Database, owner string, live, s SkillRecord, reason string) (SkillRecord, error) {
	key := PromotionRequestKey(SkillPromotionKind, owner, live.Name)
	s.Disabled = live.Disabled
	store.Set(skillRequestsTable, key, skillRequestSnapshot{Skill: s, Edits: live.ID, Reason: reason, At: time.Now()})
	note := "An edit to the published skill"
	if r := strings.TrimSpace(reason); r != "" && r != "update" {
		note += " (" + r + ")"
	}
	if err := CreatePromotionRequest(store, owner, SkillPromotionKind, live.Name, note); err != nil {
		return SkillRecord{}, err
	}
	live.AwaitingApproval = true
	return live, nil
}

// withdrawSkillEdit takes back owner's pending edit to the published skill
// live, if there is one.
func withdrawSkillEdit(store Database, owner string, live SkillRecord) {
	key := PromotionRequestKey(SkillPromotionKind, owner, live.Name)
	var snap skillRequestSnapshot
	if store.Get(skillRequestsTable, key, &snap) && snap.Edits == live.ID {
		promotion.WithdrawPromotionRequest(store, owner, SkillPromotionKind, live.Name)
		store.Unset(skillRequestsTable, key)
	}
}

// PublishedSkillDrafts is owner's published skills as they edit them: each
// one carrying the edit waiting on an administrator, when there is one, in
// place of the published text, and marked AwaitingApproval. Every surface
// the author edits from reads this, or a second edit made before the first
// is approved starts from the published text and drops the first. What
// everybody runs is DeploymentSkills; what the author published is
// PublishedSkillsBy.
func PublishedSkillDrafts(db Database, owner string) []SkillRecord {
	out := PublishedSkillsBy(db, owner)
	store := skillStore(db)
	if store == nil {
		return out
	}
	for i, p := range out {
		key := PromotionRequestKey(SkillPromotionKind, owner, p.Name)
		var snap skillRequestSnapshot
		if !store.Get(skillRequestsTable, key, &snap) || snap.Edits != p.ID ||
			!promotion.PendingPromotion(store, owner, SkillPromotionKind, p.Name) {
			continue
		}
		d := snap.Skill
		d.ID, d.Owner, d.Created, d.Disabled = p.ID, owner, p.Created, p.Disabled
		d.AwaitingApproval = true
		out[i] = d
	}
	return out
}

// snapshotSkillRequest is the request hook: a publish request freezes the
// author's skill as it is now. An edit to a published skill froze its own
// copy before filing (holdPublishedSkillEdit), so that one is left alone.
func snapshotSkillRequest(owner, name string) error {
	store := skillStore(nil)
	if store == nil {
		return nil
	}
	key := PromotionRequestKey(SkillPromotionKind, owner, name)
	for _, s := range LoadSkills(nil, owner) {
		if s.ID == name || strings.EqualFold(s.Name, name) {
			store.Set(skillRequestsTable, key, skillRequestSnapshot{Skill: s, At: time.Now()})
			return nil
		}
	}
	return nil
}

// RequestedSkill is what approving owner's pending skill request filed under
// name would publish, and the published version it replaces when it is an
// edit (ok false when there is no frozen request).
func RequestedSkill(owner, name string) (want SkillRecord, replaces *SkillRecord, ok bool) {
	store := skillStore(nil)
	if store == nil {
		return want, nil, false
	}
	var snap skillRequestSnapshot
	if !store.Get(skillRequestsTable, PromotionRequestKey(SkillPromotionKind, owner, name), &snap) {
		return want, nil, false
	}
	if snap.Edits != "" {
		for _, p := range allDeploymentSkills(nil) {
			if p.ID == snap.Edits && p.Owner == owner {
				c := p
				return snap.Skill, &c, true
			}
		}
	}
	return snap.Skill, nil, true
}

// approveSkillRequest is an administrator approving a skill request: the
// first publish of a skill, or an edit to one already published. It publishes
// what the request froze; a request from before requests froze one publishes
// the author's skill as it is.
func approveSkillRequest(owner, name string) error {
	store := skillStore(nil)
	if store == nil {
		return errString("no skill store")
	}
	key := PromotionRequestKey(SkillPromotionKind, owner, name)
	var snap skillRequestSnapshot
	if !store.Get(skillRequestsTable, key, &snap) {
		return promoteSkillToDeployment(owner, name, nil)
	}
	if snap.Edits == "" {
		if err := promoteSkillToDeployment(owner, snap.Skill.ID, &snap.Skill); err != nil {
			return err
		}
		store.Unset(skillRequestsTable, key)
		return nil
	}
	all := allDeploymentSkills(nil)
	at := -1
	for i := range all {
		if all[i].ID == snap.Edits && all[i].Owner == owner {
			at = i
			break
		}
	}
	if at < 0 {
		return errString(owner + " took " + name + " back since asking, so there is no published skill to change; deny this")
	}
	want := snap.Skill
	if !strings.EqualFold(all[at].Name, want.Name) {
		for i, o := range all {
			if i != at && strings.EqualFold(o.Name, want.Name) {
				return errString("the deployment already publishes a skill called " + want.Name)
			}
		}
	}
	want.ID, want.Owner, want.Created = all[at].ID, owner, all[at].Created
	want.Tools, want.AllowedUsers = nil, nil
	want.SharedFrom, want.SharedOmitted, want.AwaitingApproval = "", nil, false
	// Whether it runs stays the author's latest word, not the one at asking.
	want.Disabled = all[at].Disabled
	want.Updated = time.Now()
	reason := "approved edit"
	if snap.Reason != "" && snap.Reason != "update" {
		reason = "approved: " + snap.Reason
	}
	writeDeploymentSkill(store, all, at, want, reason)
	store.Unset(skillRequestsTable, key)
	Log("[skills] approved %q's edit to their published skill %q", owner, want.Name)
	return nil
}

// DefinitionDiff is what changes between two versions of a skill, field by
// field, for a reviewer: the same form a tool's update request shows.
func (s SkillRecord) DefinitionDiff(o SkillRecord) string {
	return fieldMapDiff(skillDefinitionFields(s), skillDefinitionFields(o))
}

func skillDefinitionFields(s SkillRecord) map[string]string {
	s.ID, s.Owner, s.Embedding = "", "", nil
	s.Created, s.Updated = time.Time{}, time.Time{}
	s.Tools, s.AllowedUsers, s.SharedFrom, s.SharedOmitted = nil, nil, "", nil
	s.Disabled, s.AwaitingApproval = false, false
	return jsonFields(s)
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
func promoteSkillToDeployment(owner, id string, frozen *SkillRecord) error {
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
	// What the request froze is what the administrator was shown, so that is
	// what goes out, under the skill it was taken from.
	if frozen != nil {
		c := *frozen
		c.ID, c.Created = found.ID, found.Created
		found = &c
	}
	// Muted ones too: enabling one later must not produce the clash this
	// refusal exists to prevent.
	for _, s := range allDeploymentSkills(nil) {
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

	existing := allDeploymentSkills(nil)
	store.Set(deploymentSkillsTable, "all", append(existing, published))
	dropSkillRecord(nil, owner, found.ID)
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
	// The stored pool, not the published list: a muted skill is still one its
	// author can take back, and the others written back below must include the
	// muted ones or taking this one back erases them.
	all := allDeploymentSkills(db)
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
	// Refused rather than landed beside a same-named skill of their own: two
	// of one person's skills under one name is a name that no longer picks a
	// skill, in their list and in every agent that names it. Which one should
	// give way is the author's call, so nothing here renames either.
	for _, s := range LoadSkills(db, owner) {
		if s.ID != taken.ID && strings.EqualFold(strings.TrimSpace(s.Name), strings.TrimSpace(taken.Name)) {
			return errString("you already have a skill called " + s.Name +
				" in your own skills; rename or delete that one before taking this one back, so the name has one answer")
		}
	}
	store.Set(deploymentSkillsTable, "all", rest)
	// An edit waiting on an administrator was an edit to the published
	// skill; there is none now, so it is not left in the queue to approve.
	promotion.WithdrawPromotionRequest(store, owner, SkillPromotionKind, taken.Name)
	store.Unset(skillRequestsTable, PromotionRequestKey(SkillPromotionKind, owner, taken.Name))
	taken.AllowedUsers = nil
	if _, err := SaveSkillAs(db, owner, *taken, "unpublished"); err != nil {
		// Put it back rather than losing it between two tables.
		store.Set(deploymentSkillsTable, "all", all)
		return err
	}
	Log("[skills] %q took %q back from the deployment", owner, taken.Name)
	// Everybody had it; the people whose agents actually named it are told.
	shareledger.WithdrawnFromEverybody("skill", owner, taken.ID, taken.Name)
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
	body := "It has moved out of your own skills and into the deployment's, where it activates on everybody's turns. It is still yours: your name is on it, and you can take it back at any time without asking. Your edits to it go to an administrator before they reach everybody, the way a published tool's next version does."
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
		// Muted ones included: muted is not unpublished.
		Published: func() []shareledger.Grant {
			var out []shareledger.Grant
			for _, s := range allDeploymentSkills(nil) {
				if s.Owner != "" {
					out = append(out, shareledger.Grant{ID: s.ID, Name: s.Name, Owner: s.Owner, Reach: "Deployment-wide"})
				}
			}
			return out
		},
		Unpublish: func(owner, id string) error { return NarrowSkillToOwner(nil, owner, id) },
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
					Reach:     "Shared with " + ShareRecipientsText(s.AllowedUsers),
					Detail:    skillShareDetail(s),
					Revocable: true,
				})
			}
			// A published one reaches everybody, and taking it back is the
			// author's — but through the skill's own door, not this one, since
			// un-publishing is not the same act as dropping one recipient.
			// Muted ones included: muting stops it activating, it does not
			// stop it being published, and this list is what the author has
			// out there.
			for _, s := range PublishedSkillsBy(nil, owner) {
				out = append(out, shareledger.Grant{
					ID: s.ID, Name: s.Name, Reach: "Deployment-wide", Wide: true,
					Detail: skillShareDetail(s),
				})
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

// recipientsLost is who a write takes off a recipient list: in before, not in
// after.
func recipientsLost(before, after []string) []string {
	var out []string
	for _, u := range before {
		if u = strings.TrimSpace(u); u != "" && !sliceHas(after, u) {
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

// moveSkillsTo gives to every skill from owns: their own list and the ones
// they published to the deployment. People shared with keep them, now under
// to. Kept versions do not move (a move carries what was built, not its
// history). Returns how many moved.
func moveSkillsTo(from, to string) int {
	store := skillStore(nil)
	if store == nil || from == "" || to == "" || from == to {
		return 0
	}
	n := 0
	if mine := LoadSkills(store, from); len(mine) > 0 {
		for i := range mine {
			mine[i].Owner = to
			peershare.MoveOwner(store, sharedSkillsTable, from, to, mine[i].ID)
			revisions.Delete(store, revisions.KindSkill, skillRingKey(from, mine[i].ID))
		}
		store.Set(skillsTable, to, append(LoadSkills(store, to), mine...))
		store.Unset(skillsTable, from)
		n += len(mine)
	}
	pool := allDeploymentSkills(store)
	moved := false
	for i := range pool {
		if pool[i].Owner == from {
			pool[i].Owner = to
			moved = true
			n++
		}
	}
	if moved {
		store.Set(deploymentSkillsTable, "all", pool)
	}
	return n
}

// purgeSkills deletes every skill owner has, published ones included, telling
// the people they were shared with. Returns how many went.
func purgeSkills(owner string) int {
	store := skillStore(nil)
	if store == nil || owner == "" {
		return 0
	}
	n := 0
	for _, s := range LoadSkills(store, owner) {
		if DeleteSkill(store, owner, s.ID) {
			n++
		}
	}
	pool := allDeploymentSkills(store)
	kept := pool[:0:0]
	for _, s := range pool {
		if s.Owner == owner {
			shareledger.WithdrawnFromEverybody("skill", owner, s.ID, s.Name)
			if chunksDB := skillChunksDB(owner); chunksDB != nil {
				WipeChunksBySourcePrefix(chunksDB, skillSource(s.ID))
			}
			n++
			continue
		}
		kept = append(kept, s)
	}
	if len(kept) != len(pool) {
		store.Set(deploymentSkillsTable, "all", kept)
	}
	store.Unset(skillsTable, owner)
	return n
}
