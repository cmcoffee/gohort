package orchestrate

// Asks over MCP (apps/mcpserver), run here as turns in threads of their own
// (core/mcp_server_tools.go). A thread is an ordinary stored session under
// the agent, so its history carries the conversation and the owner can read
// it under Threads (threads.go); it is not a channel inbound, so nothing is
// noted into Cortex and a dedicated-Cortex agent does not run it in Cortex.

import (
	"context"
	"sort"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/prompts"
)

// kindMCP is a run asked over MCP: the owner's own client, waiting on the
// reply, so its prompt reads as a conversation (surfaceForRun), not as work
// nobody is watching.
const kindMCP = "mcp"

func registerMCPAgentHost(app *OrchestrateApp) {
	RegisterMCPAgentHost(MCPAgentHost{
		Run:  app.runMCPTurn,
		List: app.listMCPThreads,
	})
}

// runMCPTurn runs one ask in its thread and returns the reply as the client
// gets it: the text through the same enforcers a channel reply passes, the
// pictures with it.
func (app *OrchestrateApp) runMCPTurn(ctx context.Context, t MCPTurn) (ChannelReply, error) {
	res, err := app.RunAgentSyncContinuingRich(ctx, AgentSyncRun{
		AgentOwner:    t.Owner,
		RuntimeUser:   t.Owner,
		AgentKey:      t.AgentID,
		SubSessionID:  t.Thread,
		Kind:          kindMCP,
		Title:         t.Title,
		MessageSender: t.Sender,
		Message:       t.Text,
		// The owner's client, with a reply awaited: there is somebody to
		// answer a follow-up question, so no delegated-run marker.
		Interactive:    true,
		SurfaceContext: "This message came from " + chFirst(t.Sender, "the owner's MCP client") + ", a client the owner uses to reach you over MCP; your reply goes back to it.",
		StatusCallback: t.Status,
	})
	if err != nil {
		return ChannelReply{}, err
	}
	out := ChannelReply{Text: prompts.ApplyRuleEnforcers(StripMetaTags(res.Text)), Images: res.Images, Videos: res.Videos,
		AgentName: agentNameTag(t.Owner, t.AgentID)}
	return out, nil
}

// listMCPThreads is the owner's threads under prefix (the id itself, or the
// id followed by ":" and a conversation), across the agents they can reach,
// newest first. Matched whole, so one key's id that begins another's does
// not take in that key's threads.
func (app *OrchestrateApp) listMCPThreads(owner, prefix string) []MCPThread {
	udb := UserDB(app.DB, owner)
	if udb == nil || strings.TrimSpace(prefix) == "" {
		return nil
	}
	var out []MCPThread
	for _, a := range listAgents(udb, owner) {
		for _, k := range udb.Keys(sessionTable(a.ID)) {
			if k != prefix && !strings.HasPrefix(k, prefix+":") {
				continue
			}
			s, ok := loadChatSession(udb, a.ID, k)
			if !ok {
				continue
			}
			out = append(out, MCPThread{AgentID: a.ID, AgentName: a.Name, Thread: k, Title: s.Title, LastAt: s.LastAt, Messages: len(s.Messages)})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].LastAt.After(out[j].LastAt) })
	return out
}
