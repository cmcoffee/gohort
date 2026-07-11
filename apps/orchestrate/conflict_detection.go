// Conflict-detection rail — the finding-layer complement to the fact
// supersession judge. When a finding is saved, a worker-LLM judge checks whether
// it CONTRADICTS an existing finding on the same claim and SURFACES the conflict
// in the save result. It never auto-resolves: unlike the fact path (which
// tombstones the superseded fact) and single-valued graph edges (replace=true),
// findings are the user's own saved research — the rail flags the contradiction
// and leaves the decision to keep, forget, or reconcile with the user.
//
// Scope today: finding-vs-finding within the agent's derived memory. Facts
// already supersede (StoreMemoryFactP) and graph edges use replace, so those
// layers are covered. Cross-layer (finding-vs-fact) and graph-edge conflict are
// the next increments.

package orchestrate

import (
	"context"
	"fmt"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

// TunableConflictDetection gates the finding conflict rail. Off by default: it
// costs one worker round-trip per finding save that has a related-but-not-
// duplicate neighbor.
const TunableConflictDetection = "tune_conflict_detection"

func init() {
	RegisterTunable(TunableSpec{Key: TunableConflictDetection, Category: "Memory",
		Label: "Finding conflict detection (0 = off)",
		Help:  "When saving a finding, run a worker-LLM check for an existing finding it contradicts and surface the conflict in the tool result. Never auto-deletes — the user decides. Costs one worker call per save that has a related neighbor. Findings only for now; facts already supersede and graph edges use replace.",
		Kind:  KindBool, Default: 0, Min: 0, Max: 1})
}

func conflictDetectionEnabled() bool { return TuneBool(TunableConflictDetection) }

// findingConflictBandFloor is the cosine floor for an existing finding to count
// as a conflict candidate: related enough that a contradiction is plausible, but
// below the dedup ceiling (memorySaveDedupThreshold, "same finding"). A cheap
// pre-filter only — the worker judge makes the actual call. Mirrors the fact
// layer's factSupersedeBandFloor.
const findingConflictBandFloor = 0.60

// conflictScanK is how many neighbors the finding-save search pulls: enough to
// serve both the top-1 dedup check and a small conflict-candidate band.
const conflictScanK = 6

// findingConflictCandidates filters save-search neighbors down to the conflict
// band [findingConflictBandFloor, memorySaveDedupThreshold): related enough that
// a contradiction is plausible, but not a duplicate (handled separately).
func findingConflictCandidates(neighbors []SearchHit) []SearchHit {
	var cand []SearchHit
	for _, h := range neighbors {
		if s := float64(h.Score); s >= findingConflictBandFloor && s < memorySaveDedupThreshold {
			cand = append(cand, h)
		}
	}
	return cand
}

// detectFindingConflict inspects the neighbors already fetched by the finding-save
// search and, when the rail is on, asks the worker which (if any) CONTRADICT the
// new finding. Returns a surfacing note to append to the save result (empty when
// off, no band candidates, or no conflict). Never blocks or deletes.
func (t *chatTurn) detectFindingConflict(newContent string, neighbors []SearchHit) string {
	if !conflictDetectionEnabled() {
		return ""
	}
	cand := findingConflictCandidates(neighbors)
	if len(cand) == 0 {
		return ""
	}
	conflicts := judgeFindingConflicts(t.app.WorkerChat, newContent, cand)
	if len(conflicts) == 0 {
		return ""
	}
	return renderFindingConflictNote(conflicts)
}

// renderFindingConflictNote formats the surfacing message: names each conflicting
// finding with its saved date + recall id so the user can pull it up and forget
// the outdated one. Both are kept — the rail flags, it does not resolve.
func renderFindingConflictNote(conflicts []SearchHit) string {
	var b strings.Builder
	if len(conflicts) == 1 {
		b.WriteString("\n\n⚠ Possible conflict: this may contradict a finding you already saved — ")
	} else {
		fmt.Fprintf(&b, "\n\n⚠ Possible conflict: this may contradict %d findings you already saved — ", len(conflicts))
	}
	for i, h := range conflicts {
		if i > 0 {
			b.WriteString("; ")
		}
		name := strings.TrimSpace(h.Title)
		if name == "" {
			name = strings.TrimSpace(strings.TrimPrefix(h.Section, "## "))
		}
		if name == "" {
			name = "(untitled)"
		}
		fmt.Fprintf(&b, "%q", name)
		if d := recallAgeNote(h.Date); d != "" {
			fmt.Fprintf(&b, " %s", d)
		}
		fmt.Fprintf(&b, " [id: mem:%s]", h.ReportID)
	}
	b.WriteString(". Both are kept — recall them and forget the outdated one if this supersedes it, or reconcile with the user.")
	return b.String()
}

// judgeFindingConflicts asks the worker which candidate findings the new finding
// CONTRADICTS (incompatible claims about the same thing, cannot both be true).
// Mirrors the fact layer's judgeSupersedes: a plain JSON array of indices, worker
// tier, no-think. Best-effort — nil chat, no candidates, error, or unparseable
// reply yields no conflict (the finding is simply saved without a flag).
func judgeFindingConflicts(chat FactChatFunc, newContent string, candidates []SearchHit) []SearchHit {
	if chat == nil || len(candidates) == 0 {
		return nil
	}
	var list strings.Builder
	for i, h := range candidates {
		fmt.Fprintf(&list, "%d. %s\n", i+1, strings.ReplaceAll(knowledgeSearchExcerpt(h.Text), "\n", " "))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	resp, err := chat(ctx, []Message{
		{Role: "user", Content: fmt.Sprintf(`A memory holds saved research findings. A NEW finding is being saved. For each EXISTING finding listed, decide whether the new finding CONTRADICTS it: they make INCOMPATIBLE claims about the same thing and cannot both be currently true.

Do NOT flag a finding that merely adds detail, covers a different aspect, or agrees — only a genuine contradiction of the same claim. When unsure, do NOT flag.

NEW finding: %q

EXISTING findings:
%s
Reply with ONLY a JSON array of the numbers of existing findings the new one contradicts. Reply [] if none.`, newContent, list.String())},
	}, WithSystemPrompt("You detect when a new finding contradicts existing ones (incompatible claims about the same thing). Reply with ONLY a JSON array of indices."),
		WithThink(false),
		WithMaxTokens(128))
	if err != nil || resp == nil {
		return nil
	}
	var idx []int
	if DecodeJSON(ResponseText(resp), &idx) != nil {
		return nil
	}
	var out []SearchHit
	for _, n := range idx {
		if n >= 1 && n <= len(candidates) {
			out = append(out, candidates[n-1])
		}
	}
	return out
}
