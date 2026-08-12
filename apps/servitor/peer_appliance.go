// peer_appliance.go — reaching an appliance that lives on another instance.
//
// This is a NETWORK problem wearing a data-sharing costume. A box on the lab
// network answers the instance sitting on that network and nothing else; a
// laptop cannot open an SSH session to it however many credentials it is given.
// Copying the appliance across would move credentials onto the laptop and still
// fail to route.
//
// So a stub appliance points at (peer, remote appliance id), and asking it a
// question sends the QUESTION to that peer. The far side runs its own
// investigator against its own SSH config, on its own network, under its own
// risk gate and its own per-(agent, appliance) grants. What comes back is prose.
//
// Both halves live here: serving other instances (the hooks core calls) and
// consuming them (the stub dispatch).
package servitor

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"

	"github.com/cmcoffee/gohort/apps/orchestrate"
)

// registerPeerInvestigation wires servitor into core's peer surface. Called
// once from Routes, where T.DB is live.
func (T *Servitor) registerPeerInvestigation() {
	// Serve: run one investigation for a peer, as the key's owner.
	//
	// InvestigateSync is reused verbatim rather than reimplemented — it is the
	// same call the reference-source investigate_<system> tool makes, so a
	// question arriving over the wire is answered by exactly the machinery that
	// answers one asked locally, including the auto-deny that keeps it
	// read-only. A second implementation here would be the place the two
	// silently diverged on what "read-only" means.
	PeerInvestigateFunc = func(ctx context.Context, user, applianceID, question string) (string, error) {
		return T.InvestigateSync(ctx, user, applianceID, question)
	}

	// Advertise: what the calling key may ask about, named so the far side can
	// build something a person recognizes instead of a list of UUIDs.
	PeerInvestigableFunc = func(user string, ids []string) []PeerInvestigable {
		if user == "" || len(ids) == 0 {
			return nil
		}
		udb := UserDB(T.DB, user)
		if udb == nil {
			return nil
		}
		var out []PeerInvestigable
		for _, id := range ids {
			a, _, _, ok := T.resolveAppliance(user, udb, id)
			if !ok || a.ID == "" {
				// Granted an id that no longer resolves. Skipped rather than
				// advertised as unreachable: the caller cannot act on it, and
				// the operator sees the stale grant in the admin key editor.
				continue
			}
			// A stub must never point at a stub — that is a loop across two
			// instances, and neither end can see the cycle.
			if strings.TrimSpace(a.PeerName) != "" {
				continue
			}
			out = append(out, PeerInvestigable{
				ID:   a.ID,
				Name: applianceLabel(a.Name, a.ID),
				Kind: firstNonEmptyStr(a.Type, "ssh"),
				Desc: peerApplianceDesc(a),
			})
		}
		sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
		return out
	}
}

// peerApplianceDesc renders a one-line description for the manifest, so the
// consuming side can label a stub with something better than an id.
func peerApplianceDesc(a Appliance) string {
	switch a.Type {
	case "repo":
		return repoDisplayTarget(a)
	case "bundle":
		return bundleDisplayTarget(a)
	case "toolset":
		return toolsetDisplayTarget(a)
	case "workspace":
		return fmt.Sprintf("%d members", len(a.Members))
	case "command":
		return a.Command
	}
	return a.Host
}

func firstNonEmptyStr(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}

// --- consuming side ---------------------------------------------------------

// peerExecFor returns the exec function for a stub appliance: the same
// (command) -> (output, error) shape the local SSH and local-command paths
// have, so runSession's sshExec seam swaps transport without anything above it
// knowing.
//
// That substitution IS the feature. The plan, the probes, the risk gate, the
// approval prompts, the scratch directory, the knowledge docs and the graph all
// sit above this line and run identically, so a machine reached through a peer
// behaves like one reached directly.
func peerExecFor(ctx context.Context, a Appliance) func(string) (string, error) {
	return func(cmd string) (string, error) {
		peer, ok := GetRemotePeer(a.PeerName)
		if !ok {
			return "", fmt.Errorf("peer %q is not registered on this instance — add it under Peers, or delete this system", a.PeerName)
		}
		return PeerExec(ctx, peer, a.RemoteID, cmd)
	}
}

// peerApplianceOptions lists the appliances a registered peer says this
// instance may investigate, for the "add a remote system" picker.
func peerApplianceOptions() map[string][]PeerInvestigable {
	out := map[string][]PeerInvestigable{}
	for _, p := range ListRemotePeers() {
		if !p.Offers(PeerCapInvestigate) || len(p.Investigable) == 0 {
			continue
		}
		out[p.Name] = p.Investigable
	}
	return out
}

// handlePeerAppliances GETs what each registered peer says this instance may
// investigate — the source for the "add a remote system" picker.
func (T *Servitor) handlePeerAppliances(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if _, _, ok := RequireUser(w, r, T.DB); !ok {
		return
	}
	type row struct {
		Peer string `json:"peer"`
		ID   string `json:"id"`
		Name string `json:"name"`
		Kind string `json:"kind,omitempty"`
		Desc string `json:"desc,omitempty"`
	}
	out := []row{}
	byPeer := peerApplianceOptions()
	names := make([]string, 0, len(byPeer))
	for name := range byPeer {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		for _, it := range byPeer[name] {
			out = append(out, row{Peer: name, ID: it.ID, Name: it.Name, Kind: it.Kind, Desc: it.Desc})
		}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(out)
}

// --- sharing one appliance TO a peer, from the appliance's own page ----------
//
// The grant is stored on the peer key (one key, one peer, revocable in one
// place). But "who can see this box?" is a question you ask while looking at
// the box, and servitor already taught that sharing is something you set on an
// appliance — the Shared toggle lives right there. Hiding this one grant in a
// different app was a discoverability failure, so the appliance page gets a
// second door onto the same data.
//
// Gated by canManageAppliance, matching the existing Shared toggle: the OWNER
// can share their own system, not just an admin. That is safe because
// SetPeerKeyScope refuses any appliance the key's owner cannot already resolve
// — so granting can never widen what that identity reaches, only expose to a
// peer what it already had.

// handleAppliancePeers lists the peer keys that could investigate one appliance
// and which of them currently may, and toggles that membership.
func (T *Servitor) handleAppliancePeers(w http.ResponseWriter, r *http.Request) {
	userID, udb, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	if udb == nil {
		http.Error(w, "no database", http.StatusInternalServerError)
		return
	}

	switch r.Method {
	case http.MethodGet:
		id := strings.TrimSpace(r.URL.Query().Get("appliance_id"))
		a, owner, _, found := T.resolveAppliance(userID, udb, id)
		if !found {
			http.Error(w, "appliance not found", http.StatusNotFound)
			return
		}
		type row struct {
			KeyID   string `json:"key_id"`
			Label   string `json:"label"`
			Owner   string `json:"owner,omitempty"`
			Granted bool   `json:"granted"`
			// Why this key cannot be offered, when it cannot. Shown rather than
			// filtered out: a key missing from the list with no explanation
			// reads as "there are no peers", which is a different problem with
			// a different fix.
			Blocked string `json:"blocked,omitempty"`
		}
		out := []row{}
		for _, k := range ListPeerKeys() {
			if !k.Allows(PeerCapInvestigate) {
				continue
			}
			rw := row{KeyID: k.ID, Label: k.Label, Owner: k.Owner}
			for _, granted := range k.Appliances {
				if granted == a.ID {
					rw.Granted = true
					break
				}
			}
			switch {
			case k.Owner != "" && k.Owner != owner && !a.Shared:
				rw.Blocked = "this key runs as " + k.Owner + ", who cannot reach this system — share the system with all users first, or use a key owned by " + owner
			case k.Disabled:
				rw.Blocked = "this key is disabled"
			}
			out = append(out, rw)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"can_manage": canManageAppliance(userID, a, servitorIsAdmin(r)),
			"keys":       out,
		})

	case http.MethodPost, http.MethodPut:
		var body struct {
			ApplianceID string `json:"appliance_id"`
			KeyID       string `json:"key_id"`
			Granted     bool   `json:"granted"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		a, owner, _, found := T.resolveAppliance(userID, udb, strings.TrimSpace(body.ApplianceID))
		if !found {
			http.Error(w, "appliance not found", http.StatusNotFound)
			return
		}
		if !canManageAppliance(userID, a, servitorIsAdmin(r)) {
			http.Error(w, "not allowed to change this system's sharing", http.StatusForbidden)
			return
		}
		var key PeerKey
		for _, k := range ListPeerKeys() {
			if k.ID == strings.TrimSpace(body.KeyID) {
				key = k
				break
			}
		}
		if key.ID == "" {
			http.Error(w, "no such peer key", http.StatusNotFound)
			return
		}
		if !key.Allows(PeerCapInvestigate) {
			http.Error(w, "that key is not granted the investigate capability — an admin sets that under Resource Sharing", http.StatusBadRequest)
			return
		}
		// A key that has never been scoped adopts this appliance's owner. It
		// reached nothing before, so this widens nothing; it just saves an
		// admin round trip to set a field whose only correct value here is the
		// owner of the system being granted.
		keyOwner := key.Owner
		if keyOwner == "" {
			keyOwner = owner
		}
		next := make([]string, 0, len(key.Appliances)+1)
		for _, id := range key.Appliances {
			if id != a.ID {
				next = append(next, id)
			}
		}
		if body.Granted {
			next = append(next, a.ID)
		}
		// SetPeerKeyScope re-validates every id against the owner, so removing
		// one from a key whose OTHER entries have gone stale surfaces that here
		// rather than silently rewriting a broken grant.
		if _, err := SetPeerKeyScope(key.ID, keyOwner, next); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		Log("[servitor.peer] user=%q set appliance %q on key %q to granted=%v", userID, a.ID, key.Label, body.Granted)
		w.WriteHeader(http.StatusOK)

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// --- gathered knowledge, both directions ------------------------------------

// registerPeerKnowledge serves this instance's gathered knowledge to peers.
// Called alongside registerPeerInvestigation.
func (T *Servitor) registerPeerKnowledge() {
	PeerKnowledgeFunc = func(user, applianceID string) (PeerKnowledge, error) {
		udb := UserDB(T.DB, user)
		if udb == nil {
			return PeerKnowledge{}, fmt.Errorf("no store for %q", user)
		}
		a, _, ownerUDB, ok := T.resolveAppliance(user, udb, applianceID)
		if !ok || ownerUDB == nil {
			return PeerKnowledge{}, fmt.Errorf("appliance %q not found", applianceID)
		}
		out := PeerKnowledge{
			ApplianceID: a.ID,
			Name:        applianceLabel(a.Name, a.ID),
			Kind:        firstNonEmptyStr(a.Type, "ssh"),
			Scanned:     a.Scanned,
		}
		// Each document carries the timestamp the ORIGIN holds, not now().
		for _, name := range knowledgeDocNames {
			var entry KnowledgeDocEntry
			if !ownerUDB.Get(knowledgeTable, a.ID+":"+name, &entry) || strings.TrimSpace(entry.Content) == "" {
				continue
			}
			out.Docs = append(out.Docs, PeerKnowledgeDoc{
				Name: name, Content: entry.Content, Updated: entry.Updated,
			})
		}
		for _, f := range factsForAppliance(ownerUDB, a.ID) {
			if strings.TrimSpace(f.Key) == "" {
				continue
			}
			out.Facts = append(out.Facts, PeerKnowledgeFact{
				Key: f.Key, Value: f.Value, Tags: f.Tags, Updated: f.Updated,
			})
		}
		if orch, scope, ok := mapScope(a.ID); ok {
			ents, edges := orch.ScopedGraphExport(scope)
			byID := map[string]string{}
			for _, e := range ents {
				byID[e.ID] = e.Name
				out.Entities = append(out.Entities, PeerKnowledgeEntity{
					Kind: e.Kind, Name: e.Name, Aliases: e.Aliases, Attrs: e.Attrs,
					Updated: e.Updated.UTC().Format(time.RFC3339),
				})
			}
			for _, ed := range edges {
				from, fok := byID[ed.From]
				to, tok := byID[ed.To]
				if !fok || !tok {
					continue
				}
				out.Edges = append(out.Edges, PeerKnowledgeEdge{From: from, Rel: ed.Rel, To: to, Note: ed.Note})
			}
		}
		return out, nil
	}
}

// pullPeerKnowledge backfills what the owning instance already knows about a
// machine reached through a peer, so the first question here does not re-derive
// months of work.
//
// MERGED BY RECENCY, not replaced. Replacement was right while a stub was
// passive; it is a data-loss bug now that this instance investigates the same
// machine and records what it finds — a second pull would delete everything
// learned locally. So each document and fact is kept from whichever side has
// the newer timestamp, which is only possible because the origin's timestamps
// travel with the content.
//
// Returns what came across and what was kept local, because a merge that
// silently reports a total tells you nothing about which copy you are now
// reading.
func (T *Servitor) pullPeerKnowledge(ctx context.Context, ownerUDB Database, a Appliance) (imported, kept int, err error) {
	if strings.TrimSpace(a.PeerName) == "" || ownerUDB == nil {
		return 0, 0, fmt.Errorf("not a remote system")
	}
	peer, ok := GetRemotePeer(a.PeerName)
	if !ok {
		return 0, 0, fmt.Errorf("peer %q is not registered here", a.PeerName)
	}
	kn, ferr := PeerFetchKnowledge(ctx, peer, a.RemoteID)
	if ferr != nil {
		return 0, 0, ferr
	}
	for _, d := range kn.Docs {
		if newerHere(docUpdatedAt(ownerUDB, a.ID, d.Name), d.Updated) {
			kept++
			continue
		}
		// The ORIGIN's timestamp, deliberately. See writeDocAt.
		writeDocAt(ownerUDB, a.ID, d.Name, d.Content, d.Updated)
		imported++
	}
	for _, f := range kn.Facts {
		var mine SshFact
		if ownerUDB.Get(factsTable, a.ID+":"+f.Key, &mine) && newerHere(mine.Updated, f.Updated) {
			kept++
			continue
		}
		ownerUDB.Set(factsTable, a.ID+":"+f.Key, SshFact{
			ID:            a.ID + ":" + f.Key,
			ApplianceID:   a.ID,
			ApplianceName: a.Name,
			Key:           f.Key,
			Value:         f.Value,
			Tags:          f.Tags,
			Updated:       f.Updated,
		})
		imported++
	}
	if len(kn.Entities) > 0 || len(kn.Edges) > 0 {
		if orch, scope, ok := mapScope(a.ID); ok {
			ents := make([]GraphEntity, 0, len(kn.Entities))
			for _, e := range kn.Entities {
				g := GraphEntity{Kind: e.Kind, Name: e.Name, Aliases: e.Aliases, Attrs: e.Attrs}
				if t, perr := time.Parse(time.RFC3339, e.Updated); perr == nil {
					g.Updated = t
				}
				ents = append(ents, g)
			}
			edges := make([]orchestrate.GraphEdgeByName, 0, len(kn.Edges))
			for _, ed := range kn.Edges {
				edges = append(edges, orchestrate.GraphEdgeByName{
					From: ed.From, Rel: ed.Rel, To: ed.To, Note: ed.Note,
				})
			}
			gi, gk := orch.ScopedGraphImport(scope, ents, edges)
			imported += gi
			kept += gk
		}
	}
	return imported, kept, nil
}

// newerHere reports whether the local copy is more recently updated than the
// incoming one. An unparseable or missing local timestamp loses: the incoming
// side at least states when it learned the thing.
func newerHere(local, incoming string) bool {
	lt, lerr := time.Parse(time.RFC3339, strings.TrimSpace(local))
	if lerr != nil {
		return false
	}
	it, ierr := time.Parse(time.RFC3339, strings.TrimSpace(incoming))
	if ierr != nil {
		return true
	}
	return lt.After(it)
}

// docUpdatedAt returns the stored timestamp of one local document, or "".
func docUpdatedAt(udb Database, applianceID, doc string) string {
	var entry KnowledgeDocEntry
	if udb == nil || !udb.Get(knowledgeTable, applianceID+":"+doc, &entry) {
		return ""
	}
	return entry.Updated
}

// refreshPeerKnowledge re-syncs a stub's local copy of the owning instance's
// knowledge and reports what landed.
func (T *Servitor) refreshPeerKnowledge(ctx context.Context, id string, appliance Appliance, ownerUDB Database) {
	emit(id, probeEvent{Kind: "status", Text: "Syncing what " + appliance.PeerName + " knows about " + appliance.Name + "…"})
	imported, kept, err := T.pullPeerKnowledge(ctx, ownerUDB, appliance)
	if err != nil {
		probeSessions.AppendEvent(id, probeEvent{Kind: "error", Text: err.Error()}, true)
		probeSessions.ScheduleCleanup(id)
		return
	}
	if imported == 0 && kept == 0 {
		probeSessions.AppendEvent(id, probeEvent{Kind: "reply", Text: "Synced, but " + appliance.PeerName +
			" has nothing recorded about this system yet. Map it on " + appliance.PeerName +
			" first — a refresh here copies what it knows, it does not make it go and look."}, true)
		probeSessions.ScheduleCleanup(id)
		return
	}
	msg := fmt.Sprintf("Took %d item(s) from %s", imported, appliance.PeerName)
	if kept > 0 {
		// Named explicitly: after a merge you are reading a mixture, and which
		// side won matters when the two disagree.
		msg += fmt.Sprintf(" and kept %d that this instance had learned more recently", kept)
	}
	msg += ".\n\nAges shown against this system are the ORIGIN's — when each thing was learned, not when it was copied here."
	probeSessions.AppendEvent(id, probeEvent{Kind: "reply", Text: msg}, true)
	probeSessions.ScheduleCleanup(id)
}

// registerPeerExec serves the command transport: run one command against a
// local appliance for a peer that cannot reach it.
//
// It runs the command RAW — no risk classification, no grant check, no approval
// prompt. That is deliberate and is the operator's stated decision: the calling
// instance already gated it, and applying a second policy here would make a
// peer-reached appliance behave differently from a local one, which is exactly
// what this transport exists to prevent. The protection is the grant: a key
// reaches only the appliances named on it, and nothing else on this machine.
func (T *Servitor) registerPeerExec() {
	PeerExecFunc = func(ctx context.Context, user, applianceID, command string) (string, error) {
		udb := UserDB(T.DB, user)
		if udb == nil {
			return "", fmt.Errorf("no store for %q", user)
		}
		a, _, _, ok := T.resolveAppliance(user, udb, applianceID)
		if !ok || a.ID == "" {
			return "", fmt.Errorf("appliance %q not found", applianceID)
		}
		// A stub must not forward onward. Two instances relaying to each other
		// is a loop neither end can see, and the far one would be executing on
		// behalf of a key it never issued.
		if strings.TrimSpace(a.PeerName) != "" {
			return "", fmt.Errorf("%q is itself a remote system here — commands are not relayed onward", a.Name)
		}
		exec := &Servitor{}
		exec.AppCore = T.AppCore
		if a.Type == "command" {
			return exec.exec_local_ctx(ctx, command, a.WorkDir, a.EnvVars)
		}
		client, err := acquireConn(user, a)
		if err != nil {
			return "", fmt.Errorf("connecting to %s: %w", applianceLabel(a.Name, a.ID), err)
		}
		exec.input.host, exec.input.port, exec.input.user, exec.input.password = a.Host, a.Port, a.User, a.Password
		if exec.input.port == 0 {
			exec.input.port = 22
		}
		if exec.input.user == "" {
			exec.input.user = "root"
		}
		exec.conn = client
		return exec.exec_command_ctx(ctx, command)
	}
}
