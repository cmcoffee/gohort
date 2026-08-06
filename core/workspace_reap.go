// Reclaiming the workspace, by recognizing what may go rather than what may stay.
//
// The survey settled the design. 2.5GB across two workspaces, and the oldest
// files — everything past 180 days — came to 536KB of it. Age alone reclaims
// 0.02%. Five downloaded videos were a quarter of the total. So the thing worth
// removing is not old files, it is large derived artifacts, and they are
// identifiable by how the framework wrote them.
//
// WHICH WAY THE ALLOWLIST POINTS IS THE WHOLE SAFETY ARGUMENT. This deletes
// only what it positively recognizes: a flat file, at a workspace root, whose
// name matches a producer the framework itself uses, past an age window.
// Everything else is untouchable — every subdirectory, every unrecognized name.
//
// The alternative (delete everything except registered exclusions) fails in the
// wrong direction. A missed registration there deletes someone's case files,
// silently and permanently, and surfaces months later when they go looking. A
// missed prefix here means some space is not reclaimed, and we notice because
// the disk did not shrink. An app that writes into the workspace is therefore
// safe without knowing this code exists — which is the only property that
// survives the next app nobody remembers to register.
//
// Nothing here recurses. Directories are read with ReadDir, never walked, so
// casefile/ and .attachments/ are not skipped by a rule that could be got
// wrong — they are never visited.
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

// reapableArtifacts are the filenames the framework itself produces for
// transient, re-derivable output. Each is written flat into the workspace root
// by a tool that hands the model a reference and expects it to deliver with
// cleanup=true — which happens when the user asked for the file, and does not
// when they only asked what was in it. That gap is what accumulates.
var reapableArtifacts = []struct {
	Prefix, Ext, Producer string
}{
	{"gen-", ".png", "generate_image"},
	{"edit-", ".png", "image(edit)"},
	{"video-", ".mp4", "download_video"},
}

// ArtifactReapAge is how long a transient artifact is kept. Long enough that a
// conversation can still reference last week's picture; short enough to matter,
// since the survey found the bulk of the bytes at 7-90 days old.
var ArtifactReapAge = 14 * 24 * time.Hour

// ReapCandidate is one artifact eligible for removal.
type ReapCandidate struct {
	Owner    string
	Agent    string // "" for the user's own root
	Path     string // absolute
	Name     string
	Producer string // the tool that wrote it, for the report
	Bytes    int64
	Age      time.Duration
}

// FindReapableArtifacts returns what a reap WOULD remove. Read-only, and the
// same walk the reaper itself uses, so a dry run cannot disagree with the run.
func FindReapableArtifacts(db Database, olderThan time.Duration) []ReapCandidate {
	base := WorkspacesDir()
	if base == "" || db == nil {
		return nil
	}
	var out []ReapCandidate
	for _, u := range AuthListUsers(db) {
		user := strings.TrimSpace(u.Username)
		if user == "" {
			continue
		}
		out = append(out, scanReapable(filepath.Join(base, user), user, "", olderThan)...)
		agentsRoot := filepath.Join(base, AgentWorkspacesDirName, user)
		entries, err := os.ReadDir(agentsRoot)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() {
				out = append(out, scanReapable(filepath.Join(agentsRoot, e.Name()), user, e.Name(), olderThan)...)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Bytes > out[j].Bytes })
	return out
}

// scanReapable reads ONE directory — no recursion, by design. A subdirectory is
// app or user data (casefile/, .attachments/) and is never entered, so no rule
// about skipping it can be got wrong.
func scanReapable(dir, user, agent string, olderThan time.Duration) []ReapCandidate {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	now := time.Now()
	var out []ReapCandidate
	for _, e := range entries {
		if e.IsDir() || !e.Type().IsRegular() {
			continue
		}
		producer, ok := artifactProducer(e.Name())
		if !ok {
			continue
		}
		fi, ierr := e.Info()
		if ierr != nil {
			continue
		}
		age := now.Sub(fi.ModTime())
		if age < olderThan {
			continue
		}
		out = append(out, ReapCandidate{
			Owner: user, Agent: agent,
			Path: filepath.Join(dir, e.Name()), Name: e.Name(),
			Producer: producer, Bytes: fi.Size(), Age: age,
		})
	}
	return out
}

// artifactProducer reports whether a filename is one the framework wrote, and
// which tool wrote it. Prefix AND extension must both match: "video-notes.txt"
// is somebody's file, not a download.
func artifactProducer(name string) (string, bool) {
	for _, a := range reapableArtifacts {
		if strings.HasPrefix(name, a.Prefix) && strings.HasSuffix(name, a.Ext) && len(name) > len(a.Prefix)+len(a.Ext) {
			return a.Producer, true
		}
	}
	return "", false
}

// ReapArtifacts removes what FindReapableArtifacts reports. Returns how many
// files went and how many bytes came back. A file that fails to delete is
// logged and skipped — one unreadable entry is not a reason to abandon the
// rest, and the next run will try it again.
func ReapArtifacts(db Database, olderThan time.Duration) (int, int64) {
	var files int
	var bytes int64
	for _, c := range FindReapableArtifacts(db, olderThan) {
		if err := os.Remove(c.Path); err != nil {
			Log("[workspace-reap] could not remove %s: %v", c.Path, err)
			continue
		}
		files++
		bytes += c.Bytes
	}
	// Nothing to notify: the video cache self-heals (a missing file drops its
	// row and re-downloads), and image refs resolve through the image space,
	// which prunes on its own.
	return files, bytes
}

// FormatReapCandidates renders a dry run. Largest first, because that is the
// order in which the answer to "is this worth doing" becomes clear.
func FormatReapCandidates(list []ReapCandidate) string {
	if len(list) == 0 {
		return ""
	}
	var total int64
	byProducer := map[string]int{}
	bytesByProducer := map[string]int64{}
	for _, c := range list {
		total += c.Bytes
		byProducer[c.Producer]++
		bytesByProducer[c.Producer] += c.Bytes
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s in %d file(s) could be reclaimed:\n", HumanSize(total), len(list))
	producers := make([]string, 0, len(byProducer))
	for p := range byProducer {
		producers = append(producers, p)
	}
	sort.Slice(producers, func(i, j int) bool { return bytesByProducer[producers[i]] > bytesByProducer[producers[j]] })
	for _, p := range producers {
		fmt.Fprintf(&b, "  %-18s %4d file(s)  %s\n", p, byProducer[p], HumanSize(bytesByProducer[p]))
	}
	b.WriteString("\nLargest:\n")
	for i, c := range list {
		if i >= 10 {
			fmt.Fprintf(&b, "  … and %d more\n", len(list)-10)
			break
		}
		where := c.Owner
		if c.Agent != "" {
			where += "/" + c.Agent
		}
		fmt.Fprintf(&b, "  %8s  %-34s %-22s %s old\n", HumanSize(c.Bytes), c.Name, where, roundDuration(c.Age))
	}
	b.WriteString("\nOnly flat files at a workspace root whose names the framework itself wrote are listed. " +
		"Subdirectories are never read, so app and user data (casefile/, .attachments/) cannot appear here.")
	return b.String()
}

func init() {
	RegisterMaintenanceFunc(
		"survey_reapable_artifacts",
		"Survey reclaimable artifacts (dry run)",
		"Read-only. Lists the generated images and downloaded videos the framework "+
			"wrote into a workspace and nothing ever deleted — flat files at a workspace "+
			"root, matching a known producer, older than the retention window. "+
			"Subdirectories are never read, so app data is not a candidate. Deletes nothing.",
		func(ctx context.Context) int {
			list := FindReapableArtifacts(RootDB, ArtifactReapAge)
			Log("[workspace-reap] dry run over %q, window %s", WorkspacesDir(), roundDuration(ArtifactReapAge))
			if len(list) == 0 {
				Log("[workspace-reap] nothing eligible")
				return 0
			}
			Log("[workspace-reap]\n%s", FormatReapCandidates(list))
			return len(list)
		},
	)
	RegisterMaintenanceFunc(
		"reap_workspace_artifacts",
		"Reclaim workspace artifacts (DELETES)",
		"Removes exactly what the dry run above lists: framework-produced images and "+
			"videos at a workspace root, past the retention window. Run the dry run first "+
			"— it uses the same walk, so what it shows is what this removes.",
		func(ctx context.Context) int {
			list := FindReapableArtifacts(RootDB, ArtifactReapAge)
			if len(list) == 0 {
				Log("[workspace-reap] nothing eligible; nothing removed")
				return 0
			}
			Log("[workspace-reap] removing %d file(s):\n%s", len(list), FormatReapCandidates(list))
			files, bytes := ReapArtifacts(RootDB, ArtifactReapAge)
			Log("[workspace-reap] removed %d file(s), reclaimed %s", files, HumanSize(bytes))
			return files
		},
	)
}
