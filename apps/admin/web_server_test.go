package admin

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/serverconf"
	"github.com/cmcoffee/snugforge/kvlite"
)

// The web server's settings through the real routes: only an administrator
// reads or saves them; a broken value is refused with what is wrong; a save
// that would leave out the address it comes from is refused, judged with the
// proxy list being saved; a good one is kept and says what waits for a
// restart.
func TestTheWebServerSettingsThroughTheirRoutes(t *testing.T) {
	adb := &DBase{Store: kvlite.MemStore()}
	adb.Set(AuthTable, "user:alice", AuthUser{Username: "alice"})
	adb.Set(AuthTable, "user:root", AuthUser{Username: "root", Admin: true})
	prevAuth, prevRoot := AuthDB, RootDB
	AuthDB = func() Database { return adb }
	RootDB = &DBase{Store: kvlite.MemStore()}
	saved := serverconf.Settings{Addr: ":8080", MaxConcurrent: 1}
	pl, ps, pr := serverconf.Load, serverconf.Save, serverconf.Running
	serverconf.Load = func() serverconf.Settings { return saved }
	serverconf.Save = func(s serverconf.Settings) error { saved = s; return nil }
	serverconf.Running = serverconf.Settings{Addr: ":8080", MaxConcurrent: 1}
	t.Cleanup(func() {
		AuthDB, RootDB = prevAuth, prevRoot
		serverconf.Load, serverconf.Save, serverconf.Running = pl, ps, pr
	})

	mux := http.NewServeMux()
	(&AdminApp{}).RegisterRoutes(mux, "")
	// Arriving through a proxy at 10.0.0.5, for a person at 192.168.1.20.
	do := func(user, method, body string) (int, string) {
		r := httptest.NewRequest(method, "/api/web-server", strings.NewReader(body))
		r.RemoteAddr = "10.0.0.5:4000"
		r.Header.Set("X-Forwarded-For", "192.168.1.20")
		r.AddCookie(&http.Cookie{Name: "gohort_session", Value: AuthCreateSession(adb, user)})
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w.Code, w.Body.String()
	}

	if code, _ := do("alice", "POST", `{"addr":":9999","max_concurrent":1}`); code < 400 || saved.Addr != ":8080" {
		t.Fatalf("a user who is not an administrator saved: %d", code)
	}
	if code, body := do("root", "POST", `{"addr":"9999","max_concurrent":1}`); code != 400 || !strings.Contains(body, "host:port") {
		t.Errorf("a broken address: %d %s", code, body)
	}
	// Without the proxy listed, this request is 10.0.0.5, which the list
	// leaves out.
	if code, body := do("root", "POST", `{"addr":":8080","max_concurrent":1,"admin_allowed_ips":"192.168.1.0/24"}`); code != 400 || !strings.Contains(body, "lock you out") || !strings.Contains(body, "10.0.0.5") {
		t.Errorf("a list that leaves out this request's address: %d %s", code, body)
	}
	// Listing the proxy in the same save makes this request 192.168.1.20.
	code, body := do("root", "POST", `{"addr":":9090","max_concurrent":2,"admin_allowed_ips":"192.168.1.0/24","trusted_proxies":"10.0.0.5"}`)
	if code != 200 || saved.Addr != ":9090" || saved.TrustedProxies != "10.0.0.5" || saved.AdminAllowedIPs != "192.168.1.0/24" {
		t.Fatalf("a good save: %d %s, kept %+v", code, body, saved)
	}
	if !strings.Contains(body, "waiting for a restart: the address, the app limit") {
		t.Errorf("the save does not say what waits for a restart: %s", body)
	}
	if code, body := do("root", "GET", ""); code != 200 || !strings.Contains(body, `"running":"HTTP on :8080`) {
		t.Errorf("the page: %d %s", code, body)
	}
}
