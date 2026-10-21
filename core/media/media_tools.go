package media

// How the media functions run ffmpeg and ffprobe, and the jobs that take
// them over.
//
// Frame sampling, video metadata, a video's sound and audio prepared for
// transcription are jobs in core/flows: each has these functions as its
// native flow, run there through a sandboxed folder (flows' Tools), and an
// admin can adapt one to another tool. flows sets the hooks below when it
// loads. Without it (this package's own tests), the functions run their
// tools here, in a temporary folder, as they always did.

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"time"
)

// Tools runs a media tool for one call: Run starts bin with args in Dir,
// the folder the call's files are in (named relative to it), and returns
// what it wrote to stdout.
type Tools interface {
	Dir() string
	Run(ctx context.Context, limit time.Duration, bin string, args ...string) ([]byte, error)
}

// The jobs that take these functions over, set by core/flows.
var (
	FramesHook     func(data []byte, count int) ([][]byte, error)
	MetadataHook   func(data []byte) (string, error)
	AudioHook      func(data []byte) ([]byte, error)
	WAVHook        func(data []byte) ([]byte, error)
	TranscribeHook func(ctx context.Context, audio []byte, filename string) (string, error)
)

// mediaInput is what a call names the file it was given. ffmpeg probes the
// content, so its extension does not matter.
const mediaInput = "in.media"

// writeInput writes data as mediaInput in t's folder.
func writeInput(t Tools, data []byte) error {
	if len(data) == 0 {
		return os.ErrInvalid
	}
	return os.WriteFile(filepath.Join(t.Dir(), mediaInput), data, 0o600)
}

// localTools runs the tools here, directly, in a folder of their own.
type localTools struct{ dir string }

func (l localTools) Dir() string { return l.dir }

func (l localTools) Run(ctx context.Context, limit time.Duration, bin string, args ...string) ([]byte, error) {
	cmd, cancel := ffmpegCommand(limit, bin, args...)
	defer cancel()
	cmd.Dir = l.dir
	var out bytes.Buffer
	cmd.Stdout = &out
	err := cmd.Run()
	return out.Bytes(), err
}

// withLocalTools runs fn with a fresh folder of its own, removed after.
func withLocalTools(fn func(Tools) error) error {
	dir, err := os.MkdirTemp("", "gohort-media-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	return fn(localTools{dir: dir})
}
