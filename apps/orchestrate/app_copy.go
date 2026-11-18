package orchestrate

// An app's own copies (docs/design/app-history.md, stage 2, piece C). An app
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

import (
	"fmt"
	"sort"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
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
	busy             map[string]bool
	made, tools      []string
}

func newAppCopier(udb Database, owner, slug, via string) *appCopier {
	return &appCopier{udb: udb, owner: owner, slug: slug, via: via,
		agents: map[string]AgentRecord{}, pipelines: map[string]PipelineDef{}, machines: map[string]MachineDef{}, busy: map[string]bool{}}
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
	saved, err := saveAgent(c.udb, c.agentCopy(src, ""))
	if err != nil {
		return AgentRecord{}, fmt.Errorf("copying the agent %q into the app: %w", src.Name, err)
	}
	c.agents[src.ID] = saved
	c.made = append(c.made, fmt.Sprintf("agent %q", src.Name))
	c.copyTools(src)
	for _, k := range c.udb.Keys(agentsTable) {
		var sub AgentRecord
		if !c.udb.Get(agentsTable, k, &sub) || sub.OwnedBy != src.ID || (sub.Owner != c.owner && sub.Owner != seedOwner) {
			continue
		}
		cp, err := saveAgent(c.udb, c.agentCopy(sub, saved.ID))
		if err != nil {
			return AgentRecord{}, fmt.Errorf("copying %q's sub-agent %q into the app: %w", src.Name, sub.Name, err)
		}
		c.agents[sub.ID] = cp
		c.copyTools(sub)
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

// copyTools copies into the app's tool space the person's tools src runs on
// that are theirs alone: its agent-scoped tools and the pool tools it names.
// A tool the space has by that name already is left as the space has it.
func (c *appCopier) copyTools(src AgentRecord) {
	var tools []PersistentTempTool
	tools = append(tools, AgentScopedTools(c.udb, c.owner, src.ID)...)
	for _, n := range src.AllowedTools {
		if p, ok := UserToolByName(c.udb, c.owner, strings.TrimSpace(n)); ok && len(p.ScopeAgents) == 0 {
			tools = append(tools, p)
		}
	}
	for _, p := range tools {
		if _, ok := apptools.ByName(c.owner, c.slug, p.Tool.Name); ok {
			continue
		}
		if err := apptools.CopyIn(c.owner, c.slug, p.Tool, CopiedFrom{ID: p.ID, Name: p.Tool.Name, At: time.Now()}, c.via); err != nil {
			Log("[app-copy] %s/%s: the tool %q was not copied: %v", c.owner, c.slug, p.Tool.Name, err)
			continue
		}
		c.tools = append(c.tools, p.Tool.Name)
	}
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
	return b.String()
}

// copyBindings makes the agent and pipeline this save binds the app's own,
// when the save binds them anew: a new app, or one now naming an agent or
// pipeline other than the one it had. An app that keeps what it pointed at
// keeps pointing until its first change copies it (piece D). A maintainer's
// save binds nothing new.
func (aw *appWrite) copyBindings() error {
	if aw.maintained || aw.t == nil || aw.t.udb == nil {
		return nil
	}
	c := newAppCopier(aw.t.udb, aw.spec.Owner, aw.spec.Slug, aw.t.changedVia())
	if c.via == "" {
		c.via = "Builder"
	}
	if id := strings.TrimSpace(aw.spec.AgentID); id != "" && (!aw.isUpdate || id != aw.prior.AgentID) {
		if a, ok := loadAgent(aw.t.udb, id); ok && a.OwningApp != aw.spec.Slug {
			cp, err := c.agent(a)
			if err != nil {
				return err
			}
			aw.spec.AgentID = cp.ID
		}
	}
	if id := strings.TrimSpace(aw.spec.PipelineID); id != "" && (!aw.isUpdate || id != aw.prior.PipelineID) {
		if d, ok := LoadPipelineDef(aw.t.udb, aw.spec.Owner, id); ok && d.OwningApp != aw.spec.Slug {
			cp, err := c.pipeline(d)
			if err != nil {
				return err
			}
			aw.spec.PipelineID = cp.ID
		}
	}
	aw.copyNote = c.note()
	return nil
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
