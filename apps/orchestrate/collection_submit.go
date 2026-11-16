package orchestrate

// Who may add to a collection, and which of its documents are older versions
// (docs/design/knowledge-one-curation.md, "Submitters and versions").
//
// Curation is the collection's plan, run once or on its schedule. Agents do
// not curate; the owner's agents may SUBMIT documents, each submitter either
// checked at the door (the collection check, and a second source for a
// secondary page) or always allowed (a process the owner trusts, such as one
// that fetches the latest admin guide). A submitter adds; it never takes out.
//
// An older version is kept, not removed: its passages are marked superseded
// by the document that replaced it (the chunk's own MemoryProvenance), so
// search ranks them below the current one and labels them, and a question
// about the older version still finds it. The check marks one when it finds a
// page supersedes a document; a submitter can say what it replaces; the owner
// or a maintainer can set or clear it on the documents list.
//
//	GET  /api/collections/{id}/submitters              the submitters, as rows
//	POST /api/collections/{id}/submitters              save them
//	POST /api/collections/{id}/sources/{doc}/superseded  {"by": doc id, or "" to clear}

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/jsonapi"
)

// How a submitter's documents are taken.
const (
	trustChecked = "checked"
	trustAlways  = "always"
)

// submitterTrust is how agentID's submissions to c are taken: checked,
// always, or "" when it is not listed. An unlisted agent that reaches the
// tool anyway (one stored as curator by an earlier version, which
// submittableCollectionsFor lets through) is checked: only a listed "always"
// skips the check.
func submitterTrust(c Collection, agentID string) string {
	if t, ok := c.Submitters[strings.TrimSpace(agentID)]; ok {
		return normalTrust(t)
	}
	return ""
}

// normalTrust reads a stored or sent trust; anything but "always" is checked.
func normalTrust(t string) string {
	if strings.EqualFold(strings.TrimSpace(t), trustAlways) {
		return trustAlways
	}
	return trustChecked
}

// submittableCollectionsFor are the collections of user's that agentID may
// submit to. Only the owner's own (or, for an administrator, the
// deployment's): a submitter writes on its owner's runs.
func submittableCollectionsFor(udb Database, user, agentID string) []Collection {
	agentID = strings.TrimSpace(agentID)
	if agentID == "" {
		return nil
	}
	admin := UserIsAdmin(user)
	var out []Collection
	for _, c := range ListCollections(udb, user) {
		ownerless := IsDeploymentScope(c) && c.Owner == ""
		if c.Owner != user && !(ownerless && admin) {
			continue
		}
		if submitterTrust(c, agentID) == "" {
			// A curator stored by name, from before ids were stored.
			who := strings.TrimSpace(c.CuratorAgent)
			if who == "" || who == collectionAutoCurator {
				continue
			}
			if a, ok := findAgentByNameOrID(udb, user, who); !ok || a.ID != agentID {
				continue
			}
		}
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// --- Older versions ----------------------------------------------------------

// docChunks are one document's passages in collection c.
func (T *OrchestrateApp) docChunks(c Collection, doc string) []EmbeddedChunk {
	prefix := collectionSource(c.ID)
	return ChunksWhere(T.collectionDB(c), func(x EmbeddedChunk) bool {
		return x.ReportID == doc && strings.HasPrefix(x.Source, prefix)
	})
}

// markSuperseded marks doc in c as an older version of by. False when either
// is not in the collection, or they are the same.
func (T *OrchestrateApp) markSuperseded(c Collection, doc, by string) bool {
	doc, by = strings.TrimSpace(doc), strings.TrimSpace(by)
	if doc == "" || by == "" || doc == by || len(T.docChunks(c, by)) == 0 {
		return false
	}
	chunks := T.docChunks(c, doc)
	if len(chunks) == 0 {
		return false
	}
	now := time.Now()
	for i := range chunks {
		chunks[i].Reason, chunks[i].Successor, chunks[i].RetiredAt = RetireSuperseded, by, now
	}
	PutChunks(T.collectionDB(c), chunks)
	Log("[orchestrate.collections] collection=%s: %s marked as an older version of %s", c.ID, doc, by)
	return true
}

// clearSuperseded makes doc in c current again.
func (T *OrchestrateApp) clearSuperseded(c Collection, doc string) {
	chunks := T.docChunks(c, doc)
	var changed []EmbeddedChunk
	for _, ch := range chunks {
		if ch.Reason == RetireSuperseded {
			ch.Reason, ch.Successor, ch.RetiredAt = RetireLive, "", time.Time{}
			changed = append(changed, ch)
		}
	}
	PutChunks(T.collectionDB(c), changed)
}

// forgetSuccessor makes current again every document in c that removed had
// replaced: with the newer version gone, the older one is what there is.
func (T *OrchestrateApp) forgetSuccessor(c Collection, removed string) {
	forgetSuccessorIn(T.collectionDB(c), c, removed)
}

// forgetSuccessorIn is forgetSuccessor on the store c's passages are in, for
// a tool with no app at hand.
func forgetSuccessorIn(db Database, c Collection, removed string) {
	prefix := collectionSource(c.ID)
	var changed []EmbeddedChunk
	for _, ch := range ChunksWhere(db, func(x EmbeddedChunk) bool {
		return x.Reason == RetireSuperseded && x.Successor == removed && strings.HasPrefix(x.Source, prefix)
	}) {
		ch.Reason, ch.Successor, ch.RetiredAt = RetireLive, "", time.Time{}
		changed = append(changed, ch)
	}
	PutChunks(db, changed)
}

// docByTitleOrID finds a document in c by its id or its exact title, for a
// submitter saying what its document replaces.
func (T *OrchestrateApp) docByTitleOrID(c Collection, ref string) (string, bool) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", false
	}
	prefix := collectionSource(c.ID)
	found := ""
	for _, ch := range ChunksWhere(T.collectionDB(c), func(x EmbeddedChunk) bool { return strings.HasPrefix(x.Source, prefix) }) {
		if ch.ReportID == ref {
			return ref, true
		}
		if found == "" && strings.EqualFold(strings.TrimSpace(ch.Title), ref) {
			found = ch.ReportID
		}
	}
	return found, found != ""
}

// collectionSuperseded sets or clears which document replaced one: from the
// documents list, for the owner or a maintainer.
func (T *OrchestrateApp) collectionSuperseded(call *jsonapi.Call, body struct {
	By string `json:"by"`
}) (any, error) {
	c, err := T.collectionAt(call, "supersede")
	if err != nil {
		return nil, err
	}
	doc := call.R.PathValue("doc")
	if len(T.docChunks(c, doc)) == 0 {
		return nil, errNotFound
	}
	if strings.TrimSpace(body.By) == "" {
		T.clearSuperseded(c, doc)
		return map[string]any{"ok": true}, nil
	}
	if !T.markSuperseded(c, doc, body.By) {
		return nil, jsonapi.BadRequest("pick another document in this collection as the one that replaced it")
	}
	return map[string]any{"ok": true}, nil
}

// --- Submitters on the page --------------------------------------------------

// submitterRow is one submitter as the page edits it.
type submitterRow struct {
	Agent string `json:"agent"`
	Trust string `json:"trust"`
}

func (T *OrchestrateApp) collectionSubmitters(call *jsonapi.Call) (any, error) {
	c, err := T.collectionAt(call, "steward")
	if err != nil {
		return nil, err
	}
	rows := []submitterRow{}
	for agent, trust := range c.Submitters {
		rows = append(rows, submitterRow{Agent: agent, Trust: normalTrust(trust)})
	}
	if who := strings.TrimSpace(c.CuratorAgent); who != "" && who != collectionAutoCurator {
		if _, listed := c.Submitters[who]; !listed {
			rows = append(rows, submitterRow{Agent: who, Trust: trustChecked})
		}
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Agent < rows[j].Agent })
	return map[string]any{"submitters": rows}, nil
}

// collectionSubmittersSave keeps the submitters as edited: each one of the
// owner's own agents. An agent stored as curator by an earlier version moves
// here on the first save.
func (T *OrchestrateApp) collectionSubmittersSave(call *jsonapi.Call, body struct {
	Submitters json.RawMessage `json:"submitters"`
}) (any, error) {
	c, err := T.collectionAt(call, "steward")
	if err != nil {
		return nil, err
	}
	var rows []submitterRow
	if len(body.Submitters) > 0 && string(body.Submitters) != "null" {
		if err := json.Unmarshal(body.Submitters, &rows); err != nil {
			return nil, jsonapi.BadRequest("submitters: a list of {agent, trust}")
		}
	}
	udb := UserDB(T.DB, call.User)
	out := map[string]string{}
	for _, r := range rows {
		who := strings.TrimSpace(r.Agent)
		if who == "" {
			continue
		}
		a, ok := findAgentByNameOrID(udb, call.User, who)
		if !ok || a.ID == "" || hiddenAppAgent(a.ID) || (a.Owner != "" && a.Owner != call.User) {
			return nil, jsonapi.BadRequest(fmt.Sprintf("%q is not one of your agents", who))
		}
		out[a.ID] = normalTrust(r.Trust)
	}
	if len(out) == 0 {
		out = nil
	}
	c.Submitters = out
	if who := strings.TrimSpace(c.CuratorAgent); who != collectionAutoCurator {
		c.CuratorAgent = ""
	}
	saveCollection(T.collectionHome(call.UDB, c), c)
	return map[string]any{"ok": true}, nil
}

// supersededNote is the line for a page added that replaced an older version.
func supersededNote(oldTitle string) string {
	return fmt.Sprintf("%q is marked as the older version, ranked below this one.", oldTitle)
}
