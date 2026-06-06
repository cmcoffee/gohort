package servitor

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

// handleKnowledgeExport serves a per-appliance markdown brief of all the
// knowledge servitor has accumulated about a system (profile, knowledge
// docs, CLI maps, techniques, non-secret facts, log map) as a download.
// Credentials and secret-bearing facts are EXCLUDED so the file is safe
// to hand to an external LLM (e.g. Claude) as context for building or
// improving a support / diagnostic tool for the system.
//
// GET /api/knowledge/export?appliance_id=<id>.
func (T *Servitor) handleKnowledgeExport(w http.ResponseWriter, r *http.Request) {
	_, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	id := strings.TrimSpace(r.URL.Query().Get("appliance_id"))
	if id == "" || udb == nil {
		http.Error(w, "appliance_id required", http.StatusBadRequest)
		return
	}
	var a Appliance
	if !udb.Get(applianceTable, id, &a) {
		http.Error(w, "appliance not found", http.StatusNotFound)
		return
	}
	fname := safeFilename(a.Name)
	if fname == "" {
		fname = safeFilename(a.ID)
	}
	if fname == "" {
		fname = "appliance"
	}
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	w.Header().Set("Content-Disposition", "attachment; filename=\""+fname+"-knowledge.md\"")
	_, _ = w.Write([]byte(buildKnowledgeExport(udb, a)))
}

// knowledgeDocLabels gives the five structured docs human section titles.
var knowledgeDocLabels = map[string]string{
	"overview":   "Overview",
	"databases":  "Databases",
	"filesystem": "Filesystem",
	"services":   "Services",
	"apps":       "Applications",
}

// buildKnowledgeExport assembles the redacted markdown brief for one
// appliance. Mirrors the in-session context builder but is destined for
// an external reader, so it leads with an LLM-oriented preamble and omits
// anything credential-bearing.
func buildKnowledgeExport(udb Database, a Appliance) string {
	now := time.Now()
	name := strings.TrimSpace(a.Name)
	if name == "" {
		name = a.ID
	}

	var b strings.Builder
	b.WriteString("# System Knowledge: " + name + "\n\n")
	b.WriteString("> Operational knowledge about this system, exported from servitor. ")
	b.WriteString("Credentials and connection secrets have been excluded. ")
	b.WriteString("Use this as background context to build or improve a support, diagnostic, or runbook tool for this system. ")
	b.WriteString("Facts annotated with an age may be stale; verify before relying on them.\n\n")
	stamp := "Exported " + now.Format("2006-01-02 15:04 MST")
	if strings.TrimSpace(a.Scanned) != "" {
		stamp = "Last mapped " + a.Scanned + ". " + stamp
	}
	b.WriteString("_" + stamp + "._\n\n")

	// Identity (non-secret fields only; host/user/password deliberately omitted).
	b.WriteString("## Identity\n\n")
	b.WriteString("- Name: " + name + "\n")
	if t := strings.TrimSpace(a.Type); t != "" {
		b.WriteString("- Type: " + t + "\n")
	}
	if p := strings.TrimSpace(a.PersonaName); p != "" {
		b.WriteString("- Role: " + p + "\n")
	}
	b.WriteString("\n")

	// Full system profile (the map run's structured output).
	if prof := strings.TrimSpace(a.Profile); prof != "" {
		b.WriteString("## System Profile\n\n" + prof + "\n\n")
	}

	// The five structured knowledge docs, age-annotated.
	docs := allDocs(udb, a.ID)
	for _, doc := range knowledgeDocNames {
		content := strings.TrimSpace(docs[doc])
		if content == "" {
			continue
		}
		label := knowledgeDocLabels[doc]
		if label == "" {
			label = doc
		}
		b.WriteString("## " + label + "\n\n" + content + "\n\n")
	}

	// CLI maps (per-command reference), sorted for determinism.
	if maps := cliMapsForAppliance(udb, a.ID); len(maps) > 0 {
		cmds := make([]string, 0, len(maps))
		for cmd := range maps {
			cmds = append(cmds, cmd)
		}
		sort.Strings(cmds)
		b.WriteString("## Command Reference\n\n")
		for _, cmd := range cmds {
			b.WriteString("### " + cmd + "\n\n" + strings.TrimSpace(maps[cmd]) + "\n\n")
		}
	}

	// Known techniques (confirmed working approaches).
	if tech := strings.TrimSpace(techniquesFor(udb, a.ID)); tech != "" {
		b.WriteString("## Known Techniques\n\n" + tech + "\n\n")
	}

	// Facts, sorted by key, with secret-bearing ones excluded.
	facts := factsForAppliance(udb, a.ID)
	sort.Slice(facts, func(i, j int) bool { return facts[i].Key < facts[j].Key })
	redacted := 0
	wroteHeader := false
	for _, f := range facts {
		if isSecretFact(f) {
			redacted++
			continue
		}
		if strings.TrimSpace(f.Value) == "" {
			continue
		}
		if !wroteHeader {
			b.WriteString("## Known Facts\n\n")
			wroteHeader = true
		}
		line := "- **" + f.Key + "**: " + strings.TrimSpace(f.Value)
		if age := factAgeStr(f.Updated, now); age != "" {
			line += "  _(" + age + ")_"
		}
		b.WriteString(line + "\n")
	}
	if wroteHeader {
		b.WriteString("\n")
	}
	if redacted > 0 {
		b.WriteString(fmt.Sprintf("_%d credential/secret fact(s) were excluded from this export._\n\n", redacted))
	}

	// Log file inventory.
	if len(a.LogMap) > 0 {
		b.WriteString("## Log Files\n\n")
		for _, e := range a.LogMap {
			path := strings.TrimSpace(e.Path)
			if path == "" {
				continue
			}
			line := "- `" + path + "`"
			if svc := strings.TrimSpace(e.Service); svc != "" {
				line += " (" + svc + ")"
			}
			if desc := strings.TrimSpace(e.Desc); desc != "" {
				line += " — " + desc
			}
			b.WriteString(line + "\n")
		}
		b.WriteString("\n")
	}

	return b.String()
}

// isSecretFact reports whether a stored fact carries a credential or
// other secret and must be kept out of an export. Key-based (and tag-
// based) because that is how servitor names them: the connection-auth
// facts end in "_auth" (mysql_auth, postgres_auth, redis_auth,
// mongo_auth), and anything obviously password/token/key shaped is
// excluded too. Plain paths (e.g. sqlite_path) are NOT secrets.
func isSecretFact(f SshFact) bool {
	k := strings.ToLower(f.Key)
	if strings.HasSuffix(k, "_auth") {
		return true
	}
	for _, marker := range []string{"password", "passwd", "secret", "token", "credential", "api_key", "apikey", "private_key", "privkey"} {
		if strings.Contains(k, marker) {
			return true
		}
	}
	for _, t := range f.Tags {
		if strings.EqualFold(t, "secret") || strings.EqualFold(t, "credential") {
			return true
		}
	}
	return false
}

// safeFilename reduces a display name to a download-safe slug.
func safeFilename(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	lastDash := false
	for _, r := range s {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
			lastDash = false
		default:
			if !lastDash && b.Len() > 0 {
				b.WriteByte('-')
				lastDash = true
			}
		}
	}
	return strings.Trim(b.String(), "-")
}
