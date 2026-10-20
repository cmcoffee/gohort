package scribe

// A publish is shown on the Monitor while it runs, with the dialog's Stop.
//
// Asked for from the Publish dialog or started by Keep published after the
// Curator changed a guide, it outlives the page that started it, and a
// publish of a long guide can take minutes. Keep published in particular runs
// with nobody watching, so without a row it was work nobody could see or stop.

import (
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

func init() { RegisterLiveProvider(publishLiveEntries) }

// publishLiveEntries is one row per publish still running.
func publishLiveEntries() []LiveEntry {
	publishJobsMu.Lock()
	defer publishJobsMu.Unlock()
	var out []LiveEntry
	for _, j := range publishJobs {
		if j.Done || j.guideID == "" {
			continue
		}
		label := "Publishing " + firstNonEmpty(j.title, "a guide")
		if j.background {
			label = "Keeping " + firstNonEmpty(j.title, "a guide") + " published"
		}
		if j.distilling {
			label = "Distilling " + firstNonEmpty(j.title, "a guide") + " into " + firstNonEmpty(j.Target, "another guide")
		} else if j.linking {
			label = "Linking " + firstNonEmpty(j.title, "a guide") + " to a page"
			if j.Target != "" {
				label += " in " + j.Target
			}
		} else if j.importing {
			label = "Importing a page"
			if j.Target != "" {
				label += " from " + j.Target
			}
		} else if j.Target != "" {
			label += " to " + j.Target
		}
		status := fmt.Sprintf("%ds", int(time.Since(j.Started).Seconds()))
		if j.Now != "" {
			status = j.Now + " · " + status
		}
		out = append(out, LiveEntry{
			ID:         "scribe-publish:" + j.guideID,
			Label:      label,
			Background: j.background,
			Status:     status,
			App:        "Scribe",
			Path:       "/scribe",
			URL:        "/scribe/",
			CancelURL:  cancelURL(j),
			Owner:      j.owner, // the title is the owner's words: masked for others
		})
	}
	sort.Slice(out, func(a, b int) bool { return out[a].ID < out[b].ID })
	return out
}

// cancelURL is where the row's Stop posts: the guide's publish, or the import.
func cancelURL(j *publishJob) string {
	if j.importing {
		return "/scribe/import/page/stop?job=" + url.QueryEscape(strings.TrimPrefix(j.guideID, importJobPrefix))
	}
	if j.distilling {
		return "/scribe/distill/stop?id=" + url.QueryEscape(strings.TrimPrefix(j.guideID, distillJobPrefix))
	}
	return "/scribe/publish/stop?id=" + url.QueryEscape(j.guideID)
}
