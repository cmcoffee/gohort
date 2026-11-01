package service

// A connector is how a service is reached and read; whether gohort uses it is
// decided where it is put to use: a tie-in, a configuration page that names
// it (Web Search's engine, the Worker LLM, a Research Source, a flow's step,
// ...). A services connector (a spec, or an image backend) is in use exactly
// while a tie-in names it: there is no approving it by itself. Its Approved
// mark is that state, kept by Settle after every tie-in save, at startup, and
// after a change.
//
// A connector changed by someone who is not an administrator (the Builder
// on their behalf) since it was put to use is held out of use until an
// administrator accepts the change, by saving a page that names it.

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/flows"
	"github.com/cmcoffee/gohort/core/media"
)

// TieInConnector reports whether a connector of kind is put to use only by
// the pages that name it: a spec, or an image backend.
func TieInConnector(kind string) bool { return kind == Kind || kind == RestImageConnectorKind }

// MusicTable holds the Music tie-in: the music connectors offered to agents
// as tools, under "agents".
const MusicTable = "music_config"

// connectorReviewTable holds a connector changed since it was put to use,
// by whom, until an administrator accepts it.
const connectorReviewTable = "connector_review"

// ConnectorChange is a change waiting for an administrator to accept it.
type ConnectorChange struct {
	By string    `json:"by"`
	At time.Time `json:"at"`
}

// MarkChanged holds connector name out of use until an administrator
// accepts the change by saving a page that names it.
func MarkChanged(db Database, name, by string) {
	if db != nil {
		db.Set(connectorReviewTable, name, ConnectorChange{By: by, At: time.Now()})
	}
}

// PendingChange is the change connector name waits on, if any.
func PendingChange(db Database, name string) (ConnectorChange, bool) {
	var c ConnectorChange
	if db == nil {
		return c, false
	}
	return c, db.Get(connectorReviewTable, name, &c)
}

// serviceName is the connector a provider setting names: "<job>:<name>".
func serviceName(provider string) string {
	name, ok := media.ChosenConnection(strings.TrimSpace(provider))
	if !ok {
		return ""
	}
	return name
}

// TieIns are the pages that name each connector: its name, then the pages.
func TieIns(db Database) map[string][]string {
	out := map[string][]string{}
	add := func(name, page string) {
		if name = strings.TrimSpace(name); name != "" {
			out[name] = append(out[name], page)
		}
	}
	if db == nil {
		return out
	}
	var s string
	db.Get(SearchTable, "provider", &s)
	add(serviceName(s), "Web Search")
	s = ""
	db.Get(ImageTable, "provider", &s)
	if s = strings.TrimSpace(s); s != "none" && !strings.HasPrefix(s, "peer:") {
		add(s, "Image Generation")
	}
	var agents []string
	db.Get(ImageTable, "agents", &agents)
	for _, a := range agents {
		add(a, "Image Generation (offered to agents)")
	}
	var emb EmbeddingConfig
	db.Get(EmbeddingTable, "current", &emb)
	add(serviceName(emb.Provider), "Embeddings")
	var tr media.TranscribeConfig
	db.Get(TranscribeTable, "current", &tr)
	add(serviceName(tr.Provider), "Audio Transcription")
	var sp media.SpeechConfig
	db.Get(media.SpeechTable, "current", &sp)
	add(serviceName(sp.Provider), "Speech")
	s = ""
	db.Get(BrowseTable, "source", &s)
	add(serviceName(s), "Page Rendering")
	s = ""
	db.Get(MailTable, "provider", &s)
	add(serviceName(s), "Mail")
	for table, page := range map[string]string{LLMTable: "Worker LLM", LeadLLMTable: "Lead LLM"} {
		s = ""
		db.Get(table, "provider", &s)
		add(serviceName(s), page)
	}
	agents = nil
	db.Get(MusicTable, "agents", &agents)
	for _, a := range agents {
		add(a, "Music (offered to agents)")
	}
	for _, rs := range ListResearchSources(db) {
		if !rs.Off {
			add(rs.Connector, "Research Sources")
		}
	}
	for _, f := range flowsIn(db) {
		for _, st := range f.Steps {
			add(st.Connector, "Flow "+f.Name)
		}
	}
	for _, c := range ListConnectors(db) {
		if c.Kind != flows.ConnectorKind || !c.Approved {
			continue
		}
		if a, err := adaptedOf(c); err == nil {
			add(a.Service, "System Hooks ("+c.Name+")")
		}
	}
	for name, pages := range out {
		sort.Strings(pages)
		out[name] = dedupe(pages)
	}
	return out
}

// flowsIn are the saved flows in db.
func flowsIn(db Database) []Flow {
	var out []Flow
	for _, k := range db.Keys(FlowTable) {
		var f Flow
		if db.Get(FlowTable, k, &f) {
			out = append(out, f)
		}
	}
	return out
}

// Settle puts every services connector a tie-in names to use, unless a
// change waits on it, and takes every other out of use. accepted are the
// connectors an administrator has just accepted, by saving a page that
// names them: a change waiting on one is accepted with it.
func Settle(db Database, accepted ...string) {
	if db == nil {
		return
	}
	for _, name := range accepted {
		db.Unset(connectorReviewTable, strings.TrimSpace(name))
	}
	uses := TieIns(db)
	for _, c := range ListConnectors(db) {
		if !TieInConnector(c.Kind) {
			continue
		}
		_, held := PendingChange(db, c.Name)
		want := len(uses[c.Name]) > 0 && !held
		switch {
		case want && !c.Approved:
			if err := ApproveConnector(db, c.Name); err != nil {
				Log("[connectors] %s is named by %s but could not be put to use: %v", c.Name, strings.Join(uses[c.Name], ", "), err)
			}
		case !want && c.Approved:
			UnapproveConnector(db, c.Name)
		}
	}
}

// adaptedOf is the adapted System Hook a connector of that kind holds.
func adaptedOf(c Connector) (flows.Adapted, error) {
	var a flows.Adapted
	err := json.Unmarshal(c.Spec, &a)
	return a, err
}

// MigrateTieIns seeds, once, the two tie-ins that had no page before every
// approved connector was offered to agents: Image Generation's "also
// offered to agents", with every approved image connector but the default,
// and Music's, with every approved music connector, so agents keep what
// they had. Then, at every start, it settles what is in use.
func MigrateTieIns(db Database) {
	NewMigrationRunner("core", "").Once("connectors_in_use:v1", func() int {
		return seedTieIns(db)
	})
	Settle(db)
}

func seedTieIns(db Database) int {
	if db == nil {
		return 0
	}
	var def string
	db.Get(ImageTable, "provider", &def)
	var images, music []string
	for _, c := range ListConnectors(db) {
		if !c.Approved || !TieInConnector(c.Kind) {
			continue
		}
		if (c.Kind == RestImageConnectorKind || Provides(c, "image.generate") || Provides(c, "image.edit")) && c.Name != strings.TrimSpace(def) {
			images = append(images, c.Name)
		}
		if Provides(c, "music.generate") {
			music = append(music, c.Name)
		}
	}
	changed := 0
	if len(images) > 0 {
		db.Set(ImageTable, "agents", images)
		changed++
		Log("[migration] Image Generation offers %s to agents, as being approved did", strings.Join(images, ", "))
	}
	if len(music) > 0 {
		db.Set(MusicTable, "agents", music)
		changed++
		Log("[migration] Music offers %s to agents, as being approved did", strings.Join(music, ", "))
	}
	return changed
}

// previews are the model connectors an administrator's LLM test is trying
// before a page has put them to use, each counted while it runs. A tier
// reads its connector with no request to carry the mark, so it is by name.
var previews = struct {
	sync.Mutex
	n map[string]int
}{n: map[string]int{}}

type previewKey struct{}

// previewMark is one preview, carried on its context: the connector,
// whether it has ended, and the preview it was made under, so a test of
// several connectors (a flow's steps) previews each.
type previewMark struct {
	name   string
	ended  atomic.Bool
	parent *previewMark
}

// Preview lets an administrator's test on a settings page (its Test, the
// voices offered) reach connector name before any page has put it to use,
// as choosing it there and testing before saving needs. Calls under the
// returned context reach it, and a model connector can be read for a tier;
// end ends it. Nothing else reaches a connector not in use.
func Preview(ctx context.Context, name string) (context.Context, func()) {
	name = strings.TrimSpace(name)
	if name == "" {
		return ctx, func() {}
	}
	previews.Lock()
	previews.n[name]++
	previews.Unlock()
	parent, _ := ctx.Value(previewKey{}).(*previewMark)
	mark := &previewMark{name: name, parent: parent}
	var once sync.Once
	return context.WithValue(ctx, previewKey{}, mark), func() {
		once.Do(func() {
			mark.ended.Store(true)
			previews.Lock()
			if previews.n[name]--; previews.n[name] <= 0 {
				delete(previews.n, name)
			}
			previews.Unlock()
		})
	}
}

// callable reports whether a call under ctx may reach connector c: in use,
// or under an administrator's preview of it.
func callable(ctx context.Context, c Connector) bool {
	if c.Approved {
		return true
	}
	for mark, _ := ctx.Value(previewKey{}).(*previewMark); mark != nil; mark = mark.parent {
		if mark.name == c.Name && !mark.ended.Load() {
			return true
		}
	}
	return false
}

// readable reports whether model connector c may be read for a tier: in
// use, or under an administrator's preview of it.
func readable(c Connector) bool {
	if c.Approved {
		return true
	}
	previews.Lock()
	defer previews.Unlock()
	return previews.n[c.Name] > 0
}

// RemoveConnector deletes connector name unless a page still names it: that
// page would be left naming nothing, so what uses it is cleared first.
func RemoveConnector(db Database, name string) error {
	if pages := TieIns(db)[strings.TrimSpace(name)]; len(pages) > 0 {
		return fmt.Errorf("%s is in use by %s: take it off there first, then delete it", name, strings.Join(pages, ", "))
	}
	c, _ := GetConnector(db, strings.TrimSpace(name))
	if err := DeleteConnector(db, name); err != nil {
		return err
	}
	forgetRecord(c)
	return nil
}
