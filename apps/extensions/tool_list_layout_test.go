package extensions

// A Tools row's first line is the name, its states and its agents. The notes
// that are sentences (which version is published, who it is shared with, what
// it hides) were on it too, ellipsized among a dozen badges; they read beside
// the description on the second line.

import (
	"encoding/json"
	"testing"
)

// columnLines maps each column of the table reading source to its line (1
// when unset).
func columnLines(v any, source string, out map[string]float64) {
	switch x := v.(type) {
	case map[string]any:
		if cols, ok := x["columns"].([]any); ok && x["source"] == source {
			for _, c := range cols {
				if m, ok := c.(map[string]any); ok {
					f, _ := m["field"].(string)
					line, _ := m["line"].(float64)
					if line == 0 {
						line = 1
					}
					out[f] = line
				}
			}
		}
		for _, e := range x {
			columnLines(e, source, out)
		}
	case []any:
		for _, e := range x {
			columnLines(e, source, out)
		}
	}
}

func TestAToolRowsNotesReadBesideItsDescription(t *testing.T) {
	app, _ := authedExtensions(t)
	b, err := json.Marshal(app.toolsSection("alice"))
	if err != nil {
		t.Fatal(err)
	}
	var tree any
	if err := json.Unmarshal(b, &tree); err != nil {
		t.Fatal(err)
	}
	lines := map[string]float64{}
	columnLines(tree, "api/tools", lines)
	for _, f := range []string{"description", "release", "shared_with", "shadows"} {
		if lines[f] != 2 {
			t.Errorf("%s is on line %v, want the second line", f, lines[f])
		}
	}
	for _, f := range []string{"name", "category", "agent_list", "disabled", "trial"} {
		if lines[f] != 1 {
			t.Errorf("%s is on line %v, want the first", f, lines[f])
		}
	}
}
