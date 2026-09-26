package textutil

import (
	"net/url"
	"regexp"
	"strings"
)

// SameOriginURLHint returns guidance to append when a network tool refuses a
// url, IF what it was handed is a path on this server rather than a malformed
// URL. Empty for anything else, so an ordinary typo still gets the plain
// "must be http:// or https://" message.
//
// The refusal is correct — fetch_url and browse_page reach the public
// internet, and "/apps/foo/" is not out there. But "must be an http:// URL"
// answers a question the caller wasn't asking. An agent that had just patched
// one of its own apps tried to LOOK at it: /apps/<slug>/ against fetch_url,
// then browse_page, then a guessed public hostname that 404'd, then treating
// the app as an agent — six tool errors, none of which pointed at the two
// tools that actually do this. The capability existed the whole time.
//
// So the message names it. app_def(action="verify") loads an app page in a
// real browser and reports JS errors and failed requests; show_html(url=…)
// renders any same-origin path beside the conversation for the user to see.
func SameOriginURLHint(target string) string {
	t := strings.TrimSpace(target)
	// A path on this server: one leading slash, and NOT "//host" (which is a
	// protocol-relative URL, i.e. a genuinely external address).
	if !strings.HasPrefix(t, "/") || strings.HasPrefix(t, "//") {
		return ""
	}
	if slug := customAppSlug(t); slug != "" {
		return ", that is a path on THIS server, not a public URL. It looks like your own app: to check that it renders, call app_def(action=\"verify\", id=\"" +
			slug + "\") (loads it in a real browser and reports JS errors); to show it to the user, call show_html(url=\"" + t + "\")."
	}
	return ", that is a path on THIS server, not a public URL. These tools reach the public internet. To display a page from this server to the user, call show_html(url=\"" + t + "\")."
}

// customAppSlug extracts the app slug from a /apps/<slug>/… path, or "" when
// the path isn't a custom-app page.
func customAppSlug(path string) string {
	// Both mounts. The app moved from /custom to /apps and the old path still
	// answers (it redirects), so a /custom/ URL is a real page and the hint
	// that explains it has to fire for one — a model working from an older
	// prompt, or a person pasting a bookmark, gets the same help.
	prefix := "/apps/"
	if !strings.HasPrefix(path, prefix) {
		prefix = "/custom/"
	}
	if !strings.HasPrefix(path, prefix) {
		return ""
	}
	rest := strings.TrimPrefix(path, prefix)
	if i := strings.IndexByte(rest, '/'); i >= 0 {
		rest = rest[:i]
	}
	// "/apps/" alone, or a reserved sub-route, names no app.
	if rest == "" || rest == "pub" {
		return ""
	}
	return rest
}

// secretQueryParams are query parameters that carry a credential outright.
// Matched by exact name, so a pagination "page_token" or a "tokenizer" option
// is not caught; "token" is on the list but, like the rest, only counts with a
// value long enough to be a key.
var secretQueryParams = map[string]bool{
	"key": true, "api_key": true, "apikey": true, "api-key": true,
	"access_token": true, "auth_token": true, "token": true,
	"client_secret": true, "secret": true, "password": true, "passwd": true,
}

// secretShapes are key formats recognisable on sight, wherever they sit in a
// URL: Google API keys, OpenAI-style keys, GitHub tokens, Slack tokens, AWS
// access key ids.
var secretShapes = regexp.MustCompile(`AIza[0-9A-Za-z_\-]{30,}|\bsk-[A-Za-z0-9_\-]{20,}|\bgh[pousr]_[A-Za-z0-9]{30,}|\bxox[abposr]-[A-Za-z0-9\-]{10,}|\bAKIA[0-9A-Z]{16}\b`)

// URLSecretReason says why a URL looks like it carries a secret (a credential
// in a query parameter, a recognisable key, a password in the userinfo), or ""
// when it does not.
//
// A network tool refuses such a URL. Secrets never travel as tool arguments:
// a credential's own fetch tool attaches it server-side. Observed 2026-09-26:
// an agent fetching a Gemini URL wrote "?key=AIza..." into a plain fetch_url
// call, a key that was in no tool result, so either invented or recalled from
// somewhere it should not have been, and sent to the network either way.
func URLSecretReason(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return ""
	}
	if u.User != nil {
		if _, hasPass := u.User.Password(); hasPass {
			return "a password in the URL itself (user:password@host)"
		}
	}
	for name, vals := range u.Query() {
		if !secretQueryParams[strings.ToLower(name)] {
			continue
		}
		for _, v := range vals {
			if len(strings.TrimSpace(v)) >= 16 {
				return "a credential in its \"" + name + "\" query parameter"
			}
		}
	}
	if secretShapes.MatchString(raw) {
		return "what looks like an API key or token"
	}
	return ""
}
