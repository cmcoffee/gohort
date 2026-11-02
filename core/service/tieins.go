package service

// A connector is how a service is reached and read; whether gohort uses it is
// decided where it is referenced: a configuration page that names it (Web
// Search's engine, Image Generation, Research Sources, a flow's step, ...).
// Those are its references: they guard its deletion and show on its row. A
// connector has no on/off state of its own; it is served from its save, and
// whatever reaches it does so through a reference.
//
// A connector changed by someone who is not an administrator (the Builder
// on their behalf) is held until an administrator accepts the change, by
// saving a page that names it: no call reaches it meanwhile.

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/flows"
	"github.com/cmcoffee/gohort/core/media"
)

// TieInConnector reports whether a connector of kind is reached only
// through the pages that name it: a service.
func TieInConnector(kind string) bool { return kind == Kind }

// MusicTable holds the Music tie-in: the music connectors offered to agents
// as tools, under "agents".
const MusicTable = "music_config"

// connectorReviewTable holds a connector changed by someone who is not an
// administrator, by whom, until an administrator accepts it.
const connectorReviewTable = "connector_review"

// ConnectorChange is a change waiting for an administrator to accept it.
type ConnectorChange struct {
	By string    `json:"by"`
	At time.Time `json:"at"`
}

// MarkChanged holds connector name until an administrator accepts the
// change by saving a page that names it.
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

// AgentConnections are the connections an agent's tools reach, by name:
// the image connectors (the one Image Generation uses and those offered to
// agents), the music connectors offered to agents, the Research Sources
// offered through query_source, and those a flow's steps call. An agent's
// credential scope and a connection's Access decide who reaches these; a
// connection a page has serve the whole instance is not among them.
func AgentConnections(db Database) []string {
	if db == nil {
		return nil
	}
	var names []string
	var s string
	db.Get(ImageTable, "provider", &s)
	names = append(names, strings.TrimSpace(s))
	for _, table := range []string{ImageTable, MusicTable} {
		var agents []string
		db.Get(table, "agents", &agents)
		names = append(names, agents...)
	}
	for _, rs := range ListResearchSources(db) {
		if !rs.Off && strings.TrimSpace(rs.ToolName) != "" {
			names = append(names, rs.Connector)
		}
	}
	for _, f := range flowsIn(db) {
		for _, st := range f.Steps {
			names = append(names, st.Connector)
		}
	}
	var out []string
	seen := map[string]bool{}
	for _, n := range names {
		n = strings.TrimSpace(n)
		if n == "" || seen[n] {
			continue
		}
		seen[n] = true
		if c, ok := GetConnector(db, n); ok && c.Kind == Kind {
			out = append(out, n)
		}
	}
	sort.Strings(out)
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

// AcceptChanges accepts the changes waiting on connectors named, as an
// administrator saving a page that names them does, and serves each again
// as it now is.
func AcceptChanges(db Database, named ...string) {
	if db == nil {
		return
	}
	for _, name := range named {
		name = strings.TrimSpace(name)
		if _, held := PendingChange(db, name); !held {
			continue
		}
		db.Unset(connectorReviewTable, name)
		if c, ok := GetConnector(db, name); ok && c.Kind == Kind {
			if err := (handler{}).Materialize(c); err != nil {
				Log("[connectors] %s: the change was accepted, but serving it failed: %v", name, err)
			}
		}
	}
}

// held reports whether a change waits on connector name.
func held(name string) bool {
	_, ok := PendingChange(RootDB, name)
	return ok
}

// Referenced reports whether a page names connector name.
func Referenced(db Database, name string) bool {
	return len(TieIns(db)[strings.TrimSpace(name)]) > 0
}

// adaptedOf is the adapted System Hook a connector of that kind holds.
func adaptedOf(c Connector) (flows.Adapted, error) {
	var a flows.Adapted
	err := json.Unmarshal(c.Spec, &a)
	return a, err
}

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
// voices offered) reach connector name while a change waits on it, as
// choosing it there and testing before saving (which accepts the change)
// needs. Calls under the returned context reach it; end ends it.
func Preview(ctx context.Context, name string) (context.Context, func()) {
	name = strings.TrimSpace(name)
	if name == "" {
		return ctx, func() {}
	}
	parent, _ := ctx.Value(previewKey{}).(*previewMark)
	mark := &previewMark{name: name, parent: parent}
	return context.WithValue(ctx, previewKey{}, mark), func() { mark.ended.Store(true) }
}

// callable reports whether a call under ctx may reach connector c: no
// change waits on it, or it is under an administrator's preview.
func callable(ctx context.Context, c Connector) bool {
	if !held(c.Name) {
		return true
	}
	for mark, _ := ctx.Value(previewKey{}).(*previewMark); mark != nil; mark = mark.parent {
		if mark.name == c.Name && !mark.ended.Load() {
			return true
		}
	}
	return false
}

// RemoveConnector deletes connector name unless a page still names it: that
// page would be left naming nothing, so what uses it is cleared first.
func RemoveConnector(db Database, name string) error {
	if pages := TieIns(db)[strings.TrimSpace(name)]; len(pages) > 0 {
		return fmt.Errorf("%s is referenced by %s: take it off there first, then delete it", name, strings.Join(pages, ", "))
	}
	c, _ := GetConnector(db, strings.TrimSpace(name))
	if err := DeleteConnector(db, name); err != nil {
		return err
	}
	forgetRecord(c)
	return nil
}
