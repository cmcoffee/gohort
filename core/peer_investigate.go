// Asking another instance to investigate one of its own systems.
//
// The problem this solves is WHERE THINGS ARE, not who owns them. An appliance
// on the lab network is reachable from the instance that sits on that network
// and from nowhere else; a laptop running gohort cannot open an SSH session to
// it however many credentials it holds. Copying the appliance across would not
// help — it would just move credentials onto a laptop and still fail to route.
//
// So the question travels instead of the data. This mirrors what peer image
// generation already does: the work runs where the resources are, and what
// crosses the wire is a request and a result.
//
// WHY A QUESTION AND NOT A COMMAND. The obvious alternative is to proxy exec —
// let the caller's investigator compose a command and have this side run it.
// That would make a peer key a remote shell on this instance's network, with
// the risk classifier running on the REQUESTING side, which inverts the whole
// reason classify_command_scoped and command_grants.go exist. Here the serving
// instance runs its OWN investigator: its risk gate, its per-(agent, appliance)
// grants, its read-only dispatch. The peer key grants "you may ask this system
// questions", which is a permission a person can read and review.
package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// PeerInvestigateFunc runs one investigation on this instance and returns the
// answer. Set by the app that owns appliances (servitor) at route registration;
// nil means this build cannot serve investigations however a key is granted.
//
// A func var rather than an import because core must not depend on an app.
var PeerInvestigateFunc func(ctx context.Context, user, applianceID, question string) (string, error)

// PeerInvestigableFunc returns the advertisable detail for the appliance ids a
// key may reach, so the manifest can name them and the calling side can build
// something a person recognizes rather than a list of UUIDs.
var PeerInvestigableFunc func(user string, ids []string) []PeerInvestigable

// PeerInvestigable is one appliance a peer key may ask about.
type PeerInvestigable struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Kind string `json:"kind,omitempty"` // the appliance type, for the caller's label
	Desc string `json:"desc,omitempty"`
}

// peerInvestigateTimeout bounds one investigation. Generous: a real one dials a
// host, plans, and runs several probes, and the caller is waiting on a single
// synchronous answer rather than a stream.
const peerInvestigateTimeout = 10 * time.Minute

// HandlePeerInvestigate serves POST /api/peer/v1/investigate.
//
// Body: {"appliance_id": "...", "question": "..."}.
func HandlePeerInvestigate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		peerDeny(w, http.StatusMethodNotAllowed, "POST an {appliance_id, question} body")
		return
	}
	k, ok := peerAuthorize(w, r, PeerCapInvestigate)
	if !ok {
		return
	}
	if PeerInvestigateFunc == nil {
		peerDeny(w, http.StatusServiceUnavailable, "this instance has no investigator wired")
		return
	}
	var body struct {
		ApplianceID string `json:"appliance_id"`
		Question    string `json:"question"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil {
		peerDeny(w, http.StatusBadRequest, "bad request body: "+err.Error())
		return
	}
	id := strings.TrimSpace(body.ApplianceID)
	question := strings.TrimSpace(body.Question)
	if id == "" || question == "" {
		peerDeny(w, http.StatusBadRequest, "appliance_id and question are both required")
		return
	}
	// The capability alone reaches nothing. Naming the id the key was NOT
	// granted is deliberate: a peer misconfigured against the wrong key cannot
	// otherwise tell that from an appliance this instance does not have.
	if !k.AllowsAppliance(id) {
		peerDeny(w, http.StatusForbidden, fmt.Sprintf(
			"this key may not investigate %q (granted: %s)", id, strings.Join(k.Appliances, ", ")))
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), peerInvestigateTimeout)
	defer cancel()
	answer, err := PeerInvestigateFunc(ctx, k.Owner, id, question)
	if err != nil {
		// A failed investigation is a 200 with an error field, not a 5xx: the
		// request was well-formed and authorized, and the caller wants the
		// reason in the same shape as an answer rather than an HTTP failure it
		// has to guess the meaning of.
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"error": err.Error()})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"answer": answer})
}

// peerInvestigablesFor is the manifest's view of what a key may reach.
func peerInvestigablesFor(k PeerKey) []PeerInvestigable {
	if PeerInvestigableFunc == nil || !k.Allows(PeerCapInvestigate) || strings.TrimSpace(k.Owner) == "" {
		return nil
	}
	return PeerInvestigableFunc(k.Owner, k.Appliances)
}

// --- calling side -----------------------------------------------------------

// PeerInvestigate asks a remote peer to investigate one of ITS appliances and
// returns the answer.
//
// Errors distinguish "the call failed" from "the investigation failed", because
// they need different fixes: the first is configuration or reachability, the
// second is a question the far side could not answer.
func PeerInvestigate(ctx context.Context, p RemotePeer, applianceID, question string) (string, error) {
	if strings.TrimSpace(p.BaseURL) == "" || strings.TrimSpace(p.Key) == "" {
		return "", fmt.Errorf("peer %q is not configured with a URL and key", p.Name)
	}
	if !p.Offers(PeerCapInvestigate) {
		return "", peerMissingCapErr(p, PeerCapInvestigate)
	}
	payload, err := json.Marshal(map[string]string{
		"appliance_id": applianceID,
		"question":     question,
	})
	if err != nil {
		return "", err
	}
	base := strings.TrimRight(strings.TrimSpace(p.BaseURL), "/")
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/api/peer/v1/investigate", strings.NewReader(string(payload)))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(peerKeyHeader, p.Key)

	// Timeout matches the server-side cap: a client that gives up sooner than
	// the far side finishes turns a slow answer into a failure with no way to
	// retrieve the work that was already done.
	resp, err := (&http.Client{Timeout: peerInvestigateTimeout}).Do(req)
	if err != nil {
		// A timeout here is not a broken link — it is an investigation still
		// running on the far side with nobody left to receive it. Said plainly,
		// because "context deadline exceeded" reads as a network fault and
		// sends people to check DNS and certificates.
		if errors.Is(err, context.DeadlineExceeded) || strings.Contains(err.Error(), "Client.Timeout") {
			return "", fmt.Errorf(
				"peer %q did not answer within %s. The investigation is probably still running over there — "+
					"a narrower question usually returns; a mapping-sized one will not. "+
					"To re-map that system, do it on %s itself; a refresh here only re-syncs what it already knows",
				p.Name, peerInvestigateTimeout, p.Name)
		}
		return "", fmt.Errorf("reaching peer %q: %w", p.Name, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))

	var out struct {
		Answer string `json:"answer"`
		Error  string `json:"error"`
	}
	_ = json.Unmarshal(raw, &out)
	if resp.StatusCode != http.StatusOK {
		msg := strings.TrimSpace(out.Error)
		if msg == "" {
			msg = strings.TrimSpace(string(raw))
		}
		return "", fmt.Errorf("peer %q refused the investigation (%d): %s", p.Name, resp.StatusCode, msg)
	}
	if strings.TrimSpace(out.Error) != "" {
		return "", fmt.Errorf("peer %q could not answer: %s", p.Name, out.Error)
	}
	if strings.TrimSpace(out.Answer) == "" {
		return "", fmt.Errorf("peer %q returned an empty answer", p.Name)
	}
	return out.Answer, nil
}

// --- gathered knowledge -----------------------------------------------------
//
// The other half of the pattern every LOCAL source already has: an instant read
// of what has already been gathered, beside the slow live investigation. A
// remote stub with only the second half pays a full round trip for questions
// that were answered months ago and written down.
//
// TIMESTAMPS TRAVEL WITH THE CONTENT. Servitor's whole staleness discipline —
// docStaleAfter, the "[last updated: X]" annotations, repoOverviewStale — rests
// on knowing when something was learned. A pulled doc stamped with the PULL
// time would read as fresh while describing how a system worked in June, which
// is worse than not having it: the consumer would state it confidently. So the
// origin's Updated is carried per document and per fact, and the fetch time is
// recorded separately.

// PeerKnowledgeDoc is one structured document as the origin holds it.
type PeerKnowledgeDoc struct {
	Name    string `json:"name"`
	Content string `json:"content"`
	Updated string `json:"updated,omitempty"` // RFC3339, from the ORIGIN
}

// PeerKnowledgeFact is one recorded value as the origin holds it.
type PeerKnowledgeFact struct {
	Key     string   `json:"key"`
	Value   string   `json:"value"`
	Tags    []string `json:"tags,omitempty"`
	Updated string   `json:"updated,omitempty"` // RFC3339, from the ORIGIN
}

// PeerKnowledgeEntity is one node of the origin's topology.
type PeerKnowledgeEntity struct {
	Kind    string            `json:"kind,omitempty"`
	Name    string            `json:"name"`
	Aliases []string          `json:"aliases,omitempty"`
	Attrs   map[string]string `json:"attrs,omitempty"`
	Updated string            `json:"updated,omitempty"` // RFC3339, from the ORIGIN
}

// PeerKnowledgeEdge is one recorded relationship, by entity NAME rather than
// slug id: the receiving side re-slugs on write, and an id minted from a
// different name normalization would dangle.
type PeerKnowledgeEdge struct {
	From string `json:"from"`
	Rel  string `json:"rel"`
	To   string `json:"to"`
	Note string `json:"note,omitempty"`
}

// PeerKnowledge is everything one appliance's origin has written down about it.
type PeerKnowledge struct {
	ApplianceID string              `json:"appliance_id"`
	Name        string              `json:"name"`
	Kind        string              `json:"kind,omitempty"`
	Docs        []PeerKnowledgeDoc  `json:"docs,omitempty"`
	Facts       []PeerKnowledgeFact `json:"facts,omitempty"`
	// Entities and Edges are the accumulated topology — what map_neighbors and
	// map_path traverse, and what makes a member scoutable. Docs and facts
	// without it leave a copied system with values but no shape, so trace
	// questions about it start from nothing.
	Entities []PeerKnowledgeEntity `json:"entities,omitempty"`
	Edges    []PeerKnowledgeEdge   `json:"edges,omitempty"`
	// Scanned is when the ORIGIN last mapped the system — the age of the whole
	// picture, distinct from any one document's.
	Scanned string `json:"scanned,omitempty"`
	// FetchedAt is when this copy was taken. Never used as a content age: it
	// says how current the COPY is, not how current the knowledge is, and
	// conflating the two is the failure this whole struct is shaped to avoid.
	FetchedAt string `json:"fetched_at,omitempty"`
}

// PeerKnowledgeFunc returns the gathered knowledge for one appliance, in the
// owner's context. Set by servitor; nil means this build serves no knowledge.
var PeerKnowledgeFunc func(user, applianceID string) (PeerKnowledge, error)

// HandlePeerKnowledge serves POST /api/peer/v1/knowledge.
func HandlePeerKnowledge(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		peerDeny(w, http.StatusMethodNotAllowed, "POST an {appliance_id} body")
		return
	}
	k, ok := peerAuthorize(w, r, PeerCapKnowledge)
	if !ok {
		return
	}
	if PeerKnowledgeFunc == nil {
		peerDeny(w, http.StatusServiceUnavailable, "this instance serves no gathered knowledge")
		return
	}
	var body struct {
		ApplianceID string `json:"appliance_id"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil {
		peerDeny(w, http.StatusBadRequest, "bad request body: "+err.Error())
		return
	}
	id := strings.TrimSpace(body.ApplianceID)
	if id == "" {
		peerDeny(w, http.StatusBadRequest, "appliance_id is required")
		return
	}
	if !k.AllowsApplianceFor(PeerCapKnowledge, id) {
		peerDeny(w, http.StatusForbidden, fmt.Sprintf(
			"this key may not read knowledge for %q (granted: %s)", id, strings.Join(k.Appliances, ", ")))
		return
	}
	kn, err := PeerKnowledgeFunc(k.Owner, id)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"error": err.Error()})
		return
	}
	kn.FetchedAt = time.Now().UTC().Format(time.RFC3339)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(kn)
}

// PeerFetchKnowledge pulls one appliance's gathered knowledge from a peer.
func PeerFetchKnowledge(ctx context.Context, p RemotePeer, applianceID string) (PeerKnowledge, error) {
	var out PeerKnowledge
	if strings.TrimSpace(p.BaseURL) == "" || strings.TrimSpace(p.Key) == "" {
		return out, fmt.Errorf("peer %q is not configured with a URL and key", p.Name)
	}
	if !p.Offers(PeerCapKnowledge) {
		return out, peerMissingCapErr(p, PeerCapKnowledge)
	}
	payload, _ := json.Marshal(map[string]string{"appliance_id": applianceID})
	base := strings.TrimRight(strings.TrimSpace(p.BaseURL), "/")
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/api/peer/v1/knowledge", strings.NewReader(string(payload)))
	if err != nil {
		return out, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(peerKeyHeader, p.Key)

	resp, err := (&http.Client{Timeout: 60 * time.Second}).Do(req)
	if err != nil {
		return out, fmt.Errorf("reaching peer %q: %w", p.Name, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if resp.StatusCode != http.StatusOK {
		var e struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(raw, &e)
		msg := strings.TrimSpace(e.Error)
		if msg == "" {
			msg = strings.TrimSpace(string(raw))
		}
		return out, fmt.Errorf("peer %q refused the knowledge request (%d): %s", p.Name, resp.StatusCode, msg)
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return out, fmt.Errorf("peer %q returned unreadable knowledge: %w", p.Name, err)
	}
	return out, nil
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
		"(Admin → Resource Sharing → Keys → Grants, over there), or this list is simply out of date — "+
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
	ctx, cancel := context.WithTimeout(r.Context(), peerExecTimeout)
	defer cancel()
	out, err := PeerExecFunc(ctx, k.Owner, id, cmd)
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
	req.Header.Set(peerKeyHeader, p.Key)

	resp, err := (&http.Client{Timeout: peerExecTimeout}).Do(req)
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
