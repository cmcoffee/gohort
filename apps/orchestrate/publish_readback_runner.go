package orchestrate

// Reading a published copy back through a person's own integration (an API
// or an MCP server), for a publish run that must not overwrite edits made at
// the destination, and for a run that only reads (docs.ReadViaCredential).
//
// The model finds the page; it never copies it. Every integration call's
// result is numbered, and the model names the number of the one that holds
// the page (report_current_page). The body is taken from that result as the
// tool returned it, so a long page costs no output and loses nothing.

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/docs"
)

// publishResults numbers an integration run's tool results.
type publishResults struct {
	mu   sync.Mutex
	outs []string
	// clip shows the model each result with its long text cut short
	// (docs.ClipForModel), the whole kept here: the document travels by
	// reference, and a page read back in full would carry it into every
	// round again.
	clip bool
}

// publishClipAt is the size past which a result is clipped for the model.
const publishClipAt = 4000

// wrap numbers td's results: "(result 3)" heads each one the model sees.
func (r *publishResults) wrap(td AgentToolDef) AgentToolDef {
	h := td.Handler
	if h == nil {
		return td
	}
	td.Handler = func(ctx context.Context, args map[string]any) (string, error) {
		out, err := h(ctx, args)
		if err != nil {
			return out, err
		}
		r.mu.Lock()
		r.outs = append(r.outs, out)
		n := len(r.outs)
		r.mu.Unlock()
		if r.clip {
			return fmt.Sprintf("(result %d)\n%s", n, docs.ClipForModel(out, n, publishClipAt)), nil
		}
		return fmt.Sprintf("(result %d)\n%s", n, out), nil
	}
	return td
}

// copyAt is the page body in result n.
func (r *publishResults) copyAt(n int) (docs.PublishedCopy, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if n < 1 || n > len(r.outs) {
		return docs.PublishedCopy{}, fmt.Errorf("there is no result %d: give the number shown at the top of the result that holds the page", n)
	}
	cp := docs.BodyFromToolResult(r.outs[n-1])
	if strings.TrimSpace(cp.Body) == "" {
		return docs.PublishedCopy{}, fmt.Errorf("result %d holds no page body: read the page with its content and give that result's number", n)
	}
	return cp, nil
}

func intArg(args map[string]any, key string) int {
	switch v := args[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	case string:
		var n int
		fmt.Sscanf(strings.TrimSpace(v), "%d", &n)
		return n
	}
	return 0
}

var (
	// A tool is a READ when its name says it gets something and nothing in
	// it says it changes anything. MCP tool names arrive lowercased with no
	// word boundaries ("getconfluencepage"), so these match anywhere.
	publishReadVerbRE  = regexp.MustCompile(`(?i)get|list|search|read|fetch|find|lookup|query|view`)
	publishWriteVerbRE = regexp.MustCompile(`(?i)create|update|edit|delete|remove|post|put|write|publish|upload|replace|append|move|archive|trash|destroy|purge|transition|comment|add|set`)
)

// publishToolReads reports whether a call only reads: an API call by its
// method, an MCP tool by its name.
func publishToolReads(td AgentToolDef, args map[string]any) bool {
	if strings.HasPrefix(td.Tool.Name, "fetch_url_") {
		m := strings.ToUpper(strings.TrimSpace(stringArg(args, "method")))
		return m == "" || m == "GET" || m == "HEAD"
	}
	name := td.Tool.Name
	return publishReadVerbRE.MatchString(name) && !publishWriteVerbRE.MatchString(name)
}

// publishWriteGate holds back an integration's writes until the copy has been
// read and the producer has said to go ahead (docs.CheckBeforeUpdate). After
// a stop, nothing more goes through at all.
type publishWriteGate struct {
	mu      sync.Mutex
	cleared bool
	stopped error
}

func (g *publishWriteGate) wrap(td AgentToolDef) AgentToolDef {
	h := td.Handler
	if h == nil {
		return td
	}
	td.Handler = func(ctx context.Context, args map[string]any) (string, error) {
		g.mu.Lock()
		cleared, stopped := g.cleared, g.stopped
		g.mu.Unlock()
		if stopped != nil {
			return "", fmt.Errorf("the update was stopped, so nothing more goes to this integration: call report_published with ok=false")
		}
		if !cleared && !publishToolReads(td, args) {
			return "", fmt.Errorf("read the page as it stands first and call report_current_page with that result's number: nothing is changed before that")
		}
		return h(ctx, args)
	}
	return td
}

// check runs the producer's check on the copy in result n.
func (g *publishWriteGate) check(ctx context.Context, res *publishResults, n int) (string, error) {
	cp, err := res.copyAt(n)
	if err != nil {
		return "", err
	}
	if err := docs.CheckBeforeUpdate(ctx, cp); err != nil {
		g.mu.Lock()
		g.stopped = err
		g.mu.Unlock()
		docs.PublishStep(ctx, "Stopped before the update: %s", err.Error())
		return "Stop here and change nothing: " + err.Error() + " Call report_published with ok=false and that as the note.", nil
	}
	g.mu.Lock()
	g.cleared = true
	g.mu.Unlock()
	docs.PublishStep(ctx, "Read the page as it stands: nobody has changed it there, so the update goes ahead")
	return "Nobody has changed it there since it was last published: go ahead with the update.", nil
}

func (g *publishWriteGate) stoppedErr() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.stopped
}

// reportCurrentPageTool is how a run points at the result holding the page.
func reportCurrentPageTool(handler func(n int) (string, error)) AgentToolDef {
	return AgentToolDef{
		Tool: Tool{
			Name:        "report_current_page",
			Description: "Point at the result that holds the page's full content as it stands now: give the number shown at the top of that result. Do not copy the page.",
			Parameters: map[string]ToolParam{
				"result": {Type: "integer", Description: "The result's number, from its \"(result N)\" line."},
			},
			Required: []string{"result"},
		},
		Handler: func(_ context.Context, args map[string]any) (string, error) {
			return handler(intArg(args, "result"))
		},
	}
}

// credentialReaderPrompt is the whole brief of a read-back run.
const credentialReaderPrompt = `You read one published document through one integration (an API, or an MCP server's tools) and point at it. You change nothing.

Find the document at the address you are given and read it with its full content (the body, not only its title or properties). Every result is numbered at its top. Call report_current_page with the number of the result that holds the content, then reply with one line saying what you read. If you cannot read it, say plainly why.`

// registerCredentialReader installs the closure core/docs calls to read a
// copy back through a person's own integration. The run holds only the
// integration's reads: a write is refused whatever the model tries.
func registerCredentialReader(app *OrchestrateApp) {
	docs.RegisterCredentialReader(func(ctx context.Context, user, credential, instruction string) (docs.PublishedCopy, error) {
		if app == nil {
			return docs.PublishedCopy{}, errors.New("orchestrate runtime not initialized")
		}
		sess := &ToolSession{Username: user}
		tools, err := publishIntegrationTools(sess, credential)
		if err != nil {
			return docs.PublishedCopy{}, err
		}
		res := &publishResults{}
		for i := range tools {
			td := tools[i]
			h := td.Handler
			if h != nil {
				td.Handler = func(c context.Context, args map[string]any) (string, error) {
					if !publishToolReads(td, args) {
						return "", fmt.Errorf("this run only reads: nothing is changed")
					}
					return h(c, args)
				}
			}
			tools[i] = res.wrap(td)
		}
		var got *docs.PublishedCopy
		report := reportCurrentPageTool(func(n int) (string, error) {
			cp, err := res.copyAt(n)
			if err != nil {
				return "", err
			}
			got = &cp
			return "Recorded. Reply with one line and stop.", nil
		})
		resp, _, err := app.RunAgentLoop(ctx, []Message{{Role: "user", Content: instruction}}, AgentLoopConfig{
			SystemPrompt: credentialReaderPrompt,
			Tools:        append(tools, report),
			MaxRounds:    8,
		})
		if err != nil {
			return docs.PublishedCopy{}, err
		}
		if got == nil {
			said := ""
			if resp != nil {
				said = strings.TrimSpace(resp.Content)
			}
			return docs.PublishedCopy{}, fmt.Errorf("the read ended without pointing at the page: %s", chFirst(said, "it said nothing"))
		}
		return *got, nil
	})
}
