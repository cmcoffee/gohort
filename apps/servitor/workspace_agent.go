package servitor

// Making an agent from a workspace (docs/design/servitor-systems-model.md, stage 3).
//
// A workspace is a coordinator over several systems, which is what an agent
// with those systems as its Sources is. The button makes the person an agent
// of their own: the workspace's name, each member they can reach attached as
// a Source (so it gets its investigate_<system> tool), and the roles and links
// written into its instructions, where the coordinator used to read them from
// the roster. Nothing is moved or deleted: the workspace keeps working until
// its owner removes it, and its sessions and memory stay with it.
//
//	POST /api/workspace/agent {"appliance_id": "<workspace id>"}
//	  -> {"agent_id", "name", "url", "attached": [names], "skipped": [names]}

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/appagents"
)

func (T *Servitor) handleWorkspaceAgent(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	userID, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	var req struct {
		ApplianceID string `json:"appliance_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.ApplianceID) == "" {
		http.Error(w, "appliance_id required", http.StatusBadRequest)
		return
	}
	ws, _, _, found := T.resolveAppliance(userID, udb, req.ApplianceID)
	if !found {
		http.Error(w, "system not found", http.StatusNotFound)
		return
	}
	if ws.Type != "workspace" {
		http.Error(w, "not a workspace", http.StatusBadRequest)
		return
	}
	if appagents.CreateUserAgent == nil || SetAgentSource == nil {
		http.Error(w, "agents are not available on this server", http.StatusServiceUnavailable)
		return
	}
	// Only members this person can reach: the agent is theirs, and a Source
	// they cannot open gives it nothing.
	var members []Appliance
	var skipped []string
	for _, id := range ws.Members {
		if m, _, _, ok := T.resolveAppliance(userID, udb, id); ok {
			members = append(members, m)
		} else {
			skipped = append(skipped, id)
		}
	}
	if len(members) == 0 {
		http.Error(w, "none of this workspace's systems is one you can reach, so an agent made from it would have nothing to investigate", http.StatusBadRequest)
		return
	}
	agentID, name, err := appagents.CreateUserAgent(userID, appagents.UserAgent{
		Name:        ws.Name,
		Description: fmt.Sprintf("Investigates the %s systems together: %s.", ws.Name, systemNames(members)),
		Prompt:      workspaceAgentPrompt(ws, members),
		MemoryMode:  "agent",
	})
	if err != nil {
		http.Error(w, "could not make the agent: "+err.Error(), http.StatusInternalServerError)
		return
	}
	var attached []string
	for _, m := range members {
		if err := SetAgentSource(userID, agentID, ReferenceSelection{Kind: "system", ItemID: m.ID}, true); err != nil {
			Log("[servitor] agent %s from workspace %s: attaching %s: %v", agentID, ws.ID, m.Name, err)
			skipped = append(skipped, m.Name)
			continue
		}
		attached = append(attached, m.Name)
	}
	Log("[servitor] %s made agent %q from workspace %s, %d system(s) attached", userID, name, ws.ID, len(attached))
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"agent_id": agentID, "name": name, "url": "/orchestrate/agent/" + agentID,
		"attached": attached, "skipped": skipped,
	})
}

// workspaceAgentPrompt is the instructions an agent made from ws starts with:
// how to work the systems, what the owner wrote for the workspace, and the
// roster's roles and links, which are what routed a question to one member.
func workspaceAgentPrompt(ws Appliance, members []Appliance) string {
	var b strings.Builder
	fmt.Fprintf(&b, "You investigate the %s systems together. Each one is in your Sources with its own investigate tool. Ask the system whose role fits the question first; ask several in turn only when the question spans them, and say which system each finding came from.\n", ws.Name)
	if notes := strings.TrimSpace(ws.Instructions); notes != "" {
		b.WriteString("\nNotes on these systems:\n" + notes + "\n")
	}
	names := map[string]string{}
	b.WriteString("\nThe systems:\n")
	for _, m := range members {
		names[m.ID] = m.Name
		line := "- " + m.Name + " (" + systemKindLabel(m.Type) + ")"
		if role := strings.TrimSpace(ws.MemberRoles[m.ID]); role != "" {
			line += ": " + role
		}
		b.WriteString(line + "\n")
	}
	var links []string
	for _, l := range ws.MemberLinks {
		from, to := names[l.From], names[l.To]
		if from == "" || to == "" {
			continue
		}
		links = append(links, "- "+from+" "+memberRelLabel(l.Rel)+" "+to)
	}
	if len(links) > 0 {
		b.WriteString("\nHow they relate:\n" + strings.Join(links, "\n") + "\n")
	}
	return strings.TrimSpace(b.String())
}

// systemKindLabel names a system's type for a person or a model.
func systemKindLabel(t string) string {
	switch t {
	case "ssh":
		return "SSH system"
	case "repo":
		return "Git repository"
	case "bundle":
		return "evidence bundle"
	case "command":
		return "local command system"
	case "remote":
		return "remote system"
	case "toolset":
		return "tool-backed service"
	}
	return "system"
}

func systemNames(members []Appliance) string {
	names := make([]string, len(members))
	for i, m := range members {
		names[i] = m.Name
	}
	return strings.Join(names, ", ")
}
