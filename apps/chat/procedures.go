package chat

import (
	"encoding/json"
	"fmt"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

const proceduresTable = "chat_procedures"

// Procedure is a learned recipe the chat saves from successful tool interactions.
type Procedure struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Steps       string `json:"steps"`
}

// loadProcedures returns all saved procedures from the database.
func loadProcedures(db Database) []Procedure {
	if db == nil {
		return nil
	}
	var procs []Procedure
	for _, key := range db.Keys(proceduresTable) {
		var p Procedure
		if db.Get(proceduresTable, key, &p) {
			procs = append(procs, p)
		}
	}
	return procs
}

// saveProcedure stores a procedure in the database.
func saveProcedure(db Database, p Procedure) {
	if db == nil || p.Name == "" {
		return
	}
	db.Set(proceduresTable, p.Name, p)
	Debug("[chat] saved procedure: %s", p.Name)
}

// deleteProcedure removes a procedure from the database.
func deleteProcedure(db Database, name string) {
	if db == nil || name == "" {
		return
	}
	db.Unset(proceduresTable, name)
	Debug("[chat] deleted procedure: %s", name)
}

// buildProcedurePrompt generates the system prompt section describing
// saved procedures and how to create/manage them.
func buildProcedurePrompt(db Database) string {
	procs := loadProcedures(db)

	var b strings.Builder
	b.WriteString(`

LEARNED PROCEDURES:
You can save reusable procedures when you figure out how to accomplish something through trial and error. Next time someone asks a similar question, follow the saved procedure instead of rediscovering the solution.

IMPORTANT: Procedures are NOT tools. Do NOT call them with <tool_call>. They are step-by-step instructions for YOU to follow manually — read the steps and execute them yourself using your available tools. A procedure named "timezone_convert" means YOU should follow its steps (call get_local_time, do the math, etc.), not call a tool named "timezone_convert".

To save a procedure after successfully completing a task, include this in your response:
<save_procedure>
{"name": "short_name", "description": "when to use this", "steps": "1. Step one\n2. Step two\n3. Step three"}
</save_procedure>

To delete an outdated procedure:
<delete_procedure>procedure_name</delete_procedure>

RULES FOR SAVING PROCEDURES:
- Only save after a SUCCESSFUL outcome — never save a failed approach
- The steps must be specific enough to follow without guessing
- Include the tool names and argument formats in the steps
- Update existing procedures if you find a better approach
- GENERALIZE: procedures must solve a CLASS of problems, not one specific case. "timezone_convert" is correct — it handles ANY timezone. "singapore_time" is wrong — it only handles one city. "unit_convert" is correct. "convert_miles_to_km" is too narrow. Before saving, ask: would this procedure help with a DIFFERENT input of the same type? If not, generalize it.
- Use variables in steps, not hardcoded values: "Calculate offset = target_UTC_offset - local_UTC_offset" not "Add 15 hours"
- DO NOT save obvious workflows. "Search, then broaden search, then respond" is what you would do anyway — saving it as a procedure adds nothing. Only save procedures that encode NON-OBVIOUS knowledge: specific calculations, format conversions, API quirks, workarounds for tool limitations, or domain-specific logic that you figured out through trial and error.
- DO NOT save procedures that are just "call tool X with query Y". That's not a procedure — that's a single tool call. Procedures are for multi-step processes with logic between the steps.
`)

	if len(procs) > 0 {
		b.WriteString("\nSAVED PROCEDURES (use these when relevant):\n")
		for _, p := range procs {
			fmt.Fprintf(&b, "\n### %s\n%s\nSteps:\n%s\n", p.Name, p.Description, p.Steps)
		}
	} else {
		b.WriteString("\nNo procedures saved yet. Save one when you figure out a reusable approach.\n")
	}

	return b.String()
}

// parseProcedureActions scans the LLM response for <save_procedure> and
// <delete_procedure> tags and executes them. Returns the response text
// with the tags stripped.
func parseProcedureActions(db Database, content string) string {
	// Handle saves.
	for {
		start := strings.Index(content, "<save_procedure>")
		if start < 0 {
			break
		}
		end := strings.Index(content, "</save_procedure>")
		if end < 0 || end <= start {
			break
		}
		jsonStr := strings.TrimSpace(content[start+len("<save_procedure>") : end])
		var p Procedure
		if json.Unmarshal([]byte(jsonStr), &p) == nil && p.Name != "" {
			saveProcedure(db, p)
		}
		content = content[:start] + content[end+len("</save_procedure>"):]
	}

	// Handle deletes.
	for {
		start := strings.Index(content, "<delete_procedure>")
		if start < 0 {
			break
		}
		end := strings.Index(content, "</delete_procedure>")
		if end < 0 || end <= start {
			break
		}
		name := strings.TrimSpace(content[start+len("<delete_procedure>") : end])
		if name != "" {
			deleteProcedure(db, name)
		}
		content = content[:start] + content[end+len("</delete_procedure>"):]
	}

	return strings.TrimSpace(content)
}
