package orchestrate

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/ui"
)

// availableWorkerToolOptions returns the worker tool catalog as
// SelectOptions for the Tools modal in the agent editor / chat
// toolbar.
//
// Sources merged:
//
//  1. Globally-registered ChatTools minus BlockedTools, filtered to
//     capabilities {Read, Network}.
//  2. The given user's persistent temp tools — surfaces user-defined
//     tools (vapi-style API wrappers, scripts, etc.) so admin groups
//     that include them produce visible section headers in the modal.
//     Empty user → no temp tools surfaced (e.g. unauthenticated
//     preview path).
//
// keep_going is dropped because the agent loop auto-loads it; users
// shouldn't have to remember to enable it, and unchecking it would
// just silently get re-added.
//
// Sorting: capability buckets first (Network, Network+Read, Read,
// Other), then toolbox sections alphabetically. Within each section,
// tools sort by name. Tied priority breaks on group name so distinct
// toolbox sections never interleave.
// frameworkInfrastructureTools lists names of tools the framework
// auto-includes turn-bound (knowledge_save / store_fact / etc.)
// that aren't tagged via the FrameworkTool interface — typically
// because they're per-turn closures built by chatTurn rather than
// global registry entries. Most of these aren't in
// RegisteredChatTools() at all, so the picker doesn't see them
// anyway; this map is defense in depth for the few that ARE
// globally registered but framework-managed via different paths.
//
// New framework tools should implement the FrameworkTool
// interface (IsFrameworkTool() bool) rather than being added
// here — that hides them from every picker in one shot.
var frameworkInfrastructureTools = map[string]bool{
	"knowledge_save":     true,
	"knowledge_search":   true,
	"forget_knowledge":   true,
	"store_fact":         true,
	"forget_fact":        true,
	"list_facts":         true,
	"agents":             true,
	"present_build_plan": true,
	"mark_step_done":     true,
}

// frameworkUtilityTools are pure, deterministic, side-effect-free helpers
// (CapRead) the framework keeps ALWAYS-ON for every agent — arithmetic,
// date math, timezone conversion. There's no reason to ever withhold them
// (and the calculator in particular guards against the LLM's unreliable
// mental math), so they're force-included into the catalog (see runner.go,
// next to workspace) and hidden from the curation UI / default pool — the
// admin never manages them, but the model always has them. Keep this set to
// genuinely pure utilities; anything touching network/state stays curated.
var frameworkUtilityTools = []string{"calculate", "date_math", "time_in_zone", "parse_xml"}

// supersededWorkerTools are standalone registered tools whose function is
// fully covered by a grouped tool, so showing both just bloats the schema
// and invites LLM oscillation between near-duplicates. find_video /
// view_video / download_video / transcribe are all subsumed by the
// `video` action-grouped tool (find | download | view | transcribe |
// transcode). They stay REGISTERED — phantom (its own surface) and any
// agent that explicitly allowlists one keep working — but are dropped
// from orchestrate's default pool + curation picker so a default-pool
// agent sees one `video` tool instead of five near-duplicates.
var supersededWorkerTools = []string{
	// video family → the `video` grouped tool
	"find_video", "view_video", "download_video", "transcribe",
	// image family → the `image` grouped tool
	"find_image", "fetch_image", "generate_image",
}

// isSupersededWorkerTool covers the fixed list above plus the per-connector
// image backends, which can't be listed statically — every approved rest_image
// connector materializes its own generate_image_<name> tool, and `image` now
// reaches all of them through its `backend` param. Ten connectors used to mean
// ten near-identical tools in the picker.
//
// They stay REGISTERED and grantable: an agent already allowlisted onto one
// specific backend keeps exactly that access, which is what makes the collapse
// non-breaking. This only drops them from the default pool and the picker.
func isSupersededWorkerTool(name string) bool {
	return slices.Contains(supersededWorkerTools, name) ||
		strings.HasPrefix(name, RestImageToolPrefix)
}

func availableWorkerToolOptions(user string) []ui.SelectOption {
	// AuthDB is a hook the binary installs at start-up, so it is nil
	// wherever this app is exercised without one. Every other caller in
	// core nil-checks it and this function did not, which turned "list
	// the names a phase could legitimately name" into a segfault the
	// moment anything but the web editor asked. Both uses below are
	// ADDITIONS to the registered tools gathered here — the shared
	// temp-tool pool, and the group labels — so a nil store degrades to
	// a shorter, plainer list rather than a wrong one.
	var authDB Database
	if AuthDB != nil {
		authDB = AuthDB()
	}

	pool := FilterChatTools(BlockedTools)
	defs := make([]AgentToolDef, 0, len(pool))
	for _, t := range pool {
		// Framework-tagged tools hide via the interface — single
		// source of truth across every picker in the codebase.
		if IsFrameworkTool(t) {
			continue
		}
		if frameworkInfrastructureTools[t.Name()] {
			continue
		}
		if slices.Contains(frameworkUtilityTools, t.Name()) {
			continue // always-on utilities — never curated, hidden from the picker + default pool
		}
		if isSupersededWorkerTool(t.Name()) {
			continue // covered by a grouped tool (image/video) — registered but out of the default pool
		}
		defs = append(defs, ChatToolToAgentToolDefWithSession(t, nil))
	}
	defs = FilterToolsByCaps(defs, []Capability{CapRead, CapNetwork})

	// Surface this user's persistent temp tools too — admin groups
	// often target these (vapi wrappers, etc.) and the modal needs
	// to see them for the group's section header to appear. Caps
	// gating doesn't apply here (these are admin-approved tools the
	// user already has access to at runtime).
	if user != "" {
		// Shared rows only — agent-scoped rows belong to specific agents' kits
		// and must not surface as user-wide picker options.
		for _, p := range SharedUserTools(authDB, user) {
			// Carry the tool's self-declared Category so it groups under that
			// header; default the user's OWN Builder-authored tools to a clear
			// "My Tools" group instead of the generic "Other" bucket, where they
			// were easy to miss when enabling them on an agent.
			cat := p.Tool.Category
			if cat == "" {
				cat = "My Tools"
			}
			defs = append(defs, AgentToolDef{
				Tool: Tool{
					Name:        p.Tool.Name,
					Description: p.Tool.Description,
					Category:    cat,
				},
			})
		}
		// Client-bridge tools (from_client.*) are NOT surfaced here.
		// The runtime always includes them in the catalog whenever
		// the user has registered a desktop (see LocalToolsForUser
		// + resolveWorkerTools) and the desktop's per-call approval
		// modal is the real enforcement point. A per-agent checkbox
		// here would create a confusing UX: the LLM would see the
		// tool in the catalog regardless of the toggle state. If a
		// user wants to disable the bridge surface entirely, they
		// close gohort-desktop; per-tool gating belongs on the
		// desktop side (the approval modal).
	}

	// Use group membership to drive the section header so admins see
	// toolbox organization without losing per-tool toggle granularity.
	// A tool in agent_management_toolbox shows up under the "Agent
	// Management" header; tools in no group fall back to their
	// capability label ("Network", "Read", …).
	memberToGroup := map[string]string{}
	for _, g := range LoadToolGroups(authDB) {
		for _, m := range g.Members {
			if _, already := memberToGroup[m]; already {
				continue // first group wins on overlap
			}
			memberToGroup[m] = g.Name
		}
	}

	opts := make([]ui.SelectOption, 0, len(defs))
	for _, d := range defs {
		if d.Tool.Name == "keep_going" {
			continue
		}
		// Grouping precedence: the category the tool CLAIMS for itself wins
		// (self-declared, ownership-scoped), then the legacy ToolGroup.Members
		// mapping (admin-curated), then the capability label as a last resort.
		group := capGroupLabel(d.Tool.Caps)
		if gName, ok := memberToGroup[d.Tool.Name]; ok {
			group = gName
		}
		if d.Tool.Category != "" {
			group = d.Tool.Category
		}
		opts = append(opts, ui.SelectOption{
			Value: d.Tool.Name,
			Label: d.Tool.Name,
			Help:  firstLine(d.Tool.Description),
			Group: group,
		})
	}
	sort.Slice(opts, func(i, j int) bool {
		if opts[i].Group != opts[j].Group {
			oi := capGroupOrder(opts[i].Group)
			oj := capGroupOrder(opts[j].Group)
			if oi != oj {
				return oi < oj
			}
			// Same priority bucket (e.g. multiple toolbox names all
			// returning the same constant) — break the tie by group
			// name so the comparator obeys strict ordering. Without
			// this, sort.Slice sees equal priority for distinct group
			// strings and interleaves the rows.
			return opts[i].Group < opts[j].Group
		}
		return opts[i].Value < opts[j].Value
	})
	return opts
}

// availableWorkerToolNames returns just the names from the default
// pool — REGISTERED ChatTool names only, excluding persistent temp
// tools (which the modal displays via availableWorkerToolOptions but
// which the runner must NOT route through GetAgentToolsWithSession,
// since that lookup only knows about registered tools). Temp tools
// reach the runner separately, via temptool.BuildAgentToolDefs in
// runPlan/runWorkerStep. Passing user="" skips the temp-tool merge
// the options function would do — we don't want those names here.
func availableWorkerToolNames() []string {
	// Pass user="" so the options builder skips the temp-tool merge.
	// The result is purely the registered ChatTool surface filtered
	// by caps + blocklist, which is exactly what the runner's
	// AllowedTools intersection should match against.
	opts := availableWorkerToolOptions("")
	names := make([]string, 0, len(opts))
	for _, o := range opts {
		names = append(names, o.Value)
	}
	return names
}

// internetWorkerToolNames returns the names of worker tools that
// contact the network (implement InternetTool with IsInternetTool()
// returning true). Used by the chat page to ship a small filter list
// to the browser so the Tools button count can subtract these when
// Private mode is enabled — matching the runtime filter the agent
// loop applies in private mode (see resolveWorkerTools in runner.go).
func internetWorkerToolNames() []string {
	pool := FilterChatTools(BlockedTools)
	var out []string
	for _, t := range pool {
		if it, ok := t.(InternetTool); ok && it.IsInternetTool() {
			out = append(out, t.Name())
		}
	}
	sort.Strings(out)
	return out
}

// capGroupLabel maps a tool's capability set to a human-readable
// checklist group header. Tools with no caps fall under "Other" so
// they don't get silently bucketed with Network tools.
func capGroupLabel(caps []Capability) string {
	hasRead, hasNet := false, false
	for _, c := range caps {
		switch c {
		case CapRead:
			hasRead = true
		case CapNetwork:
			hasNet = true
		}
	}
	switch {
	case hasNet && hasRead:
		return "Network + Read"
	case hasNet:
		return "Network"
	case hasRead:
		return "Read"
	default:
		return "Other"
	}
}

// capGroupOrder sorts section headers in the checklist. Capability
// groups land first in their familiar order (Network, Network+Read,
// Read, Other); anything else — i.e. a toolbox display name — sorts
// alphabetically below them by returning a high constant. Toolbox
// sections then cluster together, ordered by name within the
// alpha-sort below.
func capGroupOrder(g string) int {
	switch g {
	case "Network":
		return 0
	case "Network + Read":
		return 1
	case "Read":
		return 2
	case "Other":
		return 3
	default:
		// Toolbox display name — sort after the capability groups.
		return 100
	}
}

// firstLine returns the first non-empty line of s, clipped to keep
// the checklist help text scannable. Tool descriptions sometimes run
// for paragraphs — we only want the lede.
func firstLine(s string) string {
	for _, ln := range strings.Split(s, "\n") {
		ln = strings.TrimSpace(ln)
		if ln == "" {
			continue
		}
		if len(ln) > 140 {
			ln = ln[:140] + "…"
		}
		return ln
	}
	return ""
}

// buildAttachedSourceToolDefs mints the per-source tools for every reference
// source attached to this agent.
//
// Mirrors buildAttachedPipelineToolDefs: curated by the owner, small in number,
// so the tools go in directly rather than behind a lazy load_tool. Resolution
// runs through core.ReferenceItemTools, the same call the writer apps make, so
// an agent and a guide asking the same system a question get identical tools
// rather than two implementations that drift.
//
// A selection whose source is no longer registered — the app was removed, the
// item deleted — contributes nothing and is logged rather than erroring the
// turn: an agent that cannot start because one of five attachments went missing
// is worse than one that runs with four.
//
// sess is the turn's session and is passed straight through: a source
// whose tools dispatch their own sub-run (servitor's investigate_<system>)
// roots it on sess.Context(), so stopping this turn stops the work it sent
// out. Without it the sub-run is detached and a cancel reaches the agent
// but not the investigation it started.
func (t *chatTurn) buildAttachedSourceToolDefs(sess *ToolSession) []AgentToolDef {
	if t == nil || len(t.agent.AttachedSources) == 0 {
		return nil
	}
	var out []AgentToolDef
	seen := map[string]bool{}
	for _, ref := range t.agent.AttachedSources {
		kind, item := strings.TrimSpace(ref.Kind), strings.TrimSpace(ref.ItemID)
		if kind == "" || item == "" {
			continue
		}
		defs := ReferenceItemToolsWithSession(sess, t.user, kind, item)
		if len(defs) == 0 {
			// A BREADCRUMB, not just a server log. The attachment is a
			// frozen {kind, item_id}; when that id stops resolving — the
			// item was removed, unshared, or re-created on the far side of
			// a peer — ItemTools returns nil by contract and the agent
			// simply has no investigate_/search_ tool for it. Nothing said
			// so anywhere the person could see, so the tool "vanished" and
			// the only cure anybody found was detach-and-reattach, which
			// rewrites the id.
			//
			// Reported live once, hit for real: a change to a remote
			// peered system left the attachment pointing at an item that
			// no longer resolved, and the log line below was the only
			// record of it.
			Log("[orchestrate.tools] agent=%s: attached source %s/%s resolved to no tools (removed or no longer shared?)",
				t.agent.ID, kind, item)
			t.turnDiag("attached_source_empty", "the attached source "+kind+"/"+item+
				" gave this turn no tools — it was removed, is no longer shared, or was re-created with a "+
				"different id on the far side. Detach and re-attach it (Configure → Sources) to point at "+
				"what is there now.")
			continue
		}
		for _, d := range defs {
			// Two attachments can mint the same tool name — the same system
			// attached twice, or two items whose names slug identically. First
			// wins, because the alternative is a catalog with a duplicate name
			// where which one runs is undefined.
			if seen[d.Tool.Name] {
				continue
			}
			seen[d.Tool.Name] = true
			out = append(out, d)
		}
	}
	return out
}

// referenceSelectionsFromArgs parses the "<kind>:<item_id>" strings the agent
// CRUD tools accept into selections.
//
// A flat string list rather than an array of objects because that is the shape
// models reliably produce, and the id half can itself contain colons (a UUID
// will not, a future kind's id might), so the split is on the FIRST colon only.
// Anything without a colon is dropped rather than guessed at: inventing a kind
// would attach the agent to a source nobody chose.
func referenceSelectionsFromArgs(args map[string]any, key string) []ReferenceSelection {
	var out []ReferenceSelection
	seen := map[string]bool{}
	for _, raw := range stringSliceFromArgs(args, key) {
		kind, item, found := strings.Cut(strings.TrimSpace(raw), ":")
		kind, item = strings.TrimSpace(kind), strings.TrimSpace(item)
		if !found || kind == "" || item == "" {
			Log("[orchestrate.agents] ignoring attached source %q — expected \"<kind>:<item_id>\"", raw)
			continue
		}
		if seen[kind+":"+item] {
			continue
		}
		seen[kind+":"+item] = true
		out = append(out, ReferenceSelection{Kind: kind, ItemID: item})
	}
	return out
}

// listReferenceSourcesToolDef lets the Builder discover what an agent can be
// attached to. Without it, attached_sources is a parameter whose valid values
// are undiscoverable — the Builder would have to be told the ids by the user,
// which defeats the point of it doing the wiring.
func listReferenceSourcesToolDef(user string) AgentToolDef {
	return AgentToolDef{
		Tool: Tool{
			Name: "list_reference_sources",
			Description: "List the cross-app knowledge sources an agent can be attached to (attached_sources) — servitor systems, evidence bundles, tool-backed services, whole servitor workspaces, registered file-store folders, connected document spaces. " +
				"Returns each source's kind and its items with ids, ready to pass as \"<kind>:<item_id>\". " +
				"Attaching one gives the agent named tools for it, shaped by the source: instant search over what has already been gathered, its recorded facts and a live read-only investigation for a system; list/search/read for a folder of files. No arguments.",
		},
		Handler: func(map[string]any) (string, error) { return renderReferenceSources(user), nil },
	}
}

// renderReferenceSources is the tool body, split out so it is testable without a
// session.
func renderReferenceSources(user string) string {
	groups := ReferenceGroups(user)
	if len(groups) == 0 {
		return "No reference sources are available to this user. Servitor systems appear once appliances exist; file stores once an admin registers a folder (and the user is allowed to reach it); document spaces once connected."
	}
	var b strings.Builder
	b.WriteString("Attachable reference sources. Pass these as attached_sources entries in the form \"<kind>:<item_id>\":\n\n")
	for _, g := range groups {
		fmt.Fprintf(&b, "## %s (kind: %s)\n", g.Label, g.Kind)
		for _, it := range g.Items {
			fmt.Fprintf(&b, "- %s:%s — %s", g.Kind, it.ID, it.Name)
			if strings.TrimSpace(it.Desc) != "" {
				fmt.Fprintf(&b, " (%s)", it.Desc)
			}
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}
	return b.String()
}

// attachedSourceToolOptions lists the tools an attached reference source
// contributes, as options a phase's / stage's Tools list may name.
//
// These names exist NOWHERE in availableWorkerToolOptions: an attached
// source's tools are minted per agent at turn time (see
// buildAttachedSourceToolDefs) and are in no global registry, so every
// picker that offers "the tools a step may name" was offering a catalog
// that omitted them — while the runtime filter those lists feed
// (narrowCatalog) ran against a catalog that HAS them. A phase that named
// any tool therefore stripped every attached source from the turn, and
// the author had no way to put them back: the name they needed was not
// on the list they were ticking.
//
// Reported live: an agent attached to a file store, standing in a phase
// with a Tools list, said its "Diagnostic logs" tools were not in its
// catalog — correctly, and for a reason no surface stated.
//
// Grouped by source label, and the ITEM is named in the help rather than
// the group, because two stores mint three tools each and what the author
// needs to see is which store a name belongs to.
//
// Offered whether or not any given agent is attached to the item: a
// machine is portable and is edited without an agent in hand, and the
// runtime narrowing is a name match either way. An unattached name simply
// matches nothing, which is the same cost as any other unticked box.
func attachedSourceToolOptions(user string) []ui.SelectOption {
	if strings.TrimSpace(user) == "" {
		return nil
	}
	var out []ui.SelectOption
	seen := map[string]bool{}
	for _, g := range ReferenceGroups(user) {
		for _, it := range g.Items {
			for _, td := range ReferenceItemTools(user, g.Kind, it.ID) {
				name := td.Tool.Name
				if name == "" || seen[name] {
					continue
				}
				seen[name] = true
				// Grouped per ITEM, not per source kind. One header for
				// "File stores" puts three stores' nine tools in one
				// undifferentiated run, and which store a name belongs to
				// is the only thing the author is actually choosing by.
				out = append(out, ui.SelectOption{
					Value: name, Label: name,
					Group: g.Label + " · " + chFirst(it.Name, it.ID),
					Help:  firstLine(td.Tool.Description),
				})
			}
		}
	}
	return out
}

// phaseToolOptions is the pool a PHASE or a pipeline STAGE may narrow to:
// the worker catalog plus what this user's attached sources mint.
//
// Distinct from availableWorkerToolOptions, which the AGENT editor uses.
// The two lists answer different questions: the agent's list decides what
// the agent CARRIES (attached sources are chosen in the Sources picker,
// not there), while a phase's list decides what it may REACH out of
// whatever the turn assembled — and that assembly includes its sources.
func phaseToolOptions(user string) []ui.SelectOption {
	return append(availableWorkerToolOptions(user), attachedSourceToolOptions(user)...)
}
