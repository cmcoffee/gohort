package enginseer

import (
	"fmt"
	"strings"

	"github.com/cmcoffee/gohort/apps/orchestrate"
	. "github.com/cmcoffee/gohort/core"
)

// repoAgentScope is the per-repo orchestrate scope the code map lives in. The
// map tools carry it explicitly, so they read/write the right repo's graph
// regardless of the scope the chat itself runs in.
func repoAgentScope(repoID string) orchestrate.AgentScope {
	return orchestrate.AgentScope{AgentID: repoInvestigatorAgentID, ScopeUser: repoScope(repoID)}
}

// mapTools are the code-graph recording + recall tools bound to one repo.
// link_entities builds the code map (package→imports→package, handler→calls→
// service, service→reads→table); recall_map reads it back so questions start
// from what's already understood instead of re-tracing every time.
func mapTools(repoID string) []AgentToolDef {
	return []AgentToolDef{
		{
			Tool: Tool{
				Name:        "link_entities",
				Description: "Record a STRUCTURAL relationship in this codebase's map — how components connect. State it subject-relation-object, e.g. subject='internal/auth' relation='calls' object='internal/db', or subject='UserHandler' relation='reads' object='users table'. Each package/module/type/table/service is its own entity; put details (file path, kind) in subject_attrs. Do this as you learn how the code fits together, so the map grows and future questions build on it.",
				Parameters: map[string]ToolParam{
					"subject":       {Type: "string", Description: "The subject entity — a package path, type, function, table, or service."},
					"subject_kind":  {Type: "string", Description: "package | module | file | type | function | table | service | endpoint. Defaults to thing."},
					"relation":      {Type: "string", Description: "e.g. 'imports', 'calls', 'implements', 'reads', 'writes', 'defines', 'routes to', 'depends on'."},
					"object":        {Type: "string", Description: "The object entity."},
					"object_kind":   {Type: "string", Description: "See subject_kind."},
					"subject_attrs": {Type: "object", Description: "Optional details about the subject as key/value strings, e.g. {\"path\":\"internal/auth/token.go\"}."},
					"note":          {Type: "string", Description: "Optional qualifier on the relationship."},
					"replace":       {Type: "boolean", Description: "True if this CORRECTS a single-valued relation."},
				},
				Required: []string{"subject", "relation", "object"},
			},
			Handler: func(args map[string]any) (string, error) {
				subject, _ := args["subject"].(string)
				relation, _ := args["relation"].(string)
				object, _ := args["object"].(string)
				if strings.TrimSpace(subject) == "" || strings.TrimSpace(relation) == "" || strings.TrimSpace(object) == "" {
					return "", fmt.Errorf("subject, relation, and object are required")
				}
				orch := findOrchestrate()
				if orch == nil {
					return "", fmt.Errorf("orchestrate not initialized")
				}
				attrs := map[string]string{}
				if raw, ok := args["subject_attrs"].(map[string]any); ok {
					for k, v := range raw {
						if s, ok := v.(string); ok {
							attrs[k] = s
						}
					}
				}
				subjectKind, _ := args["subject_kind"].(string)
				objectKind, _ := args["object_kind"].(string)
				note, _ := args["note"].(string)
				replace, _ := args["replace"].(bool)
				if err := orch.SeedScopedGraphLink(repoAgentScope(repoID), subjectKind, subject, attrs, relation, objectKind, object, note, replace); err != nil {
					return "", err
				}
				return fmt.Sprintf("Recorded: %s → %s → %s", subject, relation, object), nil
			},
		},
		{
			Tool: Tool{
				Name:        "recall_map",
				Description: "Return the code map recorded so far for this repository — the entities and how they connect. Consult it BEFORE diving into files, so you build on prior understanding instead of re-tracing. Empty if nothing has been mapped yet.",
				Parameters:  map[string]ToolParam{},
			},
			Handler: func(args map[string]any) (string, error) {
				orch := findOrchestrate()
				if orch == nil {
					return "", fmt.Errorf("orchestrate not initialized")
				}
				m := orch.ScopedGraphSummary(repoAgentScope(repoID))
				if strings.TrimSpace(m) == "" {
					return "(nothing mapped yet — build it with link_entities as you learn the structure)", nil
				}
				return m, nil
			},
		},
	}
}
