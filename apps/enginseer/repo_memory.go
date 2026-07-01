package enginseer

import (
	"net/http"
	"strings"

	"github.com/cmcoffee/gohort/apps/orchestrate"
)

// repoMemoryModalScript mounts the SHARED editable Memory modal for a repo's
// scope — the same component servitor uses (orchestrate.AgentMemoryModalScript),
// pointed at this app's per-repo endpoints. Shows the code map (graph),
// Reference Memory, and Explicit facts the investigator accumulates.
var repoMemoryModalScript = orchestrate.AgentMemoryModalScript("enginseer_repo_memory", "enginseerMemBase()")

// enginseerHeadScript defines the JS the shared modal needs: getRepoID reads the
// repo picker; enginseerMemBase returns the selected repo's endpoint prefix (or
// null → the modal alerts and aborts). Mirrors servitor's servitorMemBase.
const enginseerHeadScript = `<script>
(function(){
  function getRepoID(){
    var sel = document.querySelector('.ui-agent-extras select');
    return sel ? sel.value : '';
  }
  function say(msg){ (window.uiAlert || window.alert)(msg); }
  window.getRepoID = getRepoID;
  window.enginseerMemBase = function(){
    var id = getRepoID();
    if(!id){ say('Pick a repository first'); return null; }
    return 'api/repos/' + encodeURIComponent(id) + '/';
  };
  function register(){
    if(!window.uiRegisterClientAction){ setTimeout(register, 50); return; }
    window.uiRegisterClientAction('enginseer_clear', function(ctx){
      if(ctx.clearConvo) ctx.clearConvo();
      if(ctx.clearActivity) ctx.clearActivity();
    });
    window.uiRegisterClientAction('enginseer_refresh', function(){
      var id = getRepoID();
      if(!id){ say('Pick a repository first'); return; }
      fetch('api/repos/' + encodeURIComponent(id) + '/refresh', {method:'POST'})
        .then(function(r){
          if(r.ok || r.status===202){ say('Re-cloning the repository — this may take a moment.'); }
          else { say('Refresh failed'); }
        }).catch(function(e){ say('Refresh failed: ' + (e && e.message || e)); });
    });
    window.uiRegisterClientAction('enginseer_map_repo', async function(){
      var id = getRepoID();
      if(!id){ say('Pick a repository first'); return; }
      if(!(await (window.uiConfirm ? window.uiConfirm('Map this repository\'s architecture? The Enginseer walks the code and builds the map in the background — watch it appear under Memory → Graph Memory.') : Promise.resolve(true)))) return;
      fetch('api/map', {method:'POST', headers:{'Content-Type':'application/json'}, body: JSON.stringify({repo_id:id})})
        .then(function(r){
          if(r.ok || r.status===202){ say('Mapping the repository — its structure will appear under Memory shortly.'); }
          else { return r.text().then(function(t){ say('Map failed: ' + t); }); }
        }).catch(function(e){ say('Map failed: ' + (e && e.message || e)); });
    });
  }
  register();
})();
</script>`

// handleRepoMemory routes /api/repos/<id>/<suffix> to orchestrate's per-scope
// memory handlers, scoped to the repo (app:enginseer:<id>). The caller
// (handleRepoOne) has already verified the repo belongs to the requesting user;
// the orchestrate handlers add a session gate on top.
func (T *Enginseer) handleRepoMemory(w http.ResponseWriter, r *http.Request, repoID, suffix string) {
	orch := findOrchestrate()
	if orch == nil {
		http.Error(w, "orchestrate unavailable", http.StatusServiceUnavailable)
		return
	}
	scope := repoScope(repoID)
	id := repoInvestigatorAgentID
	switch {
	case suffix == "facts":
		orch.PublicHandleAgentFactsForScope(w, r, scope, id)
	case suffix == "graph":
		orch.PublicHandleAgentGraphForScope(w, r, scope, id)
	case suffix == "graph/edge":
		orch.PublicHandleAgentGraphEdgeDeleteForScope(w, r, scope, id)
	case strings.HasPrefix(suffix, "graph/entity/"):
		sub := strings.TrimPrefix(suffix, "graph/entity/")
		switch {
		case strings.HasSuffix(sub, "/attr"):
			orch.PublicHandleAgentGraphAttrDeleteForScope(w, r, scope, id, strings.TrimSuffix(sub, "/attr"))
		case strings.HasSuffix(sub, "/alias"):
			orch.PublicHandleAgentGraphAliasDeleteForScope(w, r, scope, id, strings.TrimSuffix(sub, "/alias"))
		default:
			orch.PublicHandleAgentGraphEntityDeleteForScope(w, r, scope, id, sub)
		}
	case suffix == "inferred":
		orch.PublicHandleAgentInferredListForScope(w, r, scope, id)
	case strings.HasPrefix(suffix, "inferred/"):
		orch.PublicHandleAgentInferredDeleteForScope(w, r, scope, id, strings.TrimPrefix(suffix, "inferred/"))
	case suffix == "knowledge/auto-inferred":
		orch.PublicHandleAgentKnowledgeAutoInferredWipeForScope(w, r, scope, id)
	case suffix == "agent":
		orch.PublicHandleAgentRecordForScope(w, r, scope, id)
	default:
		http.NotFound(w, r)
	}
}
