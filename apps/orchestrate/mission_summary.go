package orchestrate

// A scheduled agent's mission, as the Scheduler list shows it.
//
// A mission is often a few paragraphs, and the list printed every word of it
// under the row's name, so the overview read as a wall of text. Up to forty
// words is easy to read at a glance and shows as written. Past that, the worker
// writes a summary of at most forty words when the mission is set, kept on the
// record against a fingerprint of the mission it summarizes. Until it lands, or
// if it never can, the row shows the first forty words.
//
// Display only. The run is always handed the whole mission, and the editor
// shows the whole mission.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"sync"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

// missionListWords is the most a mission shows in the list before it is
// summarized.
const missionListWords = 40

// missionPrint fingerprints a mission, so a summary is known to be of it.
func missionPrint(mission string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(mission)))
	return hex.EncodeToString(sum[:8])
}

// firstWords is the first n words of s, with "…" when it was cut.
func firstWords(s string, n int) string {
	words := strings.Fields(s)
	if len(words) <= n {
		return strings.Join(words, " ")
	}
	return strings.Join(words[:n], " ") + "…"
}

// missionForList is what the Scheduler shows for a mission.
func missionForList(sa StandingAgent) string {
	mission := strings.TrimSpace(sa.Mission)
	if len(strings.Fields(mission)) <= missionListWords {
		return mission
	}
	if sa.MissionSummary != "" && sa.MissionSummaryOf == missionPrint(mission) {
		return sa.MissionSummary
	}
	return firstWords(mission, missionListWords)
}

// missionNeedsSummary reports a long mission with no summary of its current
// text.
func missionNeedsSummary(sa StandingAgent) bool {
	mission := strings.TrimSpace(sa.Mission)
	return len(strings.Fields(mission)) > missionListWords && sa.MissionSummaryOf != missionPrint(mission)
}

// summarizing is the schedules being summarized now, so a list refreshed while
// one is in flight does not ask again, and when each last failed, so a model
// that is down is not asked on every refresh of the page.
var summarizing = struct {
	sync.Mutex
	m      map[string]bool
	failed map[string]time.Time
}{m: map[string]bool{}, failed: map[string]time.Time{}}

// missionSummaryRetry is how long a failed summary waits before it is tried
// again.
const missionSummaryRetry = 10 * time.Minute

// missionSummaryAsk is the worker's model for a summary; a test replaces it.
var missionSummaryAsk = func(ctx context.Context, mission string) (string, error) {
	orchRefMu.Lock()
	app := orchRef
	orchRefMu.Unlock()
	if app == nil || app.LLM == nil {
		return "", Error("no model to summarize with")
	}
	resp, err := app.LLM.Chat(ctx, []Message{{Role: "user", Content: "Mission:\n" + mission}},
		WithSystemPrompt("You summarize the standing instructions a scheduled agent is given on every run, for a one-line list. Write at most 40 words: what it does each run and where the result goes. Plain sentences, no preamble, no quotes, no markdown. Reply with ONLY the summary."),
		WithMaxTokens(160),
		WithRouteKey("app.orchestrate.mission_summary"),
		WithThink(false),
	)
	if err != nil {
		return "", err
	}
	if resp == nil {
		return "", Error("empty reply")
	}
	return resp.Content, nil
}

// summarizeMissionLater writes the summary off the request, when the mission
// is long and has none of its current text. Safe to call on every list: it
// does nothing when there is nothing to do or the work is already in flight.
// Background lane: a person using the model goes first.
func summarizeMissionLater(sa StandingAgent) {
	if !missionNeedsSummary(sa) {
		return
	}
	key := sa.Owner + "\x00" + sa.Name
	summarizing.Lock()
	if summarizing.m[key] || time.Since(summarizing.failed[key]) < missionSummaryRetry {
		summarizing.Unlock()
		return
	}
	summarizing.m[key] = true
	summarizing.Unlock()
	owner, name, mission := sa.Owner, sa.Name, strings.TrimSpace(sa.Mission)
	go func() {
		defer func() {
			summarizing.Lock()
			delete(summarizing.m, key)
			summarizing.Unlock()
			if r := recover(); r != nil {
				Log("[orchestrate.mission_summary] panicked for %s/%s: %v", owner, name, r)
			}
		}()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		out, err := missionSummaryAsk(ctx, mission)
		summary := strings.Trim(strings.TrimSpace(out), "\"'")
		if err != nil || summary == "" {
			Debug("[orchestrate.mission_summary] %s/%s: no summary: %v", owner, name, err)
			summarizing.Lock()
			summarizing.failed[key] = time.Now()
			summarizing.Unlock()
			return
		}
		// Held to the limit even when the model ran over it.
		summary = firstWords(summary, missionListWords)
		// Read again just before writing: the record may have moved on (a fire
		// re-armed it, an edit changed the mission) while the model worked.
		cur, ok := GetStandingAgent(RootDB, owner, name)
		if !ok || strings.TrimSpace(cur.Mission) != mission {
			return
		}
		cur.MissionSummary, cur.MissionSummaryOf = summary, missionPrint(mission)
		SaveStandingAgent(RootDB, cur)
	}()
}
