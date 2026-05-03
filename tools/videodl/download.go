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

	cmd := exec.CommandContext(ctx, "yt-dlp",
		"-f", "best[ext=mp4]/best",
		"-o", outPattern,
		"--no-playlist",
		"--no-warnings",
		"--no-progress",
		"--max-filesize", fmt.Sprintf("%d", downloadMaxBytes),
		url,
	)
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
