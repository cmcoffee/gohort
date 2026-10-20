// Reaching another instance's systems: the peer as a wire.
//
// The problem this solves is WHERE THINGS ARE, not who owns them. An appliance
// on the lab network is reachable from the instance that sits on that network
// and from nowhere else; a laptop running gohort cannot open an SSH session to
// it however many credentials it holds.
//
// So the command travels, and nothing else does. The calling instance's
// servitor investigates with its own records, its own knowledge and its own
// risk gate; this side authenticates the key, checks the system is in scope,
// runs the command and returns the output. A peer key lends resources, never
// the instance: there is no route that asks this instance's investigator a
// question or copies what it has gathered (both were retired).
package core

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// PeerInvestigableFunc returns the advertisable detail for the appliance ids a
// key may reach, so the manifest can name them and the calling side can build
// something a person recognizes rather than a list of UUIDs.
var PeerInvestigableFunc func(user string, ids []string) []PeerInvestigable

// PeerInvestigable is one appliance a peer key may run commands on. The name
// is from when a peer could also be asked to investigate it.
type PeerInvestigable struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Kind string `json:"kind,omitempty"` // the appliance type, for the caller's label
	Desc string `json:"desc,omitempty"`
}

// peerInvestigablesFor is the manifest's view of what a key may reach.
func peerInvestigablesFor(k PeerKey) []PeerInvestigable {
	if PeerInvestigableFunc == nil || !k.Allows(PeerCapExec) || strings.TrimSpace(k.Owner) == "" {
		return nil
	}
	return PeerInvestigableFunc(k.Owner, k.Appliances)
}

// peerMissingCapErr explains a capability the cached manifest does not list.
//
// The capability list is a SNAPSHOT taken at the last probe, and usableCaps
// keeps only what was both served and granted — so "it offers: …" is three
// different situations wearing one sentence: the far side does not run code
// that serves this yet, the key was never granted it, or both are fine and this
// copy is simply old. Naming all three, with the age of the snapshot, is the
// difference between one fix and an afternoon.
func peerMissingCapErr(p RemotePeer, want string) error {
	when := strings.TrimSpace(p.LastChecked)
	if when == "" {
		when = "never"
	}
	offers := strings.Join(p.Caps, ", ")
	if offers == "" {
		offers = "nothing"
	}
	return fmt.Errorf("peer %q is not offering %q. As of the last check (%s) it offered: %s. "+
		"Either that instance does not serve %q yet (rebuild and restart it), or its key was not granted it "+
		"(Admin → Resource Sharing → Keys → Grants, over there), or this list is simply out of date: "+
		"re-check the peer under Peers to refresh it",
		p.Name, want, when, offers, want)
}

// --- command transport ------------------------------------------------------
//
// The peer as a WIRE rather than a participant. The calling instance runs the
// whole investigation — its plan, its probes, its risk gate, its approvals, its
// accumulated knowledge — and only the command travels. This side authenticates
// the key, checks the appliance is in scope, and runs it.
//
// THE CALLER GATES. That is the operator's decision and it is the thing that
// makes the experience 1:1: an appliance reached this way behaves exactly like a
// local one, because every layer above the exec call is unchanged. The cost is
// real and worth stating plainly — a key granted exec IS a shell on the named
// appliances, so the grant is per appliance, revocable, and separate from every
// other capability. It is for instances the operator owns on both ends.

// PeerExecFunc runs one command against a local appliance and returns its
// output. Set by servitor; nil means this build serves no command transport.
//
// Deliberately does NOT apply the local risk gate: the calling instance already
// classified and approved this command, and running it through a second,
// different policy would make a peer-reached appliance behave unlike a local
// one — which is the whole property this transport exists to provide.
var PeerExecFunc func(ctx context.Context, user, applianceID, command string) (string, error)

// peerExecTimeout bounds one command. Matches servitor's own per-command cap
// closely enough that a command which would time out locally times out here.
const peerExecTimeout = 5 * time.Minute

// HandlePeerExec serves POST /api/peer/v1/exec.
func HandlePeerExec(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		peerDeny(w, http.StatusMethodNotAllowed, "POST an {appliance_id, command} body")
		return
	}
	k, ok := peerAuthorize(w, r, PeerCapExec)
	if !ok {
		return
	}
	r, release, ok := peerHold(w, r, k, PeerCapExec)
	if !ok {
		return
	}
	defer release()
	if PeerExecFunc == nil {
		peerDeny(w, http.StatusServiceUnavailable, "this instance offers no command transport")
		return
	}
	var body struct {
		ApplianceID string `json:"appliance_id"`
		Command     string `json:"command"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil {
		peerDeny(w, http.StatusBadRequest, "bad request body: "+err.Error())
		return
	}
	id, cmd := strings.TrimSpace(body.ApplianceID), body.Command
	if id == "" || strings.TrimSpace(cmd) == "" {
		peerDeny(w, http.StatusBadRequest, "appliance_id and command are both required")
		return
	}
	if !k.AllowsApplianceFor(PeerCapExec, id) {
		peerDeny(w, http.StatusForbidden, fmt.Sprintf(
			"this key may not run commands on %q (granted: %s)", id, strings.Join(k.Appliances, ", ")))
		return
	}
	// AUDIT. This is the widest grant in the system — a shell on a named
	// appliance, run with no risk gate on this side by design — and until now it
	// ran without leaving any record at all. Minting a key logged, a command
	// actually EXECUTED did not, so after an incident
	// there was nothing to read.
	//
	// Logged before the run, not after: a command that hangs or takes the
	// process down with it is exactly the one worth having a line about, and an
	// after-only log loses precisely those.
	Log("[peer] %s EXEC on %s: %s", k.Label, id, truncateForAudit(cmd))
	ctx, cancel := context.WithTimeout(r.Context(), peerExecTimeout)
	defer cancel()
	started := time.Now()
	out, err := PeerExecFunc(ctx, k.Owner, id, cmd)
	// The outcome, on its own line, so a grep for one key's activity shows what
	// happened as well as what was asked.
	if err != nil {
		Log("[peer] %s EXEC on %s failed after %s: %v", k.Label, id,
			time.Since(started).Round(time.Millisecond), err)
	} else {
		Log("[peer] %s EXEC on %s ok in %s (%d bytes)", k.Label, id,
			time.Since(started).Round(time.Millisecond), len(out))
	}
	// Counted like every other capability. This handler never touched the
	// key, so exec registered as zero activity forever, and exec is the
	// widest grant in the system. A key that had run a
	// hundred commands on somebody's lab box looked unused in the admin table.
	touchPeerKey(k)
	w.Header().Set("Content-Type", "application/json")
	if err != nil {
		// A command that failed still has output worth returning: a non-zero
		// exit with stderr is the ANSWER to many probes, and swallowing it in
		// favour of the error would make the caller's investigator blind to the
		// most informative case.
		_ = json.NewEncoder(w).Encode(map[string]any{"output": out, "error": err.Error()})
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"output": out})
}

// PeerExec runs one command on a peer's appliance and returns its output.
func PeerExec(ctx context.Context, p RemotePeer, applianceID, command string) (string, error) {
	if strings.TrimSpace(p.BaseURL) == "" || strings.TrimSpace(p.Key) == "" {
		return "", fmt.Errorf("peer %q is not configured with a URL and key", p.Name)
	}
	if !p.Offers(PeerCapExec) {
		return "", peerMissingCapErr(p, PeerCapExec)
	}
	payload, _ := json.Marshal(map[string]string{"appliance_id": applianceID, "command": command})
	base := strings.TrimRight(strings.TrimSpace(p.BaseURL), "/")
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/api/peer/v1/exec", strings.NewReader(string(payload)))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := PeerClientFor(p, peerExecTimeout).Do(req)
	if err != nil {
		return "", fmt.Errorf("reaching peer %q: %w", p.Name, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<20))

	var out struct {
		Output string `json:"output"`
		Error  string `json:"error"`
	}
	_ = json.Unmarshal(raw, &out)
	if resp.StatusCode != http.StatusOK {
		msg := strings.TrimSpace(out.Error)
		if msg == "" {
			msg = strings.TrimSpace(string(raw))
		}
		return "", fmt.Errorf("peer %q refused the command (%d): %s", p.Name, resp.StatusCode, msg)
	}
	if strings.TrimSpace(out.Error) != "" {
		// Output travels WITH the error for the same reason the server sends
		// both: the caller's investigator reads stderr and a non-zero exit as
		// findings, not as a failed request.
		return out.Output, fmt.Errorf("%s", out.Error)
	}
	return out.Output, nil
}

// auditCommandMax bounds a command in the audit log. Long enough that a real
// command survives whole; short enough that a peer cannot push the log around
// by sending a megabyte of shell.
const auditCommandMax = 400

// truncateForAudit renders a command for the audit log on ONE line.
//
// Newlines are escaped rather than kept, because a multi-line command would
// otherwise split the record across lines and a later line could be crafted to
// look like a separate log entry — an audit trail an attacker can forge entries
// into is worse than none.
func truncateForAudit(cmd string) string {
	cmd = strings.ReplaceAll(cmd, "\r", "")
	cmd = strings.ReplaceAll(cmd, "\n", "\\n")
	if len(cmd) > auditCommandMax {
		// Head AND tail, and a fingerprint of the whole. Keeping only the
		// head let a peer pad a command with harmless text and put what it
		// actually ran past the cut, where the log never showed it.
		sum := sha256.Sum256([]byte(cmd))
		head, tail := cmd[:auditCommandMax*3/5], cmd[len(cmd)-auditCommandMax*2/5:]
		return fmt.Sprintf("%s … %s (%d bytes total, sha256 %x)", head, tail, len(cmd), sum[:8])
	}
	return cmd
}
