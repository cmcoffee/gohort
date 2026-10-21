package flows

// The media jobs: what gohort does with video and audio. Each has the code
// that always did it (core/media) as its native flow, now run through a Box
// (a sandboxed folder of its own, no network), and an admin can adapt one to
// another tool: a different frame grabber, a local whisper.cpp for
// transcription where no speech service is set up.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/cmcoffee/gohort/core/media"
	"github.com/cmcoffee/gohort/core/netgate"
	"github.com/cmcoffee/gohort/core/sandbox"
)

// The formats the media jobs take.
var (
	videoFormats = []string{"video/mp4", "video/quicktime", "video/webm", "video/x-matroska", "video/avi"}
	audioFormats = []string{"audio/mpeg", "audio/wave", "audio/mp4", "application/ogg", "audio/flac", "audio/aac"}
)

// transcribeReady reports whether a speech-to-text service is set up.
func transcribeReady() bool {
	cfg := media.GetTranscribeConfig()
	return cfg.Enabled && strings.TrimSpace(cfg.Endpoint) != ""
}

func init() {
	jobs = append(jobs,
		Job{
			ID: "media.frames", Group: GroupMedia, Title: "Video frame sampling",
			UsedFor: "videos you attach or download, so a model sees stills from them",
			Takes:   videoFormats, Becomes: KindImage, Files: true,
			Params:  []Param{{Name: "count", Help: "how many frames, spread across the video", Default: "4"}},
			Builtin: true, Uses: []string{"ffprobe", "ffmpeg"}, timeout: 10 * time.Minute,
			run: func(ctx context.Context, b *Box, in Input) ([]byte, [][]byte, error) {
				n, _ := strconv.Atoi(in.Params["count"])
				frames, err := media.SampleFrames(ctx, b, in.Data, n)
				return nil, frames, err
			},
		},
		Job{
			ID: "media.metadata", Group: GroupMedia, Title: "Video details (length, size, camera, place)",
			UsedFor: "videos you attach or download, so a model knows what it is looking at",
			Takes:   videoFormats, Becomes: KindText,
			Builtin: true, Uses: []string{"ffprobe"}, timeout: 2 * time.Minute,
			run: func(ctx context.Context, b *Box, in Input) ([]byte, [][]byte, error) {
				s, err := media.ProbeMetadata(ctx, b, in.Data)
				return []byte(s), nil, err
			},
		},
		Job{
			ID: "media.audio", Group: GroupMedia, Title: "Sound from video",
			UsedFor: "transcribing what is said in a video",
			Takes:   videoFormats, Becomes: KindAudio,
			Builtin: true, Uses: []string{"ffmpeg"}, timeout: 6 * time.Minute,
			run: func(ctx context.Context, b *Box, in Input) ([]byte, [][]byte, error) {
				out, err := media.AudioTrack(ctx, b, in.Data)
				return out, nil, err
			},
		},
		Job{
			ID: "media.wav", Group: GroupMedia, Title: "Audio prepared for transcription",
			UsedFor: "voice memos and other audio a speech service cannot read as sent",
			Takes:   audioFormats, Becomes: KindAudio,
			Builtin: true, Uses: []string{"ffmpeg"}, timeout: 6 * time.Minute,
			run: func(ctx context.Context, b *Box, in Input) ([]byte, [][]byte, error) {
				out, err := media.ToWAV(ctx, b, in.Data)
				return out, nil, err
			},
		},
		Job{
			ID: "media.transcribe", Group: GroupMedia, Title: "Transcribing audio",
			UsedFor: "voice memos, the sound of videos, and audio you attach",
			Takes:   audioFormats, Becomes: KindText,
			Builtin: true, ready: transcribeReady, timeout: 15 * time.Minute,
			install: "set up a speech-to-text service under Audio Transcription (STT), or adapt it to a command such as whisper.cpp's whisper-cli",
			run: func(ctx context.Context, _ *Box, in Input) ([]byte, [][]byte, error) {
				name := firstNonEmpty(in.Name, "audio"+extFor(in.From))
				text, err := media.TranscribeWith(ctx, media.GetTranscribeConfig(), in.Data, name)
				return []byte(text), nil, err
			},
		},
	)

	// The media functions run as these jobs.
	media.FramesHook = func(data []byte, count int) ([][]byte, error) {
		res, err := Do(context.Background(), "media.frames", Input{Data: data, From: Sniff(data),
			Params: map[string]string{"count": strconv.Itoa(count)}})
		return res.Files, err
	}
	media.MetadataHook = func(data []byte) (string, error) {
		res, err := Do(context.Background(), "media.metadata", Input{Data: data, From: Sniff(data)})
		s := strings.TrimSpace(string(res.Out))
		// An adapted tool's details are prefaced as the native ones are, so
		// the model reads them as the video's.
		if err == nil && s != "" && !strings.HasPrefix(s, "[video_context]") {
			s = "[video_context]\n" + s
		}
		return s, err
	}
	media.AudioHook = func(data []byte) ([]byte, error) {
		res, err := Do(context.Background(), "media.audio", Input{Data: data, From: Sniff(data)})
		return res.Out, err
	}
	media.WAVHook = func(data []byte) ([]byte, error) {
		res, err := Do(context.Background(), "media.wav", Input{Data: data, From: Sniff(data)})
		return res.Out, err
	}
	media.TranscribeHook = func(ctx context.Context, audio []byte, filename string) (string, error) {
		res, err := Do(ctx, "media.transcribe", Input{Data: audio, From: Sniff(audio), Name: filename})
		return string(res.Out), err
	}
}

// Box is the folder a built-in job's run works in, and how it runs the
// tools it drives there: each in the sandbox, seeing the system folders and
// this folder, with the network only if the job has it. It is the
// media.Tools the media code runs its ffmpeg and ffprobe through.
type Box struct {
	dir     string
	network bool
	logs    []string
}

// Dir is the box's folder.
func (b *Box) Dir() string { return b.dir }

// Run runs bin with args in the box, sandboxed, and returns what it wrote
// to stdout. stderr goes to the box's log.
func (b *Box) Run(ctx context.Context, limit time.Duration, bin string, args ...string) ([]byte, error) {
	if !Installed(bin) {
		return nil, fmt.Errorf("%s is not installed on this machine", bin)
	}
	words := append([]string{bin}, args...)
	quoted := make([]string, len(words))
	for i, w := range words {
		quoted[i] = quote(w)
	}
	const stdout = ".stdout"
	line := "exec " + strings.Join(quoted, " ") + " > " + stdout
	ctx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	ctx = netgate.WithWorkspaceNetwork(ctx, b.network)
	res := sandbox.RunSandboxedShellIn(ctx, sandbox.ShellRun{Command: line, WorkspaceDir: b.dir, RawNetwork: b.network, MaxOutput: keepLog * 4})
	if t := tail(res.Output, keepLog); t != "" {
		b.logs = append(b.logs, t)
	}
	out, _ := os.ReadFile(filepath.Join(b.dir, stdout))
	os.Remove(filepath.Join(b.dir, stdout))
	switch {
	case res.TimedOut || errors.Is(ctx.Err(), context.DeadlineExceeded):
		return out, fmt.Errorf("%s did not finish within %s", bin, limit)
	case res.Err != nil:
		return out, fmt.Errorf("%s failed: %v%s", bin, res.Err, said(tail(res.Output, keepLog)))
	}
	return out, nil
}

// runBuiltin runs a built-in job's Go in a box of its own and checks what
// it gives.
func runBuiltin(ctx context.Context, j Job, in Input) (Result, error) {
	if err := checkParams(j, in.Params); err != nil {
		return Result{}, err
	}
	if j.TakesFile() && len(in.Data) > maxInput {
		return Result{}, fmt.Errorf("the file is %d MB, over the %d MB a job takes", len(in.Data)>>20, maxInput>>20)
	}
	start := time.Now()
	var b *Box
	if len(j.Uses) > 0 {
		select {
		case runs <- struct{}{}:
			defer func() { <-runs }()
		case <-ctx.Done():
			return Result{}, ctx.Err()
		}
		dir, err := runDir()
		if err != nil {
			return Result{}, err
		}
		defer os.RemoveAll(dir)
		b = &Box{dir: dir, network: j.Network}
	}
	ctx, cancel := context.WithTimeout(ctx, j.defaultTimeout())
	defer cancel()
	out, files, err := j.run(ctx, b, in)
	res := Result{By: j.By(), Took: time.Since(start)}
	if b != nil {
		res.Log = tail(strings.Join(b.logs, "\n"), keepLog)
	}
	if err != nil {
		return res, err
	}
	want := j.target()
	if j.Files {
		if len(files) == 0 {
			return res, errors.New("it gave nothing")
		}
		for i, f := range files {
			if files[i], err = want.accept(f); err != nil {
				return res, fmt.Errorf("result %d was not used: %v", i+1, err)
			}
		}
		res.Files = files
		return res, nil
	}
	if out, err = want.accept(out); err != nil {
		return res, fmt.Errorf("what it gave was not used: %v", err)
	}
	res.Out = out
	return res, nil
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}
