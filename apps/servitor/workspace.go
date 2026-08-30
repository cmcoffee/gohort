package servitor

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/bundle"
)

// A workspace is a master appliance whose configuration is a list of OTHER
// appliances — repos and live systems, mixed — plus any knowledge collections
// the operator linked. It owns no host, no clone, and no credentials. What it
// adds is a coordinator: one question fans out across the members that matter
// and the answers are stitched into a single cross-domain reply.
//
// The shape is scout-then-drill. Scouting is cheap and always runs: repos are
// already ingested so they can be searched directly, and a live system's
// accumulated map is read rather than dialed. Drilling — spawning a member's
// own investigator, with its own credentials, docs and worker — happens only
// where the scout found something, because a full investigation into a member
// with nothing to say is the entire cost of the question wasted.
//
// Members are never copied into the workspace. resolveAppliance runs each one
// in its OWNER's context, so a shared member keeps its own creds, clone and
// accumulated knowledge, and a member the caller can no longer see is skipped
// with a note instead of failing the question.

func init() {
	RegisterTunable(TunableSpec{
		Key:      "tune_servitor_workspace_drill_cap",
		Category: "Limits",
		Label:    "Workspace members drilled per question",
		Help:     "Maximum member appliances a single workspace question may investigate. Members beyond the cap are reported as skipped rather than silently dropped.",
		Kind:     KindInt,
		Default:  6,
		Min:      1,
		Max:      20,
	})
	RegisterTunable(TunableSpec{
		Key:      "tune_servitor_workspace_parallel",
		Category: "Limits",
		Label:    "Workspace cluster fan-out width",
		Help:     "How many cluster nodes a single investigate_cluster call queries at once. Higher finishes sooner; each node holds an SSH session and an LLM worker while it runs.",
		Kind:     KindInt,
		Default:  4,
		Min:      1,
		Max:      12,
	})
}

func workspace_drill_cap() int { return TuneInt("tune_servitor_workspace_drill_cap") }
func workspace_parallel() int  { return TuneInt("tune_servitor_workspace_parallel") }

// wsMember is one resolved member of a workspace, carrying the owner context
// its investigation must run in.
type wsMember struct {
	ID    string
	Rec   Appliance
	Owner string   // username whose store holds the record, clone and knowledge
	UDB   Database // the REQUESTING user's store — where their sessions live
}

// Name is the member's display label, falling back to its ID.
func (m wsMember) Name() string {
	if n := strings.TrimSpace(m.Rec.Name); n != "" {
		return n
	}
	return m.ID
}

// Kind collapses the appliance type into what the coordinator can DO with the
// member. It is NOT cosmetic: the lead routes on it, the scout picks its cheap
// pass from it, and the search tools refuse a member of the wrong kind. Folding
// every non-repo type into "system" told the lead that a log dump was a live
// host — which is wrong in the one direction that matters, since a live host can
// be re-queried and a dump cannot.
func (m wsMember) Kind() string {
	switch m.Rec.Type {
	case "repo":
		return "repo"
	case "bundle":
		return "evidence"
	case "toolset":
		return "service"
	default:
		return "system"
	}
}

// KindNote is a one-line description of what this kind of member IS, rendered
// into the roster. The lead cannot route well on a bare label: "evidence" only
// helps if it also knows the evidence is fixed and cannot be re-queried.
func (m wsMember) KindNote() string {
	switch m.Kind() {
	case "repo":
		return "ingested source code — searchable directly with search_code"
	case "evidence":
		return "an uploaded snapshot (logs, a dump). FIXED: it cannot be re-queried, so anything not captured is unobtainable rather than merely unknown"
	case "service":
		return "reached only through the tools bound to it — no shell, no filesystem"
	default:
		return "a live system, reachable and re-queryable"
	}
}

// Target is the member's concrete address — a host for a system, a git remote
// for a repo — so the lead can tell two similarly named members apart.
func (m wsMember) Target() string {
	switch m.Rec.Type {
	case "repo":
		return repoDisplayTarget(m.Rec)
	case "command":
		return m.Rec.Command
	case "bundle":
		return bundleDisplayTarget(m.Rec)
	case "toolset":
		return toolsetDisplayTarget(m.Rec)
	default:
		return m.Rec.Host
	}
}

// workspaceMembers resolves every member ID on the workspace record. Members
// that no longer resolve — deleted, or un-shared since the workspace was built
// — come back in missing so the coordinator can say so rather than pretending
// the workspace is smaller than the operator configured it.
func (T *Servitor) workspaceMembers(reqUser string, reqUDB Database, ws Appliance) (members []wsMember, missing []string) {
	for _, id := range ws.Members {
		rec, owner, _, ok := T.resolveAppliance(reqUser, reqUDB, id)
		if !ok || rec.ID == "" {
			missing = append(missing, id)
			continue
		}
		if rec.Type == "workspace" {
			// Workspaces don't nest: a member that is itself a coordinator has no
			// worker to dispatch, and allowing it invites a cycle.
			missing = append(missing, id+" (workspaces cannot be nested)")
			continue
		}
		members = append(members, wsMember{ID: id, Rec: rec, Owner: owner, UDB: reqUDB})
	}
	return members, missing
}

// ── Scout ────────────────────────────────────────────────────────────────────

// memberScout is what the cheap pass learned about one member: whether the
// question appears to touch it, and the evidence for that judgment.
//
// Role and Capability are NOT question-dependent. They describe what the member
// is, and they are rendered for every member on every question — otherwise the
// lead can only see members the question happened to mention, and has no way to
// know that a function lives on exactly one node of a cluster.
type memberScout struct {
	Member     wsMember
	Role       string          // operator-declared role, from the workspace record
	Capability string          // derived from the member's own accumulated map
	Score      int             // relative relevance; 0 means nothing matched
	Hits       []repoSearchHit // repo members: matching code lines
	Docs       []scoutDoc      // system members: knowledge docs that mention the question
	Facts      []string        // system members: stored facts that mention the question
	Note       string          // why this member scored nothing, when that's worth saying
}

// scoutDoc is a matching knowledge doc plus how old it is. Age travels with the
// match because the scout reads the accumulated map rather than the live system:
// a confident answer built on a map from two months ago describes how the system
// USED to work, and nothing else in the roster would reveal that.
type scoutDoc struct {
	Name string
	Age  string // "3 days ago", may carry "— STALE, re-verify"
}

// capability_lines is how many lines of a member's map are worth quoting as its
// standing capability summary. Enough to name the services that matter, short
// enough that a ten-member workspace does not bury the question.
const capability_lines = 6

// memberCapability derives a compact "what this member does" summary from the
// knowledge the member already accumulated — its services doc first, then apps,
// then the overview. Returns "" for a member that was never mapped.
//
// This is derived rather than declared, so it stays true as a system changes
// without anyone maintaining it. Its weakness is the mirror of that strength: it
// reports what was FOUND, so a stopped service or an unmapped box goes quiet.
// That is why an explicit MemberRoles entry outranks it in the roster.
func memberCapability(udb Database, applianceID string) string {
	if udb == nil || applianceID == "" {
		return ""
	}
	for _, doc := range []string{"services", "apps", "overview"} {
		if picked := extract_capability_lines(readDoc(udb, applianceID, doc)); len(picked) > 0 {
			return strings.Join(picked, "; ")
		}
	}
	return ""
}

// extract_capability_lines pulls the structural lines out of a knowledge doc —
// the bullets and headings that name services — and drops prose. An overview's
// paragraphs describe the host in sentences; its bullet list is the inventory,
// and only the inventory is useful as a routing hint.
func extract_capability_lines(content string) []string {
	var picked []string
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "*") && !strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimLeft(line, "-*# \t")
		line = strings.TrimSpace(strings.ReplaceAll(line, "**", ""))
		if line == "" || strings.HasPrefix(line, "[Last updated:") {
			continue
		}
		if len(line) > 110 {
			line = line[:110] + "…"
		}
		picked = append(picked, line)
		if len(picked) >= capability_lines {
			break
		}
	}
	return picked
}

// Relevant reports whether the drill should consider this member.
func (s memberScout) Relevant() bool { return s.Score > 0 }

// scout_terms reduces a question to the distinctive words worth searching for.
// Stop words are dropped because searchRepo is a substring match — scanning
// every repo for "the" finds everything and ranks nothing.
func scout_terms(question string) []string {
	stop := map[string]bool{
		"the": true, "and": true, "for": true, "are": true, "was": true, "were": true,
		"what": true, "why": true, "how": true, "when": true, "where": true, "which": true,
		"does": true, "did": true, "has": true, "have": true, "had": true, "can": true,
		"this": true, "that": true, "these": true, "those": true, "with": true, "from": true,
		"our": true, "its": true, "his": true, "her": true, "their": true, "any": true,
		"all": true, "not": true, "but": true, "into": true, "out": true, "you": true,
		"get": true, "got": true, "run": true, "running": true, "show": true, "tell": true,
		"see": true, "look": true, "find": true, "check": true, "please": true, "would": true,
		"should": true, "could": true, "there": true, "then": true, "than": true, "who": true,
	}
	seen := make(map[string]bool)
	var out []string
	for _, w := range strings.Fields(strings.ToLower(question)) {
		w = strings.Trim(w, ".,;:!?\"'()[]{}`<>")
		if len(w) < 3 || stop[w] || seen[w] {
			continue
		}
		seen[w] = true
		out = append(out, w)
		if len(out) >= 12 {
			break
		}
	}
	return out
}

// scoutWorkspace scores every member against the question without spending a
// worker on any of them. Repo members are searched directly (their code is
// already ingested); live systems are judged from the map we accumulated on
// previous visits, because a running host cannot be searched without dialing it
// — that's what the drill is for.
func (T *Servitor) scoutWorkspace(ws Appliance, members []wsMember, question string) []memberScout {
	terms := scout_terms(question)
	now := time.Now()
	out := make([]memberScout, 0, len(members))
	for _, m := range members {
		s := memberScout{Member: m, Role: strings.TrimSpace(ws.MemberRoles[m.ID])}
		if ownerUDB := UserDB(T.DB, m.Owner); ownerUDB != nil {
			s.Capability = memberCapability(ownerUDB, m.ID)
		}
		switch m.Kind() {
		case "repo":
			if repoFileCount(m.Owner, m.ID) == 0 {
				s.Note = "not ingested yet — run Refresh on this repo to make it searchable"
				break
			}
			seen := make(map[string]bool)
			for _, term := range terms {
				for _, h := range searchRepo(m.Owner, m.ID, term, 6) {
					key := fmt.Sprintf("%s:%d", h.Path, h.Line)
					if seen[key] {
						continue
					}
					seen[key] = true
					s.Hits = append(s.Hits, h)
					s.Score++
				}
				if len(s.Hits) >= 18 {
					break
				}
			}
		case "evidence":
			// The bundle analogue of the repo pass: search the evidence itself
			// rather than only what was recorded about it. Without this an
			// un-mapped bundle scouts as empty and the lead skips the one member
			// that actually holds the answer.
			if bundle.Open(m.Owner, m.ID).FileCount() == 0 {
				s.Note = "no evidence ingested yet — upload this bundle's files to make it searchable"
				break
			}
			seen := make(map[string]bool)
			for _, term := range terms {
				res, err := bundle.Open(m.Owner, m.ID).Search(bundle.Query{Pattern: regexpQuoteMeta(term), MaxHits: 6})
				if err != nil {
					continue
				}
				for _, h := range res.Hits {
					key := fmt.Sprintf("%s:%d", h.Path, h.Line)
					if seen[key] {
						continue
					}
					seen[key] = true
					s.Hits = append(s.Hits, repoSearchHit{Path: h.Path, Line: h.Line, Text: h.Text})
					s.Score++
				}
				if len(s.Hits) >= 18 {
					break
				}
			}
			if s.Score == 0 {
				s.Note = "the question's terms do not appear in this bundle's ingested text"
			}
		default:
			ownerUDB := UserDB(T.DB, m.Owner)
			if ownerUDB == nil {
				s.Note = "owner store unavailable"
				break
			}
			// A toolset's data lives behind its tools, so there is nothing local
			// to grep — the cheap pass can only read what was recorded about it.
			// Said plainly, because "nothing matched" on a service member means
			// something different than it does on a live host.
			docs := allDocs(ownerUDB, m.ID)
			for _, name := range knowledgeDocNames {
				c, age := readDocWithAge(ownerUDB, m.ID, name, now)
				if strings.TrimSpace(c) == "" || !refQueryMatch(c, question) {
					continue
				}
				s.Docs = append(s.Docs, scoutDoc{Name: name, Age: age})
				s.Score += 2
			}
			for _, f := range factsForAppliance(ownerUDB, m.ID) {
				if f.Key != "" && refQueryMatch(f.Key+" "+f.Value, question) {
					s.Facts = append(s.Facts, f.Key+": "+f.Value)
					s.Score++
				}
			}
			if s.Score == 0 && len(docs) == 0 {
				// Never mapped, so there is nothing to match against. That is the
				// opposite of "irrelevant" — the lead should know it's unexplored.
				s.Note = "never mapped — nothing known about this member yet"
				if m.Kind() == "service" {
					s.Note += "; its data is only reachable through its tools, so a dispatch is the ONLY way to find out"
				}
			}
		}
		out = append(out, s)
	}
	return out
}

// scoutBlock renders the scout for the lead: the full member roster, what each
// one is, and what the cheap pass already found. Members that scored nothing
// stay listed — "nothing matched" is information the lead needs to decide
// whether to drill anyway.
func scoutBlock(scouts []memberScout, missing []string) string {
	return scoutBlockFor(Appliance{}, scouts, missing)
}

// scoutBlockFor renders the roster, including the workspace's declared member
// links. Split from scoutBlock so the links — which live on the WORKSPACE record
// rather than on any member — can be rendered without threading the record
// through every caller that only wants the plain roster.
func scoutBlockFor(ws Appliance, scouts []memberScout, missing []string) string {
	var b strings.Builder
	b.WriteString("## Members\n\n")
	b.WriteString("Members are NOT interchangeable. A function often lives on exactly one of them. Read **Role** and **Known to run** before dispatching: they tell you where a thing lives, independently of this question.\n\n")
	b.WriteString("- **Role** is what the operator says the member is FOR. Trust it for routing even when the member's map does not currently show the service — a stopped service or an unmapped box still belongs to the node that owns it.\n")
	b.WriteString("- **Known to run** is derived from what was actually found on that member last time it was mapped. Trust it for detail, not for completeness.\n")
	b.WriteString("- A `[last updated: …]` older than a few weeks means you are reading how the system USED to work. Say so, or re-verify with a dispatch, before stating it as current.\n\n")
	for _, s := range scouts {
		m := s.Member
		fmt.Fprintf(&b, "### %s — `%s`\n", m.Name(), m.ID)
		fmt.Fprintf(&b, "- Kind: %s", m.Kind())
		if t := m.Target(); t != "" {
			fmt.Fprintf(&b, " (%s)", t)
		}
		fmt.Fprintf(&b, " — %s\n", m.KindNote())
		// Role and capability are printed for EVERY member on EVERY question.
		// This is what the lead routes on: without it, a function that lives on
		// exactly one node is invisible unless the question happened to name it.
		if s.Role != "" {
			fmt.Fprintf(&b, "- **Role: %s**\n", s.Role)
		}
		if s.Capability != "" {
			fmt.Fprintf(&b, "- Known to run: %s\n", s.Capability)
		}
		// Declared relations to other members. Printed for every member on every
		// question, like Role: a link is what makes a cross-member correlation
		// meaningful rather than a coincidence, and the lead cannot use one it
		// was never told about.
		if lines := memberLinkLines(ws, m.ID, func(id string) string { return memberDisplayName(scouts, id) }); len(lines) > 0 {
			fmt.Fprintf(&b, "- Linked: %s\n", strings.Join(lines, "; "))
		}
		if s.Role == "" && s.Capability == "" && s.Note == "" {
			b.WriteString("- Role not declared and nothing mapped yet — ask this member directly if the question might involve it.\n")
		}
		if s.Note != "" {
			fmt.Fprintf(&b, "- Status: %s\n", s.Note)
		}
		switch {
		case len(s.Hits) > 0:
			label := "Code matches"
			if m.Kind() == "evidence" {
				label = "Log matches"
			}
			fmt.Fprintf(&b, "- %s (%d):\n", label, len(s.Hits))
			for i, h := range s.Hits {
				if i >= 8 {
					fmt.Fprintf(&b, "  - …and %d more\n", len(s.Hits)-8)
					break
				}
				fmt.Fprintf(&b, "  - `%s:%d` — %s\n", h.Path, h.Line, h.Text)
			}
		case len(s.Docs) > 0 || len(s.Facts) > 0:
			if len(s.Docs) > 0 {
				b.WriteString("- Its map mentions this:\n")
				for _, d := range s.Docs {
					if d.Age != "" {
						fmt.Fprintf(&b, "  - `%s` [last updated: %s]\n", d.Name, d.Age)
						continue
					}
					fmt.Fprintf(&b, "  - `%s`\n", d.Name)
				}
			}
			for i, f := range s.Facts {
				if i >= 6 {
					fmt.Fprintf(&b, "  - …and %d more facts\n", len(s.Facts)-6)
					break
				}
				fmt.Fprintf(&b, "  - %s\n", f)
			}
		case s.Note == "":
			b.WriteString("- Nothing in the cheap pass matched this member.\n")
		}
		b.WriteString("\n")
	}
	if len(missing) > 0 {
		fmt.Fprintf(&b, "## Unavailable members\n\nThese are configured on the workspace but could not be resolved — they may have been deleted or un-shared. Say so if the question depends on them: %s\n\n", strings.Join(missing, ", "))
	}
	return b.String()
}

// ── Divergence ───────────────────────────────────────────────────────────────

// concrete_value matches the kinds of token that make two nodes' answers
// comparable: IPv4 addresses (with optional port), absolute paths, and dotted
// version numbers. Prose differs freely between nodes even when the systems are
// identical; these do not.
var concrete_value = regexp.MustCompile(`\b\d{1,3}(?:\.\d{1,3}){3}(?::\d+)?\b|/[A-Za-z0-9._/-]{3,}|\bv?\d+\.\d+(?:\.\d+)+\b`)

// extract_values pulls the comparable tokens out of one node's findings.
func extract_values(text string) map[string]bool {
	out := make(map[string]bool)
	for _, v := range concrete_value.FindAllString(text, -1) {
		v = strings.Trim(v, ".,;:)]}")
		if len(v) >= 4 {
			out[v] = true
		}
	}
	return out
}

// divergence_report compares what each node reported and surfaces the values
// that did NOT appear everywhere. This is the point of asking a cluster one
// question: three separate answers are three answers, but "node-3 is the only
// one without this path" is a finding.
//
// It is deliberately framed as a lead for the coordinator to verify, not a
// conclusion. A value missing from a node's REPORT means that node's worker
// didn't mention it, which is not the same as the value being absent from the
// node — two workers can investigate the same box and write it up differently.
func divergence_report(perNode map[string]string) string {
	if len(perNode) < 2 {
		return ""
	}
	nodes := make([]string, 0, len(perNode))
	for n := range perNode {
		nodes = append(nodes, n)
	}
	sort.Strings(nodes)

	values := make(map[string][]string) // value -> nodes that mentioned it
	for _, n := range nodes {
		for v := range extract_values(perNode[n]) {
			values[v] = append(values[v], n)
		}
	}
	type diff struct {
		value   string
		present []string
		absent  []string
	}
	var diffs []diff
	shared := 0
	for v, present := range values {
		if len(present) == len(nodes) {
			shared++
			continue
		}
		sort.Strings(present)
		var absent []string
		for _, n := range nodes {
			found := false
			for _, p := range present {
				if p == n {
					found = true
					break
				}
			}
			if !found {
				absent = append(absent, n)
			}
		}
		diffs = append(diffs, diff{value: v, present: present, absent: absent})
	}
	if len(diffs) == 0 {
		return fmt.Sprintf("\n---\n\n**Cross-node comparison:** every concrete value (%d) appeared in all %d reports — no divergence detected.\n", shared, len(nodes))
	}
	// Fewest-nodes-present first: a value on one node out of three is a stronger
	// signal than one missing from a single node.
	sort.Slice(diffs, func(i, j int) bool {
		if len(diffs[i].present) != len(diffs[j].present) {
			return len(diffs[i].present) < len(diffs[j].present)
		}
		return diffs[i].value < diffs[j].value
	})
	var b strings.Builder
	fmt.Fprintf(&b, "\n---\n\n**Cross-node comparison** — %d value(s) appeared in every report; the following did not:\n\n", shared)
	for i, d := range diffs {
		if i >= 25 {
			fmt.Fprintf(&b, "- …and %d more differing value(s).\n", len(diffs)-25)
			break
		}
		fmt.Fprintf(&b, "- `%s` — reported by %s; NOT reported by %s\n", d.value, strings.Join(d.present, ", "), strings.Join(d.absent, ", "))
	}
	b.WriteString("\nThese are LEADS, not conclusions: a value missing from a report means that node's worker did not mention it, which is not proof the node lacks it. Verify any difference that matters with a targeted follow-up before stating it as fact.\n")
	return b.String()
}

// regexpQuoteMeta escapes a scout term for the bundle search, which takes a
// regular expression. Scout terms come from the user's question, so an
// unescaped "(" or "*" would either error or match something nobody asked for —
// at scout time that reads as "this member has nothing", which is the one
// conclusion the cheap pass must never reach by accident.
func regexpQuoteMeta(s string) string { return regexp.QuoteMeta(s) }

// ── Member links ─────────────────────────────────────────────────────────────

// Relations a member link may declare. Deliberately few and concrete: a
// free-text relation would be unroutable, and the point of declaring one is that
// the coordinator can act on it.
const (
	MemberRelRuns         = "runs"          // this system runs that code
	MemberRelCodeFor      = "code-for"      // this repo/service is that system's source
	MemberRelCapturedFrom = "captured-from" // this evidence came off that system
	MemberRelTalksTo      = "talks-to"      // this system calls that service
)

// MemberLink is one declared relation between two members of a workspace.
type MemberLink struct {
	From string `json:"from"` // member appliance id
	Rel  string `json:"rel"`
	To   string `json:"to"` // member appliance id
}

// memberRelLabel renders a relation for the roster, in the direction it reads.
func memberRelLabel(rel string) string {
	switch rel {
	case MemberRelRuns:
		return "runs the code in"
	case MemberRelCodeFor:
		return "is the code for"
	case MemberRelCapturedFrom:
		return "was captured from"
	case MemberRelTalksTo:
		return "talks to"
	}
	return strings.ReplaceAll(rel, "-", " ")
}

// memberRelInverse renders the same relation read from the other end, so the
// roster can state a link on BOTH members. A link declared once should not have
// to be read from only one side — the coordinator arrives at a member from
// whichever direction the question came.
func memberRelInverse(rel string) string {
	switch rel {
	case MemberRelRuns:
		return "its code runs on"
	case MemberRelCodeFor:
		return "its code is in"
	case MemberRelCapturedFrom:
		return "evidence was captured from it into"
	case MemberRelTalksTo:
		return "is called by"
	}
	return "related to"
}

// validMemberRel reports whether rel is one this code can act on.
func validMemberRel(rel string) bool {
	switch strings.TrimSpace(rel) {
	case MemberRelRuns, MemberRelCodeFor, MemberRelCapturedFrom, MemberRelTalksTo:
		return true
	}
	return false
}

// pruneMemberLinks drops links whose endpoints are not both current members,
// whose relation is unknown, or which point a member at itself. Same discipline
// as pruneMemberRoles: unchecking a member must not leave a link behind that
// resurfaces if it is re-added.
func pruneMemberLinks(links []MemberLink, members []string) []MemberLink {
	if len(links) == 0 {
		return nil
	}
	keep := make(map[string]bool, len(members))
	for _, id := range members {
		keep[id] = true
	}
	seen := map[string]bool{}
	out := make([]MemberLink, 0, len(links))
	for _, l := range links {
		l.From, l.Rel, l.To = strings.TrimSpace(l.From), strings.TrimSpace(l.Rel), strings.TrimSpace(l.To)
		if l.From == "" || l.To == "" || l.From == l.To {
			continue
		}
		if !keep[l.From] || !keep[l.To] || !validMemberRel(l.Rel) {
			continue
		}
		key := l.From + "\x00" + l.Rel + "\x00" + l.To
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, l)
	}
	if len(out) == 0 {
		return nil
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].From != out[j].From {
			return out[i].From < out[j].From
		}
		return out[i].Rel < out[j].Rel
	})
	return out
}

// memberLinkLines renders every link touching one member, from that member's
// point of view, for its roster entry.
func memberLinkLines(ws Appliance, memberID string, name func(string) string) []string {
	var out []string
	for _, l := range ws.MemberLinks {
		switch memberID {
		case l.From:
			out = append(out, memberRelLabel(l.Rel)+" "+name(l.To))
		case l.To:
			out = append(out, memberRelInverse(l.Rel)+" "+name(l.From))
		}
	}
	sort.Strings(out)
	return out
}

// memberDisplayName resolves a member id to its roster name, falling back to the
// id so a link pointing at something unresolvable still reads as something.
func memberDisplayName(scouts []memberScout, id string) string {
	for _, s := range scouts {
		if s.Member.ID == id {
			return s.Member.Name()
		}
	}
	return id
}
