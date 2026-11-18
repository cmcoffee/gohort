package orchestrate

// An app's own copies (docs/design/app-history.md, stage 2, pieces C and
// D). An app
// binding an agent or a pipeline that is not its own (app_def agent_id,
// pipeline_id) gets a copy of it, made for the app and bound in its place:
// the original is not changed, and changes to it no longer reach the app.
// What the copy runs comes with it, each one the app's own as well: an
// agent's sub-agents and the tools of the person's it names (into the app's
// tool space, an unchanged copy keeping its approval), and a pipeline's stage
// agents and machines, a machine's step agents, pipelines and machines. Each
// copy keeps its original's name and says what it was copied from
// (copied_from).
//
// A name inside an app means the app's own (appScopedRef, and the save of an
// app's pipeline or machine storing its references as ids); a name anywhere
// else, where the person's original and an app's copy tie, means the one that
// is no app's (resolveAgentName, findPipelineByNameOrID,
// findMachineByNameOrID).
//
// Piece D: an app that points at what it does not own is copied the first
// time Builder or Anvil saves it, what it was bound to taking what its
// agents learned (copyAgentMemory); the tools of the person's its agents
// name, from their pool or taken from the deployment's catalog, are copied
// into the app's space; and once none of its agents is allowed every tool,
// the app holds its own tools (AppSpec.OwnTools) and its agents stop
// loading the person's pool.

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/applocal"
	"github.com/cmcoffee/gohort/core/apptools"
)

func init() {
	RegisterAppRefResolver(appRefResolve)
}

// appCopier makes owner's app slug its own copies, each once: what two
// stages run is one copy.
type appCopier struct {
	udb              Database
	owner, slug, via string
	agents           map[string]AgentRecord
	pipelines        map[string]PipelineDef
	machines         map[string]MachineDef
	collections      map[string]string
	busy             map[string]bool
	made, tools      []string
	// memory has a copy take what its original learned: set while copying
	// what an app was already bound to (its first change, piece D).
	memory  bool
	learned []string
	// factsTo is the app's memory, when the app keeps one and this is its
	// first change: copied facts go there rather than to the copy's own.
	factsTo string
}

func newAppCopier(udb Database, owner, slug, via string) *appCopier {
	return &appCopier{udb: udb, owner: owner, slug: slug, via: via,
		agents: map[string]AgentRecord{}, pipelines: map[string]PipelineDef{}, machines: map[string]MachineDef{}, collections: map[string]string{}, busy: map[string]bool{}}
}

// copiedFrom is the record of a copy of id, name, made now.
func copiedFrom(id, name string) *CopiedFrom {
	return &CopiedFrom{ID: id, Name: name, At: time.Now()}
}

// agent is the app's own agent for src: src when it is the app's already,
// else its copy, with its sub-agents and the person's tools it names.
func (c *appCopier) agent(src AgentRecord) (AgentRecord, error) {
	if src.OwningApp == c.slug && src.Owner == c.owner {
		return src, nil
	}
	if cp, ok := c.agents[src.ID]; ok {
		return cp, nil
	}
	if src.Owner != c.owner && src.Owner != seedOwner {
		return AgentRecord{}, fmt.Errorf("the agent %q is not yours: an app carries copies of your own agents only", src.Name)
	}
	rec := c.agentCopy(src, "")
	rec.AttachedCollections = c.copyCollections(src.AttachedCollections)
	saved, err := saveAgent(c.udb, rec)
	if err != nil {
		return AgentRecord{}, fmt.Errorf("copying the agent %q into the app: %w", src.Name, err)
	}
	c.agents[src.ID] = saved
	c.made = append(c.made, fmt.Sprintf("agent %q", src.Name))
	c.copyTools(src)
	c.copyMemory(src, saved)
	for _, k := range c.udb.Keys(agentsTable) {
		var sub AgentRecord
		if !c.udb.Get(agentsTable, k, &sub) || sub.OwnedBy != src.ID || (sub.Owner != c.owner && sub.Owner != seedOwner) {
			continue
		}
		subRec := c.agentCopy(sub, saved.ID)
		subRec.AttachedCollections = c.copyCollections(sub.AttachedCollections)
		cp, err := saveAgent(c.udb, subRec)
		if err != nil {
			return AgentRecord{}, fmt.Errorf("copying %q's sub-agent %q into the app: %w", src.Name, sub.Name, err)
		}
		c.agents[sub.ID] = cp
		c.copyTools(sub)
		c.copyMemory(sub, cp)
	}
	return saved, nil
}

// agentCopy is src as the app's own copy, under parent when it is a
// sub-agent. Who it is shared with and the framework's overlay bookkeeping
// stay with the original; the person's choices while using it (always
// allowing a tool) come along, as the copy does the same job.
func (c *appCopier) agentCopy(src AgentRecord, parent string) AgentRecord {
	rec := stripAgentIdentity(src)
	rec.Owner, rec.OwnedBy = c.owner, parent
	rec.OwningApp, rec.appEdit = c.slug, c.via
	rec.CopiedFrom = copiedFrom(src.ID, src.Name)
	rec.Tools = nil
	rec.Everyone, rec.ShowOnDashboard, rec.AllowedUsers, rec.Maintainers = false, false, nil, nil
	rec.ShapeID, rec.OverriddenFields, rec.OverlayRev = "", nil, 0
	if shape, ok := shapeForSeed(src.ID); ok {
		rec.ShapeID = shape
	}
	if src.Owner == seedOwner && parent == "" {
		rec.Hidden = false
	}
	return rec
}

// copyTools copies into the app's tool space the tools src runs on that are
// the person's rather than gohort's: its agent-scoped tools, and the tools
// it names from their pool or taken from the deployment's catalog (a copy
// of the release they took, so the app travels whole). A tool the space has
// by that name already is left as the space has it.
func (c *appCopier) copyTools(src AgentRecord) {
	type found struct {
		tool PersistentTempTool
		from string
	}
	var tools []found
	for _, p := range AgentScopedTools(c.udb, c.owner, src.ID) {
		tools = append(tools, found{p, p.Tool.Name})
	}
	var adopted map[string]LentTool
	for _, n := range src.AllowedTools {
		n = canonicalToolName(strings.TrimSpace(n))
		if p, ok := UserToolByName(c.udb, c.owner, n); ok && len(p.ScopeAgents) == 0 {
			tools = append(tools, found{p, p.Tool.Name})
			continue
		}
		if adopted == nil {
			adopted = map[string]LentTool{}
			for _, l := range AdoptedToolsFor(c.udb, c.owner) {
				adopted[l.Tool.Name] = l
			}
		}
		if l, ok := adopted[n]; ok {
			tools = append(tools, found{l.PersistentTempTool, fmt.Sprintf("%s (%s's, from the catalog)", l.Tool.Name, l.Owner)})
		}
	}
	for _, f := range tools {
		p := f.tool
		if _, ok := apptools.ByName(c.owner, c.slug, p.Tool.Name); ok {
			continue
		}
		if err := apptools.CopyIn(c.owner, c.slug, p.Tool, CopiedFrom{ID: p.ID, Name: f.from, At: time.Now()}, c.via); err != nil {
			Log("[app-copy] %s/%s: the tool %q was not copied: %v", c.owner, c.slug, p.Tool.Name, err)
			continue
		}
		c.tools = append(c.tools, p.Tool.Name)
	}
}

// copyMemory has cp take what src learned, when c copies an app's first
// change: its working notes and learned findings, and its facts (whoever
// said them), once. Facts go to the app's memory when the app keeps one
// (c.factsTo, app_memory.go), else to the copy's own. What an agent learns
// is kept in the store of the person it learned it with: the owner's, and
// each other person's who used it (a shared app's users), so each person's
// is copied within their own store, where it stays theirs. An export never
// carries any of it.
func (c *appCopier) copyMemory(src, cp AgentRecord) {
	if !c.memory {
		return
	}
	from, to := factsNamespace(src.ID), factsNamespace(cp.ID)
	part := memoryPart(c.udb, c.owner, src)
	if n := strings.TrimSpace(part.Notes); n != "" {
		SaveOperatingNotes(c.udb, to, n)
	}
	factsTo := to
	if c.factsTo != "" {
		factsTo = c.factsTo
	}
	facts, people := c.copyFacts(from, factsTo)
	if facts == 0 && strings.TrimSpace(part.Notes) == "" && len(part.Findings) == 0 && people == 0 {
		return
	}
	c.noteLearned(src.Name, facts, len(part.Findings), strings.TrimSpace(part.Notes) != "", people)
	if VectorDB == nil || len(part.Findings) == 0 {
		return
	}
	// Findings are embedded again, which takes a while: after the save, on
	// work that outlives its request.
	owner, findings := c.owner, part.Findings
	ctx := context.WithoutCancel(AppContext())
	go func() {
		for i, d := range findings {
			if strings.TrimSpace(d.Text) == "" {
				continue
			}
			IngestReportTitled(ctx, VectorDB, knowledgeSource(owner, cp.ID, d.Topic),
				fmt.Sprintf("orch-know-%s-%s-copy%d", cp.ID, owner, i), d.Title, d.Text, d.Kind)
		}
		Log("[app-copy] %s: %d finding(s) of %q indexed for the app's copy", owner, len(findings), src.Name)
	}()
}

// pipeline is the app's own pipeline for src: src when it is the app's, else
// its copy, each stage's agent and machine the app's own too.
func (c *appCopier) pipeline(src PipelineDef) (PipelineDef, error) {
	if src.OwningApp == c.slug {
		return src, nil
	}
	if cp, ok := c.pipelines[src.ID]; ok {
		return cp, nil
	}
	if src.Owner != "" && src.Owner != c.owner {
		return PipelineDef{}, fmt.Errorf("the pipeline %q is not yours: an app carries copies of your own pipelines only", src.Name)
	}
	if c.busy["pipeline:"+src.ID] {
		return src, nil
	}
	c.busy["pipeline:"+src.ID] = true
	d := src
	d.ID, d.Created, d.Updated, d.Previous = "", time.Time{}, time.Time{}, nil
	d.Owner, d.OwningApp, d.CopiedFrom = c.owner, c.slug, copiedFrom(src.ID, src.Name)
	d.AllowedUsers, d.Editors, d.Published, d.Global = nil, nil, false, false
	stages, err := c.stages(src.Stages)
	if err != nil {
		return PipelineDef{}, err
	}
	d.Stages = stages
	saved, err := SavePipelineDefBy(c.udb, d, "copied for the app "+c.slug, c.via)
	if err != nil {
		return PipelineDef{}, fmt.Errorf("copying the pipeline %q into the app: %w", src.Name, err)
	}
	c.pipelines[src.ID] = saved
	c.made = append(c.made, fmt.Sprintf("pipeline %q", src.Name))
	return saved, nil
}

// stages is in with each agent and machine a stage runs made the app's own.
func (c *appCopier) stages(in []PipelineStage) ([]PipelineStage, error) {
	out := append([]PipelineStage(nil), in...)
	for i := range out {
		if a, ok := findAgentByNameOrID(c.udb, c.owner, out[i].Agent); ok {
			cp, err := c.agent(a)
			if err != nil {
				return nil, err
			}
			out[i].Agent = cp.ID
		}
		if m, ok := findMachineByNameOrID(c.udb, c.owner, out[i].Machine); ok {
			cp, err := c.machine(m)
			if err != nil {
				return nil, err
			}
			out[i].Machine = cp.ID
		}
		body, err := c.stages(out[i].Body)
		if err != nil {
			return nil, err
		}
		out[i].Body = body
	}
	return out, nil
}

// machine is the app's own machine for src: src when it is the app's, else
// its copy, each step's agent, pipeline and machine the app's own too.
func (c *appCopier) machine(src MachineDef) (MachineDef, error) {
	if src.OwningApp == c.slug {
		return src, nil
	}
	if cp, ok := c.machines[src.ID]; ok {
		return cp, nil
	}
	if src.Owner != "" && src.Owner != c.owner {
		return MachineDef{}, fmt.Errorf("the machine %q is not yours: an app carries copies of your own machines only", src.Name)
	}
	if c.busy["machine:"+src.ID] {
		return src, nil
	}
	c.busy["machine:"+src.ID] = true
	d := src
	d.ID, d.Created, d.Updated, d.Previous = "", time.Time{}, time.Time{}, nil
	d.Owner, d.OwningApp, d.CopiedFrom = c.owner, c.slug, copiedFrom(src.ID, src.Name)
	d.AllowedUsers, d.Editors, d.Published = nil, nil, false
	phases := append([]MachinePhase(nil), src.Phases...)
	for i := range phases {
		if a, ok := findAgentByNameOrID(c.udb, c.owner, phases[i].Agent); ok {
			cp, err := c.agent(a)
			if err != nil {
				return MachineDef{}, err
			}
			phases[i].Agent = cp.ID
		}
		if p, ok := findPipelineByNameOrID(c.udb, c.owner, phases[i].Pipeline); ok {
			cp, err := c.pipeline(p)
			if err != nil {
				return MachineDef{}, err
			}
			phases[i].Pipeline = cp.ID
		}
		if m, ok := findMachineByNameOrID(c.udb, c.owner, phases[i].Machine); ok {
			cp, err := c.machine(m)
			if err != nil {
				return MachineDef{}, err
			}
			phases[i].Machine = cp.ID
		}
	}
	d.Phases = phases
	saved, err := SaveMachineDefBy(c.udb, d, "copied for the app "+c.slug, c.via)
	if err != nil {
		return MachineDef{}, fmt.Errorf("copying the machine %q into the app: %w", src.Name, err)
	}
	c.machines[src.ID] = saved
	c.made = append(c.made, fmt.Sprintf("machine %q", src.Name))
	return saved, nil
}

// note says what was copied, for the save's reply; "" for nothing.
func (c *appCopier) note() string {
	if len(c.made) == 0 && len(c.tools) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\nCopied into the app, as its own:")
	if len(c.made) > 0 {
		b.WriteString(" " + strings.Join(c.made, ", "))
	}
	if len(c.tools) > 0 {
		sort.Strings(c.tools)
		if len(c.made) > 0 {
			b.WriteString(";")
		}
		b.WriteString(" its tools " + strings.Join(c.tools, ", ") + " (into the app's own tools)")
	}
	b.WriteString(". The originals are unchanged, and changes to them no longer reach the app: change the app's copies (they keep their names, and inside the app a name means the app's own).")
	if len(c.learned) > 0 {
		b.WriteString(" What the originals learned came with them, once: " + strings.Join(c.learned, ", ") + ".")
	}
	return b.String()
}

// copyBindings makes what the app runs its own when Builder or Anvil saves
// it: the agent and pipeline it is bound to, when they are not its own (on
// its first change, what it was already bound to takes what its agents
// learned), and the tools of the person's that its own agents name. Then the
// app holds its own tools, unless one of its agents is allowed every tool:
// nothing says which of the person's tools that one needs, so the app keeps
// loading the pool and the save asks for the agent's tools to be named. A
// maintainer's save copies nothing.
func (aw *appWrite) copyBindings() error {
	if aw.maintained || aw.t == nil || aw.t.udb == nil {
		return nil
	}
	udb, slug := aw.t.udb, aw.spec.Slug
	c := newAppCopier(udb, aw.spec.Owner, slug, aw.t.changedVia())
	if c.via == "" {
		c.via = "Builder"
	}
	// Whether the app keeps a memory of the people using it (app_memory.go).
	// Undecided is an app not saved since that existed: a new app starts
	// without one unless it says so; an existing app's first change switches
	// it on, and what its agents learned comes into it, so it keeps
	// remembering as it did.
	var owned []AgentRecord
	firstChange := aw.isUpdate && aw.prior.Memory == ""
	if aw.spec.Memory == "" {
		aw.spec.Memory = map[bool]string{true: "on", false: "off"}[aw.isUpdate]
	}
	if firstChange && aw.spec.Memory == "on" {
		c.factsTo = applocal.AppMemoryNamespace(aw.spec.Owner, slug)
		for _, a := range listAgents(udb, aw.spec.Owner) {
			if a.OwningApp == slug && a.Owner == aw.spec.Owner {
				owned = append(owned, a)
			}
		}
	}
	if id := strings.TrimSpace(aw.spec.AgentID); id != "" {
		if a, ok := loadAgent(udb, id); ok && a.OwningApp != slug {
			c.memory = aw.isUpdate && id == aw.prior.AgentID
			cp, err := c.agent(a)
			if err != nil {
				return err
			}
			aw.spec.AgentID = cp.ID
		}
	}
	if id := strings.TrimSpace(aw.spec.PipelineID); id != "" {
		if d, ok := LoadPipelineDef(udb, aw.spec.Owner, id); ok && d.OwningApp != slug {
			c.memory = aw.isUpdate && id == aw.prior.PipelineID
			cp, err := c.pipeline(d)
			if err != nil {
				return err
			}
			aw.spec.PipelineID = cp.ID
		}
	}
	c.memory = false
	for _, a := range owned {
		c.adoptAgentFacts(a)
	}
	var everyTool []string
	for _, a := range listAgents(udb, aw.spec.Owner) {
		if a.OwningApp != slug || a.Owner != aw.spec.Owner {
			continue
		}
		c.copyTools(a)
		// What it reads becomes the app's own too (piece E2, step 6).
		if ids := c.copyCollections(a.AttachedCollections); !sameStrings(ids, a.AttachedCollections) {
			a.AttachedCollections, a.appEdit = ids, c.via
			if _, err := saveAgent(udb, a); err != nil {
				return fmt.Errorf("pointing the agent %q at the app's copies of what it reads: %w", a.Name, err)
			}
		}
		if allowedEveryTool(a) {
			everyTool = append(everyTool, a.Name)
		}
	}
	aw.copyNote = c.note()
	aw.spec.OwnTools = len(everyTool) == 0
	if len(everyTool) > 0 {
		sort.Strings(everyTool)
		aw.copyNote += fmt.Sprintf("\n\nThe app still loads your own tools beside its own: %s allowed every tool, so nothing says which of yours the app needs. Name its tools (update_agent allowed_tools, 4 to 10 of them): the next save copies those into the app's own tools, and the app then holds everything it runs on and travels whole.",
			strings.Join(everyTool, ", ")+map[bool]string{true: " is", false: " are"}[len(everyTool) == 1])
	}
	return nil
}

// allowedEveryTool reports whether a is allowed every tool: no list of its
// own (the default pool), or "*". One that may use no tools is not.
func allowedEveryTool(a AgentRecord) bool {
	if isNoToolsSentinel(a.AllowedTools) {
		return false
	}
	if len(a.AllowedTools) == 0 {
		return true
	}
	for _, n := range a.AllowedTools {
		if strings.TrimSpace(n) == "*" {
			return true
		}
	}
	return false
}

// appScopedRef is the id of owner's app slug's own agent, pipeline or
// machine (kind) that ref names, by id or by name; false when none of the
// app's does.
func appScopedRef(udb Database, owner, slug, kind, ref string) (string, bool) {
	ref = strings.TrimSpace(ref)
	if udb == nil || slug == "" || ref == "" {
		return "", false
	}
	type item struct{ id, name, app string }
	var items []item
	switch kind {
	case "agent":
		for _, a := range listAgents(udb, owner) {
			items = append(items, item{a.ID, a.Name, a.OwningApp})
		}
	case "pipeline":
		for _, d := range ListPipelineDefs(udb, owner) {
			items = append(items, item{d.ID, d.Name, d.OwningApp})
		}
	case "machine":
		for _, d := range ListMachineDefs(udb, owner) {
			items = append(items, item{d.ID, d.Name, d.OwningApp})
		}
	}
	key := normalizeAgentKey(ref)
	for _, it := range items {
		if it.app == slug && it.id == ref {
			return it.id, true
		}
	}
	for _, it := range items {
		if it.app == slug && (strings.EqualFold(it.name, ref) || normalizeAgentKey(it.name) == key) {
			return it.id, true
		}
	}
	return "", false
}

// appRefResolve is core's RegisterAppRefResolver: ref in owner's app slug's
// pipeline or machine as the id of the app's own it names, else as it was.
func appRefResolve(owner, slug, kind, ref string) string {
	o, ok := agenthostApp()
	if !ok || o.DB == nil {
		return ref
	}
	if id, ok := appScopedRef(UserDB(o.DB, owner), owner, slug, kind, ref); ok {
		return id
	}
	return ref
}

// soleNoAppAgent is the one of tied that is no app's, when exactly one is:
// outside an app, the person's own agent is what its name means, not an
// app's copy of it.
func soleNoAppAgent(tied []AgentRecord) (AgentRecord, bool) {
	var found []AgentRecord
	for _, a := range tied {
		if strings.TrimSpace(a.OwningApp) == "" {
			found = append(found, a)
		}
	}
	if len(found) == 1 {
		return found[0], true
	}
	return AgentRecord{}, false
}

// copyFacts copies the facts of namespace from into to, in the owner's
// store and in each other person's (where what an agent learned with them
// is kept), and says how many the owner's held and how many other people's
// had any.
func (c *appCopier) copyFacts(from, to string) (owner, people int) {
	owner = CopyMemoryFacts(c.udb, from, to)
	if orchestrateBaseDB == nil || AuthDB == nil || AuthDB() == nil {
		return owner, 0
	}
	for _, u := range AuthListUsers(AuthDB()) {
		if u.Username == c.owner {
			continue
		}
		if CopyMemoryFacts(UserDB(orchestrateBaseDB, u.Username), from, to) > 0 {
			people++
		}
	}
	return owner, people
}

// noteLearned records, for the save's reply, what of name's memory came
// along.
func (c *appCopier) noteLearned(name string, facts, findings int, notes bool, people int) {
	learned := fmt.Sprintf("%s (%d facts", name, facts)
	if findings > 0 {
		learned += fmt.Sprintf(", %d findings", findings)
	}
	if notes {
		learned += ", its working notes"
	}
	if people > 0 {
		learned += fmt.Sprintf("; and what it learned with %d other %s, each kept in their own store", people, map[bool]string{true: "person", false: "people"}[people == 1])
	}
	c.learned = append(c.learned, learned+")")
}

// adoptAgentFacts moves into the app's memory what an agent already the
// app's learned under its own id, on the app's first change once the app
// keeps a memory of its own: from then on its facts are the app's.
func (c *appCopier) adoptAgentFacts(a AgentRecord) {
	if c.factsTo == "" {
		return
	}
	facts, people := c.copyFacts(factsNamespace(a.ID), c.factsTo)
	if facts > 0 || people > 0 {
		c.noteLearned(a.Name, facts, 0, false, people)
	}
}

// copyCollections is ids with each collection of the person's that is not
// the app's replaced by the app's own copy of it (piece E2, step 6): its
// blueprint, and its documents, which are copied as they are indexed (their
// passages and embeddings), so the copy knows what the original knew without
// indexing anything again. A collection that is not the person's own (the
// deployment's, or one shared with them) is the deployment's, as an MCP
// server is, and is left as it is.
func (c *appCopier) copyCollections(ids []string) []string {
	if len(ids) == 0 {
		return ids
	}
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		out = append(out, c.copyCollection(id))
	}
	return out
}

// copyCollection is the app's own collection for id: id when it is the app's
// already or not the person's to copy, else its copy.
func (c *appCopier) copyCollection(id string) string {
	if cp, ok := c.collections[id]; ok {
		return cp
	}
	src, ok := loadCollection(c.udb, c.owner, id)
	if !ok || src.OwningApp == c.slug || src.Owner != c.owner || IsDeploymentScope(src) {
		return id
	}
	cp := Collection{
		ID: UUIDv4(), Owner: c.owner, Created: time.Now(), OwningApp: c.slug, CopiedFrom: copiedFrom(src.ID, src.Name),
		Name: src.Name, Description: src.Description, FilterRules: src.FilterRules, SearchTerms: src.SearchTerms,
		DocsPerRun: src.DocsPerRun, Articles: src.Articles, SuggestionsOff: src.SuggestionsOff,
		IngestedURLs: src.IngestedURLs, ArticlesFetched: src.ArticlesFetched, Refresh: src.Refresh,
	}
	if cp.Refresh == "" {
		if task, _, _, ok := refreshScheduled(c.owner, src.ID); ok {
			cp.Refresh = task.Every
		}
	}
	// Who submits to it are agents of the person's: the app's copies of
	// them when the app has copied them, else as they were.
	for agent, mode := range src.Submitters {
		if cp.Submitters == nil {
			cp.Submitters = map[string]string{}
		}
		if a, ok := c.agents[agent]; ok {
			agent = a.ID
		}
		cp.Submitters[agent] = mode
	}
	SaveCollectionBy(c.udb, cp, c.via)
	docs := 0
	if VectorDB != nil {
		chunks := ChunksForSource(VectorDB, collectionSource(src.ID))
		seen := map[string]bool{}
		for i := range chunks {
			chunks[i].ID = UUIDv4()
			chunks[i].Source = collectionSource(cp.ID)
			chunks[i].ReportID = strings.Replace(chunks[i].ReportID, src.ID, cp.ID, 1)
			seen[chunks[i].ReportID] = true
		}
		PutChunks(VectorDB, chunks)
		docs = len(seen)
	}
	if err := setCollectionRefresh(c.udb, cp, cp.Refresh, c.via); err != nil {
		Log("[app-copy] %s/%s: the copy of collection %q could not be scheduled: %v", c.owner, c.slug, src.Name, err)
	}
	c.collections[id] = cp.ID
	c.made = append(c.made, fmt.Sprintf("collection %q (%d documents)", src.Name, docs))
	return cp.ID
}

// sameStrings reports whether a and b hold the same strings in order.
func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
