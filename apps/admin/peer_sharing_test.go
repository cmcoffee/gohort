package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/ui"
	"github.com/cmcoffee/snugforge/kvlite"
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

// Capabilities this instance will not answer must say so on their own option.
// Granting one silently does nothing, and an operator who checked a box is
// entitled to know that before they go looking for why the peer is refused.
//
// Two distinct reasons qualify, and the copy has to name whichever applies:
// NOT IMPLEMENTED (nothing the operator can do but wait) and NOTHING TO LEND
// (implemented, idle because of how this instance is configured, and fixable
// from the LLM settings page). Collapsing them into one message would tell an
// operator to wait for a feature they already have.
func TestUnservedCapabilitiesSaySoInTheUI(t *testing.T) {
	for _, o := range peerCapOptions() {
		lower := strings.ToLower(o.Help)
		unbuilt := strings.Contains(lower, "not implemented")
		idle := strings.Contains(lower, "nothing to lend")
		if PeerCapabilityServed(o.Value) && (unbuilt || idle) {
			t.Errorf("capability %q IS served but its help says otherwise: %q", o.Value, o.Help)
		}
		if !PeerCapabilityServed(o.Value) && !unbuilt && !idle {
			t.Errorf("capability %q will not be answered but its help does not say so: %q", o.Value, o.Help)
		}
	}
}

// walkSharing collects every form and table in the sharing surface, descending
// into Stacks — the sections group related components, so a direct type
// assertion on Section.Body finds nothing.
func walkSharing(t *testing.T) (forms map[string]ui.FormPanel, tables map[string]ui.Table) {
	t.Helper()
	forms, tables = map[string]ui.FormPanel{}, map[string]ui.Table{}
	var visit func(c ui.Component)
	visit = func(c ui.Component) {
		switch b := c.(type) {
		case ui.FormPanel:
			forms[b.Source] = b
		case ui.Table:
			tables[b.Source] = b
		case ui.Stack:
			for _, child := range b.Children {
				visit(child)
			}
		}
	}
	for _, sec := range peerSharingSections() {
		visit(sec.Body)
	}
	return forms, tables
}

// Every form's Source must differ from every table's Source. A FormPanel GETs
// its Source to load current values and POSTs that state back, so a form
// sharing an endpoint with a list loads an array and saves an array — which is
// exactly what happened:
//
//	Save failed: invalid JSON: json: cannot unmarshal array into Go value of
//	type struct { Label string; Caps json.RawMessage; RatePerM int }
//
// naming a struct the operator never saw, from a form they filled in correctly.
func TestNoFormSharesASourceWithAList(t *testing.T) {
	forms, tables := walkSharing(t)
	if len(forms) == 0 || len(tables) == 0 {
		t.Fatal("expected forms and tables in the sharing surface")
	}
	for src := range forms {
		if _, clash := tables[src]; clash {
			t.Errorf("a form and a table share the source %q — the form will load the list and post it back", src)
		}
	}
	// Each form must refresh the list its writes affect, since splitting the
	// endpoints means that no longer happens on its own.
	pairs := map[string]string{
		"api/peer-keys/mint": "api/peer-keys",
		"api/peers/add":      "api/peers",
	}
	for formSrc, listSrc := range pairs {
		f, ok := forms[formSrc]
		if !ok {
			t.Errorf("no form at %q", formSrc)
			continue
		}
		var found bool
		for _, inv := range f.Invalidate {
			if inv == listSrc {
				found = true
			}
		}
		if !found {
			t.Errorf("form %q does not invalidate %q, so a new record will not appear", formSrc, listSrc)
		}
	}
}

// The blank record the mint form loads must decode into the same shape the mint
// handler accepts — that round trip IS the contract, and it is what broke.
func TestMintFormLoadsTheShapeItSaves(t *testing.T) {
	var blank struct {
		Label    string          `json:"label"`
		Caps     json.RawMessage `json:"caps"`
		RatePerM int             `json:"rate_per_min"`
	}
	if err := json.Unmarshal([]byte(`{"label":"","caps":[],"rate_per_min":0}`), &blank); err != nil {
		t.Fatalf("the blank record the form loads does not decode as a mint request: %v", err)
	}
	if caps := decodeCapsField(blank.Caps); len(caps) != 0 {
		t.Errorf("blank caps decoded to %q, want empty", caps)
	}
	// And every field the form declares must exist in that blank record, or it
	// loads as undefined and posts back something the handler ignores.
	var record map[string]any
	json.Unmarshal([]byte(`{"label":"","caps":[],"rate_per_min":0}`), &record)
	forms, _ := walkSharing(t)
	mint, ok := forms["api/peer-keys/mint"]
	if !ok {
		t.Fatal("no mint form at api/peer-keys/mint")
	}
	for _, f := range mint.Fields {
		if f.Type == "header" {
			continue
		}
		if _, ok := record[f.Field]; !ok {
			t.Errorf("form field %q is absent from the blank record the form loads", f.Field)
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

	_, tables := walkSharing(t)
	table, found := tables["api/peer-keys"]
	if !found {
		t.Fatal("no keys table in the sharing surface")
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

// Same binding check for the peers table. Status carries the probe's error
// verbatim, which is the column that actually earns its place — a peer whose
// key was revoked should say so here, not look healthy until an embed fails.
func TestPeersTableColumnsMatchTheRowShape(t *testing.T) {
	var row map[string]any
	b, _ := json.Marshal(peerRow{Name: "gpu-box", Caps: []string{"embeddings"}})
	json.Unmarshal(b, &row)

	_, tables := walkSharing(t)
	table, found := tables["api/peers"]
	if !found {
		t.Fatal("no peers table in the sharing surface")
	}
	for _, c := range table.Columns {
		if _, ok := row[c.Field]; !ok {
			t.Errorf("column %q is bound to a field the row JSON does not carry", c.Field)
		}
	}
	if _, ok := row[table.RowKey]; !ok {
		t.Errorf("RowKey %q is not present in the row JSON", table.RowKey)
	}
}

// With no peers registered, the provider dropdown still offers local — a
// select whose only option is missing would strand the whole Embeddings form.
func TestEmbeddingProviderOptionsAlwaysOfferLocal(t *testing.T) {
	opts := EmbeddingProviderOptions()
	if len(opts) == 0 || opts[0].Value != EmbeddingProviderLocal {
		t.Fatalf("local must be the first provider option, got %+v", opts)
	}
	if opts[0].Label == "" || opts[0].Help == "" {
		t.Errorf("the local option needs a label and help: %+v", opts[0])
	}
}

// findField returns a form field by name.
func findField(fields []ui.FormField, name string) (ui.FormField, bool) {
	for _, f := range fields {
		if f.Field == name {
			return f, true
		}
	}
	return ui.FormField{}, false
}

// With no peers registered, "Embed on" is a select with one option — a question
// with a single possible answer. It should not appear at all.
//
// The trap is what goes with it. The endpoint, model and key fields are gated
// on "provider:local" so they vanish while a peer is selected. Leave that
// clause in place with no dropdown to set `provider`, and they are hidden on
// every deployment that has no peers — the entire form, blank, for the
// overwhelmingly common case. The field and the clause have to appear and
// disappear together.
func TestEmbeddingFormHidesTheProviderPickerWithNoPeers(t *testing.T) {
	prev := RootDB
	t.Cleanup(func() { RootDB = prev })
	RootDB = nil // no peer store → no peers

	fields := embeddingFormFields()
	if _, ok := findField(fields, "provider"); ok {
		t.Error("the provider dropdown is shown with no peers registered")
	}
	for _, name := range []string{"endpoint", "model", "api_key"} {
		f, ok := findField(fields, name)
		if !ok {
			t.Errorf("field %q is missing from the embeddings form entirely", name)
			continue
		}
		if strings.Contains(f.ShowWhen, "provider") {
			t.Errorf("field %q is gated on %q but nothing can set provider — it would never render: %q",
				name, "provider:local", f.ShowWhen)
		}
		if f.ShowWhen != "enabled" {
			t.Errorf("field %q should show whenever embeddings are enabled, got %q", name, f.ShowWhen)
		}
	}
	// The toggle is unconditional either way.
	if f, ok := findField(fields, "enabled"); !ok || f.ShowWhen != "" {
		t.Errorf("the enable toggle must always render: %+v", f)
	}
}

// The paired case: register a peer and both the dropdown AND the gating on the
// fields it controls must come back. Testing only the empty case would let a
// change that drops the clause pass while breaking peer selection.
func TestEmbeddingFormShowsTheProviderPickerWithAPeer(t *testing.T) {
	prevDB, prevCfg := RootDB, GetEmbeddingConfig()
	t.Cleanup(func() { RootDB = prevDB; SetEmbeddingConfig(prevCfg) })
	RootDB = &DBase{Store: kvlite.MemStore()}

	// A local embedder for the manifest's dimension probe.
	embedder := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"data":[{"embedding":[0.1,0.2]}]}`))
	}))
	defer embedder.Close()
	SetEmbeddingConfig(EmbeddingConfig{Enabled: true, Endpoint: embedder.URL, Model: "nomic-embed-text"})

	pk, err := MintPeerKey("consumer", []string{PeerCapEmbeddings}, 0)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/peer/manifest", HandlePeerManifest)
	srv := httptest.NewServer(mux)
	defer srv.Close()
	if _, err := SaveRemotePeer(t.Context(), "gpu-box", srv.URL, pk.Key); err != nil {
		t.Fatalf("add peer: %v", err)
	}

	fields := embeddingFormFields()
	provider, ok := findField(fields, "provider")
	if !ok {
		t.Fatal("a peer is registered but the provider dropdown is absent")
	}
	if len(provider.Options) < 2 {
		t.Errorf("provider dropdown should offer local plus the peer, got %+v", provider.Options)
	}
	for _, name := range []string{"endpoint", "model", "api_key"} {
		f, _ := findField(fields, name)
		if !strings.Contains(f.ShowWhen, "provider:local") {
			t.Errorf("field %q must hide while a peer is selected, got ShowWhen %q", name, f.ShowWhen)
		}
	}
}
