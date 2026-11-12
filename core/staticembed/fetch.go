package staticembed

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/cmcoffee/snugforge/iotimeout"
)

// DefaultSource is where Fetch downloads from: the model's own repository on
// Hugging Face, at one pinned revision, so what arrives is what the sums
// in files describe.
const DefaultSource = "https://huggingface.co/minishlab/potion-retrieval-32M/resolve/6fc8051fab2a1e0ee76689cf08c853792ac285e7/"

// Source is where Fetch downloads from; a test points it at its own server.
var Source = DefaultSource

// stallTimeout is how long a download may receive nothing before it is given
// up: a stalled connection fails and says so instead of hanging forever.
const stallTimeout = 60 * time.Second

// Fetch downloads every file dir is missing (or holds at the wrong size) into
// dir, each checked against its pinned sum before it takes its name, so a
// cut-off or altered download never stands in for the model. progress is
// told the bytes done so far and the total to do, as they arrive; it is
// called often, and throttling it is the caller's.
func Fetch(ctx context.Context, dir string, progress func(done, total int64)) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	var todo []file
	var total int64
	for _, f := range files {
		if st, err := os.Stat(filepath.Join(dir, f.name)); err == nil && st.Size() == f.size {
			continue
		}
		todo = append(todo, f)
		total += f.size
	}
	var done int64
	for _, f := range todo {
		err := fetchOne(ctx, dir, f, func(n int64) {
			if progress != nil {
				progress(done+n, total)
			}
		})
		if err != nil {
			return err
		}
		done += f.size
	}
	return nil
}

// fetchOne downloads f to a .part file beside it, hashing as it writes, and
// renames it into place only when the sum is the pinned one.
func fetchOne(ctx context.Context, dir string, f file, progress func(int64)) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, Source+f.name, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("downloading %s: %w", f.name, err)
	}
	body := iotimeout.NewReadCloser(resp.Body, stallTimeout)
	defer body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("downloading %s: HTTP %d", f.name, resp.StatusCode)
	}
	part := filepath.Join(dir, f.name+".part")
	out, err := os.Create(part)
	if err != nil {
		return err
	}
	defer os.Remove(part) // gone once renamed; left only by a failure, and removed then too
	h := sha256.New()
	w := io.MultiWriter(out, h)
	buf := make([]byte, 256<<10)
	var n int64
	for {
		k, rerr := body.Read(buf)
		if k > 0 {
			if _, err := w.Write(buf[:k]); err != nil {
				out.Close()
				return err
			}
			n += int64(k)
			progress(n)
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			out.Close()
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("downloading %s: %w", f.name, rerr)
		}
		if n > f.size {
			out.Close()
			return fmt.Errorf("downloading %s: more than the %d bytes it should be", f.name, f.size)
		}
	}
	if err := out.Close(); err != nil {
		return err
	}
	if n != f.size {
		return fmt.Errorf("downloading %s: %d bytes arrived, want %d", f.name, n, f.size)
	}
	if err := sumMatches(f, h); err != nil {
		return err
	}
	return os.Rename(part, filepath.Join(dir, f.name))
}

// Remove deletes the model's files from dir, so the next Fetch downloads
// them whole: for files at the right size that fail their sum.
func Remove(dir string) error {
	for _, f := range files {
		if err := os.Remove(filepath.Join(dir, f.name)); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}
