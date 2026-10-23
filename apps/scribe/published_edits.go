// Edits made where a guide is published come back through the Guide Curator.
//
// A living guide is published to a wiki where other people and agents curate
// it too. Scribe stays the one copy agents read and the only thing that
// writes the page; an edit made on the page becomes a FINDING for the guide,
// one per changed section, and the Curator weaves it in like any report. The
// next update then carries it back out, merged rather than erased.
//
// What a copy said when it was last published is kept as the destination
// read it back (its baseline), never as the markdown Scribe sent: two reads of
// one page come back in one format, so only real edits differ.
//
// When the page is read:
//   - before every update (the publish guard, on every publish path): edits
//     found there are filed and the update stops, so nothing is overwritten
//     before the Curator has seen it;
//   - when an agent reads a guide with Keep published on, at most every
//     publishCheckEvery per copy, in the background: so curation on the page
//     reaches the agents that read the guide here.

package scribe

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/docs"
)

const publishBaselineTable = "guide_publish_baselines"

// publishBaseline is a published copy as it read after its last publish (or
// as it read when its edits were last taken in).
type publishBaseline struct {
	Sections []docs.DocSection `json:"sections"`
	Version  string            `json:"version,omitempty"`
	At       string            `json:"at"`
}

func baselineKey(guideID, kind string) string { return guideID + "|" + kind }

func loadBaseline(udb Database, guideID, kind string) (publishBaseline, bool) {
	var b publishBaseline
	ok := udb.Get(publishBaselineTable, baselineKey(guideID, kind), &b)
	return b, ok
}

func saveBaseline(udb Database, guideID, kind string, cur docs.PublishedCopy) {
	udb.Set(publishBaselineTable, baselineKey(guideID, kind), publishBaseline{
		Sections: docs.SplitSections(cur.Body), Version: cur.Version,
		At: time.Now().UTC().Format(time.RFC3339Nano),
	})
}

// deleteBaselines drops every baseline of a guide.
func deleteBaselines(udb Database, guideID string) {
	for _, k := range udb.Keys(publishBaselineTable) {
		if strings.HasPrefix(k, guideID+"|") {
			udb.Unset(publishBaselineTable, k)
		}
	}
}

// errEditedThere is an update stopped because the page was edited at its
// destination: its edits went to the Curator instead of being overwritten.
var errEditedThere = errors.New("edited where it is published")

// editsLock serializes taking in one copy's edits, so a guard and a
// background check reading at once do not file the same edit twice.
var editsLock sync.Map // baselineKey -> *sync.Mutex

func editsMutex(key string) *sync.Mutex {
	m, _ := editsLock.LoadOrStore(key, &sync.Mutex{})
	return m.(*sync.Mutex)
}

// publishedWhere is a copy's place, for findings and messages.
func publishedWhere(g Guide, kind string) string {
	if rec, ok := docs.FindPublishRecord(g.Published, kind); ok {
		return firstNonEmpty(rec.TargetTitle, rec.Kind)
	}
	return kind
}

// takeInEdits compares a copy as it reads now with its baseline, files a
// finding for each section edited there, and makes what it read the new
// baseline. since, when set, is baselineMark as the read started: a publish
// that landed while the copy was being read moved the baseline, and the read
// is then older than it and is dropped. Returns how many findings it filed.
func (T *Scribe) takeInEdits(user string, udb Database, g Guide, kind string, cur docs.PublishedCopy, since string) int {
	mu := editsMutex(baselineKey(g.ID, kind))
	mu.Lock()
	defer mu.Unlock()
	base, ok := loadBaseline(udb, g.ID, kind)
	if since != "" && baselineMark(base, ok) != since {
		return 0
	}
	if !ok {
		// The first look at this copy: nothing to compare it with, so it
		// becomes what the next look is compared with.
		saveBaseline(udb, g.ID, kind, cur)
		return 0
	}
	if base.Version != "" && base.Version == cur.Version {
		return 0
	}
	changes := docs.ChangedSections(base.Sections, docs.SplitSections(cur.Body))
	saveBaseline(udb, g.ID, kind, cur)
	if len(changes) == 0 {
		return 0
	}
	where := publishedWhere(g, kind)
	observed := time.Now().UTC().Format(time.RFC3339)
	filed := 0
	for _, c := range changes {
		_, err := SubmitFinding(user, "guide", DocFinding{
			Topic:      editTopic(c, where),
			Content:    editContent(c, where),
			Confidence: "probable",
			Document:   g.ID,
			Origin: DocFindingOrigin{
				SourceKind: "published copy",
				ItemID:     kind,
				ItemLabel:  where,
				Observed:   observed,
			},
		})
		if err != nil {
			Log("[scribe.published_edits] user=%q guide=%q: could not file an edit from %s: %v", user, g.ID, where, err)
			continue
		}
		filed++
	}
	if filed > 0 {
		Log("[scribe.published_edits] user=%q guide=%q: %d edit(s) in %s filed for the Curator", user, firstNonEmpty(g.Title, g.ID), filed, where)
		T.curateSoon(user)
	}
	return filed
}

// baselineMark tells one baseline from the next, "-" for none.
func baselineMark(b publishBaseline, ok bool) string {
	if !ok {
		return "-"
	}
	return b.At
}

// editClip bounds each side of an edit in a finding.
const editClip = 6000

func clipEdit(s string) string {
	if r := []rune(s); len(r) > editClip {
		return string(r[:editClip]) + "\n(cut here)"
	}
	return s
}

func editTopic(c docs.SectionChange, where string) string {
	title := firstNonEmpty(c.Title, "the opening")
	switch c.Kind {
	case "added":
		return fmt.Sprintf("Section added in %s: %s", where, title)
	case "removed":
		return fmt.Sprintf("Section removed in %s: %s", where, title)
	}
	return fmt.Sprintf("Section edited in %s: %s", where, title)
}

func editContent(c docs.SectionChange, where string) string {
	title := firstNonEmpty(c.Title, "the opening (before the first heading)")
	switch c.Kind {
	case "added":
		return fmt.Sprintf("Someone added a section %q to this guide where it is published (%s). It reads:\n\n%s", title, where, clipEdit(c.After))
	case "removed":
		return fmt.Sprintf("Someone removed the section %q from this guide where it is published (%s). As last published, it read:\n\n%s", title, where, clipEdit(c.Before))
	}
	return fmt.Sprintf("Someone edited the section %q of this guide where it is published (%s).\n\nAs last published:\n\n%s\n\nAs it reads there now:\n\n%s", title, where, clipEdit(c.Before), clipEdit(c.After))
}

// guideWatcher follows every publish of a guide (docs.RegisterPublishWatcher).
//
// A guide published by someone it is shared with is guarded the same as the
// owner's own publish, against the OWNER's guide: its baselines are the
// owner's, and edits found on the page go to the owner's Curator. It used to
// act on the publisher's own guides only, so a co-editor's publish found no
// guide of theirs and overwrote the page unchecked.
type guideWatcher struct{ app *Scribe }

// resolve finds the guide a publish is of, the publisher's own or one shared
// with them, and whose it is.
func (w guideWatcher) resolve(user, id string) (owner string, odb Database, g Guide, ok bool) {
	if w.app == nil || w.app.DB == nil {
		return "", nil, Guide{}, false
	}
	udb := UserDB(w.app.DB, user)
	if udb == nil {
		return "", nil, Guide{}, false
	}
	g, owner, odb, ok = resolveGuide(w.app.DB, udb, user, id)
	return owner, odb, g, ok && odb != nil
}

// errNoBaseline is an update stopped because there is no record of how its
// copy read after the last publish, so whether it was edited there cannot be
// told.
var errNoBaseline = errors.New("no record of how it read when last published")

// BeforeUpdate takes in any edits made on the page and, when there were
// some, stops the update: they go to the Curator first.
func (w guideWatcher) BeforeUpdate(ctx context.Context, user string, doc docs.PublishDoc, kind string, cur docs.PublishedCopy) error {
	owner, udb, g, ok := w.resolve(user, doc.SourceID)
	if !ok {
		return nil
	}
	// A copy with no baseline (its read-back failed after the last publish,
	// or it was published before baselines were kept) could hold edits made
	// there, and nothing to tell them by. It used to take what it read as the
	// baseline and update anyway, overwriting any. Now it keeps what it read
	// and stops once; the next update compares against it.
	if _, had := loadBaseline(udb, g.ID, kind); !had {
		w.app.takeInEdits(owner, udb, g, kind, cur, "")
		return fmt.Errorf("%w: %s had no record of how it read after its last publish, so Scribe cannot tell whether anyone edited it there and did not overwrite it. It has recorded how the page reads now: look it over there, then publish again to update it",
			errNoBaseline, publishedWhere(g, kind))
	}
	n := w.app.takeInEdits(owner, udb, g, kind, cur, "")
	if n == 0 {
		return nil
	}
	then := "Update it again once the Curator has placed them."
	if g.KeepPublished {
		then = "It is updated again once the Curator has placed them."
	}
	return fmt.Errorf("%w: somebody changed %d section%s in %s since it was last published, so it was not overwritten. The changes went to the Guide Curator. %s",
		errEditedThere, n, plural(n), publishedWhere(g, kind), then)
}

// Landed keeps what the copy reads after a publish as its baseline. A copy
// that could not be read back has none: the next look starts a new one.
func (w guideWatcher) Landed(user string, doc docs.PublishDoc, kind string, cur docs.PublishedCopy, read bool) {
	_, udb, g, ok := w.resolve(user, doc.SourceID)
	if !ok {
		return
	}
	mu := editsMutex(baselineKey(g.ID, kind))
	mu.Lock()
	defer mu.Unlock()
	if read {
		saveBaseline(udb, g.ID, kind, cur)
		return
	}
	udb.Unset(publishBaselineTable, baselineKey(g.ID, kind))
}

// publishCheckEvery is how often, at most, an agent reading a guide has its
// published copies looked at for edits.
var publishCheckEvery = 10 * time.Minute

var publishChecked sync.Map // baselineKey -> time.Time

// checkPublishedSoon looks at a Keep published guide's copies for edits, in
// the background, each at most every publishCheckEvery. Called when an agent
// reads the guide: it is reading what the guide says here, and curation done
// on the page should reach it.
func (T *Scribe) checkPublishedSoon(user string, g Guide) {
	if !g.KeepPublished || len(g.Published) == 0 {
		return
	}
	for _, rec := range g.Published {
		key := baselineKey(g.ID, rec.Kind)
		if last, ok := publishChecked.Load(key); ok && time.Since(last.(time.Time)) < publishCheckEvery {
			continue
		}
		publishChecked.Store(key, time.Now())
		go T.checkPublishedCopy(AppContext(), user, g.ID, rec)
	}
}

// checkPublishedCopy reads one copy and takes in its edits. A publish of the
// guide running now reads the copy itself before it writes, so this leaves it
// to that.
func (T *Scribe) checkPublishedCopy(ctx context.Context, user, guideID string, rec docs.PublishRecord) {
	if j, ok := currentPublishJob(guideID); ok && !j.Done {
		return
	}
	udb := UserDB(T.DB, user)
	if udb == nil {
		return
	}
	since := baselineMark(loadBaseline(udb, guideID, rec.Kind))
	cur, err := docs.ReadPublished(ctx, user, rec)
	if errors.Is(err, docs.ErrCannotReadBack) {
		return
	}
	if err != nil {
		Log("[scribe.published_edits] user=%q guide=%q: could not read the copy in %s: %v", user, guideID, firstNonEmpty(rec.TargetTitle, rec.Kind), err)
		return
	}
	g, ok := loadGuide(udb, guideID)
	if !ok {
		return
	}
	T.takeInEdits(user, udb, g, rec.Kind, cur, since)
}
