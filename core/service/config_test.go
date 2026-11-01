package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// embedSpec is an embeddings connection that takes its address from its
// settings, a model of the embed capability's own, and a key it sends as a
// bearer token.
func embedSpec(defaultModel string) map[string]any {
	return map[string]any{
		"config": []any{
			map[string]any{"name": "endpoint", "label": "Endpoint", "type": "url", "required": true},
			map[string]any{"name": "dims", "label": "Dimensions", "type": "number", "default": "4", "min": 1, "max": 8},
			map[string]any{"name": "api_key", "label": "API key", "type": "secret", "send": "bearer", "required": true},
		},
		"provider": map[string]any{"transport": "http", "base_url": "{config.endpoint}", "auth": map[string]any{"secret": "api_key"},
			"operations": map[string]any{"embed": map[string]any{"method": "POST", "path": "/embeddings",
				"body": map[string]any{"model": "{model}", "input": "{texts}", "dimensions": "{config.dims}"}}}},
		"provides": []any{map[string]any{"capability": "embed.text", "steps": []any{map[string]any{"call": "embed"}},
			"config":   []any{map[string]any{"name": "model", "label": "Model", "default": defaultModel}},
			"defaults": map[string]any{"model": "{config.model}"},
			"output":   map[string]any{"vectors": map[string]any{"path": "data", "each": "embedding", "as": "json"}}}},
	}
}

// A spec declares what it needs: each setting's kind of value and what a
// page draws for it. It is checked against its defaults (a stand-in where it
// has none); a setting must be well formed, used where it may be, and a
// secret is never written into a request.
func TestASpecDeclaresItsSettings(t *testing.T) {
	raw, _ := json.Marshal(embedSpec("m1"))
	s, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Validate(); err != nil {
		t.Fatalf("a spec reading its settings: %v", err)
	}
	cfg := func(m map[string]any) []any { return m["config"].([]any) }
	add := func(f map[string]any) func(map[string]any) {
		return func(m map[string]any) { m["config"] = append(cfg(m), f) }
	}
	for why, change := range map[string]func(m map[string]any){
		"uses a setting it does not declare": func(m map[string]any) {
			m["provider"].(map[string]any)["operations"].(map[string]any)["embed"].(map[string]any)["body"].(map[string]any)["size"] = "{config.size}"
		},
		"writes a secret into a request": func(m map[string]any) {
			m["provider"].(map[string]any)["headers"] = map[string]any{"X-Key": "{config.api_key}"}
		},
		"the provider uses a capability's setting": func(m map[string]any) {
			m["provider"].(map[string]any)["operations"].(map[string]any)["embed"].(map[string]any)["path"] = "/{config.model}"
		},
		"declares one twice": add(map[string]any{"name": "dims"}),
		"a capability shadows the provider": func(m map[string]any) {
			b := m["provides"].([]any)[0].(map[string]any)
			b["config"] = append(b["config"].([]any), map[string]any{"name": "dims"})
		},
		"a choice with no options":      add(map[string]any{"name": "size", "type": "choice"}),
		"a type it does not know":       add(map[string]any{"name": "size", "type": "slider"}),
		"a number defaulting to a word": func(m map[string]any) { cfg(m)[1].(map[string]any)["default"] = "many" },
		"a default outside its bounds":  func(m map[string]any) { cfg(m)[1].(map[string]any)["default"] = "9" },
		"min above max":                 func(m map[string]any) { cfg(m)[1].(map[string]any)["min"] = 9 },
		"a pattern that is not one":     add(map[string]any{"name": "size", "pattern": "("}),
		"a name that is not a token":    func(m map[string]any) { cfg(m)[0].(map[string]any)["name"] = "End Point" },
		"a secret with a default":       func(m map[string]any) { cfg(m)[2].(map[string]any)["default"] = "sk" },
		"a secret sent no way":          func(m map[string]any) { cfg(m)[2].(map[string]any)["send"] = "post" },
		"a header naming none":          func(m map[string]any) { cfg(m)[2].(map[string]any)["send"] = "header:" },
		"send on a setting that is not a secret": func(m map[string]any) {
			cfg(m)[1].(map[string]any)["send"] = "bearer"
		},
		"a secret under a capability": func(m map[string]any) {
			b := m["provides"].([]any)[0].(map[string]any)
			b["config"] = append(b["config"].([]any), map[string]any{"name": "key2", "type": "secret", "send": "bearer"})
		},
		"auth naming a setting that is not a secret": func(m map[string]any) {
			m["provider"].(map[string]any)["auth"] = map[string]any{"secret": "dims"}
		},
		"auth giving a secret and none": func(m map[string]any) {
			m["provider"].(map[string]any)["auth"] = map[string]any{"secret": "api_key", "none": true}
		},
	} {
		m := embedSpec("m1")
		change(m)
		raw, _ := json.Marshal(m)
		if s, err := Parse(raw); err == nil {
			if err := s.Validate(); err == nil {
				t.Errorf("%s: accepted", why)
			}
		}
	}
	// Options read as bare values or with labels; select is choice.
	var f ConfigField
	json.Unmarshal([]byte(`{"name":"q","type":"select","options":["a",{"value":"b","label":"Bee"}]}`), &f)
	if f.Kind() != "choice" || len(f.Options) != 2 || f.Options[0].Value != "a" || f.Options[1].Label != "Bee" {
		t.Errorf("options: %+v", f)
	}
}

// A save keeps a value only of the kind its setting takes, and none it does
// not declare; a required setting is needed, a capability's only for that
// capability.
func TestASaveTakesWhatEachSettingTakes(t *testing.T) {
	m := embedSpec("")
	m["config"] = append(m["config"].([]any),
		map[string]any{"name": "tier", "type": "choice", "options": []any{"free", "paid"}},
		map[string]any{"name": "voices", "type": "choices", "options": []any{"a", "b", "c"}},
		map[string]any{"name": "stream", "type": "toggle"},
		map[string]any{"name": "region", "pattern": "^[a-z]+-[a-z]+-[0-9]$"})
	b := m["provides"].([]any)[0].(map[string]any)
	b["config"].([]any)[0].(map[string]any)["required"] = true
	raw, _ := json.Marshal(m)
	c := Connector{Name: "vec", Kind: Kind, Spec: raw}
	good := map[string]string{"endpoint": "http://x.example/v1", "dims": "8", "tier": "paid", "voices": "a,c",
		"stream": "true", "region": "us-west-2", "embed.text/model": "m"}
	if err := CheckSettings(c, "embed.text", good); err != nil {
		t.Fatalf("good values: %v", err)
	}
	for k, v := range map[string]string{"endpoint": "ftp://x", "dims": "9", "tier": "gold", "voices": "a,d",
		"stream": "maybe", "region": "west", "size": "1", "api_key": "sk"} {
		bad := map[string]string{}
		for gk, gv := range good {
			bad[gk] = gv
		}
		bad[k] = v
		if err := CheckSettings(c, "embed.text", bad); err == nil {
			t.Errorf("%s=%q: accepted", k, v)
		}
	}
	good["dims"] = "0"
	if err := CheckSettings(c, "embed.text", good); err == nil || !strings.Contains(err.Error(), "below") {
		t.Errorf("a number below its least: %v", err)
	}
	good["dims"] = "8"
	delete(good, "embed.text/model")
	if err := CheckSettings(c, "embed.text", good); err == nil || !strings.Contains(err.Error(), "Model") {
		t.Errorf("the capability's required setting unset: %v", err)
	}
	if err := CheckSettings(c, "speech.transcribe", good); err != nil {
		t.Errorf("another capability's required setting held against this one: %v", err)
	}
}

// keyServer answers embeddings, recording the path, the key and the body.
func keyServer(t *testing.T) (*httptest.Server, *[]string) {
	t.Helper()
	var got []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		b, _ := json.Marshal(body)
		got = append(got, r.URL.Path+" "+r.Header.Get("Authorization")+" "+string(b))
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"data":[{"embedding":[0.1,0.2]}]}`))
	}))
	t.Cleanup(srv.Close)
	return srv, &got
}

// A call takes the settings the connection keeps, else the defaults, and a
// capability's own; a Test, the form's. A required setting with no value is
// refused by its label. Nothing is written into the spec.
func TestACallTakesTheConnectionsSettings(t *testing.T) {
	world(t)
	srv, got := keyServer(t)
	m := embedSpec("m1")
	m["provider"].(map[string]any)["auth"] = map[string]any{"none": true}
	m["config"] = m["config"].([]any)[:2]
	raw, _ := json.Marshal(m)
	if err := SaveConnector(RootDB, Connector{Name: "vec", Kind: Kind, Spec: raw}); err != nil {
		t.Fatal(err)
	}
	ApproveConnector(RootDB, "vec")
	c, _ := GetConnector(RootDB, "vec")
	in := map[string]any{"texts": []string{"hi"}}

	if _, err := Call(context.Background(), nil, "vec", "embed.text", in); err == nil || !strings.Contains(err.Error(), "set Endpoint for this connection") {
		t.Fatalf("no address set: %v", err)
	}
	if err := SaveSettings(c, "embed.text", map[string]string{"endpoint": srv.URL + "/v1", "embed.text/model": "big-embed"}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := Call(context.Background(), nil, "vec", "embed.text", in); err != nil {
		t.Fatalf("with its settings: %v", err)
	}
	if len(*got) != 1 || (*got)[0] != `/v1/embeddings  {"dimensions":4,"input":["hi"],"model":"big-embed"}` {
		t.Errorf("sent: %v", *got)
	}
	ctx := WithConfig(context.Background(), map[string]string{"endpoint": srv.URL + "/v2"})
	if _, err := Call(ctx, nil, "vec", "embed.text", in); err != nil {
		t.Fatalf("a Test's values: %v", err)
	}
	if len(*got) != 2 || (*got)[1] != `/v2/embeddings  {"dimensions":4,"input":["hi"],"model":"m1"}` {
		t.Errorf("sent under a Test: %v", *got)
	}
	if c, _ := GetConnector(RootDB, "vec"); !strings.Contains(string(c.Spec), "{config.endpoint}") {
		t.Errorf("a call wrote into the spec: %s", c.Spec)
	}
	if v := DefaultWith(c, "embed.text", "model", nil); v != "big-embed" {
		t.Errorf("the model it embeds with: %q", v)
	}
	// A capability's required setting, unset, refuses a call to it.
	m["provides"].([]any)[0].(map[string]any)["config"].([]any)[0].(map[string]any)["default"] = ""
	m["provides"].([]any)[0].(map[string]any)["config"].([]any)[0].(map[string]any)["required"] = true
	raw, _ = json.Marshal(m)
	SaveConnector(RootDB, Connector{Name: "vec", Kind: Kind, Spec: raw})
	SaveSettings(c, "", map[string]string{"endpoint": srv.URL + "/v1"}, nil)
	if _, err := Call(context.Background(), nil, "vec", "embed.text", in); err == nil || !strings.Contains(err.Error(), "set Model for this connection") {
		t.Errorf("a capability's setting unset: %v", err)
	}
}

// A connection's secret is kept in its space, sent as its spec says, and
// only to the address it was given for: moved elsewhere, the connection
// needs it again, and a Test's typed key goes to the form's address. Deleted,
// the connection's space goes with it.
func TestAConnectionCarriesItsSecret(t *testing.T) {
	world(t)
	srv, got := keyServer(t)
	raw, _ := json.Marshal(embedSpec("m1"))
	if err := SaveConnector(RootDB, Connector{Name: "vec", Kind: Kind, Spec: raw, ID: "c_chosen"}); err != nil {
		t.Fatal(err)
	}
	ApproveConnector(RootDB, "vec")
	c, _ := GetConnector(RootDB, "vec")
	if c.ID == "" || c.ID == "c_chosen" {
		t.Fatalf("the id: %q", c.ID)
	}
	in := map[string]any{"texts": []string{"hi"}}
	settings := map[string]string{"endpoint": srv.URL + "/v1"}

	if err := SaveSettings(c, "embed.text", settings, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := Call(context.Background(), nil, "vec", "embed.text", in); err == nil || !strings.Contains(err.Error(), "no key yet") {
		t.Fatalf("no key: %v", err)
	}
	if err := SaveSettings(c, "embed.text", settings, map[string]string{"api_key": "sk-1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := Call(context.Background(), nil, "vec", "embed.text", in); err != nil {
		t.Fatalf("with its key: %v", err)
	}
	if len(*got) != 1 || !strings.Contains((*got)[0], " Bearer sk-1 ") {
		t.Errorf("sent: %v", *got)
	}
	if c2, _ := GetConnector(RootDB, "vec"); strings.Contains(string(c2.Spec), "sk-1") || c2.ID != c.ID {
		t.Errorf("after a save: %+v", c2)
	}

	// Moved: refused without the key, and nothing is sent there.
	moved := map[string]string{"endpoint": srv.URL + "/elsewhere"}
	if err := SaveSettings(c, "embed.text", moved, nil); err == nil || !strings.Contains(err.Error(), "give it again") {
		t.Errorf("a move keeping a key given elsewhere: %v", err)
	}
	if _, err := Call(WithConfig(context.Background(), moved), nil, "vec", "embed.text", in); err == nil || !strings.Contains(err.Error(), "give the API key again") {
		t.Errorf("a Test moving the key: %v", err)
	}
	if len(*got) != 1 {
		t.Errorf("the key went to another address: %v", *got)
	}
	// A key typed with the move goes there.
	ctx := WithSecrets(WithConfig(context.Background(), moved), map[string]string{"api_key": "sk-2"})
	if _, err := Call(ctx, nil, "vec", "embed.text", in); err != nil {
		t.Fatalf("a Test with its key: %v", err)
	}
	if len(*got) != 2 || !strings.HasPrefix((*got)[1], "/elsewhere/embeddings Bearer sk-2 ") {
		t.Errorf("sent under the Test: %v", *got)
	}
	if err := SaveSettings(c, "embed.text", moved, map[string]string{"api_key": "sk-2"}); err != nil {
		t.Errorf("a move with its key: %v", err)
	}

	// Deleted and made again by the same name: a new space, nothing kept.
	if err := DeleteConnector(RootDB, "vec"); err != nil {
		t.Fatal(err)
	}
	if _, ok := GetConnectionSecret(RootDB, c, "api_key"); ok {
		t.Error("the deleted connection's secret is still kept")
	}
	SaveConnector(RootDB, Connector{Name: "vec", Kind: Kind, Spec: raw})
	again, _ := GetConnector(RootDB, "vec")
	if again.ID == c.ID || HasSecret(again, "api_key") || len(ConnectionConfig(RootDB, again)) != 0 {
		t.Errorf("made again: %+v %v", again, ConnectionConfig(RootDB, again))
	}
}

// A header or query secret goes under its name.
func TestASecretIsSentAsDeclared(t *testing.T) {
	world(t)
	var got []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r.Header.Get("X-Api-Key")+"|"+r.URL.Query().Get("key"))
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"data":[{"embedding":[0.1]}]}`))
	}))
	defer srv.Close()
	for _, send := range []string{"header:X-Api-Key", "query:key"} {
		m := embedSpec("m1")
		m["config"].([]any)[2].(map[string]any)["send"] = send
		raw, _ := json.Marshal(m)
		DeleteConnector(RootDB, "vec")
		SaveConnector(RootDB, Connector{Name: "vec", Kind: Kind, Spec: raw})
		ApproveConnector(RootDB, "vec")
		c, _ := GetConnector(RootDB, "vec")
		if err := SaveSettings(c, "", map[string]string{"endpoint": srv.URL}, map[string]string{"api_key": "k-" + send}); err != nil {
			t.Fatal(err)
		}
		if _, err := Call(context.Background(), nil, "vec", "embed.text", map[string]any{"texts": []string{"hi"}}); err != nil {
			t.Fatalf("%s: %v", send, err)
		}
	}
	if len(got) != 2 || got[0] != "k-header:X-Api-Key|" || got[1] != "|k-query:key" {
		t.Errorf("sent: %v", got)
	}
}

// A setting with options_from lists what its operation's reply holds, asked
// with the form's values and the connection's key, only of a connection in
// use or under preview.
func TestASettingListsItsOptions(t *testing.T) {
	world(t)
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"data":[{"id":"small"},{"id":"large"}]}`))
	}))
	defer srv.Close()
	m := embedSpec("m1")
	m["provider"].(map[string]any)["operations"].(map[string]any)["models"] = map[string]any{"path": "/models"}
	b := m["provides"].([]any)[0].(map[string]any)
	b["config"].([]any)[0].(map[string]any)["options_from"] = "models"
	raw, _ := json.Marshal(m)
	SaveConnector(RootDB, Connector{Name: "vec", Kind: Kind, Spec: raw})
	c, _ := GetConnector(RootDB, "vec")
	SaveSettings(c, "", map[string]string{"endpoint": srv.URL}, map[string]string{"api_key": "sk-1"})
	if _, err := ListOptions(context.Background(), "vec", "embed.text/model"); err == nil {
		t.Error("listed for a connection in no use")
	}
	ctx, end := Preview(context.Background(), "vec")
	defer end()
	got, err := ListOptions(ctx, "vec", "embed.text/model")
	if err != nil || strings.Join(got, ",") != "small,large" || auth != "Bearer sk-1" {
		t.Errorf("options: %v %v, sent %q", got, err, auth)
	}
	if _, err := ListOptions(ctx, "vec", "dims"); err == nil {
		t.Error("a setting with no options_from listed some")
	}
	for in, want := range map[string]string{`["a","b"]`: "a,b", `{"models":[{"name":"x"}]}`: "x", `{"voices":[{"voice_id":"v1"}]}`: "v1", `{"other":1}`: ""} {
		var node any
		json.Unmarshal([]byte(in), &node)
		if got := strings.Join(optionValues(node), ","); got != want {
			t.Errorf("%s: %q, want %q", in, got, want)
		}
	}
}

// A key a connection may go without (a local server started with no
// --api-key), not given, sends none; given, it is sent.
func TestAnOptionalKeyIsSentOnlyWhenGiven(t *testing.T) {
	world(t)
	srv, got := keyServer(t)
	m := embedSpec("m1")
	delete(m["config"].([]any)[2].(map[string]any), "required")
	raw, _ := json.Marshal(m)
	SaveConnector(RootDB, Connector{Name: "vec", Kind: Kind, Spec: raw})
	ApproveConnector(RootDB, "vec")
	c, _ := GetConnector(RootDB, "vec")
	SaveSettings(c, "", map[string]string{"endpoint": srv.URL}, nil)
	in := map[string]any{"texts": []string{"hi"}}
	if _, err := Call(context.Background(), nil, "vec", "embed.text", in); err != nil {
		t.Fatalf("with no key: %v", err)
	}
	SaveSettings(c, "", map[string]string{"endpoint": srv.URL}, map[string]string{"api_key": "sk-9"})
	Call(context.Background(), nil, "vec", "embed.text", in)
	if len(*got) != 2 || strings.Contains((*got)[0], "Bearer") || !strings.Contains((*got)[1], " Bearer sk-9 ") {
		t.Errorf("sent: %v", *got)
	}
}

// A kept key is told from another by its last four characters and the day
// it was given, and nothing more: a short key and a password show none of
// themselves, one never given says nothing, and one kept but unreadable
// says so.
func TestAKeptKeyShowsOnlyItsEnd(t *testing.T) {
	world(t)
	spec := keyedSearch()
	spec["config"] = append(spec["config"].([]any),
		map[string]any{"name": "password", "label": "Password", "type": "secret", "send": "basic:api"})
	raw, _ := json.Marshal(spec)
	if err := SaveConnector(RootDB, Connector{Name: "finder", Kind: Kind, Spec: raw}); err != nil {
		t.Fatal(err)
	}
	c, _ := GetConnector(RootDB, "finder")
	if got := KeptSecrets(RootDB, c); len(got) != 0 {
		t.Errorf("none given: %v", got)
	}
	SetConnectionSecret(RootDB, c, "api_key", "sk-live-0123456789abWXYZ", []string{"https://find.example"})
	SetConnectionSecret(RootDB, c, "password", "a-long-password-of-many-letters", []string{"https://find.example"})
	got := KeptSecrets(RootDB, c)
	if !strings.HasPrefix(got["api_key"], "ends in …WXYZ, given ") {
		t.Errorf("a long key: %q", got["api_key"])
	}
	if strings.Contains(got["api_key"], "0123") {
		t.Errorf("more than its end shows: %q", got["api_key"])
	}
	if !strings.HasPrefix(got["password"], "set, given ") || strings.Contains(got["password"], "ters") {
		t.Errorf("a password shows none of itself: %q", got["password"])
	}
	SetConnectionSecret(RootDB, c, "api_key", "short-key-12", []string{"https://find.example"})
	if got := KeptSecrets(RootDB, c)["api_key"]; !strings.HasPrefix(got, "set, given ") {
		t.Errorf("a short key shows none of itself: %q", got)
	}
	RootDB.Sub("connection:"+c.ID).Set("secret", "api_key", "not a secret record")
	if got := KeptSecrets(RootDB, c)["api_key"]; got != "kept but cannot be read: give it again" {
		t.Errorf("an unreadable key: %q", got)
	}
}
