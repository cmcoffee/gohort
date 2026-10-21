package flows

// Video downloads: a link in, the video out. The one job that needs the
// network, so its flows run in the sandbox WITH the host's network (and only
// it: every other job has none), and the link is checked before any flow
// runs, native or adapted: a public http(s) address only (URLGuard).
//
// The native flow is yt-dlp, run as tools/videodl always ran it: a format
// cascade that prefers a single mp4 and merges DASH streams where a site has
// no muxed one, one video (no playlists), 200 MB at most, two minutes. A
// login-gated site needs a session: a cookies.txt the operator set
// (GOHORT_YTDLP_COOKIES) is copied into the run's folder, because yt-dlp
// writes the jar back when it finishes and the original is not the sandbox's
// to change.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/cmcoffee/gohort/core/deps"
)

// URLGuard refuses a link that is not a public web address. core sets it
// (core.RefuseNonPublicHost); unset, every link is refused, which is the
// safe way to be wrong.
var URLGuard func(string) error

// The download's bounds, as tools/videodl had them.
const (
	downloadTimeout  = 2 * time.Minute
	downloadMaxBytes = 200 << 20
)

func checkURL(v string) error {
	if URLGuard == nil {
		return errors.New("links cannot be checked here")
	}
	return URLGuard(v)
}

func init() {
	jobs = append(jobs, Job{
		ID: "media.download", Group: GroupMedia, Title: "Video downloads",
		UsedFor: "fetching a video from a link (Instagram, TikTok, YouTube, X, Reddit) to watch or transcribe",
		Becomes: KindVideo, Network: true,
		Params:  []Param{{Name: "url", Help: "the link to the video", Check: checkURL}},
		Builtin: true, Uses: []string{"yt-dlp"}, timeout: downloadTimeout + 30*time.Second,
		run: downloadNative,
	})
}

// downloadNative is yt-dlp in the box.
func downloadNative(ctx context.Context, b *Box, in Input) ([]byte, [][]byte, error) {
	url := strings.TrimSpace(in.Params["url"])
	args := []string{
		// A single pre-merged mp4 where the site has one; else the best video
		// and audio merged (DASH-only sites: Reddit, Instagram, parts of
		// TikTok, which never pre-mux); else the best single stream.
		"-f", "b[ext=mp4]/bv*+ba/b",
		"--merge-output-format", "mp4",
		"-o", "video.%(ext)s",
		"--no-playlist",
		"--no-warnings",
		"--no-progress",
		"--no-cache-dir",
		"--max-filesize", fmt.Sprintf("%d", downloadMaxBytes),
	}
	cookies, err := copyCookies(b)
	if err != nil {
		return nil, nil, err
	}
	args = append(args, cookies...)
	// Nothing after "--" is an option, whatever it starts with.
	args = append(args, "--", url)
	if _, err := b.Run(ctx, downloadTimeout, "yt-dlp", args...); err != nil {
		msg := err.Error()
		if needsLogin(msg) && len(cookies) == 0 {
			return nil, nil, fmt.Errorf("this video needs a logged-in session to download; the site (e.g. Instagram) blocks anonymous access. Set GOHORT_YTDLP_COOKIES to a cookies.txt exported from a logged-in browser session, then retry. Underlying error: %s", firstLine(msg))
		}
		return nil, nil, fmt.Errorf("%s%s", msg, deps.StaleNote("yt-dlp"))
	}
	entries, err := os.ReadDir(b.Dir())
	if err != nil {
		return nil, nil, err
	}
	for _, e := range entries {
		if !e.IsDir() && strings.HasPrefix(e.Name(), "video.") && !strings.HasSuffix(e.Name(), ".part") {
			path := filepath.Join(b.Dir(), e.Name())
			if st, err := os.Stat(path); err == nil && st.Size() > downloadMaxBytes {
				return nil, nil, fmt.Errorf("the video is %d MB, over the %d MB cap", st.Size()>>20, downloadMaxBytes>>20)
			}
			data, err := os.ReadFile(path)
			return data, nil, err
		}
	}
	return nil, nil, fmt.Errorf("yt-dlp produced no file%s", deps.StaleNote("yt-dlp"))
}

// copyCookies puts the operator's cookies.txt in the box, and returns the
// flags that use it. A browser to read cookies from
// (GOHORT_YTDLP_COOKIES_FROM_BROWSER) cannot work from the sandbox, which
// does not see the browser's profile, so it is not passed.
func copyCookies(b *Box) ([]string, error) {
	path := strings.TrimSpace(os.Getenv("GOHORT_YTDLP_COOKIES"))
	if path == "" {
		return nil, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil // not there: run without, as before
	}
	if err := os.WriteFile(filepath.Join(b.Dir(), "cookies.txt"), data, 0o600); err != nil {
		return nil, err
	}
	return []string{"--cookies", "cookies.txt"}, nil
}

// needsLogin reports whether yt-dlp's complaint means the site refused an
// anonymous request and wants a logged-in session.
func needsLogin(stderr string) bool {
	s := strings.ToLower(stderr)
	return strings.Contains(s, "empty media response") ||
		strings.Contains(s, "use --cookies") ||
		strings.Contains(s, "log in") ||
		strings.Contains(s, "login required") ||
		strings.Contains(s, "rate-limit") ||
		strings.Contains(s, "requested content is not available")
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return strings.TrimSpace(s)
}
