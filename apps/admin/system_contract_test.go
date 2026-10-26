package admin

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

// What the System, Settings and Costs panels get back, through the real routes
// and the gate in front of them: status, host dependencies and the app list
// (as their shape, since they describe the host), site settings and the
// tunables read, changed and reverted whole and per app, cost rates and spend,
// the vector index, and one database record. Each as an administrator, as a
// user who is not one, by the wrong method and with a broken or missing body,
// and then what the activity log says happened. Compared with
// testdata/system_contract.golden.
//
// Run with UPDATE_GOLDEN=1 to rewrite the golden after a change that is meant.
func TestSystemRoutesContract(t *testing.T) {
	adb := &DBase{Store: kvlite.MemStore()}
	adb.Set(AuthTable, "user:alice", AuthUser{Username: "alice"})
	adb.Set(AuthTable, "user:root", AuthUser{Username: "root", Admin: true})
	prevAuth, prevRoot, prevRates := AuthDB, RootDB, GetCostRates()
	AuthDB = func() Database { return adb }
	RootDB = &DBase{Store: kvlite.MemStore()}
	t.Cleanup(func() {
		AuthDB, RootDB = prevAuth, prevRoot
		SetCostRates(prevRates)
		InvalidateTunables()
	})
	SetCostRates(CostRates{})
	InvalidateTunables()
	adb.Set("zz_browse", "a-string", "hello")
	adb.Set("zz_browse", "a-number", 42)

	ct := &contract{dates: true}
	app := &AdminApp{}
	mux := http.NewServeMux()
	app.RegisterRoutes(mux, "")
	do := func(user, method, path, body string) { ct.run(mux, adb, user, method, path, body) }

	// A dependency row leaves out what the host lacks, so its keys are the
	// host's too: the names of what is probed are the code's.
	depNames := func(out string) string {
		var rows []struct{ Name string }
		if json.Unmarshal([]byte(out), &rows) != nil {
			return out
		}
		names := make([]string, len(rows))
		for i, r := range rows {
			names[i] = r.Name
		}
		sort.Strings(names)
		return "probes " + strings.Join(names, ",")
	}
	for _, h := range []struct {
		path  string
		shape func(string) string
	}{{"/api/status", replyShape}, {"/api/dependencies", depNames}, {"/api/apps", replyShape}} {
		ct.shape = h.shape
		do("", "GET", h.path, "")
		do("alice", "GET", h.path, "")
		do("root", "GET", h.path, "")
		do("root", "POST", h.path, "{}")
	}
	ct.shape = nil

	// The settings record carries every registered knob; the one this test
	// moves is followed through the activity log instead.
	ct.drop = func(k string) bool { return strings.HasPrefix(k, "tune_") }
	for _, user := range []string{"", "alice", "root"} {
		do(user, "GET", "/api/settings", "")
	}
	ct.drop = nil
	reads := []string{"/api/settings?app=/nope", "/api/cost-rates", "/api/cost-history", "/api/cost-history?days=0",
		"/api/cost-history?days=x", "/api/cost-by-source", "/api/cost-by-source?days=7", "/api/vector-stats", "/api/vector-stats/by-kind",
		"/api/db/record?table=zz_browse&key=a-string", "/api/db/record?table=zz_browse&key=a-number", "/api/db/record?table=zz_browse&key=nope",
		"/api/db/record?table=zz_browse", "/api/settings/reset-tunables"}
	for _, p := range reads {
		do("", "GET", p, "")
		do("alice", "GET", p, "")
		do("root", "GET", p, "")
	}
	for _, p := range []string{"/api/cost-history", "/api/cost-by-source", "/api/vector-stats", "/api/vector-stats/by-kind", "/api/db/record?table=zz_browse&key=a-string"} {
		do("root", "POST", p, "{}")
	}

	do("alice", "PATCH", "/api/settings", `{"allow_signup":true}`)
	do("root", "PATCH", "/api/settings", `{"allow_signup":true}`)
	do("root", "PATCH", "/api/settings", `{"session_days":14,"max_login_attempts":7,"lockout_minutes":30,"session_absolute_days":0}`)
	do("root", "PATCH", "/api/settings", `{"session_days":999,"lockout_minutes":0}`)
	do("root", "PUT", "/api/settings", `{"service_name":"Example","external_url":"https://example.test","notify_from":"ops@example.test","site_name":" Site ","doc_brand":" Brand "}`)
	do("root", "POST", "/api/settings", `{"default_apps":["/chat"],"api_key_allow_query":false}`)
	do("root", "PATCH", "/api/settings", `{"ui_theme":"nope"}`)
	do("root", "PATCH", "/api/settings", `{"ui_theme":"indigo"}`)
	do("root", "PATCH", "/api/settings", `{"timezone":"Not/AZone"}`)
	do("root", "PATCH", "/api/settings", `{"timezone":"UTC"}`)
	do("root", "PATCH", "/api/settings", `{"timezone":""}`)
	do("root", "PATCH", "/api/settings", `{"ollama_proxy_bind":"10.0.0.1"}`)
	do("root", "PATCH", "/api/settings", `{"ollama_proxy_port":11500,"ollama_proxy_bind":"127.0.0.1","fetch_cache_quota_mb":0}`)
	do("root", "PATCH", "/api/settings", `{"channel_wake_rules":"  wake on mention  "}`)
	do("root", "PATCH", "/api/settings", `{"allow_signup":`)
	do("root", "PATCH", "/api/settings", ``)
	do("root", "PATCH", "/api/settings", `[]`)
	do("root", "DELETE", "/api/settings", "")
	// One knob changed past its ceiling, to it, and back to its default.
	var knob TunableSpec
	for _, s := range AllTunableSpecs() {
		if s.Key == "tune_account_key_lifetime" {
			knob = s
		}
	}
	if knob.Key == "" {
		t.Fatal("the knob this test moves is not registered")
	}
	do("root", "PATCH", "/api/settings", `{"`+knob.Key+`":`+jsonNumber(knob.Max)+`}`)
	do("root", "PATCH", "/api/settings", `{"`+knob.Key+`":`+jsonNumber(knob.Max+1)+`}`)
	do("root", "PATCH", "/api/settings", `{"`+knob.Key+`":`+jsonNumber(knob.Default)+`}`)
	do("root", "PATCH", "/api/settings?app=/nope", `{"`+knob.Key+`":`+jsonNumber(knob.Max)+`}`)
	ct.drop = func(k string) bool { return strings.HasPrefix(k, "tune_") }
	do("root", "GET", "/api/settings", "")
	ct.drop = nil

	do("alice", "POST", "/api/settings/reset-tunables", "")
	do("root", "POST", "/api/settings/reset-tunables?category=nope", "")
	do("root", "POST", "/api/settings/reset-tunables?app=/nope", "")
	do("root", "POST", "/api/settings/reset-tunables", "")
	do("root", "PUT", "/api/settings/reset-tunables", "")

	do("alice", "PUT", "/api/cost-rates", `{"search_per_call":0.01}`)
	do("root", "PUT", "/api/cost-rates", `{"search_per_call":0.01,"image_per_call":0.04,"cache_read_multiplier":0.1}`)
	do("root", "PUT", "/api/cost-rates", `{"worker_input_per_1k":0.002,"worker_input_per_1m":1.5,"lead_output_per_1m":9}`)
	do("root", "PUT", "/api/cost-rates", `{"search_per_call":`)
	do("root", "PUT", "/api/cost-rates", ``)
	do("root", "POST", "/api/cost-rates", `{}`)
	do("root", "GET", "/api/cost-rates", "")

	ct.activity(mux, adb, "root")
	ct.check(t, "testdata/system_contract.golden")
}

// jsonNumber writes a knob's value as a request carries it.
func jsonNumber(f float64) string {
	b, _ := json.Marshal(f)
	return string(b)
}
