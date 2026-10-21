package assist

// Attachments: files the person adds in the window (its 📎), for the app's
// tools to use, a sample to try a conversion on, say. They are NOT shown to
// the model: what needs converting is exactly what a model cannot read, and
// a file is not a message. The model is told what arrived, by id, name and
// size, and a tool that takes a file reads it with Files(ctx).
//
// They are kept per window (the browser names its window on every turn, so
// a file attached once serves the whole conversation without being sent
// again), in memory: they are inputs for a conversation, not records, and a
// restart asks for them again. A few per window, the latest kept; a window
// left alone for an hour is let go.

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"
	"sync"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

// File is one attachment.
type File struct {
	ID    string
	Name  string
	Data  []byte
	Added time.Time
}

// Label is how the model is told of it: "file-1 (photo.heic, 2.1 MB)".
func (f File) Label() string {
	if f.Name == "" {
		return fmt.Sprintf("%s (%s)", f.ID, HumanSize(int64(len(f.Data))))
	}
	return fmt.Sprintf("%s (%s, %s)", f.ID, f.Name, HumanSize(int64(len(f.Data))))
}

// upload is a file as a turn carries it.
type upload struct {
	Name string `json:"name"`
	Data string `json:"data"` // base64
}

const (
	filesKept  = 4
	fileMax    = 64 << 20
	attachBody = 96 << 20
	windowIdle = time.Hour
)

type window struct {
	files []File
	seq   int
	used  time.Time
}

var windows = struct {
	sync.Mutex
	m map[string]*window
}{m: map[string]*window{}}

// windowKey is a window's key: the person, the assist, and the browser's
// name for the window. A window name that is not a short plain token is no
// window, so nothing is kept for it.
func windowKey(user, route, id string) string {
	id = strings.TrimSpace(id)
	if id == "" || len(id) > 40 || strings.Trim(id, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-_") != "" {
		return ""
	}
	return user + "\x00" + route + "\x00" + id
}

// keep adds a turn's uploads to its window and returns their labels.
func keep(key string, ups []upload) ([]string, error) {
	if key == "" {
		return nil, fmt.Errorf("files need a window to be kept in")
	}
	var add []File
	for _, u := range ups {
		data, err := base64.StdEncoding.DecodeString(u.Data)
		if err != nil {
			return nil, fmt.Errorf("%s could not be read", firstNonEmpty(u.Name, "a file"))
		}
		if len(data) == 0 {
			continue
		}
		if len(data) > fileMax {
			return nil, fmt.Errorf("%s is %s, over the %s a file can be here", firstNonEmpty(u.Name, "a file"), HumanSize(int64(len(data))), HumanSize(fileMax))
		}
		add = append(add, File{Name: strings.TrimSpace(u.Name), Data: data})
	}
	windows.Lock()
	defer windows.Unlock()
	now := time.Now()
	for k, w := range windows.m {
		if now.Sub(w.used) > windowIdle {
			delete(windows.m, k)
		}
	}
	w := windows.m[key]
	if w == nil {
		w = &window{}
		windows.m[key] = w
	}
	w.used = now
	var labels []string
	for _, f := range add {
		w.seq++
		f.ID, f.Added = fmt.Sprintf("file-%d", w.seq), now
		w.files = append(w.files, f)
		labels = append(labels, f.Label())
	}
	if len(w.files) > filesKept {
		w.files = w.files[len(w.files)-filesKept:]
	}
	return labels, nil
}

// filesOf are a window's files, oldest first.
func filesOf(key string) []File {
	if key == "" {
		return nil
	}
	windows.Lock()
	defer windows.Unlock()
	w := windows.m[key]
	if w == nil {
		return nil
	}
	w.used = time.Now()
	return append([]File(nil), w.files...)
}

type filesKey struct{}

// Files are the attachments of the window a tool call is for, oldest
// first: what an app's tool reads a sample from.
func Files(ctx context.Context) []File {
	fs, _ := ctx.Value(filesKey{}).([]File)
	return fs
}

// WithFiles is ctx carrying files as a window's attachments, for calling
// an assist's tools outside a turn (a test, a direct run).
func WithFiles(ctx context.Context, files []File) context.Context {
	return context.WithValue(ctx, filesKey{}, files)
}

// FileByID is the window's file id names, or with id empty the latest.
func FileByID(ctx context.Context, id string) (File, error) {
	fs := Files(ctx)
	if len(fs) == 0 {
		return File{}, fmt.Errorf("no file is attached: ask for one, of the kind this reads")
	}
	if id = strings.TrimSpace(id); id == "" {
		return fs[len(fs)-1], nil
	}
	for _, f := range fs {
		if f.ID == id {
			return f, nil
		}
	}
	return File{}, fmt.Errorf("there is no %s here: the files attached are listed above", id)
}
