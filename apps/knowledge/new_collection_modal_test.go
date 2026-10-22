package knowledge

// New collection opens in the framework's modal (uiOpenModal) with the
// framework's form in it, so Create, the redirect to the new collection and
// the ✨ are the form's own. Driven, not read: an earlier conversion named
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
	form := strings.TrimSuffix(strings.TrimPrefix(newCollectionFormScript(), "<script>"), "</script>")
	script := newCollectionHarnessBefore + form + documentsListAssets[i+len("<script>"):j] + newCollectionHarnessAfter
	tmp := filepath.Join(t.TempDir(), "new_collection.js")
	if err := os.WriteFile(tmp, []byte(script), 0644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("node", tmp).CombinedOutput(); err != nil || !strings.Contains(string(out), "OK") {
		t.Fatalf("New collection:\n%s", out)
	}
}

// The form: it creates through the collections API and opens what it made,
// and its ✨ is Draft with AI, starting on the setting up.
func TestTheNewCollectionFormDraftsWithAI(t *testing.T) {
	b, err := json.Marshal(newCollectionForm())
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		Type        string `json:"type"`
		PostURL     string `json:"post_url"`
		SubmitLabel string `json:"submit_label"`
		RedirectURL string `json:"redirect_url"`
		Assist      bool   `json:"assist"`
		AssistLabel string `json:"assist_label"`
		AssistAsk   string `json:"assist_ask"`
		Fields      []struct {
			Field    string `json:"field"`
			Required bool   `json:"required"`
			Help     string `json:"help"`
		} `json:"fields"`
	}
	json.Unmarshal(b, &f)
	if f.Type != "form_panel" || f.PostURL != "/orchestrate/api/collections" || f.SubmitLabel != "Create" || f.RedirectURL != "/knowledge/c/{id}" {
		t.Errorf("the form should create and open the collection: %s", b)
	}
	if !f.Assist || f.AssistLabel != "Draft with AI" || f.AssistAsk != "Assist me with setting up a knowledge store." {
		t.Errorf("its ✨ should be Draft with AI, opening on the setting up: %s", b)
	}
	if len(f.Fields) != 2 || f.Fields[0].Field != "name" || !f.Fields[0].Required || f.Fields[1].Field != "description" || !strings.Contains(f.Fields[1].Help, "Auto-fill") {
		t.Errorf("name (required) and a description whose help says what it steers: %s", b)
	}
	if strings.Contains(string(b), "—") {
		t.Error("an em-dash in the form's copy, which the helper reads as its prompt")
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
var opened=null, mounted=null;
window.uiOpenModal=function(o){ opened=o; var body=node('div'); o.mount(body, {close:function(){}}); opened.body=body; };
window.uiMountComponent=function(cfg, host){ mounted={cfg:cfg, host:host}; };
global.fetch=function(){ return Promise.resolve({ok:true, json:function(){ return Promise.resolve({collections:[]}); }}); };
`

const newCollectionHarnessAfter = `
(newBtn._click||[]).forEach(function(f){f();});
if(!opened) throw new Error('New collection opened no modal');
if(opened.title!=='New collection') throw new Error('title '+opened.title);
if(opened.actions) throw new Error('the form has its own Create; the modal keeps only Close');
if(!mounted || mounted.host!==opened.body) throw new Error('the form was not mounted in the modal');
if(mounted.cfg.type!=='form_panel' || mounted.cfg.assist_label!=='Draft with AI') throw new Error('mounted '+JSON.stringify(mounted.cfg));
console.log('OK');
`
