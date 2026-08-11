package servitor

import (
	"context"
	"fmt"
	"strings"
	"sync"

	. "github.com/cmcoffee/gohort/core"
)

// The workspace lead. It has no transport of its own — every fact it reports
// comes from a member, reached through one of three tools: a full investigation
// (the member's own worker, credentials and accumulated knowledge), a cheap
// code search, or the linked knowledge collections. Its job is to decide WHICH
// members a question touches, ask each the right sub-question, and stitch the
// answers into one cross-domain reply.
//
// Drills are read-only. A member investigation dispatched from here auto-denies
// every gated command, so a question asked of a cluster can never mutate three
// hosts at once — and, because each drill gets its own scratch directory that is
// torn down with the run, it cannot leave three hosts littered either. Work that
// needs to change something is done by opening that member directly.

// findMember resolves a lead-supplied member reference — an ID, or a name — to
// a member. Names are matched case-insensitively because the lead reads them
// out of the roster block and will not always reproduce the exact casing.
func findMember(members []wsMember, ref string) (wsMember, bool) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return wsMember{}, false
	}
	for _, m := range members {
		if m.ID == ref {
			return m, true
		}
	}
	for _, m := range members {
		if strings.EqualFold(m.Name(), ref) {
			return m, true
		}
	}
	return wsMember{}, false
}

// memberRefList renders the valid references for an error message, so a bad
// member argument tells the lead exactly what it may use instead.
func memberRefList(members []wsMember) string {
	var out []string
	for _, m := range members {
		out = append(out, fmt.Sprintf("%s (%s)", m.ID, m.Name()))
	}
	return strings.Join(out, ", ")
}

// memberInvestigation runs ONE member's own investigator inline and returns its
// final answer. The member is investigated in its owner's context — its creds,
// its clone, its accumulated docs — via the same runSession that a direct chat
// with that appliance would use, so a drill is not a lesser investigation.
//
// Progress is mirrored into the PARENT session so the operator watches one
// stream rather than hunting for child sessions in the UI.
func (T *Servitor) memberInvestigation(ctx context.Context, parentID, reqUser string, m wsMember, task string) (string, error) {
	task = strings.TrimSpace(task)
	if task == "" {
		return "", fmt.Errorf("task is required")
	}
	label := m.Name() + ": " + task
	if len(label) > 80 {
		label = label[:80]
	}
	sid := "ws-" + UUIDv4()
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	probeSessions.Register(sid, label, cancel).SetOwner(reqUser)
	sessionAppliances.Store(sid, m.ID)
	confirm := make(chan bool, 1)
	confirmChans.Store(sid, confirm)
	RegisterInjectionQueue(sid, "", "")
	// Read-only: feed a denial to every confirmation request for the life of the
	// drill. A workspace question fans out across hosts, so there is no coherent
	// way to ask the operator to approve three concurrent commands — and no
	// reason to, since the answer to a question is never a mutation.
	go func() {
		for {
			select {
			case confirm <- false:
			case <-runCtx.Done():
				return
			}
		}
	}()
	// Marked read-only so a member that resolves tools can withhold the ones
	// needing approval UP FRONT, instead of offering them and denying each call.
	T.runSession(WithReadOnlyDrill(runCtx), sid, reqUser, m.Owner, m.Rec, confirm, []Message{{Role: "user", Content: task}}, m.UDB, false)

	events, _ := probeSessions.SnapshotEvents(sid)
	var reply, errText string
	for _, ev := range events {
		switch ev.Kind {
		case "reply":
			reply = ev.Text
		case "error":
			errText = ev.Text
		}
	}
	if strings.TrimSpace(reply) == "" {
		if strings.TrimSpace(errText) != "" {
			return "", fmt.Errorf("%s", errText)
		}
		return "", fmt.Errorf("no findings returned")
	}
	return strings.TrimSpace(reply), nil
}

// buildWorkspaceLeadPrompt is the coordinator's system prompt: what it is, what
// it may reach, and the roster the scout produced for this question.
func buildWorkspaceLeadPrompt(ws Appliance, scouts []memberScout, missing []string, hasCollections bool) string {
	var b strings.Builder
	writePersona(&b, ws)
	name := ws.Name
	if name == "" {
		name = "this workspace"
	}
	fmt.Fprintf(&b, "You are the investigation lead for **%s**, a workspace spanning %d system(s) and repositor(ies).\n\n", name, len(scouts))
	b.WriteString("You have NO direct access to any of them. Everything you report must come from a tool result in this session:\n\n")
	b.WriteString("- `investigate_member` — dispatch a member's own investigator (its credentials, its accumulated map, its worker). This is the deep tool: it runs real commands on a live system or reads real code in a repo. It is also the expensive one.\n")
	b.WriteString("- `investigate_cluster` — send ONE identical question to several members at once and get their answers side by side. Use it to survey a set of machines in one step: where something lives, which node handles a request, whether a setting matches. When the members are supposed to be configured identically, set `expect_match` and differing values are reported as drift.\n")
	b.WriteString("- `search_code` — grep a repo member's ingested source. Cheap and immediate; prefer it over a full investigation when you only need to find where something is written.\n")
	if hasCollections {
		b.WriteString("- `search_knowledge` — search the curated collections linked to this workspace (runbooks, vendor docs, guides). Reference material, not evidence about these systems: use it to interpret what you find, never as a substitute for finding it.\n")
	}
	b.WriteString("\n")

	b.WriteString("## How to work\n\n")
	b.WriteString("1. **Read the roster below first.** The cheap pass already searched every repo member and read every system member's accumulated map. Members with matches are your starting points; a member with no matches is not necessarily irrelevant, but it needs a reason before you spend an investigation on it.\n")
	b.WriteString("2. **Cross the boundary deliberately.** The value of a workspace is that a live system knows WHAT is happening and a repository knows WHY. A log line found on a host is half an answer until you find the code that emits it; a suspicious function in a repo is half an answer until you confirm what the running system actually does with it.\n")
	b.WriteString("3. **Route by role before you fan out.** The members of a cluster are not interchangeable — a scheduler, a primary database, or a queue consumer commonly lives on exactly ONE node, while other functions run on several. The roster's **Role** and **Known to run** lines say which is which. When a function belongs to one member, `investigate_member` on that member is the right call; fanning out asks two nodes a question they cannot answer and burns the budget doing it.\n")
	b.WriteString("4. **Use `investigate_cluster` to survey, and `expect_match` only for genuine peers.** One call beats three separate dispatches when you are asking every node the same thing. Set `expect_match=true` only for members the roster shows in the SAME role — for nodes with different roles, differences are the design, and reporting them as drift is noise that buries the real answer.\n")
	b.WriteString("5. **A member reporting \"not found\" is not the end of a search.** On a role-split cluster it usually means you asked the wrong node. Re-read the roster and ask the member whose role owns that function before concluding the thing does not exist.\n")
	b.WriteString("6. **One objective per dispatch.** 'Read /etc/nginx/nginx.conf and report the upstream block' — not 'look into nginx'. Pass what you already know so the member's worker does not re-derive it.\n")
	b.WriteString("7. **Escalate to a plan when the question needs several findings that build on each other** — call `set_plan`, work the steps, then answer. Skip the plan when one dispatch settles it.\n\n")

	b.WriteString("## Reporting\n\n")
	b.WriteString("- **Attribute every fact to the member it came from.** In a workspace the same filename, service name, or port exists on several members; an unattributed fact is unusable. Write 'on node-2' or 'in the orchestrator repo', every time.\n")
	b.WriteString("- **Never fabricate.** Every path, port, version, hostname, and config value must be copied character-for-character from a tool result in this session. If you did not receive it, you do not know it.\n")
	b.WriteString("- **Differences between members are the point.** When members disagree, say so explicitly and say which is which. Do not average them into a single description that is true of none of them.\n")
	b.WriteString("- **A comparison finding is a lead, not a verdict.** A value missing from one member's report means that report did not mention it. Confirm with a targeted follow-up before you state a node lacks something.\n")
	b.WriteString("- If a member could not be reached or returned nothing, say so plainly rather than filling the gap.\n\n")

	b.WriteString("## Constraints\n\n")
	b.WriteString("- Investigations dispatched from here are **read-only**: any command that would delete, overwrite, stop a service, mutate a database, or make an outbound call is refused automatically. If answering truly requires one, say what would need to run and on which member, and stop.\n")
	fmt.Fprintf(&b, "- You may investigate at most %d member(s) for a single question. Choose them deliberately.\n\n", workspace_drill_cap())

	if strings.TrimSpace(ws.Instructions) != "" {
		b.WriteString("## Operator instructions\n\n")
		b.WriteString(strings.TrimSpace(ws.Instructions))
		b.WriteString("\n\n")
	}
	b.WriteString(scoutBlock(scouts, missing))
	return b.String()
}

// workspaceLeadTools builds the coordinator's tool group. drilled is shared
// across the whole turn so the cap counts every member investigation, however
// it was dispatched.
func (T *Servitor) workspaceLeadTools(ctx context.Context, id, userID string, ws Appliance, members []wsMember) []AgentToolDef {
	var mu sync.Mutex
	drilled := make(map[string]bool)

	// claimDrill reserves budget for investigating a member. Re-investigating a
	// member already drilled this turn is free — the cap is on breadth, not on
	// follow-up questions to a member already in play.
	claimDrill := func(memberID string) error {
		mu.Lock()
		defer mu.Unlock()
		if drilled[memberID] {
			return nil
		}
		if len(drilled) >= workspace_drill_cap() {
			return fmt.Errorf("drill cap reached: %d member(s) already investigated this question (%s). Answer from what you have, and say which members you did not reach and why",
				len(drilled), strings.Join(mapKeys(drilled), ", "))
		}
		drilled[memberID] = true
		return nil
	}

	investigate_member := AgentToolDef{
		Tool: Tool{
			Name: "investigate_member",
			Description: "Dispatch a full investigation into ONE member of this workspace, using that member's own credentials, accumulated map, and worker. " +
				"For a live system the worker runs commands on it; for a repo it searches and reads the code. Read-only — destructive commands are refused. " +
				"Slow and expensive: use search_code first when you only need to locate something.",
			Parameters: map[string]ToolParam{
				"member": {Type: "string", Description: "Member ID (or exact name) from the roster."},
				"task":   {Type: "string", Description: "One clear objective: find X, read Y, verify Z. Not a topic."},
				"context": {Type: "string",
					Description: "What you already know that is relevant — paths, ports, service names, findings from other members — so the worker does not re-derive it."},
			},
			Required: []string{"member", "task"},
		},
		Handler: func(args map[string]any) (string, error) {
			ref, _ := args["member"].(string)
			m, ok := findMember(members, ref)
			if !ok {
				return "", fmt.Errorf("no member %q in this workspace. Valid members: %s", ref, memberRefList(members))
			}
			task, _ := args["task"].(string)
			if strings.TrimSpace(task) == "" {
				return "", fmt.Errorf("task is required")
			}
			if err := claimDrill(m.ID); err != nil {
				emit(id, probeEvent{Kind: "status", Text: "Drill cap reached — refusing further investigations this question."})
				return "[REFUSED] " + err.Error(), nil
			}
			if extra, _ := args["context"].(string); strings.TrimSpace(extra) != "" {
				task = "## Known context\n\n" + strings.TrimSpace(extra) + "\n\n## Task\n\n" + task
			}
			emit(id, probeEvent{Kind: "intent", Text: fmt.Sprintf("%s → %s", m.Name(), firstLine(task)), Reason: m.Kind() + " member " + m.ID})
			var result string
			var err error
			withHeartbeat(ctx, id, "Investigating "+m.Name(), func() {
				result, err = T.memberInvestigation(ctx, id, userID, m, task)
			})
			if err != nil {
				emit(id, probeEvent{Kind: "status", Text: fmt.Sprintf("%s: %s", m.Name(), err)})
				return fmt.Sprintf("[%s — NO FINDINGS] %v", m.Name(), err), nil
			}
			result = capText(result, 12000)
			emit(id, probeEvent{Kind: "output", Text: result})
			return fmt.Sprintf("## Findings from %s (`%s`)\n\n%s", m.Name(), m.ID, result), nil
		},
		NeedsConfirm: false,
	}

	investigate_cluster := AgentToolDef{
		Tool: Tool{
			Name: "investigate_cluster",
			Description: "Ask SEVERAL members the SAME question at once and get their answers side by side. " +
				"Use it to survey a set of machines in one step — where something lives, which node handles a request, whether a setting matches. " +
				"Identical wording across members is what makes the answers comparable: pass one question, not one per node. " +
				"Set expect_match when the members are supposed to be configured identically and you want differences reported as drift.",
			Parameters: map[string]ToolParam{
				"members": {Type: "array", Description: "Member IDs (or exact names) to ask. Two or more.", Items: &ToolParam{Type: "string"}},
				"task":    {Type: "string", Description: "The single question to put to every listed member, worded so it means the same thing on each."},
				"context": {Type: "string", Description: "Shared context for all of them — what you already know."},
				"expect_match": {Type: "boolean",
					Description: "True when these members should be configured identically, so values missing from some are worth flagging as drift. " +
						"False (default) when they have different roles — there, differences are the design and flagging them is noise. Check the roster's Role lines before setting this."},
			},
			Required: []string{"members", "task"},
		},
		Handler: func(args map[string]any) (string, error) {
			refs := stringList(args["members"])
			if len(refs) < 2 {
				return "", fmt.Errorf("investigate_cluster needs at least 2 members; use investigate_member for one")
			}
			task, _ := args["task"].(string)
			if strings.TrimSpace(task) == "" {
				return "", fmt.Errorf("task is required")
			}
			if extra, _ := args["context"].(string); strings.TrimSpace(extra) != "" {
				task = "## Known context\n\n" + strings.TrimSpace(extra) + "\n\n## Task\n\n" + task
			}
			var targets []wsMember
			var unknown, refused []string
			picked := make(map[string]bool)
			for _, ref := range refs {
				m, ok := findMember(members, ref)
				if !ok {
					unknown = append(unknown, ref)
					continue
				}
				if picked[m.ID] {
					continue // the same member named twice is one query, not two
				}
				if err := claimDrill(m.ID); err != nil {
					refused = append(refused, m.Name())
					continue
				}
				picked[m.ID] = true
				targets = append(targets, m)
			}
			if len(targets) < 2 && len(unknown) == 0 && len(refused) == 0 {
				return "", fmt.Errorf("investigate_cluster needs at least 2 DISTINCT members; use investigate_member for one")
			}
			if len(targets) == 0 {
				if len(refused) > 0 {
					emit(id, probeEvent{Kind: "status", Text: "Drill cap reached — fan-out refused."})
					return fmt.Sprintf("[REFUSED] Drill cap reached before any of these members could be queried: %s. Answer from what you already have, and say which members you did not reach.", strings.Join(refused, ", ")), nil
				}
				return "", fmt.Errorf("none of %q match a member of this workspace. Valid members: %s",
					strings.Join(unknown, ", "), memberRefList(members))
			}
			emit(id, probeEvent{Kind: "intent",
				Text:   fmt.Sprintf("Fan-out to %d members → %s", len(targets), firstLine(task)),
				Reason: strings.Join(memberNames(targets), ", ")})

			// Bounded fan-out: every node holds an SSH session and an LLM worker
			// while it runs, so width is a knob rather than "all of them at once".
			results := make([]string, len(targets))
			errs := make([]error, len(targets))
			lg := NewLimitGroup(workspace_parallel())
			withHeartbeat(ctx, id, fmt.Sprintf("Querying %d members", len(targets)), func() {
				for i, m := range targets {
					if ctx.Err() != nil {
						break
					}
					lg.Add(1) // blocks until a slot frees, bounding concurrency
					go func(idx int, mem wsMember) {
						defer lg.Done()
						results[idx], errs[idx] = T.memberInvestigation(ctx, id, userID, mem, task)
					}(i, m)
				}
				lg.Wait()
			})

			// Label by name, disambiguated with the ID when two members share one —
			// a comparison that says "node-1 differs" is useless if two members
			// are both called node-1.
			labels := nodeLabels(targets)
			perNode := make(map[string]string)
			var b strings.Builder
			for i, m := range targets {
				fmt.Fprintf(&b, "## %s (`%s`)\n\n", m.Name(), m.ID)
				if errs[i] != nil {
					fmt.Fprintf(&b, "NO FINDINGS — %v\n\n", errs[i])
					continue
				}
				text := capText(strings.TrimSpace(results[i]), 8000)
				perNode[labels[i]] = text
				b.WriteString(text)
				b.WriteString("\n\n")
			}
			if len(unknown) > 0 || len(refused) > 0 {
				b.WriteString("## Not queried\n\n")
				if len(unknown) > 0 {
					fmt.Fprintf(&b, "- Not a member of this workspace: %s. Valid members: %s\n", strings.Join(unknown, ", "), memberRefList(members))
				}
				if len(refused) > 0 {
					fmt.Fprintf(&b, "- Drill cap reached before reaching: %s\n", strings.Join(refused, ", "))
					emit(id, probeEvent{Kind: "status", Text: "Drill cap reached — skipped " + strings.Join(refused, ", ")})
				}
				b.WriteString("\nThe comparison below covers only the members that answered.\n\n")
			}
			// Drift analysis only when the lead says these members are supposed to
			// match. On a cluster whose nodes have distinct roles, every value
			// belonging to a single-node function would otherwise be reported as
			// something the other nodes are "missing" — the design of the cluster
			// rendered as its anomalies.
			expectMatch, _ := args["expect_match"].(bool)
			if expectMatch {
				b.WriteString(divergence_report(perNode))
			} else if len(perNode) > 1 {
				b.WriteString("\n---\n\nThese members were not declared to be identical, so no drift comparison was run — differences between them are expected. Compare their answers against each member's Role in the roster: a difference is only a finding when it contradicts what that member is FOR. If you do believe these members should match, call again with expect_match=true.\n")
			}
			out := b.String()
			emit(id, probeEvent{Kind: "output", Text: out})
			return out, nil
		},
		NeedsConfirm: false,
	}

	search_code := AgentToolDef{
		Tool: Tool{
			Name:        "search_code",
			Description: "Search a repo member's ingested source for a literal string and return matching lines with file paths. Immediate and cheap — use it to locate code before deciding whether a full investigation is warranted.",
			Parameters: map[string]ToolParam{
				"member": {Type: "string", Description: "Repo member ID (or exact name) from the roster."},
				"query":  {Type: "string", Description: "Literal text to find — a log string, function name, config key. Not a question."},
			},
			Required: []string{"member", "query"},
		},
		Handler: func(args map[string]any) (string, error) {
			ref, _ := args["member"].(string)
			m, ok := findMember(members, ref)
			if !ok {
				return "", fmt.Errorf("no member %q in this workspace. Valid members: %s", ref, memberRefList(members))
			}
			if m.Kind() != "repo" {
				alt := "investigate_member"
				if m.Kind() == "evidence" {
					alt = "search_evidence"
				}
				return "", fmt.Errorf("%s is a %s member, not a repo — use %s for it", m.Name(), m.Kind(), alt)
			}
			query, _ := args["query"].(string)
			if strings.TrimSpace(query) == "" {
				return "", fmt.Errorf("query is required")
			}
			hits := searchRepo(m.Owner, m.ID, query, 40)
			emit(id, probeEvent{Kind: "status", Text: fmt.Sprintf("search_code %s %q: %d hit(s)", m.Name(), query, len(hits))})
			if len(hits) == 0 {
				return fmt.Sprintf("No matches for %q in %s. The string is not present in the ingested source — do not infer that it is there anyway.", query, m.Name()), nil
			}
			var b strings.Builder
			fmt.Fprintf(&b, "%d match(es) for %q in %s (`%s`):\n\n", len(hits), query, m.Name(), m.ID)
			for _, h := range hits {
				fmt.Fprintf(&b, "- `%s:%d` — %s\n", h.Path, h.Line, h.Text)
			}
			return b.String(), nil
		},
		NeedsConfirm: false,
	}

	// search_evidence is the bundle analogue of search_code: a cheap, immediate
	// look INSIDE a member before deciding whether a full investigation is
	// warranted. Evidence members need their own because a dump's content is not
	// code and its search takes a time window, which is usually the whole point
	// of asking it anything.
	search_evidence := AgentToolDef{
		Tool: Tool{
			Name:        "search_evidence",
			Description: "Search an evidence member's ingested logs for a regular expression and return matching lines with file paths and line numbers. Immediate and cheap — use it to check whether a dump even mentions something before dispatching a full investigation.",
			Parameters: map[string]ToolParam{
				"member":  {Type: "string", Description: "Evidence member ID (or exact name) from the roster."},
				"pattern": {Type: "string", Description: "Regular expression to find — an error string, a request id, a hostname. Not a question."},
				"since":   {Type: "string", Description: "Optional earliest timestamp, e.g. \"2026-03-14 02:00:00\"."},
				"until":   {Type: "string", Description: "Optional latest timestamp, same format."},
			},
			Required: []string{"member", "pattern"},
		},
		Handler: func(args map[string]any) (string, error) {
			ref, _ := args["member"].(string)
			m, ok := findMember(members, ref)
			if !ok {
				return "", fmt.Errorf("no member %q in this workspace. Valid members: %s", ref, memberRefList(members))
			}
			if m.Kind() != "evidence" {
				return "", fmt.Errorf("%s is a %s member, not an evidence bundle — use search_code for a repo, or investigate_member otherwise", m.Name(), m.Kind())
			}
			pattern, _ := args["pattern"].(string)
			if strings.TrimSpace(pattern) == "" {
				return "", fmt.Errorf("pattern is required")
			}
			q := bundleQuery{Pattern: pattern, MaxHits: 40}
			var terr error
			if q.Since, terr = parseBundleArgTime(strings.TrimSpace(fmt.Sprint(args["since"]))); terr != nil && args["since"] != nil {
				return "", terr
			}
			if q.Until, terr = parseBundleArgTime(strings.TrimSpace(fmt.Sprint(args["until"]))); terr != nil && args["until"] != nil {
				return "", terr
			}
			res, err := searchBundle(m.Owner, m.ID, q)
			if err != nil {
				return "", err
			}
			emit(id, probeEvent{Kind: "status", Text: fmt.Sprintf("search_evidence %s %q: %d hit(s)", m.Name(), pattern, len(res.Hits))})
			if len(res.Hits) == 0 {
				return fmt.Sprintf("No matches for %q in %s across %d file(s) scanned. The bundle does not contain that text — but say whether the file that WOULD carry it is even in the bundle before concluding it did not happen.", pattern, m.Name(), res.Scanned), nil
			}
			var b strings.Builder
			fmt.Fprintf(&b, "%d match(es) for %q in %s (`%s`):\n\n", len(res.Hits), pattern, m.Name(), m.ID)
			for _, h := range res.Hits {
				fmt.Fprintf(&b, "- `%s:%d` — %s\n", h.Path, h.Line, h.Text)
			}
			if res.Truncated {
				b.WriteString("\nTRUNCATED — there are more matches than shown. Treat this as a lower bound, not a count.\n")
			}
			return b.String(), nil
		},
		NeedsConfirm: false,
	}

	tools := []AgentToolDef{investigate_member, investigate_cluster, search_code, search_evidence}

	if len(ws.Collections) > 0 {
		tools = append(tools, AgentToolDef{
			Tool: Tool{
				Name:        "search_knowledge",
				Description: "Search the curated knowledge collections linked to this workspace (runbooks, vendor documentation, guides). Reference material only — it says nothing about the current state of these systems.",
				Parameters: map[string]ToolParam{
					"query": {Type: "string", Description: "What to look for."},
				},
				Required: []string{"query"},
			},
			Handler: func(args map[string]any) (string, error) {
				query, _ := args["query"].(string)
				if strings.TrimSpace(query) == "" {
					return "", fmt.Errorf("query is required")
				}
				hits := SearchCollections(ctx, CollectionsDB(), userID, ws.Collections, query, 6)
				emit(id, probeEvent{Kind: "status", Text: fmt.Sprintf("search_knowledge %q: %d passage(s)", query, len(hits))})
				if len(hits) == 0 {
					return fmt.Sprintf("No passages in the linked collections matched %q.", query), nil
				}
				var b strings.Builder
				fmt.Fprintf(&b, "%d passage(s) from linked knowledge for %q:\n\n", len(hits), query)
				for i, h := range hits {
					label := strings.TrimSpace(h.Title)
					if label == "" {
						label = h.Source
					}
					fmt.Fprintf(&b, "%d. [%s] %s\n\n", i+1, label, strings.TrimSpace(h.Text))
				}
				return b.String(), nil
			},
			NeedsConfirm: false,
		})
	}
	return tools
}

// ── helpers ──────────────────────────────────────────────────────────────────

// mapKeys returns a map's keys — for naming the members already drilled in a
// cap-refusal message.
func mapKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// nodeLabels names each member for the cross-node comparison, appending the ID
// only where a bare name would be ambiguous.
func nodeLabels(members []wsMember) []string {
	count := make(map[string]int, len(members))
	for _, m := range members {
		count[m.Name()]++
	}
	out := make([]string, len(members))
	for i, m := range members {
		if count[m.Name()] > 1 {
			out[i] = fmt.Sprintf("%s (%s)", m.Name(), m.ID)
			continue
		}
		out[i] = m.Name()
	}
	return out
}

// memberNames renders a member slice for a status line.
func memberNames(members []wsMember) []string {
	out := make([]string, 0, len(members))
	for _, m := range members {
		out = append(out, m.Name())
	}
	return out
}

// stringList coerces an LLM-supplied array argument to []string, tolerating a
// single bare string (models routinely send one when the schema says array).
func stringList(v any) []string {
	switch t := v.(type) {
	case []string:
		return t
	case string:
		if s := strings.TrimSpace(t); s != "" {
			return []string{s}
		}
	case []any:
		var out []string
		for _, item := range t {
			if s, ok := item.(string); ok && strings.TrimSpace(s) != "" {
				out = append(out, strings.TrimSpace(s))
			}
		}
		return out
	}
	return nil
}

// firstLine is the one-line form of a task, for status events.
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	s = strings.TrimSpace(s)
	if len(s) > 80 {
		return s[:80] + "…"
	}
	return s
}

// capText truncates tool output, saying so, so a verbose member cannot crowd
// the lead's context out.
func capText(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "\n… [truncated]"
}
