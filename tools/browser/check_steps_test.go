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
