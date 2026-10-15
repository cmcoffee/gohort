package appscript

// gohort.notify: an app's action telling a person something.
//
// A scheduled action keeps an app's records fresh with nobody watching, which
// is only half of what such an app is for: the person also wants to hear when
// what it watches changes ("3 new cases", "the download finished"). notify
// files a notice in gohort's notifications, the same place an agent's news
// goes, and forwards it by email or to their phone when they asked for that
// (core.NoticeForwarder). Repeats of one title fold into one notice with a
// count, and only the first is forwarded, so a timer that says the same thing
// every hour does not become a stream of messages.
//
// It goes to the person the run is for: whoever pressed the button, or the
// app's owner for a scheduled run. Actions only: a data source runs on every
// page load, where a notice would be noise about the person's own looking.

import (
	"fmt"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/core/notices"
)

const (
	notifyTitleMax = 200
	notifyBodyMax  = 2000
	// notifyPerRun is the most notices one run may send: enough for a run
	// that found several things, few enough that a loop cannot flood anyone.
	notifyPerRun = 5
)

// notifier is the Notify for one run: recipient is who hears it, spec names
// the app it came from.
func notifier(recipient, owner, slug string) func(title, body string) (string, error) {
	sent := 0
	return func(title, body string) (string, error) {
		title = clipRunes(strings.TrimSpace(title), notifyTitleMax)
		body = clipRunes(strings.TrimSpace(body), notifyBodyMax)
		if title == "" {
			return "", fmt.Errorf("notify needs a title: what happened, in a line")
		}
		if sent >= notifyPerRun {
			return "", fmt.Errorf("notify: this run has already sent %d notices, the most one run may send; put the rest in one notice's body", notifyPerRun)
		}
		if RootDB == nil {
			return "", fmt.Errorf("notifications are not available here")
		}
		name := slug
		if spec, ok := LoadAppSpec(owner, slug); ok && strings.TrimSpace(spec.Name) != "" {
			name = spec.Name
		}
		link := "/apps/" + slug + "/"
		if base := strings.TrimRight(DashboardURL(), "/"); base != "" {
			link = base + link
		}
		full := strings.TrimSpace(body + "\n\nFrom the app " + name + ": " + link)
		stored, first := notices.Record(RootDB, notices.Notice{
			Owner: recipient, About: "app:" + slug, Kind: notices.KindReport, Title: title, Body: full,
		})
		sent++
		if first && NoticeForwarder != nil {
			go NoticeForwarder(recipient, name, title, full)
		}
		if !first {
			return fmt.Sprintf("noted again (%d times now; only the first is forwarded)", stored.Count), nil
		}
		return "sent", nil
	}
}

func clipRunes(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}
