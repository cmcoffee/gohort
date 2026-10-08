package customapps

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
)

// An app's owner writes its assets from the page; a viewer of a shared app
// cannot replace what everyone sees. Sounds are allowed now, and every asset
// is served sandboxed, so an SVG opened directly runs no script.
func TestAssetsAreWrittenByTheOwnerAndServedSandboxed(t *testing.T) {
	prev := AppAssetsDir()
	SetAppAssetsDir(t.TempDir())
	t.Cleanup(func() { SetAppAssetsDir(prev) })
	T := &CustomApps{}
	put := func(user, name string, body []byte) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		T.handleAssetWrite(w, httptest.NewRequest(http.MethodPut, "/apps/game/assets/"+name, bytes.NewReader(body)), user, "alice", "game", name)
		return w
	}
	if w := put("bob", "hero.png", []byte("PNG")); w.Code != http.StatusForbidden {
		t.Fatalf("a viewer wrote an asset: %d", w.Code)
	}
	if w := put("alice", "jump.ogg", []byte("OggS")); w.Code != http.StatusOK {
		t.Fatalf("the owner could not save a sound: %d %s", w.Code, w.Body.String())
	}
	if w := put("alice", "page.html", []byte("<script>")); w.Code != http.StatusBadRequest {
		t.Fatalf("an html asset was accepted: %d", w.Code)
	}
	if w := put("alice", "big.png", make([]byte, MaxAppAssetBytes+1)); w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("an oversized asset was accepted: %d", w.Code)
	}
	put("alice", "logo.svg", []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`))

	w := httptest.NewRecorder()
	T.handleAsset(w, httptest.NewRequest(http.MethodGet, "/apps/game/assets/logo.svg", nil), "alice", "game", "logo.svg")
	csp := w.Header().Get("Content-Security-Policy")
	if w.Code != http.StatusOK || !strings.HasPrefix(csp, "sandbox;") || !strings.Contains(csp, "default-src 'none'") {
		t.Fatalf("an svg served without a sandbox: %d, CSP %q", w.Code, csp)
	}
	if ct := w.Header().Get("Content-Type"); ct != "image/svg+xml" {
		t.Errorf("content type %q", ct)
	}
	w = httptest.NewRecorder()
	T.handleAsset(w, httptest.NewRequest(http.MethodGet, "/apps/game/assets/jump.ogg", nil), "alice", "game", "jump.ogg")
	if w.Code != http.StatusOK || w.Header().Get("Content-Type") != "audio/ogg" {
		t.Fatalf("the sound: %d %q", w.Code, w.Header().Get("Content-Type"))
	}
	w = httptest.NewRecorder()
	T.handleAssetList(w, "alice", "game")
	if !strings.Contains(w.Body.String(), "jump.ogg") || !strings.Contains(w.Body.String(), "logo.svg") {
		t.Fatalf("list = %s", w.Body.String())
	}
	w = httptest.NewRecorder()
	T.handleAssetWrite(w, httptest.NewRequest(http.MethodDelete, "/apps/game/assets/jump.ogg", nil), "alice", "alice", "game", "jump.ogg")
	if w.Code != http.StatusOK {
		t.Fatalf("delete: %d %s", w.Code, w.Body.String())
	}
	if _, _, err := ReadAppAsset("alice", "game", "jump.ogg"); err == nil {
		t.Error("a deleted asset still reads")
	}
}

// The page may reach its asset list as well as each asset.
func TestThePageMayListItsAssets(t *testing.T) {
	found := false
	for _, p := range appOwnPaths {
		if p == "assets" {
			found = true
		}
	}
	if !found {
		t.Fatalf("appOwnPaths %v lacks the asset list", appOwnPaths)
	}
}
