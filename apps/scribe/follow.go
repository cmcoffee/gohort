package scribe

// Following: a guide kept up to date from another, through its Sources.
//
// An agent reports into one guide, its Curator files what comes in, and Keep
// published updates its published copies. A guide that has that one among its
// Sources, set to Distill rather than Consult on its pill, takes what it needs
// from each of those changes: when a Curator pass changes it, the sections
// that pass changed are distilled into the follower (distill.go) by the
// follower's own brief, its own Curator files them, and its own Keep published
// publishes it. So one agent's reports keep two documents current, each
// written for its own reader, set where the Source is attached.
//
// Only the sections a pass changed are read, compared against the revision
// from before the pass, so a follower is not sent again what it already has.
// What triggers it is a Curator pass, as with Keep published: an edit made by
// hand is not mirrored (Update from sources is the full catch-up).
// What the followed guide drops is not dropped from the follower, which only
// ever gets findings. Two guides that would feed each other, directly or
// through others, feed neither.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/jsonapi"
)

// follows reports whether g keeps itself up to date from srcID: srcID is one
// of its Sources, set to Distill.
func follows(g Guide, srcID string) bool {
	if g.ID == srcID {
		return false
	}
	attached := false
	for _, ref := range g.References {
		if ref.Kind == guideSourceKind && ref.ItemID == srcID {
			attached = true
			break
		}
	}
	if !attached {
		return false
	}
	for _, id := range g.DistillFrom {
		if id == srcID {
			return true
		}
	}
	return false
}

// guideSourceKind is a guide attached as a Source (agent_source.go).
const guideSourceKind = "guide"

// followersOf is every guide of the user's that follows srcID.
func followersOf(udb Database, srcID string) []Guide {
	var out []Guide
	for _, g := range listGuides(udb) {
		if follows(g, srcID) {
			out = append(out, g)
		}
	}
	return out
}

// followLoops reports whether dst following src closes a loop: src already
// follows dst, directly or through others, so a change to either would come
// back round to it for good.
func followLoops(udb Database, dstID, srcID string) bool {
	guides := listGuides(udb)
	seen := map[string]bool{}
	stack := []string{srcID}
	for len(stack) > 0 {
		cur := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if cur == dstID {
			return true
		}
		if seen[cur] {
			continue
		}
		seen[cur] = true
		// What cur follows: its guide Sources set to Distill.
		for _, g := range guides {
			if g.ID != cur {
				continue
			}
			for _, id := range g.DistillFrom {
				if follows(g, id) {
					stack = append(stack, id)
				}
			}
		}
	}
	return false
}

// changedSince is src with only the sections that differ from revision revID:
// new, retitled or rewritten. With no revision to compare against (a guide
// that had none before the pass) every section counts as changed.
func changedSince(udb Database, src Guide, revID string) Guide {
	out := src
	out.Sections = nil
	rev, ok := Guide{}, false
	if revID != "" {
		if r, found := loadRevision(udb, src.ID, revID); found {
			rev, ok = r.Guide, true
		}
	}
	before := map[string]Section{}
	if ok {
		for _, s := range rev.Sections {
			before[s.ID] = s
		}
	}
	for _, s := range src.sorted() {
		if b, was := before[s.ID]; was && b.Title == s.Title && strings.TrimSpace(b.Markdown) == strings.TrimSpace(s.Markdown) {
			continue
		}
		out.Sections = append(out.Sections, s)
	}
	return out
}

// followAfter distills, into each guide that follows one this run changed, the
// sections the run changed there. Called beside keepPublishedAfter when a
// Curator run ends, failed runs included: the entries before a failure are
// real writes.
func (T *Scribe) followAfter(user string, run CuratorRun) {
	udb := UserDB(T.DB, user)
	if udb == nil {
		return
	}
	for _, f := range followTargets(udb, user, run) {
		T.startFollow(user, f.changed, f.dst, changedNote(f.changed, f.dst))
	}
}

// followTarget is one follow-on distill a run calls for: what changed in the
// followed guide, and the guide that follows it.
type followTarget struct {
	changed, dst Guide
}

// followTargets is every follow-on distill run calls for.
func followTargets(udb Database, user string, run CuratorRun) []followTarget {
	var out []followTarget
	// The revision from before the pass's first write to each guide.
	prior := map[string]string{}
	var order []string
	for _, e := range run.Entries {
		switch e.Kind {
		case OutcomePlaced, OutcomeSuperseded:
		default:
			continue
		}
		if e.GuideID == "" || e.Undone {
			continue
		}
		if _, seen := prior[e.GuideID]; !seen {
			prior[e.GuideID] = e.PriorRev
			order = append(order, e.GuideID)
		}
	}
	for _, srcID := range order {
		followers := followersOf(udb, srcID)
		if len(followers) == 0 {
			continue
		}
		src, ok := loadGuide(udb, srcID)
		if !ok {
			continue
		}
		changed := changedSince(udb, src, prior[srcID])
		if len(changed.Sections) == 0 {
			continue
		}
		for _, dst := range followers {
			// Checked again here, not only when it was set: two settings
			// saved one after the other can close a loop neither closed alone.
			if followLoops(udb, dst.ID, srcID) {
				Log("[scribe.follow] user=%q: %q and %q follow each other, so neither is distilled into the other", user, dst.Title, src.Title)
				continue
			}
			out = append(out, followTarget{changed: changed, dst: dst})
		}
	}
	return out
}

var (
	followMu      sync.Mutex
	followPending = map[string]Guide{} // job key: changed sections waiting for the run in progress
)

// followJobKey keys a follow-on distill: one per pair, so a guide followed
// twice distills into both at once, and a distill started by hand from the
// dialog (keyed by the source alone) is its own job.
func followJobKey(srcID, dstID string) string {
	return distillJobPrefix + srcID + ">" + dstID
}

// startFollow distills changed into dst as a background job on the Monitor,
// with dst's Curator placing what it takes. A pass that changes the source
// while one is still running for the pair waits for it, with what both passes
// changed. note is the job's first line: why it is reading what it reads.
func (T *Scribe) startFollow(user string, changed, dst Guide, note string) {
	key := followJobKey(changed.ID, dst.ID)
	if j, running := currentPublishJob(key); running && !j.Done {
		followMu.Lock()
		followPending[key] = mergeChanged(followPending[key], changed)
		followMu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(AppContext())
	job := &publishJob{Target: dst.Title, Started: time.Now(), cancel: cancel,
		owner: user, guideID: key, title: changed.Title, distilling: true, background: true}
	publishJobsMu.Lock()
	publishJobs[key] = job
	publishJobsMu.Unlock()
	d := distillRun{T: T, user: user, src: changed, dst: dst, what: distillDefault, curateNow: true, job: job, jobID: newID()}
	job.step(note)
	Log("[scribe.follow] user=%q: %d changed section(s) of %q into %q", user, len(changed.Sections), changed.Title, dst.Title)
	go func() {
		d.run(ctx)
		followMu.Lock()
		next, again := followPending[key]
		delete(followPending, key)
		followMu.Unlock()
		if !again || ctx.Err() != nil {
			return
		}
		// The follower as it is now: its brief may have changed meanwhile.
		if cur, ok := loadGuide(UserDB(T.DB, user), dst.ID); ok && follows(cur, changed.ID) {
			T.startFollow(user, next, cur, changedNote(next, cur))
		}
	}()
}

// mergeChanged is the sections of both a and b, b's copy winning: what two
// passes changed, for the run that waits on the one in progress.
func mergeChanged(a, b Guide) Guide {
	if a.ID == "" {
		return b
	}
	out := b
	out.Sections = nil
	idx := map[string]int{}
	for _, s := range append(append([]Section{}, a.Sections...), b.Sections...) {
		if i, ok := idx[s.ID]; ok {
			out.Sections[i] = s
			continue
		}
		idx[s.ID] = len(out.Sections)
		out.Sections = append(out.Sections, s)
	}
	return out
}

// changedNote is a follow-on distill's first line.
func changedNote(changed, dst Guide) string {
	return fmt.Sprintf("%s changed %s; taking what %s needs from them.", changed.Title, countOf(len(changed.Sections), "section"), dst.Title)
}

// --- Update from sources ------------------------------------------------------

// handleUpdateFromSources is the Update from sources button's server half:
// POST ?id=<guide>. Every guide Source set to Distill is read in full and
// sent to the Curator, the catch-up for what a pass never changed and for
// edits made there by hand. It answers {distilling: [titles], consult: n},
// and the page then asks the Author to check the sections against the n
// Sources left on Consult. Distilling from a guide was a button on the SOURCE
// ("Distill into another guide") pushing into a guide picked each time; it is
// set where the Source is attached now, so the catch-up is started there too.
func (T *Scribe) handleUpdateFromSources(c *jsonapi.Call, _ struct{}) (any, error) {
	r, udb, user := c.R, c.UDB, c.User
	g, ownerUDB, _, canEdit, found := T.resolve(r, udb, user, strings.TrimSpace(r.URL.Query().Get("id")))
	if !found {
		return nil, errNoGuide
	}
	if !canEdit {
		return nil, jsonapi.Forbidden("You need edit access to update this guide.")
	}
	// The findings go to the owner's Curator, which is the one that files
	// into the owner's guide.
	owner := firstNonEmpty(g.Owner, user)
	distilling := []string{}
	for _, id := range g.DistillFrom {
		if !follows(g, id) || followLoops(ownerUDB, g.ID, id) {
			continue
		}
		src, ok := loadGuide(ownerUDB, id)
		if !ok || len(src.Sections) == 0 {
			continue
		}
		T.startFollow(owner, src, g, fmt.Sprintf("Reading all of %s, %s, for what %s needs.", src.Title, countOf(len(src.Sections), "section"), g.Title))
		distilling = append(distilling, firstNonEmpty(src.Title, "Untitled guide"))
	}
	consult := len(g.Collections)
	for _, ref := range g.References {
		if !(ref.Kind == guideSourceKind && follows(g, ref.ItemID)) {
			consult++
		}
	}
	return map[string]any{"distilling": distilling, "consult": consult}, nil
}

// What the Author is asked to do with the Sources left on Consult. Guides set
// to Distill are left to the distill the button started beside it.
const (
	updateSourcesEvery = "Update this guide so its sections reflect its LINKED SOURCES (the attached knowledge collections and reference sources) as they stand right now. Leave out any guide among them set to Distill: it is being read in full separately, and what it holds reaches this guide through the Curator.\n\n" +
		"1. Call list_sections to see the current structure.\n" +
		"2. For the guide's subject and each section, use search_knowledge and pull_reference to gather what the linked sources CURRENTLY say.\n" +
		"3. Where a section is outdated or contradicted by the sources, call edit_section to revise it: grounded strictly in the sources, carrying any citations. Where the sources cover something the guide is missing that its writing brief says belongs in it, add_section for it. The sources can hold more than the brief covers: leave the rest out, even where it sits next to what you take.\n" +
		"4. Leave sections that already match their sources unchanged: don't rewrite for the sake of it. Work ONLY from the guide's linked sources here; do not use web research.\n\n" +
		"When done, reply with a short bulleted summary of exactly which sections you changed or added and why. If nothing needed changing, say so plainly."
	updateSourcesOther = "Update this guide from its LINKED SOURCES only: the attached knowledge collections and reference sources, not web research, and not a guide among them set to Distill, which is being read in full separately. Use search_knowledge and pull_reference to check, and edit_section to revise. Take from the sources only what the guide's writing brief says belongs in it. "
)

// guideUpdateSourcesAction is the Update from sources button: choose every
// section or say which, then the distills start and the Author gets the rest.
var guideUpdateSourcesAction = strings.Replace(guideUpdateSourcesJS, "__TEXTS__", updateSourcesTexts(), 1)

func updateSourcesTexts() string {
	b, _ := json.Marshal(map[string]string{"every": updateSourcesEvery, "other": updateSourcesOther})
	return string(b)
}

const guideUpdateSourcesJS = `function(ctx){
      var gid = ctx.recordId;
      if (!gid || !window.uiOpenSimpleModal) return;
      var texts = __TEXTS__;
      var qp = 'id=' + encodeURIComponent(gid);
      window.uiOpenSimpleModal({title:'Update from linked sources', width:'520px', mount: function(body, dlg){
        body.appendChild(el('p', {class:'guide-kn-intro', text:'Guides set to Distill in Sources are read in full, and what this guide\u2019s brief needs from them goes to its Curator. The Author checks the sections against the other Sources and revises what has drifted.'}));
        var extra = el('input', {type:'text', placeholder:'e.g. only the Install section: the rest is still right'});
        extra.style.width = '100%'; extra.style.padding = '0.45rem 0.6rem'; extra.style.background = 'var(--bg-0)';
        extra.style.color = 'var(--text)'; extra.style.border = '1px solid var(--border)'; extra.style.borderRadius = '6px';
        function go(text, btn){
          btn.disabled = true; btn.textContent = 'Starting\u2026';
          fetch('sources/update?' + qp, {method:'POST', credentials:'same-origin'})
            .then(function(r){ return r.ok ? r.json() : r.text().then(function(t){ throw new Error(t || ('HTTP ' + r.status)); }); })
            .then(function(d){
              try { dlg.close(); dlg.remove(); } catch(e){}
              var names = (d && d.distilling) || [];
              if (names.length) window.uiToast('Reading ' + names.join(', ') + ' in full for this guide: its Curator files what fits. On the Monitor while it runs.');
              if (d && d.consult > 0) {
                if (typeof window.uiComposeMessage !== 'function') { window.uiToast('No conversation on this page to ask the Author in.'); return; }
                window.uiComposeMessage(text, {send: true});
              } else if (!names.length) {
                window.uiAlert('This guide has no Sources to update from. Attach some in Sources: a guide there can be set to Distill.');
              }
            })
            .catch(function(err){ btn.disabled = false; btn.textContent = btn.dataset.label; window.uiAlert('Update failed: ' + (err && err.message || err)); });
        }
        function choice(label, help, onpick){
          var b = el('button', {class:'ui-row-btn', type:'button', text: label});
          b.dataset.label = label;
          b.style.display = 'block'; b.style.width = '100%'; b.style.textAlign = 'left'; b.style.margin = '0.5rem 0 0.15rem';
          b.addEventListener('click', function(){ onpick(b); });
          body.appendChild(b);
          body.appendChild(el('div', {class:'guide-pub-mute', style:'font-size:0.8rem', text: help}));
        }
        choice('Every section', 'Check each section against the linked sources and revise what has drifted.', function(b){ go(texts.every, b); });
        choice('Something else', 'Say which sections, or what to look for.', function(b){
          var said = extra.value.trim();
          if (!said) { extra.focus(); return; }
          go(texts.other + said, b);
        });
        body.appendChild(extra);
      }});
}`
