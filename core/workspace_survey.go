// What a workspace reaper would find, before one exists.
//
// Every other store in the deployment reclaims: the hook caches sweep on a
// TTL, managed workspaces are deleted once idle, the image ring prunes on
// every write, chat attachments are capped, the fetch cache has a byte quota.
// The per-user and per-agent workspace roots reclaim NOTHING. A generated
// picture, a downloaded file, the text a 200-page PDF spilled to
// .attachments — each lands there and stays until something deletes it, and
// nothing does.
//
// This measures that, and only measures it. Deleting a user's files is not a
// thing to get right on the second attempt, so the first pass reports what is
// there and what age it is, and the decision about a retention window comes
// after somebody has seen real numbers rather than before.
//
// Two things it distinguishes, because they change what a reaper may safely do:
//
//   - TOOL SCRIPTS are regenerated. A shell tool's script lives in its record
//     and is rewritten into whatever workspace it runs in at every dispatch, so
//     deleting one costs a file write on next use and nothing else.
//   - Everything else is the only copy there is.
package core

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// WorkspaceUsage is one directory's worth of accumulation.
type WorkspaceUsage struct {
	Owner string // username
	Agent string // agent id, or "" for the user's own root
	Path  string // absolute directory
	Files int
	Bytes int64
	// Newest is how long ago anything here was last written. A tree whose
	// newest file is a year old is abandoned; one touched this morning is live,
	// however old most of its contents are.
	Newest time.Duration
	// Regenerable counts files a tool record would rewrite on next dispatch,
	// with their bytes — reclaimable at no cost beyond one write.
	Regenerable      int
	RegenerableBytes int64
	// ByAge buckets everything else, oldest band first.
	ByAge []WorkspaceAgeBand
	// Largest is the biggest handful, so a survey answers "what is actually
	// taking the room" and not only "how much".
	Largest []WorkspaceFile
}

// WorkspaceAgeBand is a count and size for one age window.
type WorkspaceAgeBand struct {
	Label string
	Files int
	Bytes int64
}

// WorkspaceFile is one entry in the largest-files list.
type WorkspaceFile struct {
	Rel   string
	Bytes int64
	Age   time.Duration
}

// workspaceAgeBands are the windows the survey reports, longest first.
var workspaceAgeBands = []struct {
	Label string
	Min   time.Duration
}{
	{"over 180 days", 180 * 24 * time.Hour},
	{"90-180 days", 90 * 24 * time.Hour},
	{"30-90 days", 30 * 24 * time.Hour},
	{"7-30 days", 7 * 24 * time.Hour},
	{"under 7 days", 0},
}

// SurveyWorkspaces walks every user root and every per-agent workspace and
// reports what is sitting in them. Read-only.
func SurveyWorkspaces(db Database) []WorkspaceUsage {
	base := WorkspacesDir()
	if base == "" || db == nil {
		return nil
	}
	var out []WorkspaceUsage
	for _, u := range AuthListUsers(db) {
		user := strings.TrimSpace(u.Username)
		if user == "" {
			continue
		}
		regen := regenerableScriptNames(db, user)
		if usage, ok := surveyOneWorkspace(filepath.Join(base, user), user, "", regen); ok {
			out = append(out, usage)
		}
		agentsRoot := filepath.Join(base, AgentWorkspacesDirName, user)
		entries, err := os.ReadDir(agentsRoot)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			if usage, ok := surveyOneWorkspace(filepath.Join(agentsRoot, e.Name()), user, e.Name(), regen); ok {
				out = append(out, usage)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Bytes > out[j].Bytes })
	return out
}

// regenerableScriptNames is every filename a tool record would rewrite on its
// next dispatch — both the LLM-facing name and the canonical on-disk one, since
// which is present depends on when the tool was authored.
func regenerableScriptNames(db Database, user string) map[string]bool {
	out := map[string]bool{}
	for _, p := range LoadPersistentTempTools(UserDB(db, user), user) {
		if strings.TrimSpace(p.Tool.ScriptBody) == "" {
			continue // nothing to regenerate FROM; the disk copy is the only one
		}
		for _, n := range []string{p.Tool.ScriptName, p.Tool.CanonicalScriptName} {
			if n = strings.TrimSpace(n); n != "" {
				out[n] = true
			}
		}
	}
	return out
}

func surveyOneWorkspace(dir, user, agent string, regen map[string]bool) (WorkspaceUsage, bool) {
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return WorkspaceUsage{}, false
	}
	usage := WorkspaceUsage{Owner: user, Agent: agent, Path: dir, Newest: -1}
	bands := make([]WorkspaceAgeBand, len(workspaceAgeBands))
	for i, b := range workspaceAgeBands {
		bands[i].Label = b.Label
	}
	now := time.Now()
	_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil //nolint:nilerr // an unreadable entry is not worth failing a survey
		}
		fi, ierr := d.Info()
		if ierr != nil {
			return nil
		}
		age := now.Sub(fi.ModTime())
		usage.Files++
		usage.Bytes += fi.Size()
		if usage.Newest < 0 || age < usage.Newest {
			usage.Newest = age
		}
		rel, _ := filepath.Rel(dir, path)
		if regen[filepath.Base(path)] {
			usage.Regenerable++
			usage.RegenerableBytes += fi.Size()
			return nil // counted separately; it is not what a reaper is deciding about
		}
		for i, b := range workspaceAgeBands {
			if age >= b.Min {
				bands[i].Files++
				bands[i].Bytes += fi.Size()
				break
			}
		}
		usage.Largest = append(usage.Largest, WorkspaceFile{Rel: rel, Bytes: fi.Size(), Age: age})
		return nil
	})
	if usage.Files == 0 {
		return WorkspaceUsage{}, false
	}
	for _, b := range bands {
		if b.Files > 0 {
			usage.ByAge = append(usage.ByAge, b)
		}
	}
	sort.Slice(usage.Largest, func(i, j int) bool { return usage.Largest[i].Bytes > usage.Largest[j].Bytes })
	if len(usage.Largest) > 5 {
		usage.Largest = usage.Largest[:5]
	}
	if usage.Newest < 0 {
		usage.Newest = 0
	}
	return usage, true
}

// FormatWorkspaceSurvey renders the survey for a human, with a total first
// because that is the number the question was actually about.
func FormatWorkspaceSurvey(list []WorkspaceUsage) string {
	if len(list) == 0 {
		return ""
	}
	var total, regen int64
	var files int
	for _, u := range list {
		total += u.Bytes
		regen += u.RegenerableBytes
		files += u.Files
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s across %d file(s) in %d workspace(s). %s of that is tool scripts the framework rewrites on next dispatch.\n",
		HumanSize(total), files, len(list), HumanSize(regen))
	for _, u := range list {
		where := u.Owner
		if u.Agent != "" {
			where += " / agent " + u.Agent
		}
		fmt.Fprintf(&b, "\n  %s — %s in %d file(s), last written %s ago\n", where, HumanSize(u.Bytes), u.Files, roundDuration(u.Newest))
		for _, band := range u.ByAge {
			fmt.Fprintf(&b, "      %-14s %5d file(s)  %s\n", band.Label, band.Files, HumanSize(band.Bytes))
		}
		if u.Regenerable > 0 {
			fmt.Fprintf(&b, "      %-14s %5d file(s)  %s\n", "tool scripts", u.Regenerable, HumanSize(u.RegenerableBytes))
		}
		for _, f := range u.Largest {
			fmt.Fprintf(&b, "        %8s  %-40s %s old\n", HumanSize(f.Bytes), truncateMiddle(f.Rel, 40), roundDuration(f.Age))
		}
	}
	b.WriteString("\nNothing was deleted. These roots have no reaper: unlike the hook caches, managed workspaces, " +
		"the image ring and chat attachments, files here stay until something removes them.")
	return b.String()
}

func roundDuration(d time.Duration) string {
	switch {
	case d >= 24*time.Hour:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	case d >= time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
}

// truncateMiddle keeps both ends of a path — the directory that explains where
// it came from, and the filename that says what it is.
func truncateMiddle(s string, max int) string {
	if len(s) <= max || max < 8 {
		return s
	}
	keep := (max - 1) / 2
	return s[:keep] + "…" + s[len(s)-(max-1-keep):]
}

func init() {
	RegisterMaintenanceFunc(
		"survey_workspace_usage",
		"Survey workspace usage",
		"Read-only. Reports what is sitting in every per-user and per-agent workspace: "+
			"total size, age bands, the largest files, and how much is tool scripts the "+
			"framework would rewrite anyway. These roots have no reaper; this measures "+
			"what one would find. Deletes nothing.",
		func(ctx context.Context) int {
			list := SurveyWorkspaces(RootDB)
			if len(list) == 0 {
				Log("[workspace-usage] no workspace files found")
				return 0
			}
			Log("[workspace-usage]\n%s", FormatWorkspaceSurvey(list))
			var mb int64
			for _, u := range list {
				mb += u.Bytes
			}
			return int(mb / (1024 * 1024)) // MiB, so the admin count means something
		},
	)
}
