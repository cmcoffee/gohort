package servitor

import (
	"encoding/json"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

const applianceTable = "ssh_appliances"

// LogEntry describes a single log file discovered on the remote appliance.
type LogEntry struct {
	Service string `json:"service"`
	Path    string `json:"path"`
	Desc    string `json:"desc"`
}

// Appliance is a saved remote host with connection params and cached system knowledge.
type Appliance struct {
	ID   string `json:"id"`
	Type string `json:"type"` // "ssh" (default) | "command" | "repo" | "bundle" | "toolset" | "workspace"
	Name string `json:"name"`
	// SSH fields
	Host     string `json:"host"`
	Port     int    `json:"port"`
	User     string `json:"user"`
	Password string `json:"password"`
	// ToolsOnly withholds the ASKING tools for this target: a connected
	// agent gets its approved minted tools and nothing else. Open
	// questions (ask_system) route to the per-appliance investigator,
	// and the investigator RECORDS what it learns into this appliance's
	// memory scope (note_lesson / record_technique, see runSession).
	//
	// That is exactly right for a machine, where one box is one subject
	// and accumulated technique is the point. It is exactly wrong for a
	// target that stands in for MANY subjects — a command appliance
	// whose WorkDir is a parent of many log bundles — because every
	// bundle then shares one scope, and a technique learned on last
	// week's incident is in view while reading this week's.
	//
	// The alternative was one appliance per bundle, which isolates
	// correctly and reintroduces the per-bundle setup the arrangement
	// exists to avoid. This makes the isolation a property instead of a
	// discipline: with no asking route, nothing writes the scope, so
	// there is nothing to contaminate.
	//
	// The minted tools are unaffected — executing one renders a template
	// and runs it, and writes no memory at all.
	ToolsOnly bool `json:"tools_only,omitempty"`

	// Command fields (Type == "command")
	Command string   `json:"command"`  // local command name or path
	WorkDir string   `json:"work_dir"` // optional working directory
	EnvVars []string `json:"env_vars"` // optional KEY=VALUE env overrides
	// Repo fields (Type == "repo") — the code is cloned into tmpfs and ingested
	// into the encrypted RepoFilesDB; nothing but derived, encrypted content
	// persists. RepoFiles/RepoCloned track ingest status.
	RepoURL    string `json:"repo_url"`              // git remote (https://github.com/owner/repo)
	RepoBranch string `json:"repo_branch,omitempty"` // branch to clone; "" = remote default
	RepoToken  string `json:"repo_token,omitempty"`  // access token for private repos; blanked on list
	RepoFiles  int    `json:"repo_files,omitempty"`  // ingested file count
	RepoCloned string `json:"repo_cloned,omitempty"` // RFC3339 of last successful ingest
	// RepoSkipDirs are extra directory names (basename match) to exclude from
	// ingest, on top of the built-in defaults (VCS/venv/build artifacts). Lets a
	// project drop its own generated trees (e.g. "dist", "target", "coverage").
	RepoSkipDirs []string `json:"repo_skip_dirs,omitempty"`
	// Bundle fields (Type == "bundle") — uploaded evidence: a support dump, a
	// log tarball, a diagnostic capture. The upload is staged on local disk,
	// expanded, sliced into the encrypted BundleFilesDB, and the staged
	// plaintext is deleted. See docs/servitor-evidence-bundles.md.
	//
	// BundleState is the lifecycle ("staging" | "ingesting" | "ready" |
	// "failed") and is what the UI polls: expanding a multi-gigabyte dump
	// outlives its HTTP request by minutes, so the record, not the response,
	// is where progress lives.
	BundleState    string   `json:"bundle_state,omitempty"`
	BundleError    string   `json:"bundle_error,omitempty"`    // why the last ingest failed
	BundleSources  []string `json:"bundle_sources,omitempty"`  // the filenames the user uploaded
	BundleUploaded string   `json:"bundle_uploaded,omitempty"` // RFC3339 of the last upload
	BundleIngested string   `json:"bundle_ingested,omitempty"` // RFC3339 of the last successful ingest
	BundleFiles    int      `json:"bundle_files,omitempty"`    // index entries written
	BundleLines    int      `json:"bundle_lines,omitempty"`    // lines ingested as text
	BundleBytes    int64    `json:"bundle_bytes,omitempty"`    // expanded size seen
	// BundleBinaries / BundleUnopened count what is present but UNREAD — a
	// core dump, an xz archive nothing built-in could open. Kept on the record
	// rather than only in the log, so a bundle that looks thin can say why
	// without anyone going to find out.
	BundleBinaries int `json:"bundle_binaries,omitempty"`
	BundleUnopened int `json:"bundle_unopened,omitempty"`
	// Toolset fields (Type == "toolset") — an appliance investigated through a
	// CURATED set of already-authored tools rather than a host or a clone. The
	// bindings are the permission: each names one of the owner's pool tools, a
	// posture, and a fingerprint of the tool as approved. See toolset.go and
	// docs/servitor-toolset-type.md.
	Toolset []ToolBinding `json:"toolset,omitempty"`
	// Peer fields — a STUB pointing at an appliance that lives on another
	// gohort instance, because that instance is on the network the system
	// answers and this one is not. Asking it a question sends the question
	// there; the credentials, the SSH config and the accumulated knowledge
	// never leave the far side. See peer_appliance.go.
	PeerName string `json:"peer_name,omitempty"` // registered peer nickname
	RemoteID string `json:"remote_id,omitempty"` // the appliance id over there
	// RemoteKind is what the far side calls this appliance's type. Type itself
	// holds the same value (so every branch treats it natively); this is kept
	// so the edit form can show which picker mode created the record.
	RemoteKind string `json:"remote_kind,omitempty"`
	// Domain is the owner's one-paragraph account of what this target IS —
	// "a GitLab project: issues, merge requests, pipelines, file contents". The
	// bound tools' own descriptions carry most of the domain knowledge; what
	// this adds is what the target is as a whole, what a good answer looks like
	// for it, and what "absent" means here.
	Domain string `json:"domain,omitempty"`
	// LinkedRepos (system types only) are repo-appliance IDs whose ingested
	// code THIS system's investigations may search — the owner's declaration
	// that this box runs that code. The linked repos' search/read tools join
	// the worker's toolset, so a log excerpt seen on the box can be traced to
	// the line that emits it inside the same investigation. Distinct from a
	// workspace: no fan-out, one lead holds both views. Ignored on repo and
	// workspace records.
	LinkedRepos []string `json:"linked_repos,omitempty"`
	// OrchestratorTier / WorkerTier pin THIS appliance's runs to a model tier,
	// overriding the global routing stages. "" follows routing (the default and
	// what every existing record says); "lead" and "worker" pin.
	//
	// Split in two because they are different bets. The orchestrator reasons
	// about the whole investigation and is one call per round, so paying for the
	// lead there is often worth it on a system that keeps defeating the worker.
	// The workers run the SSH commands and are the high-volume half — pinning
	// THEM to the lead is a large cost change, which is why it has to be said
	// separately rather than ridden in on one switch.
	//
	// Neither can escalate past the privacy pin: with "All LLMs are private"
	// off, servitor stays on the worker whatever these say. See
	// core/llm_privacy.go.
	// ToolsRunAs decides WHOSE credentials a shared appliance's bound tools use:
	// "" / "owner" (the default, and what sharing an appliance has always meant)
	// or "caller" — each user's own.
	//
	// It exists because a credential's own cred_scope cannot answer this. A
	// toolset is usually bound to a credential in the OWNER's "My API
	// credentials", and a user-owned credential has no per-user axis at all — it
	// IS one person's. So an appliance built on one can only lend the owner's
	// access or refuse, and nothing on the credential can express which.
	//
	// It selects the IDENTITY the tool session carries, not what the credential
	// means. The credential's own scope then applies underneath, so the two
	// cannot contradict each other: run-as-caller with a shared credential is
	// still the shared secret, and run-as-caller with a per-user one resolves
	// that caller's secret.
	//
	// SSH passwords and repo tokens are deliberately NOT covered. Those are the
	// appliance's own access rather than a third-party service, and lending them
	// is what sharing an appliance means.
	ToolsRunAs       string `json:"tools_run_as,omitempty"`
	OrchestratorTier string `json:"orchestrator_tier,omitempty"`
	WorkerTier       string `json:"worker_tier,omitempty"`
	// LeadTierAvailable is COMPUTED for the edit form and never stored — the
	// POST path clears it before saving. It tells the modal whether to offer
	// "Lead", which is a deployment fact (Model Privacy) rather than anything
	// about this appliance, and shipping it on the record the form already
	// fetches avoids a second round trip that would leave the select empty
	// while it resolved.
	LeadTierAvailable bool `json:"lead_tier_available,omitempty"`
	// Workspace fields (Type == "workspace") — a master appliance that references
	// other appliances (repos and/or SSH boxes) and investigates them together.
	// It owns no store/creds of its own; each member is resolved and run in its
	// own owner's context. See docs/servitor-workspace-mvp.md.
	Members []string `json:"members,omitempty"` // member appliance IDs
	// MemberRoles maps a member ID to a short operator-written role ("scheduler
	// + primary DB", "app worker"). Members of a real cluster are rarely
	// interchangeable — a function often lives on exactly one node — and the
	// coordinator has no way to know that from the outside. Without it, a
	// question about a single-node function either fans out to every member and
	// wastes the drills, or picks wrong and reports a confident "not found".
	//
	// A role states what a node is SUPPOSED to be, which is why it beats the
	// capability summary derived from each member's map: when a service is down
	// or the map is stale, the derived view stops mentioning it and the role
	// still routes the question to the right box.
	MemberRoles map[string]string `json:"member_roles,omitempty"`
	// MemberLinks are typed relations BETWEEN members — "this dump came from
	// that box", "this GitLab project is that system's code". The generalization
	// of LinkedRepos, which already proves the idea at appliance scope but only
	// expresses system→repo.
	//
	// Without them the coordinator infers relationships from names and roles,
	// which is guessing. With them a correlation across members is meaningful:
	// merging events from two members nobody said were related produces a
	// coincidence, not a finding.
	MemberLinks []MemberLink `json:"member_links,omitempty"`
	// Collections are knowledge-collection IDs linked to this appliance so the
	// investigator can draw on curated external knowledge (runbooks, vendor docs,
	// a guide) when answering — via the search_knowledge tool — alongside what it
	// gathered from the system itself. Applies to every appliance type.
	Collections []string `json:"collections,omitempty"`
	// Sharing — an appliance/repo is owned by the user who created it and lives
	// in that user's store. When Shared is set, every authenticated user can
	// discover and operate it (in the OWNER's context: same creds, same repo
	// clone, same accumulated knowledge/scoped memory) — but each user keeps
	// their OWN chat sessions. Owner is stamped on create; the shared index in
	// T.DB (see sharing.go) lets non-owners resolve it.
	Owner  string `json:"owner,omitempty"`  // username that created + owns this record
	Shared bool   `json:"shared,omitempty"` // visible to + usable by all authenticated users
	// Shared config below (persona/instructions/profile/etc.)
	Instructions  string     `json:"instructions"`   // freeform notes injected into every session
	PersonaName   string     `json:"persona_name"`   // short label shown in the UI (e.g. "Support", "QA")
	PersonaPrompt string     `json:"persona_prompt"` // shapes how the agent approaches this appliance
	Profile       string     `json:"profile"`        // full system profile / CLI map markdown
	LogMap        []LogEntry `json:"log_map"`        // structured list of discovered log files
	Scanned       string     `json:"scanned"`        // RFC3339 timestamp of last map run
}

// pruneMemberRoles drops role entries for members that are no longer selected
// and trims the rest, so unchecking a member cannot leave a stale role behind to
// resurface if it is re-added later. Returns nil when nothing survives, keeping
// the field omitempty in the stored record.
func pruneMemberRoles(roles map[string]string, members []string) map[string]string {
	if len(roles) == 0 {
		return nil
	}
	keep := make(map[string]bool, len(members))
	for _, id := range members {
		keep[id] = true
	}
	out := make(map[string]string, len(roles))
	for id, role := range roles {
		role = strings.TrimSpace(role)
		if role == "" || !keep[id] {
			continue
		}
		if len(role) > 120 {
			role = role[:120]
		}
		out[id] = role
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// dedupeStrings trims, drops empties, and removes duplicates while preserving
// first-seen order. Used to normalize workspace member ID lists.
func dedupeStrings(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

// extractLogMap parses the structured ## Log Files JSON block from a profile.
func extractLogMap(profile string) []LogEntry {
	const header = "## Log Files"
	idx := strings.Index(profile, header)
	if idx < 0 {
		return nil
	}
	rest := profile[idx+len(header):]
	jsonStart := strings.Index(rest, "```json")
	if jsonStart < 0 {
		return nil
	}
	jsonStart += len("```json")
	jsonEnd := strings.Index(rest[jsonStart:], "```")
	if jsonEnd < 0 {
		return nil
	}
	var entries []LogEntry
	if err := json.Unmarshal([]byte(strings.TrimSpace(rest[jsonStart:jsonStart+jsonEnd])), &entries); err != nil {
		return nil
	}
	return entries
}

// mergeLogMap merges newly discovered log entries with the existing set.
// Entries with the same path are updated; entries only in old are preserved.
func mergeLogMap(old, fresh []LogEntry) []LogEntry {
	seen := make(map[string]bool, len(fresh))
	result := make([]LogEntry, 0, len(fresh)+len(old))
	for _, e := range fresh {
		result = append(result, e)
		seen[e.Path] = true
	}
	for _, e := range old {
		if !seen[e.Path] {
			result = append(result, e)
		}
	}
	return result
}

// applianceTierOverride turns an appliance's stored tier preference into a loop
// override.
//
// Returns TierUnset for anything unrecognized as well as for the empty string,
// so a value written by a future version — or a typo in a hand-edited record —
// falls back to following the routing stage rather than pinning a tier nobody
// chose.
func applianceTierOverride(pref string) LLMTier {
	switch strings.ToLower(strings.TrimSpace(pref)) {
	case "lead":
		return LEAD
	case "worker":
		return WORKER
	}
	return TierUnset
}

// normalizeApplianceTier reduces a stored/submitted tier preference to one of
// "", "lead" or "worker".
//
// Anything unrecognized becomes "" (follow routing) rather than an error at
// read time: a record written by a future version, or hand-edited, should fall
// back to the deployment's routing rather than pinning a tier nobody chose.
func normalizeApplianceTier(pref string) string {
	switch strings.ToLower(strings.TrimSpace(pref)) {
	case "lead":
		return "lead"
	case "worker":
		return "worker"
	}
	return ""
}

// normalizeToolsRunAs reduces the stored/submitted value to "owner" or "caller".
//
// Anything unrecognized becomes "owner" — the default and the behavior every
// existing record has. Failing open toward the CALLER would silently widen who
// must authenticate; failing toward the owner keeps what the appliance already
// did.
func normalizeToolsRunAs(v string) string {
	if strings.EqualFold(strings.TrimSpace(v), "caller") {
		return "caller"
	}
	return "owner"
}
