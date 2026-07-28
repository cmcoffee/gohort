var fs = require('fs');
var src = fs.readFileSync(__dirname + '/../45_document_core.js', 'utf8');

var fetchLog = [], toasts = [], responses = {}, confirmAnswer = true;
function mkNode(cls) {
  var n = {
    className: cls || '', style: {}, textContent: '', dataset: {}, _attrs: {},
    _kids: [], _on: {}, innerHTML: '',
    classList: {
      _s: (cls||'').split(' ').filter(Boolean),
      add: function(c){ if (this._s.indexOf(c)<0) this._s.push(c); },
      remove: function(c){ var i=this._s.indexOf(c); if(i>=0) this._s.splice(i,1); },
      contains: function(c){ return this._s.indexOf(c)>=0; },
      toggle: function(c,on){ on ? this.add(c) : this.remove(c); },
    },
    setAttribute: function(k,v){ this._attrs[k]=v; },
    getAttribute: function(k){ return this._attrs[k]; },
    appendChild: function(k){ this._kids.push(k); return k; },
    addEventListener: function(n2,f){ this._on[n2]=f; },
    querySelectorAll: function(sel) {
      var out=[], want = sel.replace(/^[.\[]/,'').replace(/\]$/,'');
      (function walk(node){
        node._kids.forEach(function(k){
          if (sel[0]==='.' && k.classList.contains(want)) out.push(k);
          if (sel[0]==='[' && k._attrs[want] !== undefined) out.push(k);
          walk(k);
        });
      })(this);
      out.forEach = Array.prototype.forEach.bind(out);
      return out;
    },
  };
  Object.defineProperty(n,'innerHTML',{get:function(){return '';},set:function(){ n._kids.length=0; }});
  return n;
}
var harness = new Function('el','fetchJSON','showToast','relTime','renderBulkBar','window',
  src + '\nreturn buildDocList;');
var bulkBarCalls = [];
function build(opts) {
  return harness(
    function(tag, attrs, kids) {
      var n = mkNode(attrs && attrs.class);
      if (attrs) { for (var k in attrs) { if (k==='onclick') n._on.click=attrs[k]; else if (k!=='class') n._attrs[k]=attrs[k]; } }
      if (kids) kids.forEach(function(k){ if (typeof k!=='string') n._kids.push(k); else n.textContent += k; });
      return n;
    },
    function(url, o) { fetchLog.push((o&&o.method||'GET')+' '+url);
      return Promise.resolve(responses[url] !== undefined ? responses[url] : []); },
    function(m){ toasts.push(m); },
    function(d){ return 'ago'; },
    function(items, listEl, state, sel, idOf, reload, onDelete) {
      bulkBarCalls.push({n: items.length, mode: state.mode});
      listEl._bulkOnDelete = onDelete;
    },
    {uiConfirm: function(){ return Promise.resolve(confirmAnswer); }}
  )(opts);
}
var fail=0;
function check(l,c,e){ if(c) console.log('ok   '+l); else {fail++; console.log('FAIL '+l+(e?'  '+e:''));} }
function tick(){ return new Promise(function(r){ setImmediate(r); }); }

(async function(){
  responses['/list'] = [
    {id:'a', name:'Alpha', lang:'sql',  date:'2026-01-01'},
    {id:'c', name:'Gamma', lang:'',     date:'2026-03-01'},
    {id:'b', name:'Beta',  lang:'bash', date:'2026-02-01'},
  ];
  var host = mkNode('side');
  var opened=[], deleted=[], cur='b';
  var list = build({
    host: host, listURL:'/list', idField:'id', labelField:'name', dateField:'date',
    emptyText:'Nothing yet.', metaOf:function(it){return it.lang||'';},
    currentID:function(){return cur;}, onOpen:function(id){opened.push(id);},
    deleteURL:'/del/{id}', onDeleted:function(id){deleted.push(id);},
  });
  list.reload(); await tick();
  var rows = host.querySelectorAll('.ui-chat-side-item');
  check('renders a row per record', rows.length===3, String(rows.length));
  check('sorted newest-first', rows[0].dataset.id==='c' && rows[2].dataset.id==='a',
    rows.map(function(r){return r.dataset.id;}).join(','));
  check('active row is the open record', rows[1].classList.contains('active'));
  check('tooltip carries label + meta', rows[2]._attrs.title==='Alpha — sql · ago', rows[2]._attrs.title);
  check('empty meta omits the separator', rows[0]._attrs.title==='Gamma — ago', rows[0]._attrs.title);

  rows[0]._on.click({target:{classList:{contains:function(){return false;}}}});
  check('click opens the record', opened.length===1 && opened[0]==='c');

  // The × must not also open the row behind the confirm.
  rows[0]._on.click({target:{classList:{contains:function(c){return c==='ui-chat-side-del';}}}});
  check('delete button short-circuits open', opened.length===1);

  var before = fetchLog.length;
  await rows[2]._kids[1]._on.click({stopPropagation:function(){}});
  await tick(); await tick();
  check('delete issues DELETE', fetchLog.slice(before).some(function(u){return u==='DELETE /del/a';}),
    fetchLog.slice(before).join(' | '));
  check('onDeleted fired', deleted.indexOf('a')>=0);

  // Declining the confirm must not delete.
  confirmAnswer = false;
  before = fetchLog.length;
  var rows2 = host.querySelectorAll('.ui-chat-side-item');
  await rows2[0]._kids[1]._on.click({stopPropagation:function(){}});
  await tick();
  check('declining confirm does not delete', !fetchLog.slice(before).some(function(u){return u.indexOf('DELETE')===0;}));
  confirmAnswer = true;

  // Empty list.
  responses['/list2'] = [];
  var host2 = mkNode('side');
  var list2 = build({host:host2, listURL:'/list2', idField:'id', labelField:'name', dateField:'date', emptyText:'Nothing yet.'});
  list2.reload(); await tick();
  check('empty state rendered', host2._kids.length===1 && host2._kids[0].textContent==='Nothing yet.');

  // No deleteURL -> no × buttons.
  var host3 = mkNode('side');
  responses['/list3'] = [{id:'z', name:'Zed', date:'2026-01-01'}];
  var list3 = build({host:host3, listURL:'/list3', idField:'id', labelField:'name', dateField:'date'});
  list3.reload(); await tick();
  check('no delete button without deleteURL', host3.querySelectorAll('.ui-chat-side-item')[0]._kids.length===1);

  // Bulk mode: rows must carry data-bulk-id so select-all respects the filter.
  var host4 = mkNode('side');
  responses['/list4'] = [{id:'p', name:'P', date:'2026-01-01'},{id:'q', name:'Q', date:'2026-02-01'}];
  var st = {mode:true}, selm = {q:true, GONE:true};
  var list4 = build({
    host:host4, listURL:'/list4', idField:'id', labelField:'name', dateField:'date',
    deleteURL:'/del/{id}', currentID:function(){return null;},
    bulk:{state:st, selected:selm, confirmMany:function(n){return 'Delete '+n+'?';}},
  });
  list4.reload(); await tick();
  var brows = host4.querySelectorAll('[data-bulk-id]');
  check('rows tagged data-bulk-id in bulk mode', brows.length===2, String(brows.length));
  check('stale selection pruned', selm.GONE===undefined);
  check('selected row marked', host4.querySelectorAll('.ui-chat-side-item')[0].classList.contains('selected'));
  check('bulk bar rendered', bulkBarCalls.length>0);
  check('no × while selecting', host4.querySelectorAll('.ui-chat-side-item')[0]._kids.length===1);

  console.log(fail===0 ? '\nALL DOC-LIST TESTS PASS' : '\n'+fail+' FAILURES');
  process.exit(fail?1:0);
})();
