package orchestrate

// Acting on a memory finding: Remove the entry behind it, or Ignore it.
//
// The audit used to be read-only on purpose ("wrongly evicting memory is
// worse than a stale entry"), and pointed at each layer's own editor. That
// held while findings were rare. A finding that is wrong for good (a note
// that mentions a retired tool on purpose) then came back on every open with
// no way to say so, and one that is right took a trip to another section and
// a hunt for the entry. Both are now one click, and both are still the
// owner's call: Remove asks first and names what goes, and Ignore is kept
// per finding and undone from the same panel.

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// memoryAuditIgnoredTable holds, per agent, the IDs of findings the owner set
// aside.
const memoryAuditIgnoredTable = "memory_audit_ignored"

// firstLineWhere returns the first line of text that pred accepts, as
// written, or "" when none does.
func firstLineWhere(text string, pred func(string) bool) string {
	for _, line := range strings.Split(text, "\n") {
		if pred(line) {
			return line
		}
	}
	return ""
}

// findingID is a finding's stable name: what kind it is, where, about which
// tool, which entry, and the text behind it. The text is in it on purpose, so
// an ignored finding returns once what it was about changes.
func findingID(agentID string, f MemoryFinding) string {
	t := f.target
	key := strings.Join([]string{
		agentID, f.Layer, f.Kind, f.name, f.Quote,
		t.factID, t.noteLine, fmt.Sprint(t.clearNotes), t.entityID,
		strings.Join(t.attrKeys, ","), strings.Join(t.aliases, ","), strings.Join(t.reportIDs, ","), strings.Join(t.chunkIDs, ","),
	}, "\x00")
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:8])
}

func loadIgnoredFindings(udb Database, agentID string) map[string]bool {
	var ids []string
	udb.Get(memoryAuditIgnoredTable, agentID, &ids)
	out := make(map[string]bool, len(ids))
	for _, id := range ids {
		out[id] = true
	}
	return out
}

func saveIgnoredFindings(udb Database, agentID string, ids map[string]bool) {
	if len(ids) == 0 {
		udb.Unset(memoryAuditIgnoredTable, agentID)
		return
	}
	list := make([]string, 0, len(ids))
	for id := range ids {
		list = append(list, id)
	}
	udb.Set(memoryAuditIgnoredTable, agentID, list)
}

// splitIgnoredFindings separates what the owner set aside from what still
// needs attention, capping the second, and forgets ignore marks for findings
// that no longer occur so the set cannot grow forever.
func splitIgnoredFindings(udb Database, agentID string, all []MemoryFinding) (shown, ignored []MemoryFinding) {
	marks := loadIgnoredFindings(udb, agentID)
	live := map[string]bool{}
	for _, f := range all {
		if marks[f.ID] {
			live[f.ID] = true
			f.Ignored = true
			ignored = append(ignored, f)
			continue
		}
		shown = append(shown, f)
	}
	if len(live) != len(marks) {
		saveIgnoredFindings(udb, agentID, live)
	}
	if len(shown) > maxAuditFindings {
		shown = shown[:maxAuditFindings]
	}
	return shown, ignored
}

// removeFinding deletes the entry a finding is about.
func removeFinding(udb Database, agent AgentRecord, f MemoryFinding) error {
	ns := factsNamespace(agent.ID)
	t := f.target
	switch {
	case t.factID != "":
		if !ForgetMemoryFactByID(udb, ns, t.factID) {
			return fmt.Errorf("that saved fact is already gone")
		}
	case t.clearNotes:
		SaveOperatingNotes(udb, ns, "")
	case t.noteLine != "":
		// From the notes the agent actually has, which may still be the
		// seed it started with: removing a line from those makes them its
		// own, minus that line.
		text := ResolveOperatingNotes(udb, ns, agent.SeedNotes).Text
		lines := strings.Split(text, "\n")
		for i, l := range lines {
			if l == t.noteLine {
				SaveOperatingNotes(udb, ns, strings.Join(append(lines[:i:i], lines[i+1:]...), "\n"))
				return nil
			}
		}
		return fmt.Errorf("that line is no longer in the working notes")
	case t.entityID != "":
		if t.dropEntity {
			DeleteGraphEntity(udb, ns, t.entityID)
			return nil
		}
		for _, k := range t.attrKeys {
			DeleteGraphEntityAttr(udb, ns, t.entityID, k)
		}
		for _, a := range t.aliases {
			DeleteGraphEntityAlias(udb, ns, t.entityID, a)
		}
	case len(t.reportIDs) > 0 || len(t.chunkIDs) > 0:
		if VectorDB == nil {
			return fmt.Errorf("reference memory is not available")
		}
		for _, id := range t.reportIDs {
			DeleteReportChunks(VectorDB, id)
		}
		if len(t.chunkIDs) > 0 {
			DeleteChunksByIDs(VectorDB, t.chunkIDs)
		}
	default:
		return fmt.Errorf("there is nothing precise to remove for this finding")
	}
	return nil
}

// actOnFinding runs the panel's Remove, Ignore or Restore on one finding,
// found again by ID from a fresh audit: the page names a finding, never what
// to delete.
func (T *OrchestrateApp) actOnFinding(udb Database, user string, agent AgentRecord, action, id string) error {
	var f *MemoryFinding
	for _, x := range T.auditAgentMemory(udb, user, agent.ID, agent) {
		if x.ID == id {
			x := x
			f = &x
			break
		}
	}
	marks := loadIgnoredFindings(udb, agent.ID)
	switch action {
	case "restore":
		delete(marks, id)
		saveIgnoredFindings(udb, agent.ID, marks)
		return nil
	case "ignore":
		if f == nil {
			return fmt.Errorf("that finding no longer applies")
		}
		marks[id] = true
		saveIgnoredFindings(udb, agent.ID, marks)
		Log("[orchestrate.memaudit] agent=%s ignored finding %s (%s)", agent.ID, id, f.Kind)
		return nil
	case "remove":
		if f == nil {
			return fmt.Errorf("that finding no longer applies")
		}
		if f.Remove == "" {
			return fmt.Errorf("there is nothing precise to remove for this finding")
		}
		if err := removeFinding(udb, agent, *f); err != nil {
			return err
		}
		delete(marks, id)
		saveIgnoredFindings(udb, agent.ID, marks)
		Log("[orchestrate.memaudit] agent=%s removed what finding %s was about (%s, %s)", agent.ID, id, f.Layer, f.Kind)
		return nil
	}
	return fmt.Errorf("unknown action %q", action)
}
