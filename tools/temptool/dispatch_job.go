package temptool

// The job half of an api tool (TempTool.Job, core/apijob): the submit call
// already went out the ordinary way; this waits out the job it started, then
// returns the result or fetches the file it produced and delivers it.
//
// Every poll and the file fetch go through the same doors the submit did: the
// tool's credential when it has one (its allowlist, audit and private-mode
// gate), else the public path with fetch_url's rules. A file on another host
// than the credential's (a signed download link) is fetched publicly, so the
// credential is never sent somewhere it was not made for.

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	. "github.com/cmcoffee/oddjob/core"
	"github.com/cmcoffee/oddjob/core/apijob"
	"github.com/cmcoffee/oddjob/tools/workspace"
)

// jobFileDir is where a job's file lands in the session workspace before it
// is delivered.
const jobFileDir = "jobs"

// runAPIJob waits out the job a submit started and reports how it ended.
func runAPIJob(sess *ToolSession, tt *TempTool, submitURL, raw string) (string, error) {
	statusLine, body := splitStatusLine(raw)
	if statusLine != "" && !isStatus2xx(statusLine) {
		// The submit was refused: there is no job, and the refusal is what
		// the model needs to see.
		return raw, nil
	}
	call := func(ctx context.Context, method, u string) (apijob.Response, error) {
		out, err := jobRequest(sess, tt, method, u, "")
		if err != nil {
			return apijob.Response{}, err
		}
		st, b := splitStatusLine(out)
		return apijob.Response{Status: statusCode(st), Body: []byte(b)}, nil
	}
	progress := func(elapsed time.Duration, polls int) {
		if polls == 1 || polls%10 == 0 {
			Debug("[temptool] job tool %q still waiting: %s, %d checks", tt.Name, elapsed.Round(time.Second), polls)
		}
	}
	res, err := apijob.Run(sess.Context(), *tt.Job, submitURL, []byte(body), call, progress)
	if err != nil {
		return "", fmt.Errorf("%s: %w", tt.Name, err)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Job %s finished after %s (%d checks).", chooseID(res.ID), res.Elapsed.Round(time.Second), res.Polls)
	if res.FileURL != "" {
		rel := jobFileDir + "/" + res.FileName
		size, err := fetchJobFile(sess, tt, res.FileURL, rel)
		if err != nil {
			return "", fmt.Errorf("%s: the job finished but its file could not be fetched from %s: %w", tt.Name, res.FileURL, err)
		}
		note, err := workspace.AttachWorkspaceFile(sess, rel, res.FileName, false)
		if err != nil {
			return "", fmt.Errorf("%s: the file was fetched (%s) but could not be delivered: %w", tt.Name, rel, err)
		}
		fmt.Fprintf(&b, " Delivered %s (%s) to the user; it is also in the workspace as %s. %s", res.FileName, HumanSize(size), rel, strings.TrimSpace(note))
	}
	if out := strings.TrimSpace(res.Output); out != "" {
		if len(out) > maxOutput {
			out = SpillOutput(out, maxOutput, "read_output")
		}
		b.WriteString("\n" + out)
	}
	return b.String(), nil
}

func chooseID(id string) string {
	if id == "" {
		return "(no id)"
	}
	return id
}

// statusCode reads the code from an "HTTP <code> <text>" line; 200 when there
// is no line to read.
func statusCode(line string) int {
	f := strings.Fields(line)
	if len(f) >= 2 {
		if n, err := strconv.Atoi(f[1]); err == nil {
			return n
		}
	}
	return http.StatusOK
}

// jobRequest makes one poll through the tool's own door, saving the body to
// saveTo in the workspace instead of returning it when set.
func jobRequest(sess *ToolSession, tt *TempTool, method, u, saveTo string) (string, error) {
	if !sess.NetworkAllowed() {
		return "", fmt.Errorf("network is blocked for this turn (private mode is on)")
	}
	if tt.Credential == "" {
		return dispatchPublicAPICall(sess.Context(), u, method, "", "", tt.Headers, tt.TimeoutSec)
	}
	args := map[string]any{"url": u, "method": method, "__pipe_following": true}
	if saveTo != "" {
		args["save_to"] = saveTo
	}
	return Secure().DispatchToolCallArgs(sess, tt.Credential, args)
}

// fetchJobFile saves the job's file into the workspace at rel, through the
// tool's credential when the file is on the credential's host, else publicly.
func fetchJobFile(sess *ToolSession, tt *TempTool, fileURL, rel string) (int64, error) {
	if _, err := EnsureSessionWorkspace(sess); err != nil {
		return 0, fmt.Errorf("no workspace to save it in: %w", err)
	}
	// save_to writes the file but not the folder it goes in.
	dir, err := ResolveWorkspacePath(sess.WorkspaceDir, filepath.Dir(rel))
	if err != nil {
		return 0, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return 0, err
	}
	if onCredentialHost(sess, tt.Credential, fileURL) {
		out, err := jobRequest(sess, tt, "GET", fileURL, rel)
		if err != nil {
			return 0, err
		}
		if st, _ := splitStatusLine(out); st != "" && !isStatus2xx(st) {
			return 0, fmt.Errorf("%s", strings.TrimSpace(out))
		}
		if fi, err := os.Stat(filepath.Join(sess.WorkspaceDir, rel)); err == nil {
			return fi.Size(), nil
		}
		return 0, fmt.Errorf("the download reported success but %s is not there", rel)
	}
	return downloadPublic(sess, fileURL, rel)
}

// onCredentialHost reports whether a URL is on the host the tool's
// credential was made for.
func onCredentialHost(sess *ToolSession, cred, rawURL string) bool {
	if cred == "" {
		return false
	}
	c, ok := Secure().Resolve(cred, sess.Username)
	if !ok {
		return false
	}
	base, err1 := url.Parse(c.BaseURL)
	u, err2 := url.Parse(rawURL)
	return err1 == nil && err2 == nil && base.Host != "" && strings.EqualFold(base.Host, u.Host)
}

// downloadPublic fetches a public URL into the workspace under the same size
// cap a credential's save_to has, with fetch_url's public-address rule.
func downloadPublic(sess *ToolSession, rawURL, rel string) (int64, error) {
	if err := RefuseNonPublicHost(rawURL); err != nil {
		return 0, err
	}
	dest, err := ResolveWorkspacePath(sess.WorkspaceDir, rel)
	if err != nil {
		return 0, err
	}
	req, err := http.NewRequestWithContext(sess.Context(), "GET", rawURL, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("User-Agent", publicAPIUserAgent)
	client := NewPublicHTTPClient()
	client.Timeout = 10 * time.Minute
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return 0, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	limit := int64(TuneInt("tune_secure_api_max_save_bytes"))
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return 0, err
	}
	f, err := os.Create(dest)
	if err != nil {
		return 0, err
	}
	n, err := io.Copy(f, io.LimitReader(resp.Body, limit+1))
	f.Close()
	if err != nil {
		os.Remove(dest)
		return 0, err
	}
	if n > limit {
		os.Remove(dest)
		return 0, fmt.Errorf("the file is larger than the %s a download may be", HumanSize(limit))
	}
	return n, nil
}
