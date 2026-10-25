package bridges

import (
	"encoding/json"
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
// key (the route takes it with jsonapi.EmptyBodyOK). A non-empty but malformed
// body is a client bug, rejected rather than silently creating an unnamed
// generic key the caller never asked for.
func (T *Bridges) handleKeyMint(c *jsonapi.Call, req keyMintReq) (any, error) {
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

// handleKeyOne toggles (PATCH) or revokes (DELETE) one bridge key — the
// per-bridge enable switch and the revoke action.
func (T *Bridges) handleKeyOne(w http.ResponseWriter, r *http.Request) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/api/keys/")
	var k BridgeKey
	if !T.DB.Get(bridgeKeysTable, id, &k) || k.Owner != user {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	switch r.Method {
	case http.MethodDelete:
		T.DB.Unset(bridgeKeysTable, id)
		w.WriteHeader(http.StatusNoContent)
	case http.MethodPatch, http.MethodPost:
		var req struct {
			Enabled bool `json:"enabled"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		k.Enabled = req.Enabled
		T.saveBridgeKey(k)
		Log("[bridges] bridge %q enabled=%v", k.Name, k.Enabled)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"id": k.ID, "enabled": k.Enabled})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
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
