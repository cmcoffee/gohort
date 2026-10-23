package scribe

// Distilling one guide into another.
//
// "Take the general concepts from this and send them there." A guide does it
// for each guide among its Sources set to Distill (follow.go): the sections a
// Curator pass changed there, or all of it from Update from sources. Two
// filters, in order. The distill reads the source a part at a time and writes what the
// request asks for, each piece made to stand on its own, as findings for the
// receiving guide; the source is not changed. The receiving guide's Curator
// then decides what fits, by its brief and what it already holds: folded into
// a section, a new one, or dropped, every change in its digest and undoable.
// The distill decides what to take; the Curator decides what belongs.
//
// The receiving guide is one of the person's own: a finding is filed into
// their inbox, and their Curator edits their own guides (agent_source.go). The
// source is any guide they can read.
//
// It runs as a job, on the Monitor with a Stop: one model call per part of the
// source, so a long guide is many calls and some minutes.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

// distillJobPrefix keys a distill in the job table, by its source guide.
const distillJobPrefix = "distill:"

// distillPartBudget is how much of the source one call reads. A section
// longer than this is its own part, cut at distillSectionMax.
const (
	distillPartBudget = 12000
	distillSectionMax = 24000
)

// distillDefault is what is taken unless the request names something: what
// the receiving document's brief says belongs in it. The brief is where its
// owner already said what it is for, so the dialog asks nothing more.
const distillDefault = "Whatever the receiving document's brief says belongs in it, written so it stands on its own there."

// distillPrompt is the distill's instruction.
const distillPrompt = `You distill material from one document for another. You are told what to take, given the receiving document's brief and its sections, and given one part of the source document.

Take what the request asks for from this part. Write each piece so it stands on its own in the receiving document: general, without details that only make sense in the source (its particular incident, its host names, its people) unless the request says to keep them. One piece per idea. Leave out what the receiving document already covers, what the request does not ask for, and what does not fit its brief. Taking nothing from a part is a fine answer.

The brief limits what goes INTO a piece, not only which pieces you take. Keep in a piece only what the brief says belongs, and leave out the rest of the source even where it sits right beside what you take (a command next to the log line it explains, a fix next to the symptom), unless the brief covers that too. A piece is complete when it is complete for what the brief covers.

Reply with JSON only, in this shape: {"findings": [{"topic": "one line: what this piece is about", "content": "the piece itself in markdown, holding only what the receiving document's brief covers"}]}`

// distillPart is a run of the source's sections read in one call.
type distillPart struct {
	titles []string
	text   string
}

// distillParts cuts g into parts of about distillPartBudget, whole sections
// each.
func distillParts(g Guide) []distillPart {
	var out []distillPart
	var cur distillPart
	flush := func() {
		if cur.text != "" {
			out = append(out, cur)
		}
		cur = distillPart{}
	}
	for _, s := range g.Sections {
		body := strings.TrimSpace(s.Markdown)
		if len(body) > distillSectionMax {
			body = body[:distillSectionMax] + "\n\n(this section continues; the rest is not shown)"
		}
		chunk := "## " + s.Title + "\n\n" + body + "\n\n"
		if cur.text != "" && len(cur.text)+len(chunk) > distillPartBudget {
			flush()
		}
		cur.titles = append(cur.titles, s.Title)
		cur.text += chunk
	}
	flush()
	return out
}

// distillFound is one piece the model took.
type distillFound struct {
	Topic   string `json:"topic"`
	Content string `json:"content"`
}

var jsonObjectRE = regexp.MustCompile(`(?s)\{.*\}`)

// parseDistill reads the model's reply, tolerating a fence or prose around
// the object.
func parseDistill(reply string) ([]distillFound, error) {
	var out struct {
		Findings []distillFound `json:"findings"`
	}
	raw := strings.TrimSpace(reply)
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		m := jsonObjectRE.FindString(raw)
		if m == "" || json.Unmarshal([]byte(m), &out) != nil {
			return nil, errors.New("the reply was not the JSON asked for")
		}
	}
	var kept []distillFound
	for _, f := range out.Findings {
		f.Topic, f.Content = strings.TrimSpace(f.Topic), strings.TrimSpace(f.Content)
		if f.Content != "" {
			kept = append(kept, f)
		}
	}
	return kept, nil
}

// distillRun is one distill: from src, what, into dst.
type distillRun struct {
	T         *Scribe
	user      string
	src, dst  Guide
	what      string
	curateNow bool
	job       *publishJob
	jobID     string
}

// run reads the source part by part and files what it takes.
func (d distillRun) run(ctx context.Context) {
	parts := distillParts(d.src)
	filed, failed := 0, 0
	if strings.TrimSpace(d.dst.Brief) == "" {
		// Said up front: the brief is what decides what is taken, here and
		// in the Curator.
		d.job.step(d.dst.Title + " has no brief, so what belongs there is judged by its title and sections. Write one in its Settings for a better fit.")
	}
	for i, p := range parts {
		if ctx.Err() != nil {
			break
		}
		d.job.activity(fmt.Sprintf("part %d of %d · %d sent", i+1, len(parts), filed))
		found, err := d.distillPart(ctx, p, i, len(parts))
		if err != nil {
			if ctx.Err() != nil {
				break
			}
			failed++
			d.job.step(fmt.Sprintf("Part %d of %d (%s): could not read it: %v", i+1, len(parts), strings.Join(p.titles, ", "), err))
			continue
		}
		n := 0
		for _, f := range found {
			if _, err := SubmitFinding(d.user, "guide", DocFinding{
				Content:  f.Content,
				Topic:    f.Topic,
				Document: d.dst.ID,
				Origin: DocFindingOrigin{
					SourceKind: "guide",
					ItemID:     d.src.ID,
					ItemLabel:  d.src.Title,
					RunID:      d.jobID,
					Observed:   time.Now().UTC().Format(time.RFC3339),
				},
			}); err == nil {
				n++
			}
		}
		filed += n
		d.job.step(fmt.Sprintf("Part %d of %d (%s): %s", i+1, len(parts), strings.Join(p.titles, ", "), countOf(n, "piece")))
	}
	stopped := ctx.Err() != nil
	if filed > 0 && d.curateNow && !stopped {
		d.job.activity("starting the Curator")
		go d.T.runCuratorGuarded(AppContext(), d.user, "distill")
	}
	publishJobsMu.Lock()
	defer publishJobsMu.Unlock()
	d.job.Done, d.job.Ended, d.job.Now = true, time.Now(), ""
	if d.job.cancel != nil {
		d.job.cancel()
	}
	msg := countOf(filed, "piece") + " sent to " + d.dst.Title + " for its Curator"
	switch {
	case d.job.Stopped || stopped:
		d.job.Message = "Stopped. " + msg + " before it stopped."
	case filed == 0 && failed == len(parts):
		d.job.Message = "Nothing was taken: every part failed to read."
	default:
		d.job.OK = true
		if d.curateNow && filed > 0 {
			msg += ", which is placing them now"
		} else if filed > 0 {
			msg += ", which places them on its next pass (or run it from Review)"
		}
		if failed > 0 {
			msg += ". " + countOf(failed, "part") + " could not be read"
		}
		d.job.Message = msg + "."
	}
	Log("[scribe.distill] user=%q %q into %q: %d piece(s), %d part(s) failed", d.user, d.src.Title, d.dst.Title, filed, failed)
}

// distillPart asks for what part p holds.
func (d distillRun) distillPart(ctx context.Context, p distillPart, i, n int) ([]distillFound, error) {
	var titles []string
	for _, s := range d.dst.Sections {
		titles = append(titles, s.Title)
	}
	brief := strings.TrimSpace(d.dst.Brief)
	if brief == "" {
		brief = "(none written: judge what belongs by its title and sections)"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "What to take: %s\n\n", d.what)
	fmt.Fprintf(&b, "The receiving document: %s\nIts brief:\n%s\n", d.dst.Title, brief)
	if len(titles) > 0 {
		fmt.Fprintf(&b, "Its sections: %s\n", strings.Join(titles, "; "))
	}
	fmt.Fprintf(&b, "\nFrom the source document %q, part %d of %d:\n\n%s", d.src.Title, i+1, n, p.text)
	resp, err := d.T.WorkerChat(ctx, []Message{{Role: "user", Content: b.String()}},
		WithSystemPrompt(distillPrompt), WithJSONMode(), WithCaller("scribe.distill"), WithThink(false))
	if err != nil {
		return nil, err
	}
	return parseDistill(resp.Content)
}

// handleDistillStop stops it: POST ?id=.
func (T *Scribe) handleDistillStop(w http.ResponseWriter, r *http.Request, user string) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	publishJobsMu.Lock()
	defer publishJobsMu.Unlock()
	j, ok := publishJobs[distillJobPrefix+strings.TrimSpace(r.URL.Query().Get("id"))]
	if !ok || j.owner != user {
		http.NotFound(w, r)
		return
	}
	stopping := !j.Done && j.cancel != nil
	if stopping {
		j.Stopped, j.Now = true, "stopping"
		j.cancel()
	}
	writeJSON(w, map[string]any{"stopping": stopping})
}
