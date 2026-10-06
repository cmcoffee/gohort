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

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/docs"
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
		sess := &ToolSession{Username: user}
		tools, err := publishIntegrationTools(sess, credential)
		if err != nil {
			docs.PublishStep(ctx, "Could not start: %v", err)
			return "", "", err
		}
		docs.PublishStep(ctx, "Publishing through %s, with %d of its tools", strings.TrimPrefix(credential, docs.MCPIntegrationPrefix), len(tools))
		for i := range tools {
			tools[i] = publishStepReported(ctx, tools[i])
		}
		var url, note string
		reported, ok := false, false
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
				url = strings.TrimSpace(stringArg(args, "url"))
				note = strings.TrimSpace(stringArg(args, "note"))
				ok, _ = args["ok"].(bool)
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
		resp, _, err := app.RunAgentLoop(ctx, []Message{{Role: "user", Content: instruction}}, AgentLoopConfig{
			SystemPrompt: credentialPublisherPrompt,
			Tools:        append(tools, report),
			MaxRounds:    12,
		})
		if err != nil {
			return "", "", err
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
		return note, url, nil
	})
}

// publishIntegrationTools is what a credential-backed publish may hold: the
// credential's own API tool, or, for "mcp:<server>", that server's tools. The
// confirmation on each is lifted (see registerCredentialPublisher). An MCP
// server's tools that delete, remove or archive are left out: a publish
// creates and updates, and a server's full toolset can do far more than that.
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
			docs.PublishStep(ctx, "  failed: %s", clipStep(firstLineOf(err.Error()), 200))
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
	return clipStep(d, 80)
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

func clipStep(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
