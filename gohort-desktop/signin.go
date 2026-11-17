package main

// Signing the desktop app in through the person's own browser.
//
// A sign-in through an identity provider (Microsoft, any OIDC) cannot finish
// inside this window: the provider sends the person back to the server's real
// address, outside this app's proxy, so the session cookie never reaches the
// jar. So the window's "Sign in with Microsoft" (GET /login/sso) is taken
// here instead: the app starts a sign-in on the server with a challenge, the
// hash of a secret only it holds, opens the person's browser on the server's
// confirm page, and waits. They sign in there as they always do and confirm;
// the app claims the sign-in with its secret, once, and puts the session in
// its jar. The server's half is core/desktopsignin.

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// SIGNIN_PATH starts a browser sign-in and shows the waiting page;
// SIGNIN_POLL_PATH is that page asking whether it is done.
const (
	SIGNIN_PATH      = "/__desktop/signin"
	SIGNIN_POLL_PATH = "/__desktop/signin/poll"
	// sso_start_path is the server's "sign in with Microsoft" link, which
	// this window cannot finish on its own.
	sso_start_path = "/login/sso"
)

// browser_signin is the sign-in this window is waiting on.
type browser_signin struct {
	mu       sync.Mutex
	code     string
	verifier string
	phrase   string
	open_url string
	started  time.Time
}

var signin_client = &http.Client{Timeout: 20 * time.Second}

// handle_signin starts a sign-in in the person's browser and serves the page
// that waits for it.
func (gp *gohort_proxy) handle_signin(w http.ResponseWriter, r *http.Request) {
	server := strings.TrimRight(gp.cfg.ServerURL(), "/")
	if server == "" {
		gp.serve_configure(w)
		return
	}
	verifier := random_hex(32)
	sum := sha256.Sum256([]byte(verifier))
	body, _ := json.Marshal(map[string]string{"challenge": hex.EncodeToString(sum[:])})
	resp, err := signin_client.Post(server+"/api/desktop/signin/start", "application/json", bytes.NewReader(body))
	if err != nil {
		serve_signin_message(w, "Could not reach the server to start signing in: "+err.Error())
		return
	}
	defer resp.Body.Close()
	var out struct {
		Code   string `json:"code"`
		Phrase string `json:"phrase"`
		URL    string `json:"url"`
		Error  string `json:"error"`
	}
	json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&out)
	if resp.StatusCode == http.StatusNotFound {
		serve_signin_message(w, "This server is older than signing in through the browser. Sign in with your password, or ask its administrator to update it.")
		return
	}
	if resp.StatusCode != http.StatusOK || out.Code == "" {
		serve_signin_message(w, "The server would not start a sign-in: "+first_non_empty(out.Error, resp.Status))
		return
	}
	open := server + out.URL
	gp.signin.mu.Lock()
	gp.signin.code, gp.signin.verifier, gp.signin.phrase = out.Code, verifier, out.Phrase
	gp.signin.open_url, gp.signin.started = open, time.Now()
	gp.signin.mu.Unlock()
	if gp.app != nil {
		gp.app.openURL(open)
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	fmt.Fprintf(w, signin_wait_html, html.EscapeString(out.Phrase), html.EscapeString(open))
}

// handle_signin_poll asks the server whether the person has confirmed, and
// on yes puts the session in the jar: {state: "waiting" | "done" | "ended"}.
func (gp *gohort_proxy) handle_signin_poll(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	reply := func(state, msg string) {
		json.NewEncoder(w).Encode(map[string]string{"state": state, "message": msg})
	}
	gp.signin.mu.Lock()
	code, verifier := gp.signin.code, gp.signin.verifier
	gp.signin.mu.Unlock()
	server := strings.TrimRight(gp.cfg.ServerURL(), "/")
	if code == "" || server == "" {
		reply("ended", "No sign-in is waiting. Start again.")
		return
	}
	state, msg := gp.claim_signin(server, code, verifier)
	if state != "waiting" {
		gp.signin.mu.Lock()
		if gp.signin.code == code {
			gp.signin.code, gp.signin.verifier = "", ""
		}
		gp.signin.mu.Unlock()
	}
	reply(state, msg)
}

// claim_signin is one claim of the sign-in under code.
func (gp *gohort_proxy) claim_signin(server, code, verifier string) (state, msg string) {
	body, _ := json.Marshal(map[string]string{"code": code, "verifier": verifier})
	resp, err := signin_client.Post(server+"/api/desktop/signin/claim", "application/json", bytes.NewReader(body))
	if err != nil {
		// A dropped connection is worth another try; the page asks again.
		return "waiting", ""
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusAccepted:
		return "waiting", ""
	case http.StatusOK:
	default:
		return "ended", "The sign-in ended without signing you in: it was cancelled, it waited too long, or it was confirmed somewhere else. Start again."
	}
	var out struct {
		Cookie struct {
			Name   string `json:"name"`
			Value  string `json:"value"`
			Path   string `json:"path"`
			MaxAge int    `json:"max_age"`
		} `json:"cookie"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&out); err != nil || out.Cookie.Name == "" || out.Cookie.Value == "" {
		return "ended", "The server's reply had no session in it. Start again."
	}
	u, err := url.Parse(server)
	if err != nil {
		return "ended", "The saved server address is not valid."
	}
	c := &http.Cookie{Name: out.Cookie.Name, Value: out.Cookie.Value, Path: first_non_empty(out.Cookie.Path, "/"),
		HttpOnly: true, Secure: u.Scheme == "https", MaxAge: out.Cookie.MaxAge}
	if c.MaxAge > 0 {
		c.Expires = time.Now().Add(time.Duration(c.MaxAge) * time.Second)
	}
	gp.cookies.SetCookies(u, []*http.Cookie{c})
	return "done", ""
}

func random_hex(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func first_non_empty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func serve_signin_message(w http.ResponseWriter, msg string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	fmt.Fprintf(w, signin_message_html, html.EscapeString(msg))
}

const signin_page_style = `<style>
:root{color-scheme:dark}
body{margin:0;background:#0b1020;color:#e7eaf6;font:15px/1.5 -apple-system,system-ui,sans-serif;display:flex;min-height:100vh;align-items:center;justify-content:center}
.box{max-width:460px;padding:2rem;text-align:center}
h1{font-size:1.25rem;margin:0 0 .75rem}
.phrase{font:600 1.6rem ui-monospace,Menlo,monospace;letter-spacing:.12em;margin:1rem 0;color:#a5b4fc}
.muted{color:#97a0bf;font-size:.9rem}
.status{margin:1.25rem 0 .5rem;min-height:1.5em}
a{color:#a5b4fc}
.row{display:flex;gap:1rem;justify-content:center;margin-top:1rem}
</style>`

// signin_wait_html: %s the phrase, %s the page the browser was opened on.
const signin_wait_html = `<!DOCTYPE html><html><head><meta charset="utf-8"><title>Signing in</title>` + signin_page_style + `</head><body><div class="box">
<h1>Finish signing in in your browser</h1>
<div class="muted">Your browser opened on the server's sign-in page. Sign in there, check that it shows this code, and confirm:</div>
<div class="phrase">%s</div>
<div class="status" id="st"></div>
<div class="row"><a href="%s" target="_blank" rel="noopener">Open the browser again</a><a href="/login">Cancel</a></div>
</div><script>
(function(){
  var frames = '⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏', i = 0, t0 = Date.now(), done = false;
  var st = document.getElementById('st');
  var text = st.appendChild(document.createTextNode(''));
  function tick(){
    if (done) return;
    var s = Math.round((Date.now() - t0) / 1000);
    text.nodeValue = frames[i++ %% frames.length] + ' Waiting for you to confirm in the browser' + (s >= 3 ? ' - ' + s + 's' : '');
  }
  setInterval(tick, 100); tick();
  function poll(){
    fetch('/__desktop/signin/poll', {method: 'POST'}).then(function(r){ return r.json(); }).then(function(d){
      if (d.state === 'done') { done = true; text.nodeValue = 'Signed in.'; location.replace('/'); return; }
      if (d.state === 'ended') { done = true; text.nodeValue = d.message || 'The sign-in ended.'; return; }
      setTimeout(poll, 2000);
    }).catch(function(){ setTimeout(poll, 3000); });
  }
  setTimeout(poll, 1500);
})();
</script></body></html>`

// signin_message_html: %s the message.
const signin_message_html = `<!DOCTYPE html><html><head><meta charset="utf-8"><title>Signing in</title>` + signin_page_style + `</head><body><div class="box">
<h1>Could not sign in through the browser</h1>
<div class="muted">%s</div>
<div class="row"><a href="/login">Back to sign in</a></div>
</div></body></html>`
