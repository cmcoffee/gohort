package core

// A connector is a declaring consumer of a credential, exactly as a tool
// with fetch_via: is.
//
// The admin's "orphaned" check only knew about tools, so a secured
// credential reached only through a connector was badged unreachable
// while working perfectly. That went unnoticed while nothing important
// was secured-and-connector-only; securing the peer key made it the
// first case, and the local image and messaging connectors have the same
// shape.

import (
	"encoding/json"
	"testing"

	"github.com/cmcoffee/snugforge/kvlite"
)

func TestConnectorsUsingCredentialFindsEveryKind(t *testing.T) {
	db := &DBase{Store: kvlite.MemStore()}

	// Written straight to the store rather than through SaveConnector:
	// that validates each kind's full spec, and what is under test is the
	// probe over whatever specs are already there — including ones an
	// older build wrote.
	save := func(name string, spec any) {
		raw, _ := json.Marshal(spec)
		db.Set(connectorsTable, name, Connector{Name: name, Kind: "rest_image", Spec: raw})
	}
	// Two kinds of spec, both using the shared "credential" key — which
	// is why the probe decodes that key rather than switching over
	// connector kinds, a switch that goes stale the first time somebody
	// adds one.
	save("peer-gpu-comfy", map[string]any{"credential": "peer_gpu_key", "base_url": "https://gpu"})
	save("peer-gpu-flux", map[string]any{"credential": "peer_gpu_key"})
	save("local-comfy", map[string]any{"credential": "local_key"})
	save("no-cred", map[string]any{"base_url": "https://x"})

	got := ConnectorsUsingCredential(db, "peer_gpu_key")
	if len(got) != 2 || got[0] != "peer-gpu-comfy" || got[1] != "peer-gpu-flux" {
		t.Fatalf("expected both peer backends, sorted: %v", got)
	}
	if one := ConnectorsUsingCredential(db, "local_key"); len(one) != 1 {
		t.Errorf("local: %v", one)
	}
	// Case-insensitive, because credential names are compared that way
	// everywhere else.
	if up := ConnectorsUsingCredential(db, "PEER_GPU_KEY"); len(up) != 2 {
		t.Errorf("case should not matter: %v", up)
	}
	// A credential nothing references is what "orphaned" actually means.
	if none := ConnectorsUsingCredential(db, "unused"); len(none) != 0 {
		t.Errorf("expected nothing: %v", none)
	}
	// And the degenerate inputs, since this feeds a badge rather than a
	// decision: no database, no name, a spec that is not an object.
	if ConnectorsUsingCredential(nil, "x") != nil {
		t.Error("no database should answer nothing")
	}
	if ConnectorsUsingCredential(db, "  ") != nil {
		t.Error("no name should answer nothing")
	}
	db.Set(connectorsTable, "junk",
		Connector{Name: "junk", Kind: "rest_image", Spec: json.RawMessage(`"a string"`)})
	if got := ConnectorsUsingCredential(db, "peer_gpu_key"); len(got) != 2 {
		t.Errorf("an undecodable spec should be skipped, not fatal: %v", got)
	}
}
