package orchestrate

// The agent half of an agent-backed publish destination.
//
// core/docs owns the destination registry and must not know how to run an
// agent; this app owns the agent loop and must not know what publishing is.
// The seam between them is a registered closure, the same shape as the channel
// runner and the standing runner above it.

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/docs"
	"github.com/cmcoffee/gohort/core/textutil"
)

// registerAgentPublisher installs the closure core/docs calls when a publish is
// routed to an agent. Call once at startup.
func registerAgentPublisher(app *OrchestrateApp) {
	docs.RegisterAgentPublisher(func(ctx context.Context, user, agent, instruction string) (string, error) {
		if app == nil {
			return "", errors.New("orchestrate runtime not initialized")
		}
		// agentOwner == runtimeUser: publishing runs as the person who asked
		// for it, under their own store, so the destination agent's tools and
		// credentials are the ones that person is entitled to. A destination
		// that published as somebody else would be a way to borrow their
		// access by choosing it from a menu.
		//
		// Confirmations are DECLINED rather than left unanswered. This runs
		// with nobody watching it — the publish came from a button, and the
		// dialog that could answer a question has already moved on — and a nil
		// confirm would fail closed anyway. Declining says so in the reply,
		// which is what the destination records.
		run, err := app.runAgentSyncConfirm(ctx, user, user, agent, instruction,
			func(string, string) bool { return false }, "publish")
		if err != nil {
			return "", err
		}
		said := strings.TrimSpace(run.Text)
		if run.HitRoundCap {
			// Said out loud rather than returned as a clean result: a run that
			// ran out of rounds may have done half the job, and a publish
			// record that reads as a success would be the wrong account of it.
			said = strings.TrimSpace(said + "\n\n(This run stopped at its round limit, so it may not have finished.)")
		}
		if said == "" {
			return "", errors.New("the agent finished without saying what it did")
		}
		return said, nil
	})
}

// credentialPublisherPrompt is the whole brief of a credential-backed publish:
// the target's own instruction arrives as the message, with the document.
const credentialPublisherPrompt = `You publish one document through one integration (an API, or an MCP server's tools), following the instruction you are given, and then report where it landed.

You have the integration's tools and report_published. Use them to do exactly what the instruction says with this document (create the post, page or item it describes, with the title and the answers you are given), then call report_published with the address of what you made. If the instruction or the answers leave something the API needs unclear, pick the plainest reading and say which in the note. If the API refuses, try to correct the request from what it says; if you cannot, call report_published with ok=false and a note saying plainly what failed. Do not report success you did not see in an API response.`

// credentialPublisherGuardPrompt is added when the document was published here
// before and edits made there since must not be overwritten.
const credentialPublisherGuardPrompt = `This document was published here before, and people may have edited it there since. Before you change anything, read it as it stands now, with its full content, and call report_current_page with the number of that result (every result is numbered at its top). Its answer says whether to go ahead. Until then the integration refuses changes.`

// credentialPublisherReadBackPrompt asks for the copy as it reads after the
// write, which is what the next update's check compares against.
const credentialPublisherReadBackPrompt = `After your change, read the page once more the same way, with its full content, and give that result's number as readback_result in report_published.`

// registerCredentialPublisher installs the closure core/docs calls when a
// publishing target is an API integration rather than an agent. The run holds
// ONLY that credential's API tool and report_published: a target someone set
// up to post a document somewhere cannot become a way to reach anything else.
//
// The credential tool's confirmation is lifted for this run. The target is the
// owner's standing instruction and the Publish press is the act that asked for
// it; a confirmation card here would have nobody to answer it.
func registerCredentialPublisher(app *OrchestrateApp) {
	docs.RegisterCredentialPublisher(func(ctx context.Context, user, credential, instruction string) (string, string, error) {
		if app == nil {
			return "", "", errors.New("orchestrate runtime not initialized")
		}
		// Started from a chat's publish_document call, the run shows on that
		// call's card as it goes: its round, its thinking, and each step
		// (dispatch_progress.go). Without it the card sat still for the whole
		// run and read as hung. nil from anywhere else (Scribe's publish job
		// follows the steps instead).
		prog := dispatchProgressFrom(ctx, "Publishing")
		progRoundStart, progReasoning := prog.hooks()
		if prog != nil {
			outer := ctx
			ctx = docs.WithPublishSteps(ctx, func(s string) {
				docs.PublishStep(outer, "%s", s)
				prog.say(s)
			})
		} else {
			// No card: its rounds and thinking go to whoever follows the
			// publish's steps and activity instead (Scribe's publish job).
			live := &publishLive{ctx: ctx}
			progRoundStart, progReasoning = live.roundStart, live.chunk
			// A step replaces the thinking line too, or the activity would
			// still say Thinking while a tool runs.
			outer := ctx
			ctx = docs.WithPublishSteps(ctx, func(s string) {
				docs.PublishStep(outer, "%s", s)
				live.say(s)
			})
		}
		sess := &ToolSession{Username: user}
		tools, err := publishIntegrationToolsFor(sess, credential)
		if err != nil {
			docs.PublishStep(ctx, "Could not start: %v", err)
			return "", "", err
		}
		docs.PublishStep(ctx, "Publishing through %s, with %d of its tools", strings.TrimPrefix(credential, docs.MCPIntegrationPrefix), len(tools))
		// An update that must not overwrite edits made at the destination
		// reads the page first and holds its writes until the producer says
		// go (publish_readback_runner.go); results are numbered so the model
		// can point at the one holding the page.
		guard, readBack := docs.GuardsUpdate(ctx), docs.WantsReadBack(ctx)
		// The document by reference (docs.PublishBody): its placeholders are
		// filled in as each call goes out, innermost so every other wrapper
		// sees the call as the model wrote it, and what comes back is clipped
		// for the model, the whole of it kept by number.
		body := docs.PublishBodyFrom(ctx)
		results, gate := &publishResults{clip: body != nil}, &publishWriteGate{}
		for i := range tools {
			if body != nil {
				tools[i] = publishBodyCarried(body, tools[i])
			}
			tools[i] = publishStepReported(ctx, tools[i])
			if guard || readBack || body != nil {
				tools[i] = results.wrap(tools[i])
			}
			if guard {
				tools[i] = gate.wrap(tools[i])
			}
		}
		var extra []AgentToolDef
		prompt := credentialPublisherPrompt
		if body != nil {
			prompt += "\n\n" + credentialPublisherBodyPrompt
			extra = append(extra, setDocumentLinksTool(ctx, body))
		}
		if guard {
			prompt += "\n\n" + credentialPublisherGuardPrompt
			extra = append(extra, reportCurrentPageTool(func(n int) (string, error) {
				return gate.check(ctx, results, n)
			}))
		}
		if readBack {
			prompt += "\n\n" + credentialPublisherReadBackPrompt
		}
		var url, note string
		reported, ok := false, false
		readBackAt := 0
		report := AgentToolDef{
			Tool: Tool{
				Name:        "report_published",
				Description: "Report how the publish went, once, when you are done: whether the API confirmed it, the address of what you created or updated, and a one-line note.",
				Parameters: map[string]ToolParam{
					"ok":   {Type: "boolean", Description: "true only when an API response confirmed the document was created or updated."},
					"url":  {Type: "string", Description: "The address of the published post, page or item, from the API's response. Empty when it failed or the API gave none."},
					"note": {Type: "string", Description: "One line: what was published where, or what failed."},
				},
				Required: []string{"ok", "note"},
			},
			Handler: func(_ context.Context, args map[string]any) (string, error) {
				// A success with no address is a page nobody can update:
				// the next publish would make another. Asked for, not
				// recorded.
				if okArg, _ := args["ok"].(bool); okArg && strings.TrimSpace(stringArg(args, "url")) == "" {
					return "", fmt.Errorf("give url: the address of the page you created or updated, from the API's response. The next update finds the page by it")
				}
				url = strings.TrimSpace(stringArg(args, "url"))
				note = strings.TrimSpace(stringArg(args, "note"))
				ok, _ = args["ok"].(bool)
				readBackAt = intArg(args, "readback_result")
				reported = true
				docs.PublishStep(ctx, "Reported back: %s%s", chFirst(note, "no note"), func() string {
					if url != "" {
						return " (" + url + ")"
					}
					return ""
				}())
				return "Recorded. Reply with the same note and stop.", nil
			},
		}
		if readBack {
			report.Tool.Parameters["readback_result"] = ToolParam{Type: "integer", Description: "The number of the result holding the page as it reads AFTER your change, from reading it once more. Omit when you could not read it."}
		}
		resp, _, err := app.RunAgentLoop(ctx, []Message{{Role: "user", Content: instruction}}, AgentLoopConfig{
			SystemPrompt:    prompt,
			Tools:           append(append(tools, extra...), report),
			MaxRounds:       14,
			OnRoundStart:    progRoundStart,
			ReasoningStream: progReasoning,
		})
		// A stopped update ends as the producer's own reason, whatever the
		// run said after it: that is what the producer acts on.
		if stop := gate.stoppedErr(); stop != nil {
			return "", "", stop
		}
		if err != nil {
			return "", "", err
		}
		if ok && readBackAt > 0 {
			if cp, err := results.copyAt(readBackAt); err == nil {
				docs.ReportReadBack(ctx, cp)
			}
		}
		if !reported {
			docs.PublishStep(ctx, "Ended without reporting where the document landed")
			said := ""
			if resp != nil {
				said = strings.TrimSpace(resp.Content)
			}
			return "", "", fmt.Errorf("the publish run ended without reporting where the document landed, so it may not have been published: %s", chFirst(said, "it said nothing"))
		}
		if !ok {
			return note, "", fmt.Errorf("the publish did not go through: %s", chFirst(note, "no reason given"))
		}
		// Reported done, but no call carried the document: whatever went
		// out is only what the model typed, a title or a summary. Not a
		// success, whatever it says.
		if body != nil && body.Expanded() == 0 {
			docs.PublishStep(ctx, "No call carried the document (its placeholder was never sent)")
			return note, url, fmt.Errorf("the publish run reported success, but none of its calls carried the document, so what was sent is only what it typed: publish again")
		}
		return note, url, nil
	})
}

// credentialPublisherBodyPrompt is added when the document travels by
// reference.
const credentialPublisherBodyPrompt = `The document is NOT in the instruction: you send it by writing its placeholder where the body goes, as the instruction says, and it is filled in as the call goes out. Never type the document, or a summary of it, as the body. Long text in what the API sends back is cut short for you, and the whole of it is kept: refer to a result by its number when asked.`

// publishBodyCarried fills the document's placeholders into td's calls as
// they go out.
func publishBodyCarried(body *docs.PublishBody, td AgentToolDef) AgentToolDef {
	h := td.Handler
	if h == nil {
		return td
	}
	td.Handler = func(ctx context.Context, args map[string]any) (string, error) {
		if full, hit := body.ExpandArgs(args); hit {
			args = full
		}
		return h(ctx, args)
	}
	return td
}

// setDocumentLinksTool lets the model name the destination's anchors for the
// document's links to its own sections, which the framework then rewrites:
// it does not type the document, so it cannot rewrite them there.
func setDocumentLinksTool(ctx context.Context, body *docs.PublishBody) AgentToolDef {
	return AgentToolDef{
		Tool: Tool{
			Name:        "set_document_links",
			Description: "Say where the document's links to its own sections point at this destination, before you send it. They are rewritten in the document as it goes out.",
			Parameters: map[string]ToolParam{
				"links":    {Type: "string", Description: "One line per link: \"#anchor -> where it points here\", or \"#anchor ->\" to keep its words as plain text."},
				"contents": {Type: "string", Description: "Optional: the destination's own table-of-contents markup, to put in place of the document's contents list. Empty drops the list; leave it out to keep the list."},
			},
		},
		Handler: func(_ context.Context, args map[string]any) (string, error) {
			links := docs.ParseLinkLines(stringArg(args, "links"))
			var contents *string
			if c, ok := args["contents"].(string); ok {
				contents = &c
			}
			body.SetLinks(links, contents)
			docs.PublishStep(ctx, "Links within the document set for this destination (%d)", len(links))
			return fmt.Sprintf("Recorded %d link(s). They are rewritten when the document goes out.", len(links)), nil
		},
	}
}

// publishIntegrationTools is what a credential-backed publish may hold: the
// credential's own API tool, or, for "mcp:<server>", that server's tools. The
// confirmation on each is lifted (see registerCredentialPublisher). An MCP
// server's tools that delete, remove or archive are left out: a publish
// creates and updates, and a server's full toolset can do far more than that.
// publishIntegrationToolsFor is publishIntegrationTools, a var so a test can
// stand in an integration without a credential store or an MCP server.
var publishIntegrationToolsFor = publishIntegrationTools

func publishIntegrationTools(sess *ToolSession, credential string) ([]AgentToolDef, error) {
	if server, isMCP := strings.CutPrefix(credential, docs.MCPIntegrationPrefix); isMCP {
		cfg, ok := MCP().Load(server)
		if !ok || !cfg.Enabled || !cfg.ExposeTools {
			return nil, fmt.Errorf("the MCP server %q cannot be used here: it is not set up, is switched off, or does not offer its tools to agents", server)
		}
		var out []AgentToolDef
		for _, ct := range FilterChatTools(BlockedTools) {
			c, ok := ct.(CategorizedTool)
			if !ok || c.Category() != MCPToolCategory(server) || publishWithheldRe.MatchString(ct.Name()) {
				continue
			}
			td := ChatToolToAgentToolDefWithSession(ct, sess)
			td.NeedsConfirm, td.Confirmation = false, nil
			out = append(out, td)
		}
		if len(out) == 0 {
			return nil, fmt.Errorf("the MCP server %q has no tools to publish with yet: it may not have connected", server)
		}
		return out, nil
	}
	for _, td := range Secure().BuildTools(sess) {
		if td.Tool.Name == "fetch_url_"+credential {
			td.NeedsConfirm, td.Confirmation = false, nil
			return []AgentToolDef{td}, nil
		}
	}
	return nil, fmt.Errorf("the API integration %q cannot be used here: it does not exist for you, is disabled, or is secured to the tools that declare it", credential)
}

var publishWithheldRe = regexp.MustCompile(`(?i)(?:^|[_.\-])(?:delete|remove|archive|purge|trash|destroy)`)

// publishStepReported wraps a publish run's tool so each call is a step the
// person watching can read (docs.PublishStep): what it is doing, in words, and
// what to, and why it failed if it did. The steps go to ctx, the publish's,
// which outlives the loop's own per-call contexts.
//
// Worded for a person, not traced for a developer. The steps used to be the
// raw tool name, every argument, and the first line of each response, which
// read as noise: a cloud id, "returned: {", and short lines of the document
// itself (a command from the guide passed through untouched).
func publishStepReported(ctx context.Context, td AgentToolDef) AgentToolDef {
	h := td.Handler
	if h == nil {
		return td
	}
	label := publishStepLabel(td.Tool)
	td.Handler = func(c context.Context, args map[string]any) (string, error) {
		line := label
		if what := publishStepTarget(args); what != "" {
			line += " \u00b7 " + what
		}
		docs.PublishStep(ctx, "%s", line)
		out, err := h(c, args)
		if err != nil {
			docs.PublishStep(ctx, "  failed: %s", textutil.Clip(firstLineOf(err.Error()), 200))
		}
		return out, err
	}
	return td
}

// publishStepLabel is what a tool does, for the step list: the first sentence
// of its description, which its author wrote to say exactly that. Tool names
// arrive lowercased and prefixed ("atlassian_getconfluencepage") with no word
// boundaries left to split on, so the name is only the fallback.
func publishStepLabel(t Tool) string {
	d := strings.TrimSpace(firstLineOf(t.Description))
	if i := strings.Index(d, ". "); i > 0 {
		d = d[:i]
	}
	d = strings.TrimSuffix(d, ".")
	if d == "" {
		return t.Name
	}
	return textutil.Clip(d, 80)
}

// publishStepTargetKeys are the arguments that name what a call acts on, in
// the order they are worth showing. Anything else (a cloud or site id, the
// body, formatting options) is plumbing or the document itself.
var publishStepTargetKeys = []struct{ key, word string }{
	{"title", ""}, {"pagetitle", ""}, {"name", ""},
	{"spacekey", "space"}, {"space", "space"},
	{"pageid", "page"}, {"page_id", "page"}, {"parentid", "under page"},
	{"method", ""}, {"path", ""}, {"url", ""},
}

// publishStepTarget is what a call acts on, from its identifying arguments: a
// title quoted, a page or space by id, a method and path. Only a short single
// line is ever shown, so the document's own text cannot leak into the list.
func publishStepTarget(args map[string]any) string {
	low := map[string]string{}
	for k, v := range args {
		low[strings.ToLower(k)] = strings.TrimSpace(fmt.Sprint(v))
	}
	var parts []string
	for _, tk := range publishStepTargetKeys {
		v, ok := low[tk.key]
		if !ok || v == "" || len(v) > 80 || strings.ContainsAny(v, "\n\r") {
			continue
		}
		switch {
		case tk.key == "title" || tk.key == "pagetitle" || tk.key == "name":
			parts = append(parts, "\u201c"+v+"\u201d")
		case tk.key == "url":
			// The path, not the host or query: what a person reads as "where".
			if u, err := url.Parse(v); err == nil && u.Path != "" {
				v = u.Path
			}
			parts = append(parts, v)
		case tk.word != "":
			parts = append(parts, tk.word+" "+v)
		default:
			parts = append(parts, v)
		}
		if len(parts) == 2 {
			break
		}
	}
	return strings.Join(parts, " ")
}

func firstLineOf(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return s
}

// publishLive reports a publish run that has no card to draw on: each round
// as a step, so the list shows where the time went, and the round with its
// thinking as the publish's current activity (docs.PublishActivity), worded
// like the chat's thinking line.
type publishLive struct {
	ctx       context.Context
	mu        sync.Mutex
	round     int
	spanStart time.Time
	chars     int
	lastSent  time.Time
}

func (l *publishLive) roundStart() []Message {
	l.mu.Lock()
	l.round++
	round := l.round
	l.spanStart, l.chars, l.lastSent = time.Time{}, 0, time.Time{}
	l.mu.Unlock()
	docs.PublishStep(l.ctx, "Round %d", round)
	docs.PublishActivity(l.ctx, "round %d", round)
	return nil
}

// say makes a step the current activity, in the round it is in.
func (l *publishLive) say(step string) {
	l.mu.Lock()
	round := l.round
	l.mu.Unlock()
	if round == 0 { // before the first round: setting up
		docs.PublishActivity(l.ctx, "%s", strings.TrimSpace(step))
		return
	}
	docs.PublishActivity(l.ctx, "round %d · %s", round, strings.TrimSpace(step))
}

// chunk counts reasoning and reports on the thinking tick, at once for the
// first chunk of a round so "Thinking" shows the moment it starts.
func (l *publishLive) chunk(s string) {
	now := time.Now()
	l.mu.Lock()
	if l.spanStart.IsZero() {
		l.spanStart = now
	}
	l.chars += len(s)
	due := l.lastSent.IsZero() || now.Sub(l.lastSent) >= thinkTick
	if due {
		l.lastSent = now
	}
	round, secs, tokens := l.round, int(now.Sub(l.spanStart).Seconds()), l.chars/thinkCharsPerToken
	l.mu.Unlock()
	if !due {
		return
	}
	line := fmt.Sprintf("round %d · Thinking", round)
	if secs >= 1 {
		line += fmt.Sprintf(" · %ds", secs)
	}
	if tokens > 0 {
		line += fmt.Sprintf(" · ~%d tokens", tokens)
	}
	docs.PublishActivity(l.ctx, "%s", line)
}
