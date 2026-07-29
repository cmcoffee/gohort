package core

import "strings"

// SameOriginURLHint returns guidance to append when a network tool refuses a
// url, IF what it was handed is a path on this server rather than a malformed
// URL. Empty for anything else, so an ordinary typo still gets the plain
// "must be http:// or https://" message.
//
// The refusal is correct — fetch_url and browse_page reach the public
// internet, and "/custom/foo/" is not out there. But "must be an http:// URL"
// answers a question the caller wasn't asking. An agent that had just patched
// one of its own apps tried to LOOK at it: /custom/<slug>/ against fetch_url,
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
		return " — that is a path on THIS server, not a public URL. It looks like your own app: to check that it renders, call app_def(action=\"verify\", id=\"" +
			slug + "\") (loads it in a real browser and reports JS errors); to show it to the user, call show_html(url=\"" + t + "\")."
	}
	return " — that is a path on THIS server, not a public URL. These tools reach the public internet. To display a page from this server to the user, call show_html(url=\"" + t + "\")."
}

// customAppSlug extracts the app slug from a /custom/<slug>/… path, or "" when
// the path isn't a custom-app page.
func customAppSlug(path string) string {
	const prefix = "/custom/"
	if !strings.HasPrefix(path, prefix) {
		return ""
	}
	rest := strings.TrimPrefix(path, prefix)
	if i := strings.IndexByte(rest, '/'); i >= 0 {
		rest = rest[:i]
	}
	// "/custom/" alone, or a reserved sub-route, names no app.
	if rest == "" || rest == "pub" {
		return ""
	}
	return rest
}
