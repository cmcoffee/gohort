package admin

import (
	"encoding/json"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/ui"
)

// A "checklist" field posts a JSON array, but a form the operator never touched
// can send the empty case as a bare string, a JSON-encoded string, or nothing.
// Mint rejects an empty capability list on purpose, so a decode that quietly
// produced nil would turn "I checked Embeddings" into "grant at least one
// capability" — an error naming the one thing the operator did do.
func TestDecodeCapsFieldAcceptsEveryShape(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{"json array", `["embeddings","images"]`, []string{"embeddings", "images"}},
		{"single element", `["embeddings"]`, []string{"embeddings"}},
		{"json-encoded array in a string", `"[\"embeddings\"]"`, []string{"embeddings"}},
		{"comma separated string", `"embeddings, images"`, []string{"embeddings", "images"}},
		{"empty array", `[]`, nil},
		{"empty string", `""`, nil},
		{"absent", ``, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := decodeCapsField(json.RawMessage(c.in))
			if len(got) != len(c.want) {
				t.Fatalf("decoded %q → %q, want %q", c.in, got, c.want)
			}
			for i := range got {
				if got[i] != c.want[i] {
					t.Errorf("decoded %q → %q, want %q", c.in, got, c.want)
				}
			}
		})
	}
}

// The capability checklist must offer every capability core knows about. A
// capability added to core but missing here is ungrantable through the only UI
// that issues keys, which reads as the feature not existing.
func TestPeerCapOptionsCoverEveryCapability(t *testing.T) {
	opts := peerCapOptions()
	if len(opts) != len(PeerCapabilities()) {
		t.Fatalf("checklist offers %d capabilities, core defines %d", len(opts), len(PeerCapabilities()))
	}
	seen := map[string]ui.SelectOption{}
	for _, o := range opts {
		seen[o.Value] = o
	}
	for _, c := range PeerCapabilities() {
		o, ok := seen[c]
		if !ok {
			t.Errorf("capability %q has no checklist option", c)
			continue
		}
		if o.Label == "" || o.Help == "" {
			t.Errorf("capability %q needs a label and help text: %+v", c, o)
		}
	}
}

// Capabilities that are not implemented yet must say so on their own option.
// Granting one silently does nothing, and an operator who checked a box is
// entitled to know that before they go looking for why the peer is refused.
func TestUnservedCapabilitiesSaySoInTheUI(t *testing.T) {
	for _, o := range peerCapOptions() {
		if o.Value == PeerCapEmbeddings {
			if strings.Contains(strings.ToLower(o.Help), "not implemented") {
				t.Errorf("embeddings IS implemented but its help says otherwise: %q", o.Help)
			}
			continue
		}
		if !strings.Contains(strings.ToLower(o.Help), "not implemented") {
			t.Errorf("capability %q is not served yet but its help does not say so: %q", o.Value, o.Help)
		}
	}
}

// The table has to render the fields the row JSON actually carries — a column
// bound to a field that is never emitted shows an empty cell forever, and it is
// the kind of miss nothing else catches.
func TestPeerKeyTableColumnsMatchTheRowShape(t *testing.T) {
	var row map[string]any
	b, _ := json.Marshal(peerKeyRow{ID: "x", Label: "mac", Caps: []string{"embeddings"}})
	json.Unmarshal(b, &row)

	sections := peerSharingSections()
	var table ui.Table
	var found bool
	for _, s := range sections {
		if tb, ok := s.Body.(ui.Table); ok {
			table, found = tb, true
		}
	}
	if !found {
		t.Fatal("no table section in the sharing surface")
	}
	for _, c := range table.Columns {
		if _, ok := row[c.Field]; !ok {
			t.Errorf("column %q is bound to a field the row JSON does not carry", c.Field)
		}
	}
	if _, ok := row[table.RowKey]; !ok {
		t.Errorf("RowKey %q is not present in the row JSON", table.RowKey)
	}
	// The enabled toggle drives revocation; if its field vanished the control
	// would silently stop reflecting state.
	for _, ra := range table.RowActions {
		if ra.Type == "toggle" {
			if _, ok := row[ra.Field]; !ok {
				t.Errorf("toggle bound to %q, which the row JSON does not carry", ra.Field)
			}
		}
	}
}
