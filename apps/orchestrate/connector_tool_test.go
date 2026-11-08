package orchestrate

import (
	"context"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/service"
	"github.com/cmcoffee/snugforge/kvlite"
)

// An approved connector is every user's tool, so the connector tool may only
// change or remove one on behalf of its drafter or an admin. Before this gate
// any user's Builder could repoint a live connector at a host of its choosing.

type stubConnectorKind struct{ torn int }

func (s *stubConnectorKind) Validate(Connector) error    { return nil }
func (s *stubConnectorKind) Materialize(Connector) error { return nil }
func (s *stubConnectorKind) Teardown(Connector) error    { s.torn++; return nil }
func (s *stubConnectorKind) Summary(Connector) string    { return "stub" }

func connectorOwnerFixture(t *testing.T) {
	t.Helper()
	adb := &DBase{Store: kvlite.MemStore()}
	adb.Set(AuthTable, "user:alice", AuthUser{Username: "alice"})
	adb.Set(AuthTable, "user:bob", AuthUser{Username: "bob"})
	adb.Set(AuthTable, "user:root", AuthUser{Username: "root", Admin: true})
	prevAuth, prevRoot := AuthDB, RootDB
	AuthDB = func() Database { return adb }
	RootDB = &DBase{Store: kvlite.MemStore()}
	t.Cleanup(func() { AuthDB = prevAuth; RootDB = prevRoot })
	RegisterConnectorKind("test_stub_owner", &stubConnectorKind{})
	for _, c := range []Connector{
		{Name: "alices", Kind: "test_stub_owner", Owner: "alice"},
		{Name: "unowned", Kind: "test_stub_owner"},
	} {
		if err := SaveConnector(RootDB, c); err != nil {
			t.Fatalf("seed %s: %v", c.Name, err)
		}
		if err := ApproveConnector(RootDB, c.Name); err != nil {
			t.Fatalf("approve %s: %v", c.Name, err)
		}
	}
}

func TestOnlyTheDrafterOrAnAdminManagesAConnector(t *testing.T) {
	connectorOwnerFixture(t)
	alices, _ := GetConnector(RootDB, "alices")
	unowned, _ := GetConnector(RootDB, "unowned")
	cases := []struct {
		user string
		c    Connector
		want bool
	}{
		{"alice", alices, true},
		{"bob", alices, false},
		{"root", alices, true},
		{"alice", unowned, false}, // no recorded drafter = admin-only
		{"root", unowned, true},
		{"", alices, false},
	}
	for _, tc := range cases {
		if got := mayManageConnector(&ToolSession{Username: tc.user}, tc.c); got != tc.want {
			t.Errorf("user %q on %q: got %v want %v", tc.user, tc.c.Name, got, tc.want)
		}
	}
}

func TestAnotherUserCannotDeleteOrUpdateAConnector(t *testing.T) {
	connectorOwnerFixture(t)
	bob := &ToolSession{Username: "bob"}
	if _, err := connectorDelete(map[string]any{"name": "alices"}, bob); err == nil || !strings.Contains(err.Error(), "another user") {
		t.Fatalf("bob deleting alice's connector should be refused, got %v", err)
	}
	if _, ok := GetConnector(RootDB, "alices"); !ok {
		t.Fatal("the connector was deleted anyway")
	}
	// The ownership check runs before any kind-specific patching.
	if _, err := connectorUpdate(map[string]any{"name": "alices", "url": "https://elsewhere.example"}, bob); err == nil || !strings.Contains(err.Error(), "another user") {
		t.Fatalf("bob updating alice's connector should be refused, got %v", err)
	}
	if _, err := connectorDelete(map[string]any{"name": "alices"}, &ToolSession{Username: "alice"}); err != nil {
		t.Fatalf("alice deleting her own connector: %v", err)
	}
}

// Builder drafts a service connector as its whole spec, which lands
// unapproved; a spec that names something it does not mean is refused with
// what is wrong, and nothing is saved.
func TestBuilderDraftsAServiceConnector(t *testing.T) {
	connectorOwnerFixture(t)
	sess := &ToolSession{Username: "alice"}
	spec := map[string]any{
		"config": []any{map[string]any{"name": "api_key", "type": "secret", "send": "bearer"}},
		"provider": map[string]any{"transport": "http", "base_url": "https://api.example.test",
			"auth":       map[string]any{"secret": "api_key"},
			"operations": map[string]any{"submit": map[string]any{"method": "POST", "path": "/v1/images", "body": map[string]any{"prompt": "{prompt}"}}}},
		"provides": []any{map[string]any{"capability": "image.generate", "steps": []any{map[string]any{"call": "submit"}},
			"output": map[string]any{"image": map[string]any{"path": "data.0.b64_json", "as": "base64"}}}},
	}
	// Named to people, or not drafted: an id shown on every page is what a
	// connection with no display name read as.
	if _, err := connectorCreate(map[string]any{"kind": "service", "name": "acme-images", "service_spec": spec}, sess); err == nil || !strings.Contains(err.Error(), "display_name is required") {
		t.Errorf("drafted with no display name: %v", err)
	}
	if _, ok := GetConnector(RootDB, "acme-images"); ok {
		t.Fatal("a refused draft was saved")
	}
	out, err := connectorCreate(map[string]any{"kind": "service", "name": "acme-images", "display_name": "Acme Images", "service_spec": spec}, sess)
	if err != nil {
		t.Fatal(err)
	}
	c, ok := GetConnector(RootDB, "acme-images")
	if !ok || c.Approved || !strings.Contains(out, "acme-images") || c.Title() != "Acme Images" {
		t.Fatalf("saved %v, approved %v, shown as %q: %s", ok, c.Approved, c.Title(), out)
	}
	if _, err := connectorCreate(map[string]any{"kind": "service", "name": "acme-three", "display_name": "acme images", "service_spec": spec}, sess); err != nil {
		t.Fatal(err)
	}
	if c3, _ := GetConnector(RootDB, "acme-three"); c3.Title() != "acme images 2" {
		t.Errorf("a second one shown as the first is: %q", c3.Title())
	}
	bad := spec
	bad["provides"].([]any)[0].(map[string]any)["capability"] = "image.paint"
	if _, err := connectorCreate(map[string]any{"kind": "service", "name": "acme-two", "display_name": "Acme Two", "service_spec": bad}, sess); err == nil || !strings.Contains(err.Error(), "unknown capability") {
		t.Errorf("an unknown capability: %v", err)
	}
	if _, ok := GetConnector(RootDB, "acme-two"); ok {
		t.Error("a refused spec was saved")
	}

	// Referenced by a page, then changed by alice's Builder: held, by her
	// name, until an administrator accepts it.
	RootDB.Set(ImageTable, "provider", "acme-images")
	bad["provides"].([]any)[0].(map[string]any)["capability"] = "image.generate"
	msg, err := connectorUpdate(map[string]any{"name": "acme-images", "service_spec": spec}, sess)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(msg, "HELD") {
		t.Errorf("what Builder is told of its change: %s", msg)
	}
	if _, err := service.Call(context.Background(), nil, "acme-images", "image.generate", map[string]any{"prompt": "x"}); err == nil || !strings.Contains(err.Error(), "change waiting") {
		t.Errorf("a change by someone not an administrator went live: %v", err)
	}
	if ch, ok := service.PendingChange(RootDB, "acme-images"); !ok || ch.By != "alice" {
		t.Errorf("the change waiting: %+v %v", ch, ok)
	}
	// Media Generation still names it, so her Builder cannot delete it.
	if _, err := connectorDelete(map[string]any{"name": "acme-images"}, sess); err == nil || !strings.Contains(err.Error(), "referenced by Media Generation") {
		t.Errorf("deleting one a page names: %v", err)
	}
	if _, ok := GetConnector(RootDB, "acme-images"); !ok {
		t.Error("deleted while a page names it")
	}
}

// Builder sets a ComfyUI backend up as a connection that lands out of use,
// its key given under Settings rather than through Builder; a rest_image
// draft of one is pointed there.
func TestBuilderSetsUpAComfyUIConnection(t *testing.T) {
	connectorOwnerFixture(t)
	sess := &ToolSession{Username: "alice"}
	msg, err := connectorImportComfyUI(map[string]any{"name": "studio", "base_url": "http://comfy.example:8188"}, sess)
	if err != nil {
		t.Fatal(err)
	}
	c, ok := GetConnector(RootDB, "studio")
	if !ok || c.Kind != service.Kind || c.Template != "comfyui" || c.Approved || c.Owner != "alice" {
		t.Fatalf("set up as: %+v", c)
	}
	if !strings.Contains(msg, "Media Generation") || !strings.Contains(msg, "Settings") {
		t.Errorf("what Builder is told: %s", msg)
	}
	if _, err = connectorCreate(map[string]any{"name": "old", "kind": "rest_image"}, sess); err == nil {
		t.Error("a rest_image draft was made: the kind is gone")
	}
}

// Builder sets an Automatic1111 server up as a service running the a1111
// builtin, as its catalog describes it, out of use.
func TestBuilderSetsUpAnA1111Connection(t *testing.T) {
	connectorOwnerFixture(t)
	sess := &ToolSession{Username: "alice"}
	spec := map[string]any{
		"provider": map[string]any{"transport": "http", "base_url": "http://sd.example:7860", "auth": map[string]any{"none": true},
			"operations": map[string]any{"render": map[string]any{"builtin": "a1111", "options": map[string]any{"mode": "txt2img"}}}},
		"provides": []any{map[string]any{"capability": "image.generate", "steps": []any{map[string]any{"call": "render"}},
			"output": map[string]any{"image": map[string]any{"from": "body"}}}},
	}
	if _, err := connectorCreate(map[string]any{"name": "sd", "kind": service.Kind, "display_name": "Stable Diffusion", "service_spec": spec}, sess); err != nil {
		t.Fatal(err)
	}
	c, ok := GetConnector(RootDB, "sd")
	if !ok || c.Kind != service.Kind || c.Approved || !strings.Contains(string(c.Spec), `"builtin":"a1111"`) {
		t.Fatalf("set up as: %+v", c)
	}
	if _, err := connectorCreate(map[string]any{"name": "sd2", "kind": service.Kind, "display_name": "SD 2",
		"service_spec": map[string]any{"provider": map[string]any{"transport": "http", "base_url": "http://sd.example:7860", "auth": map[string]any{"none": true},
			"operations": map[string]any{"render": map[string]any{"builtin": "a1111", "options": map[string]any{"mode": "sketch"}}}},
			"provides": spec["provides"]}}, sess); err == nil || !strings.Contains(err.Error(), "txt2img (generate) or img2img (edit)") {
		t.Errorf("an unknown mode: %v", err)
	}
}
