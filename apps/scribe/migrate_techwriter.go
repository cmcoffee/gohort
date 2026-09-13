// One-time carry-over of TechWriter's library into Scribe. Every article a
// user had becomes an article-kind document in their Scribe store, with its
// revision trail and their house-style rules. The source bucket is left as it
// was (a copy, not a move), and each user is marked done so a restart never
// imports twice.
package scribe

import (
	"sort"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/docs"
)

// TechWriter's store, as it named itself. These are the on-disk names the
// retired package wrote under, so they cannot follow any rename here.
const (
	techWriterBucket        = "techwriter"
	techWriterHistoryTable  = "techwriter_history"
	techWriterRevisionTable = "techwriter_revisions"
	techWriterRulesNS       = "techwriter"
	migrationsTable         = "scribe_migrations"
)

// twArticle / twRevision mirror TechWriter's stored records field for field.
// kvlite encodes with gob, which matches on field NAME, so these decode what
// the old package wrote without importing it.
type twArticle struct {
	ID       string
	Subject  string
	Body     string
	Date     string
	ImageURL string
}

type twRevision struct {
	ID        string
	ArticleID string
	Subject   string
	Body      string
	Date      string
}

// migrateTechWriter imports every known user's TechWriter articles once.
// Users are the auth roster; a user with no TechWriter data is marked done
// with nothing copied, so the roster is only walked in full on the first
// start after the upgrade.
func (T *Scribe) migrateTechWriter() {
	if T.DB == nil || RootDB == nil || AuthDB == nil {
		return
	}
	adb := AuthDB()
	if adb == nil {
		return
	}
	src := RootDB.Bucket(techWriterBucket)
	total := 0
	for _, u := range AuthListUsers(adb) {
		if u.Username == "" {
			continue
		}
		var done bool
		if T.DB.Get(migrationsTable, "techwriter:"+u.Username, &done) && done {
			continue
		}
		n := migrateTechWriterUser(UserDB(src, u.Username), UserDB(T.DB, u.Username), u.Username)
		T.DB.Set(migrationsTable, "techwriter:"+u.Username, true)
		if n > 0 {
			Log("[scribe] %s: %d TechWriter article(s) carried over", u.Username, n)
			total += n
		}
	}
	if total > 0 {
		Log("[scribe] TechWriter library carried over: %d article(s)", total)
	}
}

// migrateTechWriterUser copies one user's articles, revisions and rules from
// their TechWriter store into their Scribe store. Returns how many articles
// landed. An article whose id already exists here is skipped, so a partial
// earlier run finishes rather than duplicates.
func migrateTechWriterUser(src, dst Database, user string) int {
	if src == nil || dst == nil {
		return 0
	}
	// Revisions first, grouped by article, oldest first — they become each
	// article's History.
	byArticle := map[string][]twRevision{}
	for _, k := range src.Keys(techWriterRevisionTable) {
		var rev twRevision
		if src.Get(techWriterRevisionTable, k, &rev) && rev.ArticleID != "" {
			byArticle[rev.ArticleID] = append(byArticle[rev.ArticleID], rev)
		}
	}
	n := 0
	for _, k := range src.Keys(techWriterHistoryTable) {
		var rec twArticle
		if !src.Get(techWriterHistoryTable, k, &rec) {
			continue
		}
		if rec.ID == "" {
			rec.ID = k
		}
		if _, exists := loadGuide(dst, rec.ID); exists {
			continue
		}
		g := newArticle(user, rec.Subject, rec.Body)
		g.ID = rec.ID
		g.ImageURL = rec.ImageURL
		g.Created = firstNonEmpty(rec.Date, now())
		g.Updated = g.Created
		dst.Set(guidesTable, g.ID, g)

		revs := byArticle[rec.ID]
		sort.SliceStable(revs, func(i, j int) bool { return revs[i].Date < revs[j].Date })
		var rl guideRevisions
		for _, rev := range revs {
			snap := g
			snap.Title = firstNonEmpty(strings.TrimSpace(rev.Subject), g.Title)
			snap.Sections = nil
			snap.setBody(rev.Body)
			snap.Updated = firstNonEmpty(rev.Date, g.Updated)
			rl.Revisions = append(rl.Revisions, GuideRevision{
				ID:    firstNonEmpty(rev.ID, newID()),
				At:    snap.Updated,
				Note:  "TechWriter revision",
				Guide: snap,
			})
		}
		if len(rl.Revisions) == 0 {
			rl.Revisions = []GuideRevision{{ID: newID(), At: g.Updated, Note: "Carried over from TechWriter", Guide: g}}
		}
		if keep := maxRevisions(); len(rl.Revisions) > keep {
			rl.Revisions = rl.Revisions[len(rl.Revisions)-keep:]
		}
		dst.Set(revisionsTable, g.ID, rl)
		n++
	}
	// House-style rules: only when Scribe has none yet, so a user who already
	// wrote rules here keeps theirs.
	if rules := LoadDocRules(src, techWriterRulesNS); rules != "" && LoadDocRules(dst, rulesNamespace) == "" {
		docs.SaveDocRules(dst, rulesNamespace, rules)
	}
	return n
}
