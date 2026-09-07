package imagefetch

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html"
	_ "image/gif"
	_ "image/png"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"

	. "github.com/cmcoffee/gohort/core"
	_ "golang.org/x/image/webp" // register the WebP decoder for image.Decode
)

// --- Serper image search ---

type SerperImageResult struct {
	ImageURL string
	Title    string
	Source   string
	Link     string
}

type serperImageResponse struct {
	Images []struct {
		ImageURL string `json:"imageUrl"`
		Title    string `json:"title"`
		Source   string `json:"source"`
		Link     string `json:"link"`
	} `json:"images"`
}

func SerperImageSearch(query, apiKey string) ([]SerperImageResult, error) {
	payload, _ := json.Marshal(map[string]any{"q": query, "num": 10})
	req, err := http.NewRequest("POST", "https://google.serper.dev/images", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-API-KEY", apiKey)
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("serper images API error (%d): %s", resp.StatusCode, string(body))
	}

	var result serperImageResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("parsing serper images response: %w", err)
	}

	var out []SerperImageResult
	for _, img := range result.Images {
		if img.ImageURL != "" {
			out = append(out, SerperImageResult{
				ImageURL: img.ImageURL,
				Title:    img.Title,
				Source:   img.Source,
				Link:     img.Link,
			})
		}
	}
	// Count every provider call — Serper bills per request regardless
	// of how many images come back. Symmetric with web_search's
	// "count on call, not on result-content" semantics.
	ProcessUsage().AddSearchCall()
	return out, nil
}

// --- HTTP helpers ---

const browserUA = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36"

func FetchImageBytes(rawURL, referer string, timeoutSecs int) ([]byte, error) {
	req, err := http.NewRequest("GET", rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("invalid URL: %w", err)
	}
	req.Header.Set("User-Agent", browserUA)
	req.Header.Set("Accept", "image/avif,image/webp,image/apng,image/*,*/*;q=0.8")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	if referer != "" {
		req.Header.Set("Referer", referer)
	}
	client := &http.Client{Timeout: time.Duration(timeoutSecs) * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("download failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download failed: HTTP %d", resp.StatusCode)
	}
	const maxBytes = 10 * 1024 * 1024
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes))
	if err != nil {
		return nil, fmt.Errorf("read failed: %w", err)
	}
	if len(data) == 0 {
		return nil, fmt.Errorf("empty response")
	}
	return data, nil
}

// pageImageRes matches a page's representative image in its <head> meta
// tags — og:image (either attribute order) and twitter:image.
var pageImageRes = []*regexp.Regexp{
	regexp.MustCompile(`(?i)<meta[^>]+property=["']og:image(?::url)?["'][^>]+content=["']([^"']+)["']`),
	regexp.MustCompile(`(?i)<meta[^>]+content=["']([^"']+)["'][^>]+property=["']og:image(?::url)?["']`),
	regexp.MustCompile(`(?i)<meta[^>]+name=["']twitter:image(?::src)?["'][^>]+content=["']([^"']+)["']`),
}

// inspectPage fetches a source page ONCE and reports (a) its representative
// image URL (og:image / twitter:image, resolved absolute) and (b) whether
// the page actually MENTIONS the search subject. find_image uses both: grab
// the page's real image instead of the cached/thumbnail result image, and
// trust it only when the page is genuinely about what we searched for — the
// drill-in-and-verify step that discards mis-indexed / wrong results.
// Returns ("", false) if the page can't be fetched.
func inspectPage(pageURL, query string) (ogImage string, mentions bool) {
	if pageURL == "" {
		return "", false
	}
	req, err := http.NewRequest("GET", pageURL, nil)
	if err != nil {
		return "", false
	}
	req.Header.Set("User-Agent", browserUA)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,*/*;q=0.8")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	client := &http.Client{Timeout: 12 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", false
	}
	const maxHTML = 1 << 20 // 1 MB reaches the <head> metas + visible text on any sane page
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxHTML))
	if err != nil {
		return "", false
	}
	page := string(body)
	mentions = pageMentionsSubject(strings.ToLower(page), query)
	for _, re := range pageImageRes {
		if m := re.FindStringSubmatch(page); len(m) > 1 && strings.TrimSpace(m[1]) != "" {
			if abs := resolvePageURL(pageURL, html.UnescapeString(strings.TrimSpace(m[1]))); abs != "" {
				ogImage = abs
				break
			}
		}
	}
	return ogImage, mentions
}

// imageQueryFiller is generic query noise that shouldn't be required to
// appear on a source page (a page about a red Ferrari needn't say "photo").
var imageQueryFiller = map[string]bool{
	"the": true, "and": true, "for": true, "with": true, "from": true,
	"photo": true, "photos": true, "image": true, "images": true,
	"picture": true, "pictures": true, "pic": true, "pics": true,
	"png": true, "jpg": true, "jpeg": true, "gif": true,
}

// significantQueryWords reduces a query to the tokens worth matching on a
// page: alphanumeric, length >= 3, minus generic filler.
func significantQueryWords(query string) []string {
	var out []string
	for _, w := range strings.FieldsFunc(strings.ToLower(query), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}) {
		if len(w) >= 3 && !imageQueryFiller[w] {
			out = append(out, w)
		}
	}
	return out
}

// namedSubjectTokens returns the PROPER-NAME words a query is about — the ones
// no amount of looking at pixels can confirm.
//
// "Shazz Barbaric real estate" is two questions, not one. A vision model can
// answer the second (is this a real-estate person? yes) and has no way to touch
// the first, so it scores the half it can and returns a confident number for
// the whole thing. Production, one search, five candidates: 95, 95, 95, 85, 75
// — five different people, none of them him, every score a pass. Another run
// returned a 19th-century cigarette card of the Sultan of Zanzibar, matched
// through "Savage and Semi-Barbarous Chiefs", at a passing grade.
//
// Capitalization in the query is the signal, and it degrades the right way. A
// real subject's page says its own name — "golden gate bridge" appears on any
// page about the bridge — so requiring the name costs a genuine match nothing.
// A person who is not on the page cannot be rescued by the page being about
// real estate in general, which is exactly the substitution that was happening.
func namedSubjectTokens(query string) []string {
	var out []string
	for _, w := range strings.FieldsFunc(query, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}) {
		r := []rune(w)
		if len(r) < 3 || !unicode.IsUpper(r[0]) {
			continue
		}
		lower := strings.ToLower(w)
		if imageQueryFiller[lower] {
			continue
		}
		out = append(out, lower)
	}
	return out
}

// pageMentionsSubject reports whether a page (already lowercased) references
// the search subject.
//
// Two tiers, because they answer different questions. Proper names are
// REQUIRED: they are what makes the request about one thing rather than a
// category, and counting them as one token among many is how a page that never
// says "Shazz" text-matched on "real estate ranch owner Texas" — five of eight
// tokens, a comfortable 60%, name absent. Everything else keeps the majority
// rule, which is right for descriptive queries where any given adjective may
// simply not appear in the prose.
//
// An uncheckable query (no significant words) passes so it never blocks a
// result.
func pageMentionsSubject(pageLower, query string) bool {
	for _, name := range namedSubjectTokens(query) {
		if !strings.Contains(pageLower, name) {
			return false
		}
	}
	toks := significantQueryWords(query)
	if len(toks) == 0 {
		return true
	}
	need := len(toks)
	if need > 3 {
		need = (len(toks)*3 + 4) / 5 // ~60%, rounded up
	}
	hit := 0
	for _, t := range toks {
		if strings.Contains(pageLower, t) {
			hit++
		}
	}
	return hit >= need
}

// resolvePageURL resolves a possibly-relative image ref against the page it
// came from, keeping only http(s) results.
func resolvePageURL(base, ref string) string {
	b, err := url.Parse(base)
	if err != nil {
		return ""
	}
	r, err := url.Parse(ref)
	if err != nil {
		return ""
	}
	abs := b.ResolveReference(r)
	if abs.Scheme != "http" && abs.Scheme != "https" {
		return ""
	}
	return abs.String()
}

// imageMatchThreshold is the minimum 0-100 vision score for find_image to
// accept a candidate. Below it, the tool reports no confident match rather
// than returning a wrong image. Tunable.
const imageMatchThreshold = 50

// scoreScreenBlind is what the screen reports when its own answer shows it
// never saw the picture — a model with no image modality, or an endpoint that
// dropped the attachment.
//
// It has to be distinct from both a real 0 and a missing rating. A blind model
// asked to rate how well an image depicts "house" says it cannot see any image
// and then, obediently, answers 0 — which is indistinguishable from a genuine
// rejection and is scored as one. Every candidate then "fails", and the tool
// reports "none clearly depict it (best visual match 0/100) — refine the query",
// sending the caller to reword a query that was never the problem. Below -1 so
// the existing `score >= 0` (rated) and `score > bestScore` (better) tests both
// treat it as an abstention with no further changes.
const scoreScreenBlind = -2

// scoreImageMatch asks the vision LLM to actually LOOK at ONE image and rate
// 0-100 how well it depicts the query. Forcing a one-line description first
// makes the model examine the pixels instead of guessing from metadata or
// from a confusing multi-image prompt — and doubles as the tell that it saw
// nothing at all, which is why the description is read and not just the number.
// Returns -1 if no usable score came back, scoreScreenBlind if the screen never
// saw the picture.
func scoreImageMatch(sess *ToolSession, img []byte, query string) int {
	prompt := fmt.Sprintf(
		"Look closely at this image. In one sentence, describe what it ACTUALLY shows. "+
			"Then rate from 0 to 100 how well it depicts: %q "+
			"(0 = unrelated or the wrong subject, 100 = exactly that subject). "+
			"Put the rating as a plain number on its own FINAL line.", query)
	resp, err := sess.LLM.Chat(sess.Context(),
		[]Message{{Role: "user", Content: prompt, Images: [][]byte{img}}},
		WithCaller("imagefetch/find_image"),
		WithMaxRetries(0),
		WithThink(true),
	)
	if err != nil || resp == nil {
		return -1
	}
	if ModelSawNoImage(resp.Content) {
		Log("[imagefetch/find_image] the vision screen answered without seeing the image: %q",
			truncate(strings.TrimSpace(resp.Content), 160))
		return scoreScreenBlind
	}
	return parseTrailingScore(resp.Content)
}

// parseTrailingScore extracts the last integer in [0,100] from the content
// (the model is asked to end with the rating on its own line).
func parseTrailingScore(s string) int {
	fields := strings.Fields(s)
	for i := len(fields) - 1; i >= 0; i-- {
		tok := strings.Trim(fields[i], ".,;:%\"'()[]")
		if n, err := strconv.Atoi(tok); err == nil && n >= 0 && n <= 100 {
			return n
		}
	}
	return -1
}
