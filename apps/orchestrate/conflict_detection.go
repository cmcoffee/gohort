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
	"github.com/cmcoffee/gohort/core/decide"
)

// TunableConflictDetection gates the finding conflict rail.
//
// ON by default, which it was not at first. The cost is smaller than the
// original note implied: the vector search it needs is one the save ALREADY
// performs for dedup, and the worker call happens only when that search turns
// up a neighbour inside the conflict band — related enough to contradict,
// not close enough to be a duplicate. Most saves have no such neighbour and
// spend nothing.
//
// What it protects is the one layer with no supersession of its own. Facts
// supersede on write and single-valued graph edges replace; findings just
// accumulate, so a superseded one sits beside its replacement and recall
// returns both, with nothing marking which is current. An agent that keeps
// findings about the same systems over months is exactly where that bites.
const TunableConflictDetection = "tune_conflict_detection"

func init() {
	RegisterJudgeCall(JudgeCall{Key: "memory.conflict_check", Label: "Memory conflict check",
		Help: "Whether something to remember contradicts what is already remembered."})
	RegisterTunable(TunableSpec{App: "/orchestrate", Key: TunableConflictDetection, Category: "Memory",
		Label:  "Finding conflict detection",
		Help:   "When saving a finding, check for one it contradicts and surface the conflict in the result.",
		Detail: "It never auto-deletes: you decide whether to keep, forget or reconcile. It costs one worker call only on a save whose dedup search already found a related-but-not-duplicate neighbour, so most saves spend nothing.\n\nFindings only. Facts already supersede, and graph edges replace. Turn it off for an agent whose findings are independent of each other.",
		Kind:   KindBool, Default: 1, Min: 0, Max: 1})
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
// findingConflictMarker opens the conflict note, and is how a reader of a
// remember result (finishAfterSideNotes) knows one is there.
const findingConflictMarker = "⚠ Possible conflict:"

func renderFindingConflictNote(conflicts []SearchHit) string {
	var b strings.Builder
	if len(conflicts) == 1 {
		b.WriteString("\n\n" + findingConflictMarker + " this may contradict a finding you already saved: ")
	} else {
		fmt.Fprintf(&b, "\n\n"+findingConflictMarker+" this may contradict %d findings you already saved: ", len(conflicts))
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
	b.WriteString(". Both are kept: recall them and forget the outdated one if this supersedes it, or reconcile with the user.")
	return b.String()
}

// conflictJudgeExcerptMaxChars sizes the excerpt each candidate finding gets
// in the conflict judge's prompt. Wider than the 300-char UI excerpt — the
// judge must actually read the claim it's asked to refute.
const conflictJudgeExcerptMaxChars = 900

// conflictJudgeExcerpt trims a finding for the judge prompt, cutting at a word
// boundary like knowledgeSearchExcerpt but at the judge's wider cap.
func conflictJudgeExcerpt(text string) string {
	text = strings.TrimSpace(text)
	if len(text) <= conflictJudgeExcerptMaxChars {
		return text
	}
	cut := text[:conflictJudgeExcerptMaxChars]
	if idx := strings.LastIndex(cut, " "); idx > conflictJudgeExcerptMaxChars/2 {
		cut = cut[:idx]
	}
	return strings.TrimRight(cut, " \t\n") + "..."
}

// judgeFindingConflicts asks the worker which candidate findings the new finding
// CONTRADICTS (incompatible claims about the same thing, cannot both be true).
// Mirrors the fact layer's judgeSupersedes: a plain JSON array of indices, worker
// tier, no-think. Best-effort — nil chat, no candidates, error, or unparseable
// reply yields no conflict (the finding is simply saved without a flag).
// conflictQuickSystem is the conflict check's first question as System One
// reads it.
const conflictQuickSystem = `A memory holds saved research findings, and a new finding is being saved. You decide whether the new finding contradicts any existing one: an incompatible claim about the same thing, so both cannot be currently true. A finding that adds detail, covers a different aspect, or agrees is not a contradiction.`

// conflictQuick is the conflict check's first question as a fixed choice
// (core.QuickChoice): none stands on System One when it is sure; a
// contradiction needs the full check, which names which findings.
func conflictQuick(newContent string, candidates []SearchHit) QuickChoice {
	var list strings.Builder
	for i, h := range candidates {
		fmt.Fprintf(&list, "%d. %s\n", i+1, strings.ReplaceAll(conflictJudgeExcerpt(h.Text), "\n", " "))
	}
	return QuickChoice{Key: "memory.conflict_check", System: conflictQuickSystem,
		State: fmt.Sprintf("NEW finding: %q\n\nEXISTING findings:\n%s", newContent, list.String()),
		Ask:   "Does the new finding contradict any existing finding?",
		Answers: []QuickAnswer{
			{Key: "none", Means: "None: it contradicts none of them"},
			{Key: "contradicts", Means: "Contradicts: it is incompatible with at least one", Reason: true},
		}}
}

func judgeFindingConflicts(chat FactChatFunc, newContent string, candidates []SearchHit) []SearchHit {
	if chat == nil || len(candidates) == 0 {
		return nil
	}
	// System One first, where the map gives the check to it: a sure "none"
	// is the answer; a contradiction needs the full check to say which.
	if ans, ok := AskQuick(context.Background(), conflictQuick(newContent, candidates)); ok && ans == "none" {
		return nil
	}
	var list strings.Builder
	for i, h := range candidates {
		// A judge-specific excerpt, wider than the UI's search excerpt: a
		// contradiction past a 300-char display cap was invisible to the
		// judge, and the note shown to the user then named a finding the
		// judge only partially read.
		fmt.Fprintf(&list, "%d. %s\n", i+1, strings.ReplaceAll(conflictJudgeExcerpt(h.Text), "\n", " "))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	rulings, err := conflictVerdict.JudgeEach(ctx, decide.VerdictChat(chat),
		fmt.Sprintf("NEW finding: %q\n\nEXISTING findings:\n%s", newContent, list.String()), len(candidates),
		WithCaller("memory.conflict_check"), WithThink(false), WithMaxTokens(600))
	if err != nil {
		return nil
	}
	var out []SearchHit
	for i, r := range rulings {
		if r.Answer == "contradicts" {
			out = append(out, candidates[i])
		}
	}
	return out
}

// conflictVerdict is the conflict check, one verdict per existing finding,
// what each claims set beside the new one before the answer. It answered
// with bare numbers, and called "about 120 employees" a contradiction of
// "roughly 120 people" with nothing to say why.
var conflictVerdict = decide.Verdict{
	Key: "memory.conflict_check",
	Role: `A memory holds saved research findings, and a NEW finding is being saved. For each EXISTING finding listed, decide whether the new finding CONTRADICTS it: they make incompatible claims about the same thing, so both cannot be currently true.

Not a contradiction: a finding that adds detail, covers a different aspect, or agrees, including the same claim in other words or with a rounded or approximate figure. When unsure, it is not a contradiction.`,
	Answers: []decide.Choice{
		{Key: "consistent", Means: "both can be true: different aspect, added detail, or the same claim"},
		{Key: "contradicts", Means: "incompatible claims about the same thing"},
	},
	Weigh: []decide.Step{{Key: "why", Ask: "one short clause: what each claims about the same thing, or why they are about different things"}},
	Each:  "finding",
}
