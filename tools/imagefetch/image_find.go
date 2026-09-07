package imagefetch

import (
	"fmt"
	_ "image/gif"
	_ "image/png"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	. "github.com/cmcoffee/gohort/core"
	"github.com/cmcoffee/gohort/tools/browser"
	_ "golang.org/x/image/webp" // register the WebP decoder for image.Decode
)

// --- FetchImageTool ---

type FetchImageTool struct{}

func (t *FetchImageTool) Name() string { return "fetch_image" }

func (t *FetchImageTool) Caps() []Capability { return []Capability{CapNetwork, CapRead} }

// HTTP GET image
func (t *FetchImageTool) Desc() string {
	return "Download an image from a URL into your session workspace. Returns the saved path. Does NOT deliver — call workspace(action=\"attach\", path=..., cleanup=true) to ship the file. Use this after finding an image URL via web_search, or whenever you already have a specific image URL the user wants."
}

func (t *FetchImageTool) Params() map[string]ToolParam {
	return map[string]ToolParam{
		"url": {Type: "string", Description: "Direct URL of the image to download (must resolve to an image file: jpg, png, gif, webp, etc.)."},
	}
}

func (t *FetchImageTool) IsInternetTool() bool { return true }

func (t *FetchImageTool) Run(args map[string]any) (string, error) {
	return "", fmt.Errorf("fetch_image requires a session context — use GetAgentToolsWithSession")
}

func (t *FetchImageTool) RunWithSession(args map[string]any, sess *ToolSession) (string, error) {
	rawURL := StringArg(args, "url")
	if rawURL == "" {
		return "", fmt.Errorf("url is required")
	}
	return downloadImageTo(rawURL, sess)
}

// --- FindImageTool ---

type FindImageTool struct{}

func (t *FindImageTool) Name() string { return "find_image" }

func (t *FindImageTool) Caps() []Capability { return []Capability{CapNetwork, CapRead} }

// search + download
func (t *FindImageTool) Desc() string {
	return "Search for an image by description and save the SINGLE BEST MATCH into your session workspace. The framework's internal vision-LLM picks the best candidate from multiple search results. Returns the saved path. Does NOT deliver to the user — call workspace(action=\"attach\", path=..., cleanup=true) to ship the file. Use this whenever the user asks for a picture, meme, GIF, or photo."
}

func (t *FindImageTool) Params() map[string]ToolParam {
	return map[string]ToolParam{
		"query": {Type: "string", Description: "Description of the image to find (e.g. 'funny cat meme', 'golden gate bridge sunset', 'surprised pikachu')."},
	}
}

func (t *FindImageTool) IsInternetTool() bool { return true }

func (t *FindImageTool) Run(args map[string]any) (string, error) {
	return "", fmt.Errorf("find_image requires a session context — use GetAgentToolsWithSession")
}

func (t *FindImageTool) RunWithSession(args map[string]any, sess *ToolSession) (string, error) {
	query := StringArg(args, "query")
	if query == "" {
		return "", fmt.Errorf("query is required")
	}
	cfg := LoadWebSearchConfig()
	if cfg.Provider != "serper" || cfg.APIKey == "" {
		return "", fmt.Errorf("find_image requires the serper search provider with an API key configured")
	}
	results, err := SerperImageSearch(query, cfg.APIKey)
	if err != nil {
		return "", fmt.Errorf("image search failed: %w", err)
	}
	if len(results) == 0 {
		return "", fmt.Errorf("no image results found for %q", query)
	}

	// Save the chosen image to the session workspace and return its path with
	// the standard delivery hint. No auto-attach; the LLM ships it via
	// workspace(action="attach", path=..., cleanup=true).
	saveAndReturn := func(data []byte, meta SerperImageResult) (string, error) {
		wsDir, err := EnsureSessionWorkspace(sess)
		if err != nil {
			return "", fmt.Errorf("session workspace unavailable: %w", err)
		}
		name := "find-" + shortID() + extForMime(http.DetectContentType(data))
		target := filepath.Join(wsDir, name)
		if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
			return "", fmt.Errorf("create parent dir: %w", err)
		}
		if err := os.WriteFile(target, data, 0600); err != nil {
			return "", fmt.Errorf("save image: %w", err)
		}
		Log("[imagefetch/find_image] query=%q delivered %q (title: %q, source: %s)", query, name, meta.Title, meta.Source)
		msg := fmt.Sprintf(
			"NOT sent yet — this only SAVED the image to your workspace as %q (title: %q, source: %s). It is NOT delivered, and your reply text alone will NOT include it. To actually send it you MUST call workspace(action=\"attach\", path=%q, cleanup=true) — do that BEFORE you write a reply claiming you sent it. Skip the attach ONLY if the user just wants info about it (describe / identify / summarize), not the image itself.",
			name, meta.Title, meta.Source, name,
		)
		// A found image belongs in the image space too. Without this, "find a
		// photo of a barn, now make it snowy" only worked while the filename was
		// still in context, and not at all on a later turn.
		if ref := RecordRecentImage(sess, data, "found: "+truncate(query, 60), ImageFromFound); ref != "" {
			msg += editHandleHint(name, ref)
		}
		msg += showToModel(sess, data, "the image the search returned", "It is a search result, not a verified answer")
		return msg, nil
	}

	// LAZY short-circuit: evaluate results ONE AT A TIME and stop at the first
	// that matches — no need to fetch + vision-score all five every time (the
	// vision call is the expensive part). The first candidate that BOTH
	// text-matches (page/title mentions the subject) AND visually depicts the
	// query is the answer; we only look deeper on a miss. A best-vision-match
	// fallback covers results whose title was too sparse to text-match but
	// whose image is right. Per candidate: one page fetch, one image, one
	// vision call — and usually just the first.
	const maxFindCandidates = 6
	// identityRequired: the query names a specific subject, so PROVENANCE — the
	// page actually being about it — is the only evidence available, and a
	// vision score cannot substitute for it no matter how high. See
	// namedSubjectTokens.
	//
	// This is also the escape hatch for a deployment whose model has no image
	// modality at all. Provenance is a TEXT check: it needs no vision, costs no
	// vision call, and is the one guarantee that survives when the screen is
	// absent or refuses to answer. Everything below treats vision as a bonus
	// on top of it rather than a prerequisite.
	identityRequired := len(namedSubjectTokens(query)) > 0
	var bestData []byte
	var bestMeta SerperImageResult
	bestScore := -1
	bestTextMatch := false
	usable := 0
	// scored counts candidates the vision screen actually RATED. A screen that
	// returns no number is not the same as one that rates everything zero, and
	// conflating them is how "a picture of <a named person>" became "could not
	// download any usable image": asking a vision model to confirm a specific
	// person's identity is the question it most often declines, so it answers
	// with prose and no trailing rating, parseTrailingScore returns -1 for every
	// candidate, and a search that fetched six perfectly good photographs
	// reports a download failure.
	scored := 0
	// blind counts candidates whose answer showed the screen never saw the
	// picture. Those are abstentions, not rejections — but they are worth
	// counting separately, because the repair is to fix the model rather than
	// to search again.
	blind := 0
	// fallback is the candidate to use when the screen abstains entirely —
	// preferring one whose page text matched, since that is the only signal
	// left at that point.
	var fallbackData []byte
	var fallbackMeta SerperImageResult
	fallbackTextMatch := false
	for _, r := range results {
		if usable >= maxFindCandidates {
			break
		}
		ogImage, pageMentions := inspectPage(r.Link, query)
		textMatch := pageMentions || pageMentionsSubject(strings.ToLower(r.Title), query)
		// Prefer the page's real image; fall back to the (accessible) result
		// image URL when the source blocks the direct fetch.
		var data []byte
		var ok bool
		if ogImage != "" && ogImage != r.ImageURL {
			data, _, _, ok = fetchValidImage(ogImage, r.Link)
		}
		if !ok {
			data, _, _, ok = fetchValidImage(r.ImageURL, r.Link)
		}
		if !ok {
			Log("[imagefetch/find_image] candidate skipped — no usable image for %q (source blocked?)", r.Link)
			continue
		}
		usable++
		if fallbackData == nil || (textMatch && !fallbackTextMatch) {
			fallbackData, fallbackMeta, fallbackTextMatch = data, r, textMatch
		}
		// No vision configured → can't screen the pixels; take the first
		// text-matching result (or the first usable one at all).
		//
		// Except when the query names someone: then provenance is not a
		// preference, it is the entire basis for believing this is the right
		// subject. Without vision AND without the page mentioning them, there
		// is no evidence at all — keep looking, and refuse below if none of the
		// candidates ever mentions them.
		if sess.LLM == nil {
			if textMatch || (bestScore < 0 && !identityRequired) {
				return saveAndReturn(data, r)
			}
			continue
		}
		score := scoreImageMatch(sess, data, query)
		if score >= 0 {
			scored++
		}
		if score == scoreScreenBlind {
			blind++
		}
		Log("[imagefetch/find_image] query=%q candidate %d (title %q) text=%v vision=%d/100", query, usable, r.Title, textMatch, score)
		if textMatch && score >= imageMatchThreshold {
			return saveAndReturn(data, r) // confident match — stop here
		}
		if score > bestScore {
			bestData, bestMeta, bestScore, bestTextMatch = data, r, score, textMatch
		}
		// Escalation: the page IS about the subject (text-matched) but the
		// cheap image — a blocked source that fell back to Google's thumbnail,
		// or a low-res cache — didn't pass vision. Render the page in the
		// headless browser to pull its REAL image (bypasses hotlink
		// protection) and re-score before abandoning this candidate. Getting
		// the right image HERE is cheaper than paying a fresh page-fetch +
		// vision call on the next candidate, so it's faster overall when it
		// converts a multi-candidate search into a one-candidate hit.
		// A blind screen is excluded: re-rendering the page to fetch a better
		// image only helps when something is judging the pixels, and paying for
		// a headless render per candidate to be told "I can't see it" again is
		// the most expensive way to learn nothing.
		if textMatch && score < imageMatchThreshold && score != scoreScreenBlind {
			if raw, rerr := browser.FetchPageImage(r.Link); rerr == nil {
				if rdata, _, _, rok := normalizeToJPEG(raw); rok {
					rscore := scoreImageMatch(sess, rdata, query)
					Log("[imagefetch/find_image] query=%q candidate %d browser-rendered image vision=%d/100 (cheap image was %d)", query, usable, rscore, score)
					if rscore >= imageMatchThreshold {
						return saveAndReturn(rdata, r)
					}
					if rscore > bestScore {
						bestData, bestMeta, bestScore, bestTextMatch = rdata, r, rscore, textMatch
					}
				}
			} else {
				Log("[imagefetch/find_image] query=%q browser render failed for %q: %v", query, r.Link, rerr)
			}
		}
	}
	// Nothing both text- and vision-matched. Use the best vision match if it's
	// a confident depiction; otherwise reject rather than return a wrong image.
	//
	// A NAMED subject never reaches here on pixels alone. This is the exit that
	// shipped four different realtors at 95/100 for one man's name, and the one
	// that has to say what it actually knows: that it found a picture of the
	// right KIND of thing and has no evidence it is the right one.
	if bestScore >= imageMatchThreshold && (!identityRequired || bestTextMatch) {
		Log("[imagefetch/find_image] query=%q no text+vision match; using best vision match %d/100", query, bestScore)
		return saveAndReturn(bestData, bestMeta)
	}
	if identityRequired && bestScore >= imageMatchThreshold && !bestTextMatch {
		Log("[imagefetch/find_image] query=%q REFUSED: best visual match %d/100 but no source page mentions %v", query, bestScore, namedSubjectTokens(query))
		return "", identityUnverifiableError(query, usable)
	}
	switch findOutcome(usable, scored, bestScore) {
	case findNoImages:
		return "", fmt.Errorf("could not download any usable image for %q (sources may be blocking the fetch)", query)
	case findScreenAbstained:
		// Images arrived; the screen just never rated any of them. A screen that
		// will not answer is, for this purpose, the same as not having one — so
		// fall back to the no-vision behaviour (first text-matching candidate)
		// rather than throwing away results it declined to judge.
		//
		// Which makes provenance load-bearing here: with nothing screening the
		// pixels, the page mentioning the subject is the ONLY evidence left,
		// and shipping an unscreened photo of a stranger is worse than saying
		// it wasn't found. Note the screen abstains most often on exactly these
		// queries — asking a model to confirm a specific person is the question
		// it declines — so this path and the identity case coincide constantly.
		if blind > 0 {
			Log("[imagefetch/find_image] query=%q the vision screen never saw the picture on %d of %d candidate(s) — the model in use appears to have no image modality; searches will run unscreened until it does", query, blind, usable)
		}
		if identityRequired && !fallbackTextMatch {
			Log("[imagefetch/find_image] query=%q REFUSED: vision screen abstained on all %d candidate(s) and none mentions %v", query, usable, namedSubjectTokens(query))
			return "", identityUnverifiableError(query, usable)
		}
		Log("[imagefetch/find_image] query=%q vision screen returned no rating for any of %d candidate(s) — delivering the best text match unscreened", query, usable)
		return saveAndReturn(fallbackData, fallbackMeta)
	}
	// Every candidate rated, every one of them exactly zero. A screen that is
	// really looking spreads its scores; a flat zero across several different
	// pictures is the signature of one that isn't seeing them and is answering
	// the question anyway. Say that, rather than telling the caller to reword a
	// query that was never the problem — this is the exit that answered "house"
	// with "none clearly depict it" while holding photographs of houses.
	if bestScore == 0 && scored > 1 {
		return "", fmt.Errorf("found %d image(s) for %q, and the vision screen rated every one of them 0/100 — "+
			"identical zeros across %d different pictures point at a screen that is not seeing them (a model with no image "+
			"modality) rather than that many genuinely wrong results. Do NOT just reword the query; use fetch_image with a "+
			"specific image URL, and tell the user the image screen looks misconfigured", usable, query, scored)
	}
	return "", fmt.Errorf("found image(s) for %q but none clearly depict it (best visual match %d/100) — the search may have surfaced lookalikes or unrelated results; refine the query, or use fetch_image with a specific image URL", query, bestScore)
}

// identityUnverifiableError is what a search for a specific person says when it
// found photographs of the right sort and nothing tying any of them to that
// person.
//
// It has to be explicit that this is not a near-miss to be retried with better
// wording, or the model simply searches again — that is what produced six
// searches in ninety seconds this morning, each delivering a different stranger
// with more confidence than the last. Rewording cannot fix "no page about this
// person carries this photo".
func identityUnverifiableError(query string, usable int) error {
	return fmt.Errorf("found %d photo(s) matching %q in general, but NONE from a page that mentions them by name — so there is no evidence any of these is the right person, only that they look like the sort of picture asked for. Do NOT retry with a reworded query; a face cannot be confirmed from pixels and rewording will just return a different stranger. Tell the user you couldn't find a picture of them, and ask for one (a photo, a link, a profile URL) if you need it", usable, query)
}

// findResolution is how a search that produced no confident match ends.
type findResolution int

const (
	findNoImages        findResolution = iota // nothing downloadable — the sources blocked us
	findScreenAbstained                       // images arrived, the vision screen rated none of them
	findAllRejected                           // the screen rated them and they genuinely do not match
)

// findOutcome separates three failures that used to be two. The middle one is
// the one that mattered: a vision screen asked to confirm a NAMED PERSON often
// declines to answer at all, which left every candidate unscored and reported
// as though nothing had downloaded — sending the caller to debug a network
// problem that never happened.
func findOutcome(usable, scored, bestScore int) findResolution {
	switch {
	case usable == 0:
		return findNoImages
	case scored == 0:
		return findScreenAbstained
	default:
		_ = bestScore
		return findAllRejected
	}
}
