package browser

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cmcoffee/gohort/core/appcheck"
)

const checkStepsPage = `<!DOCTYPE html><html><body>
<section><h3>Entry</h3>
  <label for="item">Item</label><input id="item">
  <label for="kind">Kind</label><select id="kind"><option>Food</option><option>Drink</option></select>
  <button id="add">Add</button>
</section>
<section><h3>Items</h3><table><tbody id="rows"></tbody></table></section>
<iframe sandbox="allow-scripts" srcdoc="<button onclick=&quot;document.getElementById('out').textContent='pong'&quot;>Ping</button><div id='out'></div>"></iframe>
<script>
document.getElementById('add').onclick = function () {
  var tr = document.createElement('tr');
  tr.innerHTML = '<td></td><td></td>';
  tr.children[0].textContent = document.getElementById('item').value;
  tr.children[1].textContent = document.getElementById('kind').value;
  document.getElementById('rows').appendChild(tr);
};
</script></body></html>`

func n(i int) *int { return &i }

// The steps name what a person sees, and reach into a sandboxed frame, as an
// html section's page is.
func TestAPageChecksStepsDriveThePageAndItsFrames(t *testing.T) {
	b := testBrowser(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(checkStepsPage))
	}))
	defer srv.Close()
	page := b.MustPage(srv.URL).MustWaitLoad()

	res := runCheckSteps(page, []appcheck.Step{
		{Fill: "Item", With: "milk"},
		{Choose: "Kind", Option: "Drink"},
		{Click: "Add"},
		{See: "milk", In: "Items"},
		{See: "Drink", In: "Items"},
		{Rows: n(1), In: "Items"},
		{NotSee: "eggs"},
		{Click: "Ping"},
		{See: "pong"},
	})
	if !res.OK {
		t.Fatalf("step %d did not hold: %s", res.Step, res.Saw)
	}

	res = runCheckSteps(page, []appcheck.Step{{See: "eggs", In: "Items"}})
	if res.OK || res.Step != 1 || !strings.Contains(res.Saw, "milk") {
		t.Errorf("a missing row should fail on step 1 saying what the section shows: %+v", res)
	}
	if res = runCheckSteps(page, []appcheck.Step{{Click: "Delete"}}); res.OK || !strings.Contains(res.Saw, "no button") {
		t.Errorf("a missing button should fail saying so: %+v", res)
	}
	if res = runCheckSteps(page, []appcheck.Step{{Fill: "Price", With: "3"}}); res.OK || !strings.Contains(res.Saw, "no field") {
		t.Errorf("a missing field should fail saying so: %+v", res)
	}
}

// A row's Delete asks to confirm in a dialog whose own button is Delete too.
const checkDialogPage = `<!DOCTYPE html><html><body>
<table><tbody><tr id="dune"><td>Dune</td><td><button onclick="ask()">Delete</button></td></tr></tbody></table>
<script>
function ask() {
  var o = document.createElement('div');
  o.innerHTML = '<div role="dialog"><p>Delete this book?</p><button id="no">Cancel</button><button id="yes">Delete</button></div>';
  document.body.appendChild(o);
  o.querySelector('#no').onclick = function () { o.remove(); };
  o.querySelector('#yes').onclick = function () { o.remove(); var r = document.getElementById('dune'); if (r) r.remove(); };
}
</script></body></html>`

// While a dialog is open it is all a person can reach: a click goes to the
// dialog's button, not the row's of the same name under it (which reopened
// the dialog, so a Builder check of a delete never got past its
// confirmation), and a click the dialog does not offer says what it does.
func TestAPageCheckClicksInTheOpenDialog(t *testing.T) {
	b := testBrowser(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(checkDialogPage))
	}))
	defer srv.Close()

	page := b.MustPage(srv.URL).MustWaitLoad()
	res := runCheckSteps(page, []appcheck.Step{{Click: "Delete"}, {Click: "Delete"}, {NotSee: "Dune"}})
	if !res.OK {
		t.Fatalf("step %d did not hold: %s", res.Step, res.Saw)
	}

	page = b.MustPage(srv.URL).MustWaitLoad()
	res = runCheckSteps(page, []appcheck.Step{{Click: "Delete"}, {Click: "OK"}})
	if res.OK || res.Step != 2 || !strings.Contains(res.Saw, `"Cancel", "Delete"`) {
		t.Errorf("a click the open dialog does not offer should name its buttons: %+v", res)
	}
}

// The framework's confirm (uiConfirm) is a native <dialog> opened modal, with
// Cancel and OK.
const checkNativeDialogPage = `<!DOCTYPE html><html><body>
<table><tbody><tr id="dune"><td>Dune</td><td><button onclick="ask()">Delete</button></td></tr></tbody></table>
<script>
function ask() {
  var d = document.createElement('dialog');
  d.innerHTML = '<p>Delete this item?</p><button id="no">Cancel</button><button id="yes">OK</button>';
  document.body.appendChild(d);
  d.showModal();
  d.querySelector('#no').onclick = function () { d.close(); d.remove(); };
  d.querySelector('#yes').onclick = function () { d.close(); d.remove(); var r = document.getElementById('dune'); if (r) r.remove(); };
}
</script></body></html>`

func TestAPageCheckConfirmsTheFrameworksDialog(t *testing.T) {
	b := testBrowser(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(checkNativeDialogPage))
	}))
	defer srv.Close()
	page := b.MustPage(srv.URL).MustWaitLoad()
	res := runCheckSteps(page, []appcheck.Step{{Click: "Delete"}, {Click: "Delete"}})
	if res.OK || !strings.Contains(res.Saw, `"Cancel", "OK"`) {
		t.Errorf("a click the open dialog does not offer should name its buttons: %+v", res)
	}
	page = b.MustPage(srv.URL).MustWaitLoad()
	if res = runCheckSteps(page, []appcheck.Step{{Click: "Delete"}, {Click: "OK"}, {NotSee: "Dune"}}); !res.OK {
		t.Fatalf("step %d did not hold: %s", res.Step, res.Saw)
	}
}
