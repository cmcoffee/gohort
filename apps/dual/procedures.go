package dual

import (
	"encoding/json"
	"fmt"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

const proceduresTable = "dual_procedures"

type Procedure struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Steps       string `json:"steps"`
}

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

func saveProcedure(db Database, p Procedure) {
	if db == nil || p.Name == "" {
		return
	}
	db.Set(proceduresTable, p.Name, p)
	Debug("[dual] saved procedure: %s", p.Name)
}

func deleteProcedure(db Database, name string) {
	if db == nil || name == "" {
		return
	}
	db.Unset(proceduresTable, name)
	Debug("[dual] deleted procedure: %s", name)
}

// buildProcedureSection returns the procedure memory section appended to
// the worker's system prompt.
func buildProcedureSection(db Database) string {
	procs := loadProcedures(db)
	var b strings.Builder
	b.WriteString(`

LEARNED PROCEDURES:
When you figure out a reusable non-obvious approach, save it:
<save_procedure>
{"name": "short_name", "description": "when to use this", "steps": "1. Step\n2. Step"}
</save_procedure>

To delete an outdated procedure: <delete_procedure>name</delete_procedure>

RULES: Only save after success. Generalize for a class of inputs, not a specific case. Include tool names and argument formats. No trivial single-step procedures.
`)
	if len(procs) > 0 {
		b.WriteString("\nSAVED PROCEDURES (use these when relevant):\n")
		for _, p := range procs {
			fmt.Fprintf(&b, "\n### %s\n%s\nSteps:\n%s\n", p.Name, p.Description, p.Steps)
		}
	}
	return b.String()
}

// parseProcedureActions scans content for <save_procedure> and
// <delete_procedure> tags, executes them against db, and returns the
// content with the tags stripped.
func parseProcedureActions(db Database, content string) string {
	for {
		start := strings.Index(content, "<save_procedure>")
		if start < 0 {
			break
		}
		end := strings.Index(content, "</save_procedure>")
		if end < 0 || end <= start {
			break
		}
		raw := strings.TrimSpace(content[start+len("<save_procedure>") : end])
		var p Procedure
		if json.Unmarshal([]byte(raw), &p) == nil && p.Name != "" {
			saveProcedure(db, p)
		}
		content = content[:start] + content[end+len("</save_procedure>"):]
	}
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
