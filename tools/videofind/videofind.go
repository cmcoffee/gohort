// Package videofind provides the LLM-facing `find_video` tool —
// search the web for a video URL, return the best candidate from
// yt-dlp-supported platforms. Mirrors find_image's shape for stills.
//
// Separates the "find a URL" job from download_video's "fetch a known
// URL" job. Without this tool, the LLM burns 3-5 rounds (web_search,
// fetch_url to inspect a page, browse_page to find an embed, etc.)
// before it can call download_video. find_video collapses that to one
// call returning a URL the LLM can pass straight through.

package videofind

import (
	"fmt"
	"net/url"
	"sort"
	"strings"

	. "github.com/cmcoffee/gohort/core"
)

// FindVideoTool's standalone registration is dropped — surfaces via
// the grouped `video` tool. Type + Run stay public for dispatch.

// FindVideoTool wraps web_search with video-platform filtering.
type FindVideoTool struct{}

func (t *FindVideoTool) Name() string { return "find_video" }

func (t *FindVideoTool) Caps() []Capability {
	return []Capability{CapNetwork, CapRead}
}

// IsInternetTool: hits web_search via outbound HTTP. Hidden in
// Private mode, same as the other network-cap tools.
func (t *FindVideoTool) IsInternetTool() bool { return true }

func (t *FindVideoTool) Desc() string {
	return "Find a video URL by topic via web search. Returns the best candidate URL from yt-dlp-supported platforms (YouTube, TikTok, Vimeo, Twitter/X, Reddit, Instagram, Twitch, etc.) — the LLM then passes it to download_video. " +
		"Use when the user describes a video they want to find (\"that skater sunset clip\", \"the bear cooking demo\") and you need a URL. " +
		"Do NOT call when the user already provided a URL — pass it straight to download_video. " +
		"Returns title + source + URL; the LLM picks which to download next."
}

func (t *FindVideoTool) Params() map[string]ToolParam {
	return map[string]ToolParam{
		"query": {Type: "string", Description: "Description of the video to find (topic, creator, distinctive details). Phrase like you'd phrase a search query."},
		"prefer": {Type: "string", Description: "Optional platform hint: youtube | tiktok | vimeo | twitter | reddit | instagram | twitch | facebook. Biases the search toward that site."},
		"count": {Type: "integer", Description: "Optional. Number of candidates to return (1-5, default 1). Use >1 when you're uncertain which URL fits the user's request."},
	}
}

func (t *FindVideoTool) Run(args map[string]any) (string, error) {
	query := strings.TrimSpace(StringArg(args, "query"))
	if query == "" {
		return "", fmt.Errorf("query is required")
	}
	if !WebSearchAvailable() {
		return "", fmt.Errorf("web search is not configured for this deployment")
	}
	prefer := strings.ToLower(strings.TrimSpace(StringArg(args, "prefer")))
	count := 1
	if v, ok := args["count"].(float64); ok && v > 0 {
		count = int(v)
		if count > 5 {
			count = 5
		}
	}

	// Bias the search toward video platforms. If the user specified
	// a preferred platform, narrow to that one; otherwise scope
	// across the supported set via an OR'd site filter.
	searchQuery := query
	if prefer != "" {
		if site := platformSite(prefer); site != "" {
			searchQuery = query + " site:" + site
		}
	} else {
		searchQuery = query + " (site:youtube.com OR site:tiktok.com OR site:vimeo.com OR site:twitter.com OR site:x.com OR site:reddit.com OR site:instagram.com OR site:twitch.tv)"
	}

	raw := WebSearch(searchQuery)
	if raw == "" {
		return "", fmt.Errorf("no search results")
	}
	candidates := parseVideoCandidates(raw)
	if len(candidates) == 0 {
		return "", fmt.Errorf("no video candidates found in search results — try a more specific query or set `prefer` to a platform")
	}

	// Score by platform priority + URL specificity. Sort stably so
	// search ranking acts as a tiebreaker when scores are equal.
	sort.SliceStable(candidates, func(i, j int) bool {
		return scoreVideoURL(candidates[i].URL) > scoreVideoURL(candidates[j].URL)
	})
	if count > len(candidates) {
		count = len(candidates)
	}
	candidates = candidates[:count]

	Log("[find_video] query=%q prefer=%q candidates=%d top=%s",
		query, prefer, len(candidates), candidates[0].URL)

	if count == 1 {
		c := candidates[0]
		return fmt.Sprintf(
			"Found: %s\nTitle: %s\nSource: %s\n\nPass this URL to download_video to fetch the file.",
			c.URL, c.Title, c.Source,
		), nil
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "Found %d candidates:\n\n", len(candidates))
	for i, c := range candidates {
		fmt.Fprintf(&sb, "%d. %s\n   Title: %s\n   Source: %s\n\n", i+1, c.URL, c.Title, c.Source)
	}
	sb.WriteString("Pick the best fit and pass its URL to download_video.")
	return sb.String(), nil
}

// videoCandidate is one parsed search-result row.
type videoCandidate struct {
	URL    string
	Title  string
	Source string // hostname
}

// parseVideoCandidates pulls title+URL pairs out of WebSearch's
// blank-line-separated "N. Title\n   URL\n   Snippet" format,
// keeping only URLs that look like video platform watch/clip pages.
func parseVideoCandidates(raw string) []videoCandidate {
	var out []videoCandidate
	seen := map[string]bool{}
	blocks := strings.Split(raw, "\n\n")
	for _, block := range blocks {
		lines := strings.Split(strings.TrimSpace(block), "\n")
		if len(lines) < 2 {
			continue
		}
		title := strings.TrimSpace(lines[0])
		// Strip the "N. " numbering prefix websearch prepends.
		if dot := strings.Index(title, ". "); dot >= 0 && dot < 4 {
			title = strings.TrimSpace(title[dot+2:])
		}
		var urlStr string
		for _, ln := range lines[1:] {
			ln = strings.TrimSpace(ln)
			if strings.HasPrefix(ln, "http://") || strings.HasPrefix(ln, "https://") {
				urlStr = ln
				break
			}
		}
		if urlStr == "" || seen[urlStr] {
			continue
		}
		if !isVideoPlatformURL(urlStr) {
			continue
		}
		u, err := url.Parse(urlStr)
		if err != nil {
			continue
		}
		seen[urlStr] = true
		out = append(out, videoCandidate{
			URL:    urlStr,
			Title:  title,
			Source: u.Hostname(),
		})
	}
	return out
}

// isVideoPlatformURL filters search results down to URLs yt-dlp is
// known to handle. The list isn't exhaustive (yt-dlp supports
// ~1000 sites) but covers the high-recall platforms; obscure
// sites can still be passed to download_video directly by the LLM.
func isVideoPlatformURL(u string) bool {
	lower := strings.ToLower(u)
	for _, marker := range []string{
		"youtube.com/watch", "youtube.com/shorts/", "youtu.be/",
		"tiktok.com/", "vm.tiktok.com/",
		"vimeo.com/",
		"twitter.com/", "x.com/",
		"reddit.com/r/", "v.redd.it/",
		"instagram.com/p/", "instagram.com/reel/", "instagram.com/tv/",
		"twitch.tv/videos/", "twitch.tv/clip/", "clips.twitch.tv/",
		"facebook.com/watch", "facebook.com/reel/",
		"streamable.com/",
		"dailymotion.com/video/",
	} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

// scoreVideoURL biases ordering toward platforms with the most
// reliable yt-dlp extractors AND toward specific watch URLs over
// channel/profile/index pages. Higher = surfaced first.
func scoreVideoURL(u string) int {
	lower := strings.ToLower(u)
	score := 0
	switch {
	case strings.Contains(lower, "youtube.com/watch") || strings.Contains(lower, "youtu.be/"):
		score += 100
	case strings.Contains(lower, "youtube.com/shorts/"):
		score += 95
	case strings.Contains(lower, "vimeo.com/"):
		score += 80
	case strings.Contains(lower, "tiktok.com/") || strings.Contains(lower, "vm.tiktok.com/"):
		score += 75
	case strings.Contains(lower, "twitter.com/") || strings.Contains(lower, "x.com/"):
		score += 60
	case strings.Contains(lower, "instagram.com/"):
		score += 55
	case strings.Contains(lower, "reddit.com/r/") || strings.Contains(lower, "v.redd.it/"):
		score += 50
	case strings.Contains(lower, "twitch.tv/"):
		score += 50
	case strings.Contains(lower, "streamable.com/") || strings.Contains(lower, "dailymotion.com/"):
		score += 40
	default:
		score += 30
	}
	// Specific clip/watch URL patterns beat generic channel pages.
	if strings.Contains(lower, "/watch") ||
		strings.Contains(lower, "/shorts/") ||
		strings.Contains(lower, "/p/") ||
		strings.Contains(lower, "/reel/") ||
		strings.Contains(lower, "/status/") ||
		strings.Contains(lower, "/video/") ||
		strings.Contains(lower, "/clip/") {
		score += 10
	}
	return score
}

// platformSite maps a user-supplied prefer hint to a site: operator
// for the search query. Empty result means "no platform filter."
func platformSite(prefer string) string {
	switch prefer {
	case "youtube", "yt":
		return "youtube.com"
	case "tiktok":
		return "tiktok.com"
	case "vimeo":
		return "vimeo.com"
	case "twitter", "x":
		// Cover both legacy and rebrand domains in one filter.
		return "twitter.com OR site:x.com"
	case "reddit":
		return "reddit.com"
	case "instagram", "ig":
		return "instagram.com"
	case "twitch":
		return "twitch.tv"
	case "facebook", "fb":
		return "facebook.com"
	case "streamable":
		return "streamable.com"
	case "dailymotion":
		return "dailymotion.com"
	}
	return ""
}
