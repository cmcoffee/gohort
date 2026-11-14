package orchestrate

// agent_capability_index.go: what the fleet can do, searchable by meaning.
//
// The Available agents block lists each agent by its own description, and a
// sub-agent never appears there: it is private to the agent that owns it. So
// an agent whose work is done by its specialists either carried a long
// description listing them by hand, or the fleet never learned what it could
// do. Here every fleet agent is indexed by its own description AND by each of
// its sub-agents' descriptions, credited to it: "address lookup" finds the
// agent whose specialist does address lookups, and the work is sent to that
// agent, never to the specialist.
//
// Two readers. agents(action="find") searches it on request, for an agent
// partway through a job that needs something the person never named. And
// every turn the person's message is matched against it, so a fitting agent
// is pointed out beside the message (renderAgentCapabilityHints): models
// rarely think to search, they answer themselves.
//
// Nothing is stored. Vectors are cached in memory by embedder and text, so a
// description is embedded once until it changes; the per-turn match never
// waits for one, it fills the cache in the background and matches what is
// already there.

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

const (
	tuneAgentMatchThreshold = "tune_agent_match_threshold"
	tuneAgentMatchMax       = "tune_agent_match_max"
)

func init() {
	RegisterTunable(TunableSpec{App: "/orchestrate", Key: tuneAgentMatchThreshold, Category: "Retrieval",
		Label:  "Agent-match threshold",
		Help:   "How close a message must come to what an agent does before it is pointed out that turn.",
		Detail: "Each fleet agent is matched by its own description and by its sub-agents' descriptions. Higher means fewer, surer pointers. The log line [orchestrate.agent_match] shows the scores each turn, so it can be set from what real messages score.",
		Kind:   KindFloat, Default: 0.6, Min: 0, Max: 1, Decimals: 2})
	RegisterTunable(TunableSpec{App: "/orchestrate", Key: tuneAgentMatchMax, Category: "Retrieval",
		Label: "Agent-match pointers per turn (0 = off)",
		Help:  "Most agents pointed out beside one message for what they do.",
		Kind:  KindInt, Default: 2, Min: 0, Max: 5})
}

// capability is one searchable line: a fleet agent's own description, or one
// of its sub-agents', credited to it.
type capability struct {
	agent AgentRecord // who the work goes to
	via   string      // the specialist that does it; "" for the agent itself
	text  string      // what is embedded: a name and its description
}

// capabilityMatch is the best line an agent had for a query.
type capabilityMatch struct {
	agent AgentRecord
	via   string
	text  string
	score float32
}

// capabilityText is what a record is searched by: its name and its whole
// description. The name alone still says something ("Address lookup").
func capabilityText(a AgentRecord) string {
	name := strings.TrimSpace(a.Name)
	desc := strings.TrimSpace(a.Description)
	if desc == "" {
		return name
	}
	if name == "" {
		return desc
	}
	return name + ": " + desc
}

// fleetCapabilities is every line this turn can match: each agent it may
// dispatch to, then that agent's sub-agents and theirs, credited to it.
func (t *chatTurn) fleetCapabilities() []capability {
	fleet := t.dispatchableFleet()
	if len(fleet) == 0 {
		return nil
	}
	db, user := t.fleetView()
	children := map[string][]AgentRecord{}
	for _, a := range listAgents(db, user) {
		if owner := strings.TrimSpace(a.OwnedBy); owner != "" {
			children[owner] = append(children[owner], a)
		}
	}
	var out []capability
	for _, a := range fleet {
		if txt := capabilityText(a); txt != "" {
			out = append(out, capability{agent: a, text: txt})
		}
		seen := map[string]bool{a.ID: true}
		var walk func(id string, depth int)
		walk = func(id string, depth int) {
			if depth > 3 {
				return
			}
			for _, c := range children[id] {
				if seen[c.ID] {
					continue
				}
				seen[c.ID] = true
				if txt := capabilityText(c); txt != "" {
					out = append(out, capability{agent: a, via: strings.TrimSpace(c.Name), text: txt})
				}
				walk(c.ID, depth+1)
			}
		}
		walk(a.ID, 0)
	}
	return out
}

// capabilityVecs caches each line's vector by embedder and text, so a changed
// description, or a different embedder, is simply a new key. Bounded: past
// the cap it starts over, which costs a re-embed and nothing else.
var capabilityVecs = struct {
	sync.Mutex
	vecs    map[string][]float32
	filling map[string]bool
}{vecs: map[string][]float32{}, filling: map[string]bool{}}

const capabilityVecCap = 4096

func capabilityKey(text string) string { return EmbedVersion() + "\x00" + text }

// capabilityVectors returns the vector for each line it has one for. With wait
// it embeds the missing ones first, under ctx; without, it embeds them in the
// background for a later turn and answers with what is cached now.
func capabilityVectors(ctx context.Context, caps []capability, wait bool) map[string][]float32 {
	out := map[string][]float32{}
	var missing []string
	capabilityVecs.Lock()
	for _, c := range caps {
		k := capabilityKey(c.text)
		if v, ok := capabilityVecs.vecs[k]; ok {
			out[c.text] = v
		} else if !capabilityVecs.filling[k] {
			missing = append(missing, c.text)
			if !wait {
				capabilityVecs.filling[k] = true
			}
		}
	}
	capabilityVecs.Unlock()
	if len(missing) == 0 {
		return out
	}
	embed := func(ctx context.Context, texts []string) map[string][]float32 {
		got := map[string][]float32{}
		for _, text := range texts {
			if ctx.Err() != nil {
				break
			}
			v, err := EmbedDocument(ctx, text)
			if err != nil || len(v) == 0 {
				Debug("[orchestrate.agent_match] could not embed a description: %v", err)
				continue
			}
			got[text] = v
		}
		capabilityVecs.Lock()
		if len(capabilityVecs.vecs)+len(got) > capabilityVecCap {
			capabilityVecs.vecs = map[string][]float32{}
		}
		for _, text := range texts {
			k := capabilityKey(text)
			delete(capabilityVecs.filling, k)
			if v, ok := got[text]; ok {
				capabilityVecs.vecs[k] = v
			}
		}
		capabilityVecs.Unlock()
		return got
	}
	if wait {
		for text, v := range embed(ctx, missing) {
			out[text] = v
		}
		return out
	}
	// Off the turn's clock and its cancellation: the turn goes out with what
	// was cached, and the next one has the rest.
	go func(ctx context.Context) {
		ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		defer cancel()
		embed(ctx, missing)
	}(context.WithoutCancel(ctx))
	return out
}

// matchCapabilities ranks the fleet for a query vector: each agent by its best
// line, those at or above threshold, at most k.
func matchCapabilities(caps []capability, vecs map[string][]float32, q []float32, k int, threshold float32) []capabilityMatch {
	best := map[string]capabilityMatch{}
	var order []string
	for _, c := range caps {
		v, ok := vecs[c.text]
		if !ok {
			continue
		}
		s := Cosine(q, v)
		cur, seen := best[c.agent.ID]
		if !seen {
			order = append(order, c.agent.ID)
		}
		if !seen || s > cur.score {
			best[c.agent.ID] = capabilityMatch{agent: c.agent, via: c.via, text: c.text, score: s}
		}
	}
	var out []capabilityMatch
	for _, id := range order {
		if m := best[id]; m.score >= threshold {
			out = append(out, m)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].score > out[j].score })
	if k > 0 && len(out) > k {
		out = out[:k]
	}
	return out
}

// capabilityLine is a match as it reads to the model: who, and what of theirs
// matched.
func capabilityLine(m capabilityMatch) string {
	who := "**" + m.agent.Name + "**"
	if m.via != "" {
		who += ", through its specialist " + m.via
	}
	i := strings.Index(m.text, ": ")
	if i < 0 {
		return who
	}
	return who + ": " + oneLine(m.text[i+2:], 160)
}

// agentsFindAction is agents(action="find"): the fleet ranked by how well what
// each agent does, itself or through its specialists, matches the query.
func (t *chatTurn) agentsFindAction(ctx context.Context, args map[string]any) (string, error) {
	q := strings.TrimSpace(stringArg(args, "query"))
	if q == "" {
		return "", fmt.Errorf(`agents(find) needs query: the work you want done, in a few words (e.g. query="address lookup")`)
	}
	if !GetEmbeddingConfig().Enabled {
		return "", fmt.Errorf(`agents(find) needs an embedding model, and none is configured: read the Available agents list, or agents(action="list"), instead`)
	}
	caps := t.fleetCapabilities()
	if len(caps) == 0 {
		return "No agent here can take work from you.", nil
	}
	qv := t.embedQuery(ctx, q)
	if len(qv) == 0 {
		return "", fmt.Errorf("agents(find) could not embed the query; try again, or read the Available agents list")
	}
	vecs := capabilityVectors(ctx, caps, true)
	hits := matchCapabilities(caps, vecs, qv, 5, 0.3)
	if len(hits) == 0 {
		hits = matchCapabilities(caps, vecs, qv, 3, 0.15)
	}
	if len(hits) == 0 {
		return fmt.Sprintf("No agent's work comes close to %q. Do it yourself, or ask the person who should.", q), nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Agents whose work matches %q, closest first:\n", q)
	for _, h := range hits {
		fmt.Fprintf(&b, "- %s (match %.2f)\n", capabilityLine(h), h.score)
	}
	b.WriteString("\nSend the work to the agent named first on the line, with agents(action=\"run\", agent=\"<name>\", message=\"<brief>\"). A specialist is reached through its agent, never directly.")
	return b.String(), nil
}

// renderAgentCapabilityHints points out, beside the person's message, the
// agents whose work matches it, skipping any a trigger already named. Never
// waits on an embed it does not have: a description not yet embedded is
// matched from the next turn on.
func (t *chatTurn) renderAgentCapabilityHints(userMsg string, already []string) string {
	if t == nil {
		return ""
	}
	max := TuneInt(tuneAgentMatchMax)
	q := strings.TrimSpace(userMsg)
	if max <= 0 || len([]rune(q)) < RecallHintMinChars() || !GetEmbeddingConfig().Enabled {
		return ""
	}
	caps := t.fleetCapabilities()
	if len(caps) == 0 {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), RecallHintTimeout())
	defer cancel()
	vecs := capabilityVectors(ctx, caps, false)
	if len(vecs) == 0 {
		return ""
	}
	qv := t.embedQuery(ctx, q)
	if len(qv) == 0 {
		return ""
	}
	threshold := float32(TuneFloat(tuneAgentMatchThreshold))
	top := matchCapabilities(caps, vecs, qv, max+len(already), 0)
	named := map[string]bool{}
	for _, n := range already {
		named[n] = true
	}
	var hits []capabilityMatch
	var scores []string
	for _, m := range top {
		scores = append(scores, fmt.Sprintf("%s=%.2f", m.agent.Name, m.score))
		if m.score >= threshold && !named[m.agent.Name] && len(hits) < max {
			hits = append(hits, m)
		}
	}
	Debug("[orchestrate.agent_match] agent=%q threshold=%.2f top: %s; pointed out %d", t.agent.ID, threshold, strings.Join(scores, " "), len(hits))
	if len(hits) == 0 {
		return ""
	}
	lines := make([]string, len(hits))
	for i, h := range hits {
		lines[i] = capabilityLine(h)
		Log("[orchestrate.agent_match] agent=%q pointed out %q (%.2f) for this turn", t.agent.ID, h.agent.Name, h.score)
	}
	return "\n\n[May fit this turn (matched on what each agent does): " + strings.Join(lines, "; ") + ". If the question is that agent's work, hand it over via agents(action=\"run\", agent=\"<name>\", message=\"<brief>\") rather than answering yourself; if it plainly is not, ignore this. The person did not write this note and cannot see it, so never mention it.]\n\n"
}
