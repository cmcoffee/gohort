package browser

// A page check's steps, run in the browser (docs/design/app-checks.md). The
// steps name what a person sees, a field's label, a button's text, a section's
// title, and each one looks in the page and in every frame inside it: an html
// section renders in a sandboxed frame no script on the page can reach, but
// the browser can.

import (
	"strings"
	"time"

	"github.com/cmcoffee/gohort/core/appcheck"
	"github.com/go-rod/rod"
)

// checkStepWait is how long a see, not_see or rows step waits for the page to
// show what it names, and checkStepSettle the pause after an action for the
// page to answer it.
const (
	checkStepWait   = 5 * time.Second
	checkStepSettle = 400 * time.Millisecond
)

// runCheckSteps runs steps on page in order and reports the first that does
// not hold.
func runCheckSteps(page *rod.Page, steps []appcheck.Step) appcheck.StepsResult {
	for i, st := range steps {
		if saw, ok := runCheckStep(page, st); !ok {
			return appcheck.StepsResult{Step: i + 1, Saw: saw}
		}
	}
	return appcheck.StepsResult{OK: true}
}

// runCheckStep does or sees one step. An action happens in the first frame
// that has what it names; a see waits for any frame to show its text, a
// not_see for none to, and a rows for the named section to hold that many.
func runCheckStep(page *rod.Page, st appcheck.Step) (saw string, ok bool) {
	waiting := st.See != "" || st.NotSee != "" || st.Rows != nil
	deadline := time.Now().Add(checkStepWait)
	for {
		var first string
		hits, misses := 0, 0
		for _, fr := range checkFrames(page) {
			done, what := evalCheckStep(fr, st)
			if first == "" && what != "" {
				first = what
			}
			if done {
				hits++
				if !waiting {
					break
				}
			} else {
				misses++
			}
		}
		switch {
		case st.NotSee != "" && misses == 0:
			return "", true
		case st.NotSee == "" && hits > 0:
			if !waiting {
				time.Sleep(checkStepSettle)
			}
			return "", true
		case !waiting || time.Now().After(deadline):
			return first, false
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// checkFrames is the page and the frames inside it, two levels deep: an app
// page's sections, and a frame one of them holds.
func checkFrames(page *rod.Page) []*rod.Page {
	out := []*rod.Page{page}
	var walk func(p *rod.Page, depth int)
	walk = func(p *rod.Page, depth int) {
		if depth > 2 {
			return
		}
		els, err := p.Timeout(2 * time.Second).Elements("iframe")
		if err != nil {
			return
		}
		for _, el := range els {
			fr, err := el.Frame()
			if err != nil || fr == nil {
				continue
			}
			out = append(out, fr)
			walk(fr, depth+1)
		}
	}
	walk(page, 1)
	return out
}

// evalCheckStep runs checkStepJS on one frame.
func evalCheckStep(fr *rod.Page, st appcheck.Step) (done bool, saw string) {
	obj, err := fr.Timeout(checkStepWait).Eval(checkStepJS, st)
	if err != nil || obj == nil {
		return false, ""
	}
	return obj.Value.Get("done").Bool(), strings.TrimSpace(obj.Value.Get("saw").Str())
}

// checkStepJS finds and does one step in one document. It matches what a
// person reads, without case or extra spaces: a field by its label, its
// placeholder, its aria-label or its name; a button or link by its text; a
// section by a heading or title with that text, then what contains it.
const checkStepJS = `(step) => {
  const norm = s => String(s == null ? '' : s).replace(/\s+/g, ' ').trim().toLowerCase().replace(/\s*\*$/, '');
  const visible = el => !!(el.offsetWidth || el.offsetHeight || el.getClientRects().length);
  const clip = s => String(s || '').replace(/\s+/g, ' ').trim().slice(0, 300);
  let root = document.body;
  if (step.in) {
    const want = norm(step.in);
    const heads = Array.from(document.querySelectorAll('h1,h2,h3,h4,h5,h6,legend,caption,[class*="title"],[class*="header"]'))
      .filter(e => norm(e.textContent) === want);
    root = null;
    for (const h of heads) {
      const box = h.closest('section,fieldset,table,[class*="section"],[class*="card"],[class*="panel"]') || h.parentElement;
      if (box) { root = box; break; }
    }
    if (!root) return {done: false, saw: 'no section titled "' + step.in + '"'};
  }
  if (!root) return {done: false, saw: 'the page has no body'};
  const text = norm(root.innerText);
  if (step.see) return {done: text.includes(norm(step.see)), saw: 'the page shows: ' + clip(root.innerText)};
  if (step.not_see) return {done: !text.includes(norm(step.not_see)), saw: 'the page still shows "' + step.not_see + '"'};
  if (step.rows != null) {
    let n = root.querySelectorAll('tbody tr').length;
    if (!n) n = root.querySelectorAll('[role="row"]').length;
    return {done: n === step.rows, saw: n + ' row(s)'};
  }
  const field = label => {
    const want = norm(label);
    for (const l of document.querySelectorAll('label')) {
      if (norm(l.textContent) !== want) continue;
      const c = l.control || l.querySelector('input,textarea,select') ||
        (l.parentElement && l.parentElement.querySelector('input,textarea,select'));
      if (c) return c;
    }
    for (const c of document.querySelectorAll('input,textarea,select')) {
      if ([c.placeholder, c.getAttribute('aria-label'), c.name].some(v => norm(v) === want)) return c;
    }
    return null;
  };
  if (step.fill) {
    const c = field(step.fill);
    if (!c || c.tagName === 'SELECT') return {done: false, saw: 'no field "' + step.fill + '" to type into'};
    c.focus();
    const proto = c.tagName === 'TEXTAREA' ? HTMLTextAreaElement.prototype : HTMLInputElement.prototype;
    Object.getOwnPropertyDescriptor(proto, 'value').set.call(c, step.with);
    c.dispatchEvent(new Event('input', {bubbles: true}));
    c.dispatchEvent(new Event('change', {bubbles: true}));
    return {done: true};
  }
  if (step.choose) {
    const c = field(step.choose);
    if (!c || c.tagName !== 'SELECT') return {done: false, saw: 'no list "' + step.choose + '" to choose from'};
    const o = Array.from(c.options).find(o => norm(o.textContent) === norm(step.option));
    if (!o) return {done: false, saw: '"' + step.choose + '" offers: ' + Array.from(c.options).map(o => o.textContent.trim()).join(', ')};
    c.value = o.value;
    c.dispatchEvent(new Event('change', {bubbles: true}));
    return {done: true};
  }
  if (step.click) {
    const want = norm(step.click);
    const label = e => norm(e.innerText || e.value || e.getAttribute('aria-label') || e.title);
    const cands = Array.from(root.querySelectorAll('button,a,[role="button"],input[type="submit"],input[type="button"]')).filter(visible);
    const el = cands.find(e => label(e) === want) || cands.find(e => label(e).includes(want));
    if (!el) return {done: false, saw: 'no button or link "' + step.click + '"'};
    el.click();
    return {done: true};
  }
  return {done: false, saw: 'a step that does nothing'};
}`
