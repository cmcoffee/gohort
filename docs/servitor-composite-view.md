# The composite view — joining members, and tracing through them

Status: **spec, nothing built.** Two asks that turn out to be one shape: a
workspace that answers across code, live state, evidence, a service and docs;
and generic trace questions — *"what data sources does this function rely on?"*,
*"when I update this setting in the admin UI, what gets called?"*

## Both asks are the same shape

A cross-domain answer is a **path**: this error in the dump → the code that
emits it → the box it ran on → the change that introduced it. A trace question
is also a path: this UI field → the endpoint it POSTs → the handler → the table
it writes → what reads that later.

Both are "follow relationships between named things." Both are currently
answered by re-deriving every hop with substring search, and neither keeps the
result. That is why they belong in one spec: the machinery that makes a trace
cheap is the machinery that makes the composite view persistent.

Trace questions ARE answerable today — this whole document was researched by
grepping and reading, one hop at a time. The point is not to make them possible.
It is to stop paying full price for the same traversal every time.

## What already exists

**The workspace shell** — scout-then-drill, `MemberRoles`, a drill cap, cluster
fan-out, per-member knowledge docs with staleness. Members can now be systems,
repos, evidence bundles and tool-backed services (v0.6.020).

**One working member-to-member join.** `LinkedRepos` says "this system runs that
code," and the payoff is concrete: the worker gets the repo's search tools INSIDE
the live investigation, so a log excerpt reaches the line that emits it without
a human joining two investigations by hand. It is the precedent for everything
in slice 1.

**A real graph store.** `core/graphstore.go` has entities with aliases and
attributes, typed edges, per-namespace scoping, and lookups:
`FindGraphEntity`, `GetGraphEntity`, `ListGraphEntities`,
`GraphEntitiesMentionedIn`. Every investigator prompt tells workers to build the
map with `link_entities`, one relationship per call, and calls it "a topology you
can traverse, not flat facts on one node."

**`bundle_timeline`** merges files into one time-ordered sequence — within a
single bundle.

## What's missing

1. **The workspace accumulates nothing.** There is not one `updateDoc`,
   `storeFact` or graph write in any workspace file. Members remember; the
   workspace does not. So the cross-domain join — the expensive part, and the
   only part no member could have produced alone — exists for one answer and
   evaporates.

2. **The graph is written as a topology and read as a blob.**
   `scopedGraphBlock` renders every entity and attribute into the prompt. There
   is no neighbors query, no path query, no filter by relation. The prompts
   promise traversal; nothing implements it. **This is what blocks trace
   questions**, and it is the highest-leverage gap in the document.

3. **No member links beyond system→repo.** Nothing expresses "this dump came
   from that box", "this GitLab project is that system's code", or "this box
   talks to that service". The lead infers relationships from names and roles.

4. **No cross-member correlation.** Nothing merges a bundle timestamp against a
   live box's log against a deploy time from a service. For "why did X break on
   the 14th" that ordering IS the answer.

5. **Substring search cannot tell a definition from a reference from a comment.**
   The first hop of any trace is only as good as the token you guessed.

6. **Scout ranking is still the MVP heuristic** — comparing "repo hit at 0.7"
   against "the box's map mentions this". Two more member kinds made that
   harder, not easier.

## The build

### Slice 1 — member links

Generalize `LinkedRepos` into typed relations between members, on the workspace
record:

```go
type MemberLink struct {
    From string // member id
    Rel  string // "runs" | "code-for" | "captured-from" | "talks-to"
    To   string // member id
}
```

Rendered in the roster next to Role and Capability, so the lead routes on
declared structure instead of name similarity. `LinkedRepos` keeps working
unchanged — it is the same idea at appliance scope, and a link here does not
replace the tool-level join it performs.

Cheapest item in the document and everything else reads better with it: a
correlation across members is only meaningful once something says which members
are related.

### Slice 2 — traversal tools

Three tools over the existing graph store, available to the lead and the worker:

| Tool | Answers |
|---|---|
| `map_find(name)` | which entity is this, with aliases and attributes |
| `map_neighbors(entity, depth, rel?)` | what does this touch, filtered by relation |
| `map_path(from, to)` | how does this reach that |

`scopedGraphBlock` stays for small graphs — a whole map that fits is the cheapest
possible context — and becomes a summary plus these tools past a size threshold,
which is also the point at which pasting it stops working.

This is the slice that turns the accumulated map from documentation into an
index.

### Slice 3 — trace probes

A probe shape for trace questions, in the prompts rather than in code: while
following a chain, the worker records **each hop** with `link_entities` as it
confirms it. The trace does not just answer the question, it builds the map.

So the first *"when I update this setting, what is called?"* costs a full
grep-and-read traversal, and the next one — for that setting or any setting that
shares a hop — starts from `map_neighbors`.

The prompt has to be explicit that a hop is only recorded when READ, never when
guessed from a name. A graph half-built from plausible-looking edges is worse
than no graph, because the next question trusts it.

### Slice 4 — workspace memory

A workspace-scoped graph namespace and doc set, distinct from every member's.
What lands there is only what no member could have produced alone:

- cross-member edges the lead established by reasoning ("the 02:14 dump error
  corresponds to the deploy in MR 4102")
- which member answered which kind of question well, for scout ranking
- the workspace's own overview: what this composite IS

Member facts stay in member namespaces. The workspace records the **joins**,
which is precisely the part currently thrown away.

### Slice 5 — cross-member timeline

`workspace_timeline(since, until, members?)` — merge time-ordered lines from
evidence members, live-system log reads, and any service member whose tools
expose timestamped events, into one sequence tagged by member.

Last because it is the most expensive and the least useful without slices 1 and
4: merging events across members you have not said are related produces a
correlation nobody asked for.

## Sequencing

1 and 2 together — links make the roster honest, traversal makes the map usable,
and neither is large. 3 immediately after, because it is prompt work that starts
filling the graph traversal depends on. Then 4, then 5.

## Limits worth stating up front

**The graph is advisory, not authoritative.** It is derived from probes, it goes
stale, and it can be wrong. Every answer built from it must cite the file, host
or log line it ultimately rests on, exactly as the doc-staleness discipline
already requires. A traversal that ends in "the graph says so" is a regression.

**No symbol awareness.** Substring search cannot distinguish `foo(` the
definition from `foo(` the call from `foo` in a comment. Real call-graph
tracing needs a parser per language — a much larger piece of work, deliberately
not in this spec, and the reason the FIRST trace of any chain is still expensive.

## Open decisions

- **Does a workspace-level edge get a confidence or an age?** Member docs carry
  staleness; a reasoned cross-member join is a weaker claim than a read file
  and probably needs both. Proposal: age always, confidence when the lead
  inferred it rather than a probe confirming it.
- **Is the workspace graph a separate namespace or a view over members?**
  Proposal: separate. A view cannot hold an edge whose endpoints live in two
  different members' namespaces, which is the only kind worth storing here.
- **Who may write workspace memory — the lead, or a consolidation pass?** The
  per-appliance pattern runs a consolidation agent after each turn. Proposal:
  the same, for the same reason (a lead optimizing for the answer under-records).
