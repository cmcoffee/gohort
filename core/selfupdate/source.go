package selfupdate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// Source is a dist folder, and what a release from it has to be.
type Source struct {
	// URL is the dist folder: https://dist.example.test/gohort.
	URL string
	// Keys are the keys a release list may be signed with.
	Keys []Key
	// Product is what the release list must say it is for.
	Product string
	// UserAgent is sent with each request, product/version.
	UserAgent string
	// Client fetches; nil uses one with a proxy from the environment.
	Client *http.Client
}

// Limits on what a dist site sends back before it is checked.
const (
	maxManifest  = 1 << 20
	maxSignature = 4 << 10
)

func (s Source) client() *http.Client {
	if s.Client != nil {
		return s.Client
	}
	return &http.Client{Transport: &http.Transport{Proxy: http.ProxyFromEnvironment}}
}

// Host is the dist site's host, for saying where updates come from.
func (s Source) Host() string {
	h := strings.TrimPrefix(strings.TrimPrefix(s.URL, "https://"), "http://")
	return strings.TrimSuffix(h, "/")
}

func (s Source) url(name string) string { return strings.TrimSuffix(s.URL, "/") + "/" + name }

// get fetches one file of the dist folder, refusing more than limit bytes.
func (s Source) get(ctx context.Context, name string, limit int64) ([]byte, error) {
	body, _, err := s.open(ctx, name)
	if err != nil {
		return nil, err
	}
	defer body.Close()
	b, err := io.ReadAll(io.LimitReader(body, limit+1))
	if err != nil {
		return nil, fmt.Errorf("%s did not finish sending %s: %v", s.Host(), name, err)
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("%s sent a %s larger than one can be", s.Host(), name)
	}
	return b, nil
}

func (s Source) open(ctx context.Context, name string) (io.ReadCloser, int64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.url(name), nil)
	if err != nil {
		return nil, 0, fmt.Errorf("the update address %q is not a URL: %v", s.URL, err)
	}
	if s.UserAgent != "" {
		req.Header.Set("User-Agent", s.UserAgent)
	}
	resp, err := s.client().Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("%s did not answer: %v", s.Host(), err)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, 0, fmt.Errorf("%s answered %s for %s", s.Host(), resp.Status, name)
	}
	return resp.Body, resp.ContentLength, nil
}

// Latest is the newest release the dist folder offers, its signature checked.
func (s Source) Latest(ctx context.Context) (Manifest, error) {
	if strings.TrimSpace(s.URL) == "" {
		return Manifest{}, errors.New("no update address is set")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	manifest, err := s.get(ctx, ManifestName, maxManifest)
	if err != nil {
		return Manifest{}, err
	}
	sig, err := s.get(ctx, SignatureName, maxSignature)
	if err != nil {
		return Manifest{}, err
	}
	return ReadManifest(manifest, sig, s.Keys, s.Product)
}

// Download fetches this release's binary for platform into dir, under a
// name that is not the program's, and checks its size and hash against the
// signed manifest. progress is told how far it has got. A download that does
// not match is removed.
func (s Source) Download(ctx context.Context, m Manifest, platform, dir string, progress func(done, total int64)) (string, error) {
	f, err := m.For(platform)
	if err != nil {
		return "", err
	}
	body, _, err := s.open(ctx, f.Path)
	if err != nil {
		return "", err
	}
	defer body.Close()
	out, err := os.CreateTemp(dir, ".update-"+m.Version+"-*.download")
	if err != nil {
		return "", fmt.Errorf("cannot write in %s: %v", dir, err)
	}
	path := out.Name()
	fail := func(err error) (string, error) {
		out.Close()
		os.Remove(path)
		return "", err
	}
	h := sha256.New()
	w := &counter{w: io.MultiWriter(out, h), total: f.Size, report: progress}
	n, err := io.Copy(w, io.LimitReader(body, f.Size+1))
	switch {
	case err != nil:
		return fail(fmt.Errorf("the download stopped at %d of %d bytes: %v", n, f.Size, err))
	case n != f.Size:
		return fail(fmt.Errorf("the download is %d bytes, and the signed release list says %d", n, f.Size))
	case hex.EncodeToString(h.Sum(nil)) != strings.ToLower(f.SHA256):
		return fail(errors.New("the download does not match the signed release list's hash: not installing it"))
	}
	if err := out.Close(); err != nil {
		os.Remove(path)
		return "", err
	}
	if err := os.Chmod(path, 0o755); err != nil {
		os.Remove(path)
		return "", err
	}
	if progress != nil {
		progress(n, f.Size)
	}
	return path, nil
}

// counter tells report how far a copy has got, every 2 seconds at most.
type counter struct {
	w      io.Writer
	done   int64
	total  int64
	last   time.Time
	report func(done, total int64)
}

func (c *counter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.done += int64(n)
	if c.report != nil && time.Since(c.last) >= 2*time.Second {
		c.last = time.Now()
		c.report(c.done, c.total)
	}
	return n, err
}
