// The bulletins tool: how Builder sets up a board (bulletins.go) as part of
// building an agent that keeps others informed. Without it, asked for "an
// agent that updates the bulletin board with today's news", Builder could make
// the agent but not the board, the permission to post to it, or the followers,
// and fell back to questions it had no way to act on.
package orchestrate

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	. "github.com/cmcoffee/oddjob/core"
)

func bulletinsToolDef(t *chatTurn) AgentToolDef {
	return AgentToolDef{
		Tool: Tool{
			Name: "bulletins",
			Description: "Set up bulletin boards: short notices every following agent sees on each of its turns (today's headlines, a status, a message of the day). " +
				"A board is filled by an agent allowed to post to it (it gets a post_bulletin tool, so give it a schedule or a job that calls it), or by a watch monitor created with bulletin=<board>, which posts with no model at all. " +
				"Actions: list; create {name, desc, ttl_hours (how long a post stays current, 0 = always)}; allow_poster {board, agent, on}; follow {board, agent or \"all\", on}.",
			Parameters: map[string]ToolParam{
				"action":    {Type: "string", Enum: []string{"list", "create", "allow_poster", "follow"}, Description: "What to do."},
				"name":      {Type: "string", Description: "(create) The board's name: letters, digits, - or _, e.g. news."},
				"desc":      {Type: "string", Description: "(create) What it carries."},
				"ttl_hours": {Type: "number", Description: "(create) Hours a post stays current; 0 means it never goes out of date."},
				"board":     {Type: "string", Description: "(allow_poster, follow) The board."},
				"agent":     {Type: "string", Description: "(allow_poster, follow) The agent, by name or id; for follow, \"all\" means every agent."},
				"on":        {Type: "boolean", Description: "(allow_poster, follow) true to grant or follow (default), false to take it away."},
			},
			Required: []string{"action"},
			Caps:     []Capability{CapRead, CapWrite},
		},
		Handler: func(_ context.Context, args map[string]any) (string, error) {
			on := true
			if v, ok := args["on"].(bool); ok {
				on = v
			}
			switch strings.ToLower(strings.TrimSpace(stringArg(args, "action"))) {
			case "list":
				boards := listBulletins(t.udb)
				if len(boards) == 0 {
					return "No bulletin boards yet. Create one with action=\"create\".", nil
				}
				agents := bulletinTargets(t.udb, t.user)
				var b strings.Builder
				for _, bb := range boards {
					row := bulletinRow(bb, agents, UserLocation(t.user))
					fmt.Fprintf(&b, "- %s: %s. Followed by %s; posted to by %s; %s; %s\n",
						bb.Name, chFirst(bb.Desc, "no description"), row["followers"], row["posters"], row["posted"], row["ttl"])
				}
				return b.String(), nil
			case "create":
				name := bulletinName(stringArg(args, "name"))
				if name == "" {
					return "", fmt.Errorf("name the board: letters, digits, - or _")
				}
				if _, exists := loadBulletin(t.udb, name); exists {
					return fmt.Sprintf("A board named %q already exists; use it.", name), nil
				}
				ttl := 0
				if f, ok := args["ttl_hours"].(float64); ok && f > 0 {
					ttl = int(f)
				}
				if err := saveBulletin(t.udb, bulletinBoard{Name: name, Desc: strings.TrimSpace(stringArg(args, "desc")), TTLHours: ttl, Created: time.Now()}); err != nil {
					return "", err
				}
				return fmt.Sprintf("Created board %q. Nobody follows it or may post to it yet: set both with allow_poster and follow.", name), nil
			case "allow_poster", "follow":
				board, found := loadBulletin(t.udb, stringArg(args, "board"))
				if !found {
					return "", fmt.Errorf("no board named %q: list shows what exists", stringArg(args, "board"))
				}
				key := strings.TrimSpace(stringArg(args, "agent"))
				if strings.EqualFold(stringArg(args, "action"), "follow") && strings.EqualFold(key, "all") {
					board.AllAgents = on
					_ = saveBulletin(t.udb, board)
					return fmt.Sprintf("Every agent %s board %q.", chIf(on, "now follows", "no longer follows by default"), board.Name), nil
				}
				ag, why := t.ownAgentByNameOrID(key)
				if ag.ID == "" {
					return "", fmt.Errorf("%s", chFirst(why, "no agent named "+key))
				}
				// Both actions change the agent: follow rewrites what arrives on
				// its turns, allow_poster hands it a post_bulletin tool.
				change := fmt.Sprintf("%s bulletin board %q", chIf(on, "follow", "stop following"), board.Name)
				if strings.EqualFold(stringArg(args, "action"), "allow_poster") {
					change = fmt.Sprintf("%s post to bulletin board %q", chIf(on, "let it", "stop it being able to"), board.Name)
				}
				if msg := agentChangeGate(t.chatAsker(), t.udb, &ag, t.user, change); msg != "" {
					return "", errors.New(msg)
				}
				if strings.EqualFold(stringArg(args, "action"), "allow_poster") {
					board.Posters = removeString(board.Posters, ag.ID)
					if on {
						board.Posters = append(board.Posters, ag.ID)
					}
					_ = saveBulletin(t.udb, board)
					return fmt.Sprintf("%s %s post to %q%s.", chFirst(ag.Name, ag.ID), chIf(on, "may now", "may no longer"), board.Name,
						chIf(on, " (it has a post_bulletin tool naming this board)", "")), nil
				}
				ag.Bulletins = removeString(ag.Bulletins, board.Name)
				if on {
					ag.Bulletins = append(ag.Bulletins, board.Name)
				}
				if _, err := saveAgent(t.udb, ag); err != nil {
					return "", err
				}
				return fmt.Sprintf("%s %s %q.", chFirst(ag.Name, ag.ID), chIf(on, "now follows", "no longer follows"), board.Name), nil
			}
			return "", fmt.Errorf("action must be list, create, allow_poster or follow")
		},
	}
}
