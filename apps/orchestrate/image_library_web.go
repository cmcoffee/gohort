// Showing the owner what their agent's picture library actually contains.
//
// Every other surface here is read BY the agent: manifests, schema lists,
// recall sentences. The owner had none, and the cost of that showed up as a
// real session — asked for three people's reference photos, the agent handed
// back three of its own renders and described them as real. Nobody could have
// caught that earlier, because there was nowhere to look. The library was only
// ever described in prose the agent wrote about itself.
//
// So this is deliberately a LOOKING surface. The thumbnail is the load-bearing
// column: "is that actually Craig" is answered by a glance and by nothing else
// a listing could print. A name, a caption and an origin can all be confidently
// wrong together — that is exactly what happened — and none of them is the
// picture.
//
// Two verbs, matching the two ways an entry goes wrong: FORGET what should not
// be there, and LABEL what nobody said the subject of. Origin is deliberately
// not editable. It records where the pixels came from, and a field the owner
// can set is a field that can be set wrongly — an entry whose provenance was
// never captured cannot have one restored by typing it, only by keeping the
// picture again from the real thing.
package orchestrate

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"time"

	. "github.com/cmcoffee/gohort/core"
)

// urlQ escapes a value for a query string. An agent id is a uuid and a kept
// name is already restricted to letters, digits, - and _ by safeKeptName, so
// nothing here needs escaping today — which is exactly why it would be missed
// the day either rule loosens.
func urlQ(s string) string { return url.QueryEscape(s) }

// imageLibraryRow is one kept picture as the table reads it.
type imageLibraryRow struct {
	// Thumb is a URL, not bytes: the listing stays small and each picture is
	// fetched lazily by the browser, so a fifty-image library does not build a
	// fifty-megabyte JSON response to render one screen.
	Thumb   string `json:"thumb"`
	Ref     string `json:"ref"`
	Subject string `json:"subject"`
	Origin  string `json:"origin"`
	Shows   string `json:"shows"`
	Kept    string `json:"kept"`
	// Name is the bare id the action endpoints take; Ref is what a person
	// reads. Kept apart so the table's row key never has to be parsed back out
	// of display text.
	Name      string `json:"name"`
	Inherited bool   `json:"inherited"`
}

// handleAgentImages lists one agent's kept library.
func (T *OrchestrateApp) handleAgentImages(w http.ResponseWriter, r *http.Request) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	agentID := strings.TrimSpace(r.URL.Query().Get("id"))
	if agentID == "" {
		writeJSON(w, []imageLibraryRow{})
		return
	}
	sess := &ToolSession{Username: user, AgentID: agentID}
	all := KeptImages(sess)
	rows := make([]imageLibraryRow, 0, len(all))
	for _, k := range all {
		rows = append(rows, imageLibraryRow{
			Thumb:     "api/agent-images/raw?id=" + urlQ(agentID) + "&name=" + urlQ(k.Name),
			Ref:       k.Ref,
			Name:      k.Name,
			Subject:   librarySubject(k),
			Origin:    libraryOrigin(k),
			Shows:     libraryShows(k),
			Kept:      libraryWhen(k.When),
			Inherited: k.Inherited,
		})
	}
	writeJSON(w, rows)
}

// librarySubject says who the picture is of, and says so plainly when nobody
// ever recorded it — which is the state every entry kept before subjects
// existed is in, and the one worth acting on.
func librarySubject(k KeptImage) string {
	label := SubjectLabel(k.Subject)
	if label == "" {
		return "— not labelled"
	}
	if k.Subject.Person && strings.TrimSpace(k.Subject.Handle) == "" {
		// A name with no handle is a label somebody typed, not an
		// identification the transport confirmed. Worth the distinction here
		// for the same reason it is worth it to the agent.
		return label + " (name only)"
	}
	return label
}

// libraryOrigin is the column that matters most and the one a listing is most
// tempted to leave out. Unknown is its own answer, never blank: a blank cell
// reads as "nothing to say", and the whole point is that there IS something to
// say — nobody recorded where this came from, so it may be a render.
func libraryOrigin(k KeptImage) string {
	switch {
	case k.Origin.AgentMade():
		return "made by the agent"
	case k.Origin == ImageOriginUnknown:
		return "unrecorded"
	default:
		return string(k.Origin)
	}
}

// libraryShows is the agent's own words, note first. Truncated: this is a
// scanning column, and the picture beside it is the real description.
func libraryShows(k KeptImage) string {
	out := strings.TrimSpace(k.Note)
	if c := strings.TrimSpace(k.Caption); c != "" {
		if out != "" {
			out += " — "
		}
		out += c
	}
	if len([]rune(out)) > 110 {
		out = string([]rune(out)[:110]) + "…"
	}
	return out
}

func libraryWhen(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format("2 Jan 2006")
}

// handleAgentImageRaw serves one picture's bytes.
//
// Scoped to the signed-in user's own library by construction: the session is
// built from the authenticated user, so an id in the query can only ever reach
// that user's own agents. There is no path here to another account's pictures,
// which for a store holding photographs of real people is the property that
// matters more than any of the display work above.
func (T *OrchestrateApp) handleAgentImageRaw(w http.ResponseWriter, r *http.Request) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	q := r.URL.Query()
	sess := &ToolSession{Username: user, AgentID: strings.TrimSpace(q.Get("id"))}
	data, found := ResolveKeptImage(sess, RecentImageRefPrefix+strings.TrimSpace(q.Get("name")))
	if !found || len(data) == 0 {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", http.DetectContentType(data))
	// Private, not public: these are photographs of people, and a shared cache
	// between accounts is not a risk worth taking for a 48-pixel thumbnail.
	w.Header().Set("Cache-Control", "private, max-age=300")
	_, _ = w.Write(data)
}

// handleAgentImageAction applies forget or label to one entry.
func (T *OrchestrateApp) handleAgentImageAction(w http.ResponseWriter, r *http.Request) {
	user, _, ok := RequireUser(w, r, T.DB)
	if !ok {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	// Query params, not a JSON body. Both callers are UI controls — a row
	// button that posts to a URL, and a client action that builds one — and a
	// body would mean the button could not call this at all without its own
	// serializer. The values are short and non-secret.
	q := r.URL.Query()
	agentID := strings.TrimSpace(q.Get("id"))
	name := strings.TrimSpace(q.Get("name"))
	if agentID == "" || name == "" {
		http.Error(w, "id and name are required", http.StatusBadRequest)
		return
	}
	sess := &ToolSession{Username: user, AgentID: agentID}
	switch strings.ToLower(strings.TrimSpace(q.Get("action"))) {
	case "forget":
		gone, err := ForgetImage(sess, name)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if !gone {
			http.Error(w, "nothing kept under that name", http.StatusNotFound)
			return
		}
		Log("[images] %s forgot %q from agent %s", user, name, agentID)
		w.WriteHeader(http.StatusNoContent)
	case "label":
		of := strings.TrimSpace(q.Get("of"))
		if of == "" {
			http.Error(w, "say who or what it shows", http.StatusBadRequest)
			return
		}
		// The OWNER is labelling, at their own keyboard, so there is no speaker
		// to anchor a handle from — this is a name, and it says so. Anchoring
		// happens when the agent labels a picture of whoever it is talking to,
		// which is the only moment a handle is actually known.
		_, conflict, err := LabelKeptImage(sess, name, ImageSubject{Person: q.Get("is_person") != "false", Name: of})
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		Log("[images] %s labelled %q as %q on agent %s", user, name, of, agentID)
		if conflict != "" {
			// 200 with a body rather than an error: the label WAS applied, and
			// reporting it as a failure would have the owner apply it twice.
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]string{
				"conflict": conflict,
				"message":  conflict + " is also filed as that subject. Nothing was deleted — forget whichever is wrong, or a request naming them has two answers.",
			})
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "action must be forget or label", http.StatusBadRequest)
	}
}

// imageLibraryHeadHTML registers the relabel prompt.
//
// A client action rather than a plain POST button because labelling needs a
// value typed by a person, and rather than anything in core/ui because "who is
// this a picture of" is this app's question, not the framework's.
func imageLibraryHeadHTML(agentID string) string {
	return `<script>
window.uiRegisterClientAction('agent_image_label', function(ctx) {
  var rec = ctx.record || {};
  // Pre-filled with the current label so a correction is an edit rather than a
  // retype, and empty for the unlabelled rows that are the reason this exists.
  var current = (rec.subject || '').replace(/ \(name only\)$/, '');
  if (current.charAt(0) === '—') current = '';
  Promise.resolve(window.uiPrompt('Who or what is this a picture of?', current)).then(function(of) {
    if (of === null) return;
    of = String(of).trim();
    if (!of) return;
    var url = '../api/agent-images/action?action=label&id=' + encodeURIComponent(` + jsString(agentID) + `) +
      '&name=' + encodeURIComponent(rec.name || '') + '&of=' + encodeURIComponent(of);
    fetch(url, {method: 'POST'}).then(function(r) {
      if (!r.ok) { return r.text().then(function(t) { throw new Error(t || ('HTTP ' + r.status)); }); }
      // 204 on a clean label; a body means another entry already holds this
      // subject, which is worth saying out loud — the label DID apply, so this
      // is information, not a failure.
      if (r.status === 204) return null;
      return r.json();
    }).then(function(d) {
      if (d && d.message) window.uiAlert ? window.uiAlert(d.message) : alert(d.message);
      if (ctx.reload) ctx.reload();
    }).catch(function(e) {
      var m = 'Could not label that: ' + e.message;
      window.uiAlert ? window.uiAlert(m) : alert(m);
    });
  });
});
</script>`
}

// jsString renders a Go string as a JavaScript literal, so an id carrying a
// quote cannot end the literal and start being code. Nothing produces such an
// id today; that is the condition under which this gets forgotten.
func jsString(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		return `""`
	}
	return string(b)
}
