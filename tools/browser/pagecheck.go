// Page verification: load a page in the shared headless browser and
// report what happened client-side — console errors, uncaught
// exceptions, failed requests, and a caller-supplied DOM probe. This is
// the observing half of core.CheckPageAsUser (which mints the auth
// session); Builder's app_def action=verify is the consumer.
package browser

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"

	. "github.com/cmcoffee/gohort/core"
)

func init() {
	BrowserCheckPage = CheckPage
}

// checkMaxEvents caps each collected list so a page stuck in an error
// loop (a setInterval that throws every tick) can't balloon the report.
const checkMaxEvents = 20

// checkMaxRequests caps the full request log — roomier than the error
// lists because a normal page load makes many requests and callers scan
// this list for specific endpoints.
const checkMaxRequests = 200

// CheckPage loads target with the shared headless browser and reports
// the client-side result. Wrapped in the same outer-budget guard as
// fetch: a wedged Chromium returns a clear error instead of hanging the
// caller alongside it.
func CheckPage(target string, cookies []PageCheckCookie, probeJS string, insecureTLS bool) (*PageCheckReport, error) {
	budget := browsePageTotalBudget()
	type result struct {
		rep *PageCheckReport
		err error
	}
	ch := make(chan result, 1)
	go func() {
		rep, err := shared.checkPage(target, cookies, probeJS, insecureTLS)
		ch <- result{rep: rep, err: err}
	}()
	select {
	case r := <-ch:
		return r.rep, r.err
	case <-time.After(budget):
		Log("[browser] page check outer budget %v exceeded for %s — Chromium likely wedged", budget, target)
		return nil, fmt.Errorf("page check timed out after %v on %s — Chromium appears wedged; wait and retry, or restart the gohort process if it persists", budget, target)
	}
}

func (t *BrowsePageTool) checkPage(target string, cookies []PageCheckCookie, probeJS string, insecureTLS bool) (*PageCheckReport, error) {
	t.launch()
	if t.initErr != nil {
		return nil, t.initErr
	}
	t.mu.Lock()
	b := t.browser
	t.mu.Unlock()

	page, err := b.Page(proto.TargetCreateTarget{})
	if err != nil {
		return nil, fmt.Errorf("creating browser page: %w", err)
	}
	defer page.MustClose()

	if insecureTLS {
		// The dashboard's loopback cert is self-signed; scope the trust
		// to this page's CDP session, not the shared browser.
		if err := (proto.SecuritySetIgnoreCertificateErrors{Ignore: true}).Call(page); err != nil {
			return nil, fmt.Errorf("accepting local certificate: %w", err)
		}
	}
	if len(cookies) > 0 {
		var cps []*proto.NetworkCookieParam
		for _, c := range cookies {
			cps = append(cps, &proto.NetworkCookieParam{
				Name: c.Name, Value: c.Value, Domain: c.Domain, Path: c.Path,
				Secure: c.Secure, HTTPOnly: c.HTTPOnly,
			})
		}
		if err := page.SetCookies(cps); err != nil {
			return nil, fmt.Errorf("setting session cookie: %w", err)
		}
	}

	rep := &PageCheckReport{}
	var mu sync.Mutex
	// pending tracks requests the page has sent whose responses haven't
	// arrived yet (keyed by CDP request id). A data endpoint backed by a
	// slow script can outlive the idle wait — without this the report
	// claims the endpoint was "never fetched" when it was merely slow.
	pending := map[proto.NetworkRequestID]string{}
	appendCapped := func(list *[]string, s string) {
		if s = strings.TrimSpace(s); s == "" || len(*list) >= checkMaxEvents {
			return
		}
		*list = append(*list, s)
	}
	// Event collection runs for the page's whole life; rod auto-enables
	// the Runtime/Network domains for the subscribed event types. The
	// wait func returns when the page (and its CDP session) closes.
	wait := page.EachEvent(
		func(e *proto.RuntimeConsoleAPICalled) {
			if e.Type != proto.RuntimeConsoleAPICalledTypeError {
				return
			}
			var parts []string
			for _, a := range e.Args {
				parts = append(parts, remoteObjText(a))
			}
			mu.Lock()
			appendCapped(&rep.ConsoleErrors, strings.Join(parts, " "))
			mu.Unlock()
		},
		func(e *proto.RuntimeExceptionThrown) {
			msg := ""
			if e.ExceptionDetails != nil {
				msg = e.ExceptionDetails.Text
				if e.ExceptionDetails.Exception != nil && e.ExceptionDetails.Exception.Description != "" {
					msg = strings.TrimSpace(msg + " " + e.ExceptionDetails.Exception.Description)
				}
			}
			mu.Lock()
			appendCapped(&rep.PageErrors, msg)
			mu.Unlock()
		},
		func(e *proto.NetworkRequestWillBeSent) {
			if e.Request == nil || strings.HasPrefix(e.Request.URL, "data:") {
				return
			}
			mu.Lock()
			pending[e.RequestID] = e.Request.URL
			mu.Unlock()
		},
		func(e *proto.NetworkLoadingFailed) {
			mu.Lock()
			u, ok := pending[e.RequestID]
			delete(pending, e.RequestID)
			// Canceled loads (navigation churn, aborted fetches) are
			// browser noise; real transport failures are app defects.
			if ok && !e.Canceled {
				appendCapped(&rep.FailedRequests, fmt.Sprintf("%s → request failed: %s", u, e.ErrorText))
			}
			mu.Unlock()
		},
		func(e *proto.NetworkResponseReceived) {
			if e.Response == nil {
				return
			}
			mu.Lock()
			delete(pending, e.RequestID)
			if len(rep.Requests) < checkMaxRequests {
				rep.Requests = append(rep.Requests, PageRequest{URL: e.Response.URL, Status: e.Response.Status})
			}
			if e.Response.Status >= 400 {
				appendCapped(&rep.FailedRequests, fmt.Sprintf("%s → HTTP %d", e.Response.URL, e.Response.Status))
			}
			mu.Unlock()
		},
	)
	go wait()

	navErr := rod.Try(func() {
		page.Timeout(browsePageNavTimeout()).MustNavigate(target)
		// Let scripts, section mounts, and data-source fetches settle;
		// ignore timeout — report whatever state the page reached.
		_ = page.Timeout(browsePageIdleWait()).WaitIdle(500 * time.Millisecond)
	})
	if navErr != nil {
		msg := navErr.Error()
		if te, ok := navErr.(*rod.TryError); ok {
			if inner, isErr := te.Value.(error); isErr {
				msg = inner.Error()
			} else {
				msg = fmt.Sprintf("%v", te.Value)
			}
		}
		return nil, fmt.Errorf("navigation failed: %s", msg)
	}

	// Grace period for in-flight responses: WaitIdle can time out while a
	// slow data endpoint (a script making several sequential external
	// fetches) is still working. Wait up to one HTTPRequestTimeout more for
	// pending requests to land before snapshotting, so a slow-but-working
	// source reports its real response instead of showing as unfetched.
	// Bounded well under the outer budget (3×HTTPRequestTimeout).
	for deadline := time.Now().Add(HTTPRequestTimeout); ; {
		mu.Lock()
		n := len(pending)
		mu.Unlock()
		if n == 0 || time.Now().After(deadline) {
			break
		}
		time.Sleep(250 * time.Millisecond)
	}

	if probeJS != "" {
		if obj, err := page.Eval(probeJS); err == nil && obj != nil {
			mu.Lock()
			rep.ProbeJSON = obj.Value.Str()
			mu.Unlock()
		} else if err != nil {
			mu.Lock()
			appendCapped(&rep.PageErrors, "verification probe failed: "+err.Error())
			mu.Unlock()
		}
	}
	if obj, err := page.Eval(`() => document.body ? document.body.innerText : ''`); err == nil && obj != nil {
		mu.Lock()
		rep.BodyText = strings.TrimSpace(obj.Value.Str())
		mu.Unlock()
	}

	// Snapshot under the lock: the event goroutine stays live until the
	// deferred close, so hand back a copy it can't keep appending to.
	mu.Lock()
	out := &PageCheckReport{
		ConsoleErrors:  append([]string(nil), rep.ConsoleErrors...),
		PageErrors:     append([]string(nil), rep.PageErrors...),
		FailedRequests: append([]string(nil), rep.FailedRequests...),
		Requests:       append([]PageRequest(nil), rep.Requests...),
		ProbeJSON:      rep.ProbeJSON,
		BodyText:       rep.BodyText,
	}
	for _, u := range pending {
		if len(out.PendingRequests) >= checkMaxEvents {
			break
		}
		out.PendingRequests = append(out.PendingRequests, u)
	}
	mu.Unlock()
	return out, nil
}

// remoteObjText renders one console argument for the report: the
// human-readable description when the object has one, else the raw
// JSON value.
func remoteObjText(o *proto.RuntimeRemoteObject) string {
	if o == nil {
		return ""
	}
	if o.Description != "" {
		return o.Description
	}
	return o.Value.String()
}
