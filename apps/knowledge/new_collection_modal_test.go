package knowledge

// New collection opens in the framework's modal (uiOpenModal) rather than an
// overlay the page built itself. Driven, not read: the conversion once named
// the modal's handle "api", which hid the page's api() URL helper, so Create
// threw and sent nothing, with nothing on screen to say so.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestNewCollectionModalCreates(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not installed")
	}
	i := strings.Index(documentsListAssets, "<script>")
	j := strings.LastIndex(documentsListAssets, "</script>")
	if i < 0 || j < i {
		t.Fatal("the collections page has no script")
	}
	script := newCollectionHarnessBefore + documentsListAssets[i+len("<script>"):j] + newCollectionHarnessAfter
	tmp := filepath.Join(t.TempDir(), "new_collection.js")
	if err := os.WriteFile(tmp, []byte(script), 0644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("node", tmp).CombinedOutput(); err != nil || !strings.Contains(string(out), "OK") {
		t.Fatalf("New collection:\n%s", out)
	}
}

const newCollectionHarnessBefore = `
var handlers={}, els={};
function node(tag){ var n={tagName:tag, children:[], style:{}, value:'', textContent:'', className:'', disabled:false,
  appendChild:function(c){this.children.push(c);return c;}, addEventListener:function(k,f){(this['_'+k]=this['_'+k]||[]).push(f);},
  setAttribute:function(){}, querySelector:function(){return null;}, remove:function(){}, focus:function(){} };
  Object.defineProperty(n,'innerHTML',{set:function(){this.children=[];},get:function(){return '';}}); return n; }
var newBtn=node('button');
global.document={createElement:node, querySelector:function(s){ if(s==='#docs-new') return newBtn; if(s==='#docs-import') return null; return node('div'); }, body:node('body'), addEventListener:function(){}};
global.window={location:{href:''}, uiAlert:function(m){ throw new Error('alert: '+m); }};
var opened=null, closed=false;
window.uiOpenModal=function(o){ opened=o; var body=node('div'); o.mount(body, {close:function(){closed=true;}}); opened.body=body; return {close:function(){closed=true;}}; };
var fetched=[];
global.fetch=function(url,opts){ fetched.push({url:url,opts:opts}); return Promise.resolve({ok:true, json:function(){ return Promise.resolve(url.indexOf('/api/collections')>=0 && opts && opts.method==='POST' ? {id:'c1'} : []); }, text:function(){return Promise.resolve('');}}); };
`

const newCollectionHarnessAfter = `
(newBtn._click||[]).forEach(function(f){f();});
if(!opened) throw new Error('New collection opened no modal');
if(opened.title!=='New collection') throw new Error('title '+opened.title);
function find(n,tag){ if(n.tagName===tag) return n; for(var i=0;i<n.children.length;i++){var f=find(n.children[i],tag); if(f) return f;} return null; }
var name=find(opened.body,'input'); name.value='Runbooks';
var desc=find(opened.body,'textarea'); desc.value='ops docs';
var create=opened.actions.filter(function(a){return a.primary;})[0];
var mclosed=false, btn={disabled:false};
create.onClick({close:function(){mclosed=true;}}, btn);
setTimeout(function(){
  var post=fetched.filter(function(f){return f.opts&&f.opts.method==='POST';})[0];
  if(!post) throw new Error('Create sent nothing');
  if(post.url!=='/orchestrate/api/collections') throw new Error('Create went to '+post.url);
  if(JSON.parse(post.opts.body).name!=='Runbooks') throw new Error('wrong body '+post.opts.body);
  if(!mclosed) throw new Error('modal not closed after create');
  if(window.location.href!=='/knowledge/c/c1') throw new Error('no redirect: '+window.location.href);
  console.log('OK');
},20);
`
