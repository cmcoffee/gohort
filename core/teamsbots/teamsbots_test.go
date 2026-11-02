package teamsbots

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/snugforge/kvlite"
)

const appID = "11111111-2222-3333-4444-555555555555"

func world(t *testing.T) {
	t.Helper()
	prevRoot := RootDB
	RootDB = &DBase{Store: kvlite.MemStore()}
	tokens.Lock()
	tokens.by = map[string]cachedToken{}
	tokens.Unlock()
	t.Cleanup(func() { RootDB = prevRoot })
}

func bot() Bot {
	return Bot{Owner: "alice@example.test", Name: "Wren", AppID: appID, TenantID: "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"}
}

// What a bot may be: someone's, a GUID app id and tenant (or none), a name
// Teams can show, an https reply host, the conversation types Teams has;
// a new one needs its secret, and is held when the policy holds it.
func TestWhatABotMayBe(t *testing.T) {
	world(t)
	for _, c := range []struct {
		change func(*Bot)
		want   string
	}{
		{func(b *Bot) { b.Owner = "" }, "someone's"},
		{func(b *Bot) { b.AppID = "app-1" }, "GUID"},
		{func(b *Bot) { b.TenantID = "contoso" }, "tenant id is a GUID"},
		{func(b *Bot) { b.Name = strings.Repeat("x", 31) }, "30 characters"},
		{func(b *Bot) { b.Service = "Teams Bot" }, "service name"},
		{func(b *Bot) { b.ServiceHost = "http://smba.example.test" }, "https"},
		{func(b *Bot) { b.Accept = []string{"meeting"} }, "not \"meeting\""},
	} {
		b := bot()
		c.change(&b)
		if _, err := Save(b, "s", false); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("want %q, got %v", c.want, err)
		}
	}
	if _, err := Save(bot(), "", false); err == nil {
		t.Error("a new bot with no secret")
	}
	b := bot()
	b.Accept = []string{"GROUPCHAT", " personal "}
	saved, err := Save(b, "s1", true)
	if err != nil || saved.Service != DefaultService || saved.ServiceHost != DefaultServiceHost || !saved.AwaitingApproval ||
		saved.Usable() || strings.Join(saved.Accept, ",") != "groupChat,personal" || Secret(b.Owner, "teams") != "s1" {
		t.Fatalf("%+v %v", saved, err)
	}
	if !saved.Accepts("personal") || saved.Accepts("channel") || !(Bot{}).Accepts("channel") {
		t.Error("what it answers in")
	}
	SetWebhook(b.Owner, "teams", "wren-hook")
	again := bot()
	again.Name = "Wren 2"
	if got, err := Save(again, "", false); err != nil || !got.AwaitingApproval || got.Webhook != "wren-hook" || !got.Created.Equal(saved.Created) || Secret(b.Owner, "teams") != "s1" {
		t.Errorf("an update keeps what it was given: %+v %v", got, err)
	}
	other := bot()
	other.AppID = "99999999-2222-3333-4444-555555555555"
	if _, err := Save(other, "", false); err == nil {
		t.Error("a new app id kept the old app's secret")
	}
	if AdminSetDisabled(b.Owner, "teams", false) != nil {
		t.Fatal("approve")
	}
	if got, _ := Load(b.Owner, "teams"); !got.Usable() {
		t.Errorf("approved: %+v", got)
	}
	AdminSetDisabled(b.Owner, "teams", true)
	if got, _ := Save(again, "", false); got.Usable() {
		t.Error("an owner's save switched back on what an administrator switched off")
	}
	if len(List(b.Owner)) != 1 || len(List("bob")) != 0 || len(All()) != 1 {
		t.Error("listing")
	}
	if Remove(b.Owner, "teams") != nil || Secret(b.Owner, "teams") != "" {
		t.Error("removing it, with its secret")
	}
}

// microsoft stands in for the token endpoint and the Teams service.
type microsoft struct {
	mu      sync.Mutex
	srv     *httptest.Server
	asked   []url.Values
	tokenAt string
	posts   []string
	refuse  int
}

func newMicrosoft(t *testing.T) *microsoft {
	m := &microsoft{}
	m.srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		defer m.mu.Unlock()
		if strings.HasSuffix(r.URL.Path, "/oauth2/v2.0/token") {
			r.ParseForm()
			m.asked = append(m.asked, r.PostForm)
			m.tokenAt = r.URL.Path
			json.NewEncoder(w).Encode(map[string]any{"access_token": "tok-" + r.PostForm.Get("client_id"), "expires_in": 3600})
			return
		}
		b, _ := io.ReadAll(r.Body)
		m.posts = append(m.posts, r.URL.Path+" "+r.Header.Get("Authorization")+" "+string(b))
		if m.refuse != 0 {
			w.WriteHeader(m.refuse)
			return
		}
		w.Write([]byte(`{"id":"r1"}`))
	}))
	prevURL, prevClient := TokenURL, HTTPClient
	TokenURL = func(tenant string) string { return m.srv.URL + "/" + tenant + "/oauth2/v2.0/token" }
	HTTPClient = m.srv.Client()
	t.Cleanup(func() { m.srv.Close(); TokenURL, HTTPClient = prevURL, prevClient })
	return m
}

// A reply is signed with a token minted from the bot's own app id and
// secret at its tenant, minted once and kept; it goes only under the bot's
// service host, and a refused token is minted again.
func TestARepliesTokenAndWhereItGoes(t *testing.T) {
	world(t)
	m := newMicrosoft(t)
	b := bot()
	b.ServiceHost = m.srv.URL
	b, _ = Save(b, "sekret", false)
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		if err := Send(ctx, b, m.srv.URL+"/amer/", "19:c@thread", map[string]string{"text": "hi"}); err != nil {
			t.Fatal(err)
		}
	}
	if len(m.asked) != 1 || m.asked[0].Get("client_id") != appID || m.asked[0].Get("client_secret") != "sekret" ||
		m.asked[0].Get("grant_type") != "client_credentials" || m.asked[0].Get("scope") != "https://api.botframework.com/.default" ||
		m.tokenAt != "/"+b.TenantID+"/oauth2/v2.0/token" {
		t.Errorf("token asked %d times: %v at %s", len(m.asked), m.asked, m.tokenAt)
	}
	if len(m.posts) != 2 || m.posts[0] != "/amer/v3/conversations/19:c@thread/activities Bearer tok-"+appID+` {"text":"hi"}` {
		t.Errorf("posts: %q", m.posts)
	}
	// A host with a path, so an address outside it is still one the
	// stand-in answers: only the bot's own check keeps the reply from it.
	narrow := b
	narrow.ServiceHost = m.srv.URL + "/amer"
	for _, outside := range []string{m.srv.URL + "/emea", m.srv.URL + "/amerx"} {
		if err := Send(ctx, narrow, outside, "c", nil); err == nil {
			t.Errorf("a reply to %s, outside its host", outside)
		}
	}
	if len(m.posts) != 2 {
		t.Errorf("posted outside its host: %q", m.posts)
	}
	m.refuse = http.StatusUnauthorized
	if Send(ctx, b, m.srv.URL+"/amer", "c", nil) == nil {
		t.Error("a refused reply")
	}
	m.refuse = 0
	Send(ctx, b, m.srv.URL+"/amer", "c", nil)
	if len(m.asked) != 2 {
		t.Errorf("a refused token is minted again: %d", len(m.asked))
	}
	multi := bot()
	multi.Service, multi.TenantID, multi.ServiceHost = "teams2", "", m.srv.URL
	multi, _ = Save(multi, "s2", false)
	Send(ctx, multi, m.srv.URL+"/emea", "c", nil)
	if m.tokenAt != "/botframework.com/oauth2/v2.0/token" {
		t.Errorf("a multi-tenant bot's token: %s", m.tokenAt)
	}
}

// The app package: a manifest naming the bot by its app id and name, with
// the scopes it answers in, this site as its links and valid domain, never
// its owner's gohort name; and the two icons, at the sizes Teams requires.
func TestTheAppPackage(t *testing.T) {
	b := bot()
	b.Accept = []string{"personal", "channel"}
	if _, err := b.AppPackage("http://gohort.example.test/hook/x"); err == nil {
		t.Error("a package for an address that is not https")
	}
	z, err := b.AppPackage("https://gohort.example.test/hook/wren")
	if err != nil {
		t.Fatal(err)
	}
	r, err := zip.NewReader(bytes.NewReader(z), int64(len(z)))
	if err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{}
	for _, f := range r.File {
		rc, _ := f.Open()
		files[f.Name], _ = io.ReadAll(rc)
		rc.Close()
	}
	var m map[string]any
	if err := json.Unmarshal(files["manifest.json"], &m); err != nil {
		t.Fatal(err)
	}
	bots, _ := json.Marshal(m["bots"])
	raw := string(files["manifest.json"])
	if m["id"] != appID || !strings.Contains(string(bots), `"botId":"`+appID+`"`) || !strings.Contains(string(bots), `"scopes":["personal","team"]`) ||
		!strings.Contains(raw, `"validDomains": [`+"\n"+`    "gohort.example.test"`) || !strings.Contains(raw, `"websiteUrl": "https://gohort.example.test"`) ||
		strings.Contains(raw, "alice") {
		t.Errorf("manifest: %s", raw)
	}
	for name, size := range map[string]int{"color.png": 192, "outline.png": 32} {
		img, err := png.Decode(bytes.NewReader(files[name]))
		if err != nil || img.Bounds().Dx() != size || img.Bounds().Dy() != size {
			t.Errorf("%s: %v %v", name, err, img)
		}
	}
}
