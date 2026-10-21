package flows

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/cmcoffee/gohort/core/media"
)

// Input is what a job is run on: the file it reads (Data, of format From,
// called Name where that matters to it), or for an export the document
// (Doc), and the values it is called with.
type Input struct {
	Data   []byte
	From   string
	Name   string
	Doc    *Document
	Params map[string]string
}

// source is the file a flow for j reads from in: the input's file, or the
// document's Markdown or HTML for an export, with its format.
func (in Input) source(j Job) ([]byte, string, error) {
	if in.Doc == nil {
		return in.Data, in.From, nil
	}
	if contains(j.Takes, "application/json") {
		if len(in.Doc.Data) == 0 {
			return nil, "", fmt.Errorf("%s is built from data, and this document has none: give it %s", j.Title, firstNonEmpty(j.Hint, "data"))
		}
		payload, err := json.Marshal(map[string]any{"title": in.Doc.Title, "date": in.Doc.Date, "data": in.Doc.Data})
		if err != nil {
			return nil, "", err
		}
		return payload, "application/json", nil
	}
	if contains(j.Takes, "text/html") {
		if len(in.Doc.HTML) == 0 {
			return nil, "", errors.New("this document has no HTML to hand it")
		}
		return in.Doc.HTML, "text/html", nil
	}
	if len(in.Doc.Markdown) == 0 {
		return nil, "", errors.New("this document has no Markdown to hand it")
	}
	return in.Doc.Markdown, "text/markdown", nil
}

// ErrNoConversion is the answer when nothing here does what was asked.
var ErrNoConversion = errors.New("nothing here does this")

// ErrBuiltin is a built-in export's answer to being run here: the app
// renders it.
var ErrBuiltin = errors.New("this is written by the app itself")

// RunNative runs j's native flow on in: Go itself for a job gohort does,
// or the first flow whose programs are installed.
func RunNative(ctx context.Context, j Job, in Input) (Result, error) {
	if j.Builtin {
		if j.run == nil {
			return Result{}, ErrBuiltin
		}
		if _, ready := j.Ready(); !ready {
			return Result{}, notInstalled(j)
		}
		return runBuiltin(ctx, j, in)
	}
	f, ok := j.Ready()
	if !ok {
		return Result{}, notInstalled(j)
	}
	src, from, err := in.source(j)
	if err != nil {
		return Result{}, err
	}
	return runFlow(ctx, flowRun{by: j.By(), job: j, steps: f.Steps, in: src, from: from, params: in.Params, timeout: j.defaultTimeout(),
		files: f.Files, py: f.Py})
}

// RunAdapted runs an adapted flow on in, against its contract.
func RunAdapted(ctx context.Context, name string, a Adapted, in Input) (Result, error) {
	if err := a.Validate(); err != nil {
		return Result{}, err
	}
	j, _ := a.Contract()
	if j.IsExport() != (in.Doc != nil) {
		if j.IsExport() {
			return Result{}, errors.New("that one writes a document, it does not read a file")
		}
		return Result{}, errors.New("that one reads a file, it does not write a document")
	}
	src, from, err := in.source(j)
	if err != nil {
		return Result{}, err
	}
	return runFlow(ctx, flowRun{by: name, job: j, steps: a.Flow(), in: src, from: from, params: in.Params,
		timeout: a.Timeout(), reads: a.Reads})
}

func notInstalled(j Job) error {
	return fmt.Errorf("%s needs %s, which is not installed here: install %s", j.Title, j.By(), j.InstallHint())
}

// ways runs the ways a job can be done, in order (the admin's preferred
// flows, gohort's own, the other adapted ones), and returns the first that
// works, or the last error.
func ways(preferred []namedAdapted, native func() (Result, error, bool), rest []namedAdapted, in Input) func(context.Context) (Result, error) {
	return func(ctx context.Context) (Result, error) {
		var lastErr error
		for _, a := range preferred {
			res, err := RunAdapted(ctx, a.Name, a.A, in)
			if err == nil {
				return res, nil
			}
			lastErr = err
		}
		if native != nil {
			res, err, tried := native()
			if tried && err == nil {
				return res, nil
			}
			if err != nil {
				lastErr = err
			}
		}
		for _, a := range rest {
			res, err := RunAdapted(ctx, a.Name, a.A, in)
			if err == nil {
				return res, nil
			}
			lastErr = err
		}
		return Result{}, lastErr
	}
}

// Do runs the job called id on in: by an adapted flow its admin preferred,
// then the native flow, then the other adapted flows for it.
func Do(ctx context.Context, id string, in Input) (Result, error) {
	j, ok := JobByID(id)
	if !ok {
		return Result{}, fmt.Errorf("there is no job %q", id)
	}
	match := func(a Adapted, aj Job) (bool, bool) { return a.Job == id, false }
	native := func() (Result, error, bool) {
		if _, ready := j.Ready(); !ready {
			return Result{}, notInstalled(j), false
		}
		res, err := RunNative(ctx, j, in)
		return res, err, true
	}
	res, err := ways(live(true, match), native, live(false, match), in)(ctx)
	if err == nil && res.Out == nil && res.Files == nil {
		return res, fmt.Errorf("%w: %s", ErrNoConversion, j.Title)
	}
	return res, err
}

// Convert turns data into kind: by an adapted flow its admin preferred,
// then gohort's own, then the other adapted ones. from is the format when
// the caller knows it (an upload's type), else it is sniffed.
func Convert(ctx context.Context, data []byte, from string, kind Kind) (Result, error) {
	if from == "" {
		from = Sniff(data)
	}
	in := Input{Data: data, From: from}
	match := func(a Adapted, j Job) (bool, bool) {
		if !j.IsRead() || j.Becomes != kind {
			return false, false
		}
		return contains(j.Takes, from), contains(j.Takes, Any)
	}
	var job *Job
	for _, j := range jobs {
		if j.IsRead() && j.Becomes == kind && contains(j.Takes, from) {
			j := j
			job = &j
			break
		}
	}
	var native func() (Result, error, bool)
	if job != nil {
		native = func() (Result, error, bool) {
			if _, ready := job.Ready(); !ready {
				// Said, but not tried: an adapted flow may still do it.
				return Result{}, notInstalled(*job), false
			}
			res, err := RunNative(ctx, *job, in)
			return res, err, true
		}
	}
	res, err := ways(live(true, match), native, live(false, match), in)(ctx)
	if err == nil && res.Out == nil {
		return res, fmt.Errorf("%w: nothing here turns %s into %s", ErrNoConversion, FormatLabel(from), kind.Label())
	}
	return res, err
}

// Export writes doc as format ext: by an adapted flow its admin preferred,
// then builtin (the app's own renderer, when the format is one), then the
// native tool, then the other adapted flows.
func Export(ctx context.Context, doc Document, ext string, builtin func() ([]byte, error)) (Result, error) {
	in := Input{Doc: &doc}
	match := func(a Adapted, j Job) (bool, bool) { return j.IsExport() && j.Gives == ext, false }
	native := func() (Result, error, bool) {
		if builtin != nil {
			start := time.Now()
			out, err := builtin()
			if err == nil {
				return Result{Out: out, By: "built in", Took: time.Since(start)}, nil, true
			}
			return Result{}, err, true
		}
		for _, j := range jobs {
			if j.IsExport() && j.Gives == ext && !j.AppRendered() {
				if _, ready := j.Ready(); !ready {
					return Result{}, notInstalled(j), false
				}
				res, err := RunNative(ctx, j, in)
				return res, err, true
			}
		}
		return Result{}, nil, false
	}
	res, err := ways(live(true, match), native, live(false, match), in)(ctx)
	if err == nil && res.Out == nil {
		return res, fmt.Errorf("%w: nothing here writes %s; add it under Admin > Capabilities > System Hooks", ErrNoConversion, OutLabel(ext))
	}
	return res, err
}

// Available are the export formats that can be written here now from a
// document in one of forms (text/markdown, text/html, application/json),
// beyond the app's own: a native flow that can run, or an adapted flow
// that is on. Sorted by name, for an export menu.
func Available(forms ...string) []string {
	takes := func(j Job) bool {
		for _, t := range j.Takes {
			if contains(forms, t) {
				return true
			}
		}
		return false
	}
	seen := map[string]bool{}
	for _, j := range jobs {
		if j.IsExport() && !j.Builtin && takes(j) {
			if _, ok := j.Ready(); ok {
				seen[j.Gives] = true
			}
		}
	}
	builtinExt := func(ext string) bool {
		for _, j := range jobs {
			if j.IsExport() && j.Gives == ext && j.Builtin {
				return true
			}
		}
		return false
	}
	for _, prefer := range []bool{false, true} {
		for _, a := range live(prefer, func(a Adapted, j Job) (bool, bool) { return j.IsExport(), false }) {
			if !builtinExt(a.J.Gives) && takes(a.J) {
				seen[a.J.Gives] = true
			}
		}
	}
	out := make([]string, 0, len(seen))
	for ext := range seen {
		out = append(out, ext)
	}
	sort.Slice(out, func(i, k int) bool { return OutLabel(out[i]) < OutLabel(out[k]) })
	return out
}

// Document reading (media.ExtractDocument) converts through here, so a
// document's tool runs sandboxed and an admin can adapt a missing one.
func init() {
	media.DocumentConverter = func(ctx context.Context, data []byte, mime string) (string, error) {
		res, err := Convert(ctx, data, mime, KindText)
		return string(res.Out), err
	}
}

// LiveFor reports whether a live (approved) adapted flow fills job id, so
// it can run even where its native tool is missing.
func LiveFor(id string) bool {
	return len(live(true, func(a Adapted, j Job) (bool, bool) { return a.Job == id, false }))+
		len(live(false, func(a Adapted, j Job) (bool, bool) { return a.Job == id, false })) > 0
}

// Live is the live (approved) adapted flow called name, with its contract.
func Live(name string) (Adapted, Job, bool) {
	adaptedMu.RLock()
	a, ok := adapted[name]
	adaptedMu.RUnlock()
	if !ok {
		return Adapted{}, Job{}, false
	}
	j, err := a.Contract()
	return a, j, err == nil
}

// Exports are the export formats that can be written here, whoever asks:
// gohort's export jobs that run here (not an app's own, and ready or filled
// by a live adapted flow), then the formats an admin added that are on, one
// per format, by name.
func Exports() []Job {
	var out []Job
	seen := map[string]bool{}
	for _, j := range jobs {
		if !j.IsExport() || j.AppRendered() || seen[j.Gives] {
			continue
		}
		if _, ready := j.Ready(); ready || LiveFor(j.ID) {
			out = append(out, j)
			seen[j.Gives] = true
		}
	}
	for _, prefer := range []bool{true, false} {
		for _, a := range live(prefer, func(a Adapted, j Job) (bool, bool) { return a.Job == "" && j.IsExport(), false }) {
			if !seen[a.J.Gives] {
				out = append(out, a.J)
				seen[a.J.Gives] = true
			}
		}
	}
	sort.Slice(out, func(i, k int) bool { return out[i].Gives < out[k].Gives })
	return out
}
