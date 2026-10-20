// peer_appliance.go: reaching an appliance that lives on another instance.
//
// This is a NETWORK problem wearing a data-sharing costume. A box on the lab
// network answers the instance sitting on that network and nothing else; a
// laptop cannot open an SSH session to it however many credentials it is given.
// Copying the appliance across would move credentials onto the laptop and still
// fail to route.
//
// So a stub appliance points at (peer, remote appliance id), and its commands
// travel to that peer, which runs them and sends the output back. Everything
// above the command (the plan, the probes, the risk gate, the approvals, the
// knowledge docs and the graph) is THIS instance's. A peer key lends
// resources, never the instance: the far side never answers with its own
// investigator, and never hands over what it has gathered (both were retired).
//
// Both halves live here: serving other instances (the hooks core calls) and
// consuming them (the stub dispatch).
package servitor

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"sort"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// registerPeerSystems wires servitor into core's peer surface: the systems a
// key may run commands on, named for the far side's picker. Called once from
// Routes, where T.DB is live.
func (T *Servitor) registerPeerSystems() {
	// Advertise: what the calling key may reach, named so the far side can
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
		// The program only. The full line can carry arguments the operator
		// never meant to publish (a token, an internal path), and the far side
		// needs a label, not the invocation.
		if f := strings.Fields(a.Command); len(f) > 0 {
			return "local command: " + filepath.Base(f[0])
		}
		return "local command"
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
		// A record made before only administrators could make one keeps no
		// shell its owner was never meant to have.
		if o := strings.TrimSpace(a.Owner); o != "" && !UserIsAdmin(o) {
			return "", fmt.Errorf("this remote system was set up by %s, who is not an administrator: a remote system runs through the deployment's peer link, so an administrator has to recreate it", o)
		}
		peer, ok := GetRemotePeer(a.PeerName)
		if !ok {
			return "", fmt.Errorf("peer %q is not registered on this instance: add it under Peers, or delete this system", a.PeerName)
		}
		// The peer returns the capture whole (see spillCapture); it is
		// spilled HERE, so the output_id the agent gets back is one this
		// instance can page and grep.
		out, err := PeerExec(ctx, peer, a.RemoteID, cmd)
		return spillPeerCapture(out), err
	}
}

// peerApplianceOptions lists the appliances a registered peer says this
// instance may run commands on, for the "add a remote system" picker.
func peerApplianceOptions() map[string][]PeerInvestigable {
	out := map[string][]PeerInvestigable{}
	for _, p := range ListRemotePeers() {
		if !p.Offers(PeerCapExec) || len(p.Investigable) == 0 {
			continue
		}
		out[p.Name] = p.Investigable
	}
	return out
}

// handlePeerAppliances GETs what each registered peer says this instance may
// reach, the source for the "add a remote system" picker.
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

// handleAppliancePeers lists the peer keys that could reach one appliance
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
			http.Error(w, "system not found", http.StatusNotFound)
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
			if !k.Allows(PeerCapExec) {
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
			case k.Owner != "" && k.Owner != owner && !T.reaches(k.Owner, a.ID):
				rw.Blocked = "this key runs as " + k.Owner + ", who cannot reach this system: share the system with " + k.Owner + " first, or use a key owned by " + owner
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
			http.Error(w, "system not found", http.StatusNotFound)
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
		if !key.Allows(PeerCapExec) {
			http.Error(w, "that key is not granted the exec capability: an admin sets that under Resource Sharing", http.StatusBadRequest)
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
			return "", fmt.Errorf("system %q not found", applianceID)
		}
		// A stub must not forward onward. Two instances relaying to each other
		// is a loop neither end can see, and the far one would be executing on
		// behalf of a key it never issued.
		if strings.TrimSpace(a.PeerName) != "" {
			return "", fmt.Errorf("%q is itself a remote system here: commands are not relayed onward", a.Name)
		}
		exec := &Servitor{}
		exec.AppCore = T.AppCore
		// Whole capture back to the caller: the agent that will page it is
		// on the calling instance, and a spill kept here would hand it an
		// output_id this instance alone could serve.
		ctx = withRawCapture(ctx)
		if a.Type == "command" {
			if err := localCommandAllowed(a); err != nil {
				return "", err
			}
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
