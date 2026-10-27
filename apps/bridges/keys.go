package bridges

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/jsonapi"
)

// handleKeys lists / creates bridge keys (a connector's credential + service).
//
//	GET  /bridges/api/keys           → [{id, name, service, created, last_seen}]  (secret masked)
//	POST /bridges/api/keys {name, service} → the created key, secret shown ONCE
func (T *Bridges) handleKeys(c *jsonapi.Call) (any, error) {
	type keyView struct {
		ID       string `json:"id"`
		Name     string `json:"name"`
		Service  string `json:"service"`
		Enabled  bool   `json:"enabled"`
		Created  string `json:"created"`
		LastSeen string `json:"last_seen,omitempty"`
	}
	out := []keyView{}
	for _, k := range T.listBridgeKeys(c.User) {
		out = append(out, keyView{
			ID: k.ID, Name: k.Name, Service: k.Service,
			Enabled: k.Enabled, Created: k.Created, LastSeen: k.LastSeen,
		})
	}
	return out, nil
}

// handleKeyMint creates a key. An empty body is fine: it mints a generic "api"
// key (the body is a pointer, so none reaches it as nil). A non-empty but malformed
// body is a client bug, rejected rather than silently creating an unnamed
// generic key the caller never asked for.
func (T *Bridges) handleKeyMint(c *jsonapi.Call, req *keyMintReq) (any, error) {
	if req == nil {
		req = &keyMintReq{}
	}
	svc := strings.TrimSpace(req.Service)
	// A key for a messaging service (imessage, …) polls and drains that
	// service's outbox, which is the deployment owner's outgoing
	// messages: an administrator's to mint. Anybody may mint a generic
	// API key for their own clients.
	if svc != "" && svc != "api" && !RequestIsAdmin(c.R) {
		return nil, jsonapi.Forbidden("only an administrator can create a key for the " + svc + " bridge")
	}
	if svc == "" {
		// A MANUALLY minted key is for the MCP server or a server-side
		// connector — never iMessage, whose key the gohort-bridge daemon
		// auto-registers (with Service:"imessage" set explicitly). So an
		// unspecified service here means a generic API key, not iMessage.
		svc = "api"
	}
	k := BridgeKey{
		ID:      newToken()[:12],
		Name:    strings.TrimSpace(req.Name),
		Key:     newToken(),
		Owner:   c.User,
		Service: svc,
		Enabled: true, // bridges start on
		Created: now(),
	}
	T.saveBridgeKey(k)
	Log("[bridges] created bridge key %q for service %s", k.Name, k.Service)
	return k, nil // secret returned once
}

// keyMintReq is what handleKeyMint is posted.
type keyMintReq struct{ Name, Service string }

// ownKey is the caller's bridge key the path names.
func (T *Bridges) ownKey(c *jsonapi.Call) (BridgeKey, bool) {
	var k BridgeKey
	if !T.DB.Get(bridgeKeysTable, c.R.PathValue("id"), &k) || k.Owner != c.User {
		return BridgeKey{}, false
	}
	return k, true
}

// keyToggleBody is the per-bridge enable switch's post.
type keyToggleBody struct {
	Enabled bool `json:"enabled"`
}

// handleKeyToggle turns one of the caller's bridge keys on or off: the
// per-bridge enable switch.
func (T *Bridges) handleKeyToggle(c *jsonapi.Call, req keyToggleBody) (any, error) {
	k, ok := T.ownKey(c)
	if !ok {
		return nil, jsonapi.NotFound("no such bridge key")
	}
	k.Enabled = req.Enabled
	T.saveBridgeKey(k)
	Log("[bridges] bridge %q enabled=%v", k.Name, k.Enabled)
	return map[string]any{"id": k.ID, "enabled": k.Enabled}, nil
}

// handleKeyRevoke deletes one of the caller's bridge keys. A key that is
// already gone, or was never theirs, is the same answer: nothing to revoke.
func (T *Bridges) handleKeyRevoke(c *jsonapi.Call, _ struct{}) (any, error) {
	if k, ok := T.ownKey(c); ok {
		T.DB.Unset(bridgeKeysTable, k.ID)
	}
	return jsonapi.Empty(http.StatusNoContent), nil
}

// handleBridgeList is the dashboard registry view: one row per connector with a
// derived connection status from its LastSeen, and how many channels route over
// its service.
//
//	GET /bridges/api/bridges → [{label, desc, status}]  (ActionList-shaped)
func (T *Bridges) handleBridgeList(c *jsonapi.Call) (any, error) {
	// Count channels per service so a bridge can show its reach.
	chanCount := map[string]int{}
	for _, ch := range ListChannels(RootDB, c.User) {
		if ch.Service != "" {
			chanCount[ch.Service]++
		}
	}
	type row struct {
		ID      string `json:"id"`
		Bridge  string `json:"bridge"`  // "Name (Service)"
		Reach   string `json:"reach"`   // "N channels"
		Status  string `json:"status"`  // connection state
		Enabled bool   `json:"enabled"` // per-bridge switch
	}
	out := []row{}
	for _, k := range T.listBridgeKeys(c.User) {
		status := "never connected"
		if k.LastSeen != "" {
			if t, err := time.Parse(time.RFC3339, k.LastSeen); err == nil {
				if ago := time.Since(t); ago < 2*time.Minute {
					status = "connected"
				} else {
					status = "last seen " + humanAgo(ago)
				}
			}
		}
		out = append(out, row{
			ID:      k.ID,
			Bridge:  k.Name + " (" + ServiceDisplayName(k.Service) + ")",
			Reach:   plural(chanCount[k.Service], "channel"),
			Status:  status,
			Enabled: k.Enabled,
		})
	}
	return out, nil
}

func humanAgo(d time.Duration) string {
	switch {
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

func plural(n int, noun string) string {
	s := ""
	if n != 1 {
		s = "s"
	}
	return fmt.Sprintf("%d %s%s", n, noun, s)
}
