package knowledge

// New collection opens the framework's form in the framework's dialog
// (uiOpenComponentDialog, as a ModalButton does), so Create, the redirect to
// the new collection and the ✨ are the form's own, and Create sits in the
// footer beside Close. Driven, not read: an earlier conversion named
// the modal's handle "api", which hid the page's api() URL helper, so Create
// threw and sent nothing, with nothing on screen to say so.

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestNewCollectionModalMountsTheForm(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not installed")
	}
	i := strings.Index(documentsListAssets, "<script>")
	j := strings.LastIndex(documentsListAssets, "</script>")
	if i < 0 || j < i {
		t.Fatal("the collections page has no script")
	}
	form := strings.TrimSuffix(strings.TrimPrefix(newCollectionFormScript(""), "<script>"), "</script>")
	script := newCollectionHarnessBefore + form + documentsListAssets[i+len("<script>"):j] + newCollectionHarnessAfter
	tmp := filepath.Join(t.TempDir(), "new_collection.js")
	if err := os.WriteFile(tmp, []byte(script), 0644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("node", tmp).CombinedOutput(); err != nil || !strings.Contains(string(out), "OK") {
		t.Fatalf("New collection:\n%s", out)
	}
}

// The form: a guided setup that creates through the collections API and opens
// what it made, with only the name required, and its ✨ Draft with AI,
// starting on the setting up. Filling carries Knowledge's curator for the
// create endpoint to put in charge when "Look after it for me" is picked. The
// Agents step is there only when there are agents to pick.
func TestTheNewCollectionFormIsAGuidedSetup(t *testing.T) {
	b, err := json.Marshal(newCollectionForm(""))
	if err != nil {
		t.Fatal(err)
	}
	type field struct {
		Field    string `json:"field"`
		Type     string `json:"type"`
		Required bool   `json:"required"`
		Default  any    `json:"default"`
		ShowWhen string `json:"show_when"`
		Options  []struct {
			Value string `json:"value"`
		} `json:"options"`
	}
	var f struct {
		Type        string `json:"type"`
		PostURL     string `json:"post_url"`
		SubmitLabel string `json:"submit_label"`
		RedirectURL string `json:"redirect_url"`
		Assist      bool   `json:"assist"`
		AssistLabel string `json:"assist_label"`
		AssistAsk   string `json:"assist_ask"`
		Steps       []struct {
			Title  string  `json:"title"`
			Fields []field `json:"fields"`
		} `json:"steps"`
	}
	json.Unmarshal(b, &f)
	if f.Type != "form_panel" || f.PostURL != "/orchestrate/api/collections" || f.SubmitLabel != "Create" || f.RedirectURL != "/knowledge/c/{id}" {
		t.Errorf("the form should create and open the collection: %s", b)
	}
	if !f.Assist || f.AssistLabel != "Draft with AI" || f.AssistAsk != "Assist me with setting up a knowledge store." {
		t.Errorf("its ✨ should be Draft with AI, opening on the setting up: %s", b)
	}
	var titles []string
	byName := map[string]field{}
	for _, st := range f.Steps {
		titles = append(titles, st.Title)
		for _, fl := range st.Fields {
			byName[fl.Field] = fl
			if fl.Required && fl.Field != "name" {
				t.Errorf("%s is required; only the name should be", fl.Field)
			}
		}
	}
	if strings.Join(titles, ",") != "Purpose,Details,Filling" {
		t.Errorf("steps %v; with no agents to pick there is no Agents step", titles)
	}
	if !byName["name"].Required {
		t.Error("the name is not required")
	}
	if c := byName["curator_agent"]; c.Type != "hidden" || c.Default != curatorAgentID {
		t.Errorf("the curator the Filling step means is not carried: %+v", c)
	}
	var fills []string
	for _, o := range byName["fill"].Options {
		fills = append(fills, o.Value)
	}
	if strings.Join(fills, ",") != "curated,by_hand" {
		t.Errorf("Filling offers %v", fills)
	}
	for _, k := range []string{"purpose", "about", "currency", "depth", "mostly", "prefer", "avoid"} {
		if _, ok := byName[k]; !ok {
			t.Errorf("no %s question", k)
		}
	}
	if strings.Contains(string(b), "\u2014") || strings.Contains(string(b), "\u2026") {
		t.Error("an em-dash or ellipsis character in the form's copy")
	}
}

const newCollectionHarnessBefore = `
function node(tag){ var n={tagName:tag, children:[], style:{}, value:'', textContent:'', className:'',
  appendChild:function(c){this.children.push(c);return c;}, addEventListener:function(k,f){(this['_'+k]=this['_'+k]||[]).push(f);},
  setAttribute:function(){}, querySelector:function(){return null;} };
  Object.defineProperty(n,'innerHTML',{set:function(){this.children=[];},get:function(){return '';}}); return n; }
var newBtn=node('button');
global.document={createElement:node, querySelector:function(s){ if(s==='#docs-new') return newBtn; if(s==='#docs-import') return null; return node('div'); }, body:node('body'), addEventListener:function(){}};
global.window={location:{href:''}, uiAlert:function(m){ throw new Error('alert: '+m); }};
var opened=null;
window.uiOpenComponentDialog=function(o){ opened=o; };
global.fetch=function(){ return Promise.resolve({ok:true, json:function(){ return Promise.resolve({collections:[]}); }}); };
`

const newCollectionHarnessAfter = `
(newBtn._click||[]).forEach(function(f){f();});
if(!opened) throw new Error('New collection opened no modal');
if(opened.title!=='New collection') throw new Error('title '+opened.title);
var c=opened.component;
if(!c || c.type!=='form_panel' || !c.submit_label || c.assist_label!=='Draft with AI') throw new Error('the dialog holds '+JSON.stringify(c));
console.log('OK');
`
