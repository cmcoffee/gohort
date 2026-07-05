package videodl

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// ytDlpAuthArgs returns yt-dlp cookie flags when the operator has configured a
// session, so login-gated sites can be fetched. Instagram now returns an empty
// media response to logged-out requests, so reels need this. Set
// GOHORT_YTDLP_COOKIES to a Netscape cookies.txt exported from a logged-in
// browser session, or GOHORT_YTDLP_COOKIES_FROM_BROWSER to a local browser name
// (e.g. "firefox") on desktop hosts. Empty when neither is set.
func ytDlpAuthArgs() []string {
	if path := strings.TrimSpace(os.Getenv("GOHORT_YTDLP_COOKIES")); path != "" {
		if _, err := os.Stat(path); err == nil {
			return []string{"--cookies", path}
		}
	}
	if b := strings.TrimSpace(os.Getenv("GOHORT_YTDLP_COOKIES_FROM_BROWSER")); b != "" {
		return []string{"--cookies-from-browser", b}
	}
	return nil
}

// needsAuth reports whether a yt-dlp stderr indicates the site refused an
// anonymous request and wants a logged-in session.
func needsAuth(stderr string) bool {
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

// downloadViaYtDlp fetches the video at url using yt-dlp into a temp file
// and returns the bytes. Caller is responsible for using the bytes
// (delivery vs. analysis) — this helper just handles the download dance
// and is shared by both download_video and view_video.
//
// Hard caps: 120s wall clock, 200 MB output.
func downloadViaYtDlp(url string) ([]byte, error) {
	if _, err := exec.LookPath("yt-dlp"); err != nil {
		return nil, fmt.Errorf("yt-dlp is not installed on this server. Install via `pip install yt-dlp` or download the static binary from https://github.com/yt-dlp/yt-dlp/releases")
	}
	dir, err := os.MkdirTemp("", "videodl-*")
	if err != nil {
		return nil, fmt.Errorf("create temp dir: %w", err)
	}
	defer os.RemoveAll(dir)

	outPattern := filepath.Join(dir, "video.%(ext)s")
	ctx, cancel := context.WithTimeout(context.Background(), downloadTimeout)
	defer cancel()

	args := []string{
		// Format cascade:
		//   b[ext=mp4]  — a single pre-merged mp4 file when the site has
		//                 one (YouTube, Twitter, Vimeo direct hosts).
		//                 Cheap: no ffmpeg merge step.
		//   bv*+ba      — best video stream + best audio stream, merged
		//                 client-side via ffmpeg. Required for DASH-only
		//                 sites (Reddit, modern Instagram, parts of TikTok)
		//                 where audio and video are NEVER pre-muxed; without
		//                 this branch yt-dlp errors with "Requested format is
		//                 not available."
		//   b           — final catch-all: best single stream of any kind.
		//                 Picks a video-only or audio-only stream on
		//                 sites that have no muxable pair; better than
		//                 failing the whole download.
		// --merge-output-format mp4 normalizes the DASH-merged container
		// to mp4 so downstream MIME detection + the "video.<ext>" output
		// glob both keep working as before.
		"-f", "b[ext=mp4]/bv*+ba/b",
		"--merge-output-format", "mp4",
		"-o", outPattern,
		"--no-playlist",
		"--no-warnings",
		"--no-progress",
		"--max-filesize", fmt.Sprintf("%d", downloadMaxBytes),
	}
	// Login-gated sites (Instagram now returns an empty media response to
	// logged-out requests) need a session; append cookies when the operator
	// has configured one. yt-dlp reads them right before the URL.
	args = append(args, ytDlpAuthArgs()...)
	args = append(args, url)
	cmd := exec.CommandContext(ctx, "yt-dlp", args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		if ctx.Err() == context.DeadlineExceeded {
			return nil, fmt.Errorf("download timed out after %s", downloadTimeout)
		}
		// The site refused an anonymous request and wants a login. Return an
		// actionable error (not the raw yt-dlp wall of text) so the agent can
		// tell the user the truth instead of guessing at the video's content.
		if needsAuth(msg) && len(ytDlpAuthArgs()) == 0 {
			return nil, fmt.Errorf("this video requires a logged-in session to download; the site (e.g. Instagram) blocks anonymous access. Set GOHORT_YTDLP_COOKIES to a cookies.txt exported from a logged-in browser session, then retry. Underlying error: %s", firstLine(msg))
		}
		return nil, fmt.Errorf("yt-dlp failed: %s", msg)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read output dir: %w", err)
	}
	var videoPath string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if strings.HasPrefix(e.Name(), "video.") {
			videoPath = filepath.Join(dir, e.Name())
			break
		}
	}
	if videoPath == "" {
		return nil, fmt.Errorf("yt-dlp produced no output file")
	}
	info, err := os.Stat(videoPath)
	if err != nil {
		return nil, err
	}
	if info.Size() > downloadMaxBytes {
		return nil, fmt.Errorf("downloaded video too large: %d bytes (cap %d)", info.Size(), downloadMaxBytes)
	}

	data, err := os.ReadFile(videoPath)
	if err != nil {
		return nil, fmt.Errorf("read output: %w", err)
	}
	return data, nil
}
