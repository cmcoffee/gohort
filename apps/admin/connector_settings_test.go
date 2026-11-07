package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/jsonapi"
	"github.com/cmcoffee/gohort/core/keystore"
	"github.com/cmcoffee/gohort/core/service"
	"github.com/cmcoffee/gohort/core/ui"
	"github.com/cmcoffee/snugforge/kvlite"
)

// settingsWorld is a store, a credential store and an embeddings server
// that answers with the model and key it was sent.
func settingsWorld(t *testing.T) (*httptest.Server, *[]string) {
	t.Helper()
	saved, savedAuth, savedCfg := RootDB, AuthDB, GetEmbeddingConfig()
	RootDB = &DBase{Store: kvlite.MemStore()}
	adb := &DBase{Store: kvlite.MemStore()}
	AuthDB = func() Database { return adb }
	t.Cleanup(func() { RootDB, AuthDB = saved, savedAuth; SetEmbeddingConfig(savedCfg) })
	var got []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		got = append(got, r.URL.Path+" "+r.Header.Get("Authorization")+" "+body["model"].(string))
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"data":[{"embedding":[0.1,0.2,0.3]}]}`))
	}))
	t.Cleanup(srv.Close)
	return srv, &got
}

// vecConnector is an embeddings connection that takes its address, model
// and key from its settings, keeping the key sk-old for base.
func vecConnector(t *testing.T, base string) {
	t.Helper()
	spec := `{"config":[{"name":"endpoint","label":"Server address","type":"url","required":true},
			{"name":"model","label":"Model","default":"small-embed"},
			{"name":"api_key","label":"API key","type":"secret","send":"bearer"}],
		"provider":{"transport":"http","base_url":"{config.endpoint}","auth":{"secret":"api_key"},
		"operations":{"embed":{"method":"POST","path":"/embeddings","body":{"model":"{model}","input":"{texts}"}}}},
		"provides":[{"capability":"embed.text","steps":[{"call":"embed"}],"output":{"vectors":{"path":"data","each":"embedding","as":"json"}},
		"defaults":{"model":"{config.model}"}}]}`
	if err := SaveConnector(RootDB, Connector{Name: "vec", Kind: service.Kind, Spec: json.RawMessage(spec)}); err != nil {
		t.Fatal(err)
	}
	c, _ := GetConnector(RootDB, "vec")
	if err := SetConnectionSecret(RootDB, c, "api_key", "sk-old", []string{base}); err != nil {
		t.Fatal(err)
	}
}

// kept is what connection vec keeps: its settings, and the key it sends,
// its own or the Keystore entry's it names.
func kept(t *testing.T) (map[string]string, string) {
	t.Helper()
	c, _ := GetConnector(RootDB, "vec")
	sec, _ := GetConnectionSecret(RootDB, c, "api_key")
	if sec.Keystore != "" {
		v, _ := keystore.Secret(RootDB, sec.Keystore)
		return ConnectionConfig(RootDB, c), v
	}
	return ConnectionConfig(RootDB, c), sec.Value
}

// postedForm decodes a page's posted body as the handler receives it.
func postedForm[T any](t *testing.T, body string) withSettings[T] {
	t.Helper()
	var f withSettings[T]
	if err := json.Unmarshal([]byte(body), &f); err != nil {
		t.Fatal(err)
	}
	return f
}

// A page shows the settings its chosen connection declares, and its key,
// under the choice; saving keeps them in the connection's space (never in
// the spec, nor the page's record), and the page reads them back with the
// key shown as kept. A call then runs on them.
func TestAPageSetsUpItsConnector(t *testing.T) {
	srv, got := settingsWorld(t)
	vecConnector(t, srv.URL)
	fields := map[string]string{}
	for _, f := range embeddingFormFields() {
		fields[f.Field] = f.Type + " " + f.ShowWhen + " " + f.Default
	}
	// The key box shows while its own key is chosen; "Key from" offers the
	// Keystore keys that fit, read when the form opens.
	if fields["config/vec/endpoint"] != "text provider:vec " || fields["config/vec/model"] != "text provider:vec small-embed" ||
		fields["config/vec/api_key"] != "password provider:vec;config/vec/api_key@keystore:own " ||
		fields["config/vec/api_key@keystore"] != "select provider:vec own" {
		t.Errorf("the settings shown: %v", fields)
	}

	a := &AdminApp{db: RootDB}
	call := &jsonapi.Call{R: httptest.NewRequest("POST", "/api/embeddings", nil)}
	form := postedForm[EmbeddingConfig](t, `{"enabled":true,"provider":"vec","config/vec/endpoint":"`+srv.URL+`","config/vec/model":"big-embed","config/vec/api_key":"sk-new","config/other/endpoint":"x"}`)
	if _, err := a.handleEmbeddingsSave(call, form); err != nil {
		t.Fatal(err)
	}
	var stored EmbeddingConfig
	RootDB.Get(EmbeddingTable, "current", &stored)
	settings, key := kept(t)
	if settings["endpoint"] != srv.URL || settings["model"] != "big-embed" || len(settings) != 2 || key != "sk-new" || stored.Model != "big-embed" {
		t.Errorf("kept: %v %q, the page's record: %+v", settings, key, stored)
	}
	if c, _ := GetConnector(RootDB, "vec"); strings.Contains(string(c.Spec), "big-embed") || strings.Contains(string(c.Spec), srv.URL) {
		t.Errorf("the page's values were written into the spec: %s", c.Spec)
	}
	out, _ := a.handleEmbeddings(call)
	read := out.(map[string]any)
	// The first key for this address went into the Keystore, which the
	// connection names; the form shows that, and no key.
	if read["config/vec/endpoint"] != srv.URL || read["config/vec/model"] != "big-embed" || read["config/vec/api_key"] != nil ||
		read["config/vec/api_key@keystore"] != service.KeystoreOf(mustConnector(t, "vec"), "api_key") || read["config"] != nil {
		t.Errorf("read back: %v", read)
	}
	if _, err := Embed(t.Context(), "hello"); err != nil {
		t.Fatalf("embedding on the page's settings: %v", err)
	}
	if len(*got) != 1 || (*got)[0] != "/embeddings Bearer sk-new big-embed" {
		t.Errorf("sent: %v", *got)
	}

	// Saved again with the key as the page showed it, the key is kept; a
	// model cleared takes its default.
	form = postedForm[EmbeddingConfig](t, `{"enabled":true,"provider":"vec","config/vec/endpoint":"`+srv.URL+`","config/vec/model":"","config/vec/api_key":"`+secretUnchanged+`"}`)
	if _, err := a.handleEmbeddingsSave(call, form); err != nil {
		t.Fatal(err)
	}
	Embed(t.Context(), "again")
	if len(*got) != 2 || (*got)[1] != "/embeddings Bearer sk-new small-embed" {
		t.Errorf("a kept key and a model left blank (its default): %v", *got)
	}
}

// A connection whose key comes from the Keystore keeps no copy of it: every
// call sends the entry's key, a rotation reaches the next call with nothing
// re-saved, a key not given for the connection's address is refused, and the
// entry cannot be deleted while the connection names it.
func TestAConnectorTakesItsKeyFromTheKeystore(t *testing.T) {
	srv, got := settingsWorld(t)
	vecConnector(t, srv.URL)
	a := &AdminApp{db: RootDB}
	call := &jsonapi.Call{R: httptest.NewRequest("POST", "/api/embeddings", nil)}
	e, err := keystore.Save(RootDB, keystore.Entry{Name: "Vec account", Addresses: []string{srv.URL}}, "sk-keystore-1")
	if err != nil {
		t.Fatal(err)
	}
	save := func(extra string) error {
		form := postedForm[EmbeddingConfig](t, `{"enabled":true,"provider":"vec","config/vec/endpoint":"`+srv.URL+`",`+extra+`}`)
		_, err := a.handleEmbeddingsSave(call, form)
		return err
	}
	if err := save(`"config/vec/api_key@keystore":"` + e.ID + `"`); err != nil {
		t.Fatal(err)
	}
	if sec, _ := GetConnectionSecret(RootDB, mustConnector(t, "vec"), "api_key"); sec.Value != "" || sec.Keystore != e.ID {
		t.Errorf("the connection kept a copy of the key, or does not name the entry: %+v", sec)
	}
	Embed(t.Context(), "hello")
	keystore.Save(RootDB, e, "sk-keystore-2")
	Embed(t.Context(), "again")
	if len(*got) != 2 || !strings.Contains((*got)[0], "Bearer sk-keystore-1") || !strings.Contains((*got)[1], "Bearer sk-keystore-2") {
		t.Errorf("sent: %v", *got)
	}

	out, _ := a.handleEmbeddings(call)
	read := out.(map[string]any)
	if read["config/vec/api_key@keystore"] != e.ID || !strings.HasPrefix(read["kept/vec/api_key"].(string), "from the Keystore: Vec account") {
		t.Errorf("the page does not say where the key comes from: %v", read)
	}
	if err := keystore.Delete(RootDB, e.ID); err == nil || !strings.Contains(err.Error(), "connector vec") {
		t.Errorf("a key a connection names was deleted, or the refusal does not say by what: %v", err)
	}

	other, _ := keystore.Save(RootDB, keystore.Entry{Name: "Elsewhere", Addresses: []string{"https://elsewhere.example.com"}}, "sk-elsewhere")
	if err := save(`"config/vec/api_key@keystore":"` + other.ID + `"`); err == nil {
		t.Error("a key not given for the connection's address was accepted")
	}
	if err := save(`"config/vec/api_key@keystore":"own","config/vec/api_key":"sk-own"`); err != nil {
		t.Fatal(err)
	}
	Embed(t.Context(), "own")
	if n := len(*got); n != 3 || !strings.Contains((*got)[2], "Bearer sk-own") {
		t.Errorf("back on its own key, sent: %v", *got)
	}
	if err := keystore.Delete(RootDB, e.ID); err != nil {
		t.Errorf("a key nothing names was not deleted: %v", err)
	}
}

// A page's Test runs on what its form holds, before a save.
func TestAPagesTestRunsOnItsForm(t *testing.T) {
	srv, got := settingsWorld(t)
	vecConnector(t, srv.URL)
	a := &AdminApp{db: RootDB}
	call := &jsonapi.Call{R: httptest.NewRequest("POST", "/api/embeddings/test", nil)}
	form := postedForm[EmbeddingConfig](t, `{"enabled":true,"provider":"vec","config/vec/endpoint":"`+srv.URL+`","config/vec/model":"tried-embed"}`)
	out, _ := a.handleEmbeddingsTest(call, form)
	if body, _ := out.(map[string]any); body["ok"] != true || len(*got) != 1 || !strings.HasSuffix((*got)[0], " tried-embed") {
		t.Errorf("the Test: %v, sent %v", out, *got)
	}
	var stored EmbeddingConfig
	if settings, key := kept(t); RootDB.Get(EmbeddingTable, "current", &stored) || len(settings) != 0 || key != "sk-old" {
		t.Errorf("the Test saved: %+v %v %q", stored, settings, key)
	}
}

// A connector a page references cannot be deleted: its row offers no
// Delete, and a delete is refused naming the page, until it is taken off
// there.
func TestAReferencedConnectorCannotBeDeleted(t *testing.T) {
	srv, _ := settingsWorld(t)
	vecConnector(t, srv.URL)
	a := &AdminApp{db: RootDB}
	RootDB.Set(EmbeddingTable, "current", EmbeddingConfig{Enabled: true, Provider: "vec"})
	deletable := func() bool {
		out, _ := a.handleConnectors(&jsonapi.Call{R: httptest.NewRequest("GET", "/api/connectors", nil)})
		b, _ := json.Marshal(out)
		var rows []map[string]any
		json.Unmarshal(b, &rows)
		for _, r := range rows {
			if r["name"] == "vec" {
				return r["deletable"] == true
			}
		}
		t.Fatal("no row for vec")
		return false
	}
	del := func() error {
		_, err := a.handleConnectorDelete(&jsonapi.Call{R: httptest.NewRequest("DELETE", "/api/connectors?name=vec", nil)}, struct{}{})
		return err
	}
	if deletable() {
		t.Error("a referenced connector offers Delete")
	}
	if err := del(); err == nil || !strings.Contains(err.Error(), "referenced by Embeddings") {
		t.Fatalf("deleting one referenced: %v", err)
	}
	if _, ok := GetConnector(RootDB, "vec"); !ok {
		t.Fatal("refused, and deleted anyway")
	}
	RootDB.Set(EmbeddingTable, "current", EmbeddingConfig{})
	if !deletable() {
		t.Error("taken off its page, it still offers no Delete")
	}
	if err := del(); err != nil {
		t.Fatalf("deleting one no page uses: %v", err)
	}
	if _, ok := GetConnector(RootDB, "vec"); ok {
		t.Error("not deleted")
	}
}

// A page saves only its own capability's settings: what the connection keeps
// for another capability stays, and a value its spec no longer declares goes.
func TestAPageKeepsTheOtherCapabilitysSettings(t *testing.T) {
	settingsWorld(t)
	spec := `{"config":[{"name":"endpoint","type":"url","required":true}],
		"provider":{"transport":"http","base_url":"{config.endpoint}","auth":{"none":true},
		"operations":{"embed":{"method":"POST","path":"/e","body":{"input":"{texts}"}},"speak":{"method":"POST","path":"/s","body":{"text":"{text}"},"read":"file"}}},
		"provides":[{"capability":"embed.text","config":[{"name":"model"}],"steps":[{"call":"embed"}],"output":{"vectors":{"path":"data","as":"json"}}},
			{"capability":"speech.synthesize","config":[{"name":"voice"}],"steps":[{"call":"speak"}],"output":{"audio":{"from":"body"}}}]}`
	if err := SaveConnector(RootDB, Connector{Name: "duo", Kind: service.Kind, Spec: json.RawMessage(spec)}); err != nil {
		t.Fatal(err)
	}
	c, _ := GetConnector(RootDB, "duo")
	SetConnectionConfig(RootDB, c, map[string]string{"endpoint": "http://a.example", "speech.synthesize/voice": "alto", "gone": "x"})
	form := postedForm[EmbeddingConfig](t, `{"provider":"duo","config/duo/endpoint":"http://b.example","config/duo/embed.text/model":"big"}`)
	if err := form.Settings.save("duo", "embed.text"); err != nil {
		t.Fatal(err)
	}
	if got := ConnectionConfig(RootDB, c); len(got) != 3 || got["speech.synthesize/voice"] != "alto" || got["embed.text/model"] != "big" || got["endpoint"] != "http://b.example" {
		t.Errorf("kept: %v", got)
	}
}

// A connection's row offers Access, which reads and keeps who may reach it
// through agents' tools; none is everyone.
func TestAConnectionsAccessIsSetOnItsRow(t *testing.T) {
	srv, _ := settingsWorld(t)
	vecConnector(t, srv.URL)
	a := &AdminApp{db: RootDB}
	out, _ := a.handleConnectors(&jsonapi.Call{R: httptest.NewRequest("GET", "/api/connectors", nil)})
	b, _ := json.Marshal(out)
	if !strings.Contains(string(b), `"has_access":true`) {
		t.Errorf("the row offers no Access: %s", b)
	}
	call := func(method string) *jsonapi.Call {
		return &jsonapi.Call{R: httptest.NewRequest(method, "/api/connectors/access?name=vec", nil)}
	}
	read := func() string {
		got, err := a.handleConnectionAccess(call("GET"))
		if err != nil {
			t.Fatal(err)
		}
		j, _ := json.Marshal(got)
		return string(j)
	}
	if got := read(); got != `{"allowed_users":[],"name":"vec"}` {
		t.Errorf("open: %s", got)
	}
	if _, err := a.handleConnectionAccessSave(call("POST"), struct {
		AllowedUsers []string `json:"allowed_users"`
	}{AllowedUsers: []string{"bob", " alice "}}); err != nil {
		t.Fatal(err)
	}
	if got := read(); got != `{"allowed_users":["alice","bob"],"name":"vec"}` {
		t.Errorf("limited: %s", got)
	}
	if (&ToolSession{Username: "carol"}).ConnectionBarred("vec") == nil {
		t.Error("carol reaches a connection limited to alice and bob")
	}
	if _, err := a.handleConnectionAccess(&jsonapi.Call{R: httptest.NewRequest("GET", "/api/connectors/access?name=nope", nil)}); err == nil {
		t.Error("access read for no connector")
	}
}

// What is wrong with a saved connection shows on its row as the list is
// read, and is gone once it is put right.
func TestAConnectionsRowSaysWhatNeedsAttention(t *testing.T) {
	srv, _ := settingsWorld(t)
	vecConnector(t, srv.URL)
	a := &AdminApp{db: RootDB}
	row := func() string {
		out, _ := a.handleConnectors(&jsonapi.Call{R: httptest.NewRequest("GET", "/api/connectors", nil)})
		b, _ := json.Marshal(out)
		var rows []map[string]any
		json.Unmarshal(b, &rows)
		for _, r := range rows {
			if r["name"] == "vec" {
				s, _ := r["check"].(string)
				return s
			}
		}
		t.Fatal("no row for vec")
		return ""
	}
	if got := row(); got != "" {
		t.Errorf("a sound connection needs attention: %s", got)
	}
	c, _ := GetConnector(RootDB, "vec")
	SetConnectionConfig(RootDB, c, map[string]string{"endpoint": "not an address"})
	if got := row(); !strings.HasPrefix(got, "Needs attention: its Server address setting is wrong") {
		t.Errorf("a bad address: %q", got)
	}
	SetConnectionConfig(RootDB, c, map[string]string{"endpoint": srv.URL})
	if got := row(); got != "" {
		t.Errorf("put right, still: %s", got)
	}
}

// Which key a connection keeps shows beside its key field, on the pages that
// choose it and its row's Settings, and on its row: its last four
// characters and the day it was given, never the key.
func TestAKeptKeyShowsItsEnd(t *testing.T) {
	srv, _ := settingsWorld(t)
	vecConnector(t, srv.URL)
	c, _ := GetConnector(RootDB, "vec")
	const key = "sk-live-0123456789abWXYZ"
	SetConnectionSecret(RootDB, c, "api_key", key, []string{srv.URL})
	want := "ends in …WXYZ, given " + time.Now().Format("2 Jan 2006")

	for name, fields := range map[string][]ui.FormField{"the page": embeddingFormFields(), "its Settings": connectionSettingsFields()} {
		var shown *ui.FormField
		for i, f := range fields {
			if f.Field == "kept/vec/api_key" {
				shown = &fields[i]
				if i == 0 || fields[i-1].Field != "config/vec/api_key" {
					t.Errorf("%s: not beside the key's field", name)
				}
			}
		}
		if shown == nil || shown.Type != "readonly" || shown.Label != "Saved API key" || !strings.HasSuffix(shown.ShowWhen, ";kept/vec/api_key") {
			t.Errorf("%s: the saved key's line: %+v", name, shown)
		}
	}

	a := &AdminApp{db: RootDB}
	call := &jsonapi.Call{R: httptest.NewRequest("GET", "/api/connectors/settings?name=vec", nil)}
	chosen := postedForm[EmbeddingConfig](t, `{"enabled":true,"provider":"vec","config/vec/endpoint":"`+srv.URL+`","config/vec/api_key":"`+secretUnchanged+`"}`)
	if _, err := a.handleEmbeddingsSave(call, chosen); err != nil {
		t.Fatal(err)
	}
	page, _ := a.handleEmbeddings(call)
	settings, _ := a.handleConnectionSettings(call)
	rows, _ := a.handleConnectors(call)
	for name, out := range map[string]any{"the page": page, "its Settings": settings, "its row": rows} {
		b, _ := json.Marshal(out)
		if strings.Contains(string(b), "0123456789") {
			t.Errorf("%s shows the key: %s", name, b)
		}
		if !strings.Contains(string(b), want) {
			t.Errorf("%s does not say which key is kept: %s", name, b)
		}
	}
	if s := settings.(map[string]any); s["kept/vec/api_key"] != want {
		t.Errorf("its Settings: %v", s)
	}
	b, _ := json.Marshal(rows)
	if !strings.Contains(string(b), `"key":"API key `+want+`"`) {
		t.Errorf("its row: %s", b)
	}
}

// A ComfyUI backend kept as a connection is configured from its template, and
// from Image Generation: its row on the backend list opens the template's form,
// the same Edit its Connectors row opens, and says what the workflow does.
func TestAComfyUIConnectionIsConfiguredFromImageGeneration(t *testing.T) {
	settingsWorld(t)
	a := &AdminApp{db: RootDB}
	list := func() []imageBackendRow {
		out, err := a.handleImageBackends(&jsonapi.Call{R: httptest.NewRequest("GET", "/"+imageBackendsSource, nil)})
		if err != nil {
			t.Fatal(err)
		}
		return out.([]imageBackendRow)
	}
	if rows := list(); len(rows) != 0 {
		t.Fatalf("listed with no image backend: %+v", rows)
	}
	r, _, err := NewComfyImageSpec("http://comfy.example:8188", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	kind, raw, err := r.AsService("")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"studio", "sketch"} {
		if err := SaveConnector(RootDB, Connector{Name: name, Kind: kind, Template: "comfyui", Spec: raw}); err != nil {
			t.Fatal(err)
		}
	}
	RootDB.Set(ImageTable, "provider", "sketch")
	rows := list()
	if len(rows) != 2 || rows[0].Name != "sketch" || rows[0].Role != "default" || rows[1].Role != "unused" {
		t.Fatalf("rows = %+v, want the default first", rows)
	}
	row := rows[1]
	// The same editor as its Connectors row: the template's form.
	if !row.EditForm || row.EditSpec || row.NeedsFix {
		t.Errorf("editor flags = form %v spec %v fix %v, want the template's form", row.EditForm, row.EditSpec, row.NeedsFix)
	}
	if row.Does != ComfyTypeGenerate || row.Size != "512×512" || row.Steps == "" {
		t.Errorf("outline = does %q size %q steps %q", row.Does, row.Size, row.Steps)
	}
	// Offered to agents reads as such.
	RootDB.Set(ImageTable, "agents", []string{"studio"})
	if rows := list(); rows[1].Role != "offered" {
		t.Errorf("an offered backend reads %q", rows[1].Role)
	}
}

// The backend list's rows carry the Connectors table's own actions, so the two
// open one editor, and an edit from either reloads both.
func TestTheBackendListSharesTheConnectorActions(t *testing.T) {
	tbl := imageBackendsTable()
	var labels []string
	for _, act := range tbl.RowActions {
		labels = append(labels, act.Label)
	}
	if got := strings.Join(labels, ","); got != "Edit,✨ Edit,✨ Fix,Settings,Test" {
		t.Errorf("row actions = %s", got)
	}
	if !slices.Contains(connectorListSources, imageBackendsSource) || !slices.Contains(connectorListSources, "api/connectors") {
		t.Errorf("an edit reloads %v, want both lists", connectorListSources)
	}
}

// Configuring a ComfyUI backend still stored as the older rest_image kind
// saves it as the connection the template now makes, not the two specs
// merged: reconfiguring is how a record from before takes the new shape.
func TestConfiguringAnOldComfyUIBackendMovesIt(t *testing.T) {
	settingsWorld(t)
	r, _, err := NewComfyImageSpec("http://comfy.example:8188", "no_auth", "", "")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(r)
	// Written as it sits in a database from before: no handler takes the
	// kind now, so it is stored as it was, not saved through one.
	RootDB.Set("connectors", "studio", Connector{Name: "studio", Kind: "rest_image", Template: "comfyui", Spec: raw})
	tpl, _ := GetConnectorTemplate("comfyui")
	vals := tpl.ReadValues(raw)
	vals["prompt_suffix"] = "crisp"
	a := &AdminApp{db: RootDB}
	call := &jsonapi.Call{R: httptest.NewRequest("POST", "/api/connectors/config", nil)}
	if _, err := a.handleConnectorConfigSave(call, struct {
		Template   string         `json:"template"`
		Connector  string         `json:"connector"`
		Name       string         `json:"name"`
		Values     map[string]any `json:"values"`
		SetDefault bool           `json:"set_default"`
	}{Template: "comfyui", Connector: "studio", Values: vals}); err != nil {
		t.Fatal(err)
	}
	c, _ := GetConnector(RootDB, "studio")
	if c.Kind != service.Kind || strings.Contains(string(c.Spec), "submit_url") || !strings.Contains(string(c.Spec), "crisp") {
		t.Errorf("saved as %s: %s", c.Kind, c.Spec)
	}
}

// mustConnector is the connector name.
func mustConnector(t *testing.T, name string) Connector {
	t.Helper()
	c, ok := GetConnector(RootDB, name)
	if !ok {
		t.Fatalf("no connector %s", name)
	}
	return c
}
