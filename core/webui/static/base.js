// gohort shared web UI utilities.
//
// Loaded by every app's RenderPage. Provides:
//   - Access-control hook (hides Push-to-TechWriter buttons unless caller IP allowed)
//   - Live session ribbon polling (shared across apps via /api/live)
//   - Small DOM helpers exposed on window.webui
//
// Apps append their own pipeline-specific JS via PageOpts.AppJS — keep
// this file focused on cross-app utilities.

(function(){
  'use strict';

  var webui = window.webui = window.webui || {};

  // ---------- access control ----------
  // Hide buttons with the .push-to-techwriter class unless the caller's
  // IP is in the TechWriter allowlist. Uses a CSS rule (rather than
  // querying buttons) so dynamically inserted buttons are also hidden.
  webui.checkAccess = function() {
    return fetch(location.origin + '/api' + '/access').then(function(r){return r.json()}).then(function(a){
      webui.access = a || {};
      if (!webui.access.techwriter) {
        var s = document.createElement('style');
        s.textContent = '.push-to-techwriter { display: none !important; }';
        document.head.appendChild(s);
      }
      return webui.access;
    }).catch(function(){
      webui.access = {};
      return webui.access;
    });
  };

  // ---------- live session ribbon ----------
  // Polls /api/live and renders a small fixed-position ribbon listing
  // active sessions across all apps. Apps that don't want it can hide
  // #webui-live-ribbon via CSS or call webui.disableRibbon().
  webui.ribbonEnabled = true;
  webui.disableRibbon = function(){ webui.ribbonEnabled = false; };

  function ensureRibbon() {
    var el = document.getElementById('webui-live-ribbon');
    if (el) return el;
    el = document.createElement('div');
    el.id = 'webui-live-ribbon';
    el.innerHTML = '<h4><span class="live-dot"></span>Live</h4><div class="items" style="display:none"></div>';
    document.body.appendChild(el);
    el.querySelector('h4').addEventListener('click', function(){
      var items = el.querySelector('.items');
      items.style.display = items.style.display === 'none' ? '' : 'none';
    });
    return el;
  }

  function refreshRibbon() {
    if (!webui.ribbonEnabled) return;
    fetch(location.origin + '/api' + '/live').then(function(r){return r.json()}).then(function(items){
      var el = ensureRibbon();
      var box = el.querySelector('.items');
      // Hide spawned child sessions — their parent's status already
      // reflects the current stage, so showing children multiplies
      // one logical operation into noisy rows.
      items = (items || []).filter(function(it) { return !it.spawned; });
      if (items.length === 0) {
        el.style.display = 'none';
        box.innerHTML = '';
        return;
      }
      // Sort by the per-entry order field (set by each app's
      // LiveProvider). Ties fall back to alphabetical app name.
      items.sort(function(a, b) {
        var oa = a.order || 0, ob = b.order || 0;
        if (oa !== ob) return oa - ob;
        return (a.app || '').localeCompare(b.app || '');
      });
      var html = '';
      for (var i = 0; i < items.length; i++) {
        var it = items[i];
        var badge = it.queued
          ? '<span class="badge q">Queued</span>'
          : '<span class="badge run">Running</span>';
        var app = it.app ? '<span class="badge">' + escapeHtml(it.app) + '</span>' : '';
        // Providers are expected to set `url` on each entry. Fall
        // back to the path as a safe default.
        var url = it.url || it.path || '';
        html += '<a class="item" href="' + url + '">' + app + badge
              + '<span class="label">' + escapeHtml(it.topic || it.label || 'Untitled') + '</span></a>';
      }
      box.innerHTML = html;
      el.style.display = 'block';
    }).catch(function(){});
  }

  // ---------- DOM helpers ----------
  function escapeHtml(s) {
    return String(s == null ? '' : s)
      .replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/>/g,'&gt;')
      .replace(/"/g,'&quot;').replace(/'/g,'&#39;');
  }
  webui.escapeHtml = escapeHtml;
  // Expose as a bare global too so individual apps can call escapeHtml
  // without the namespace prefix.
  if (typeof window !== 'undefined' && typeof window.escapeHtml === 'undefined') {
    window.escapeHtml = escapeHtml;
  }

  // ---------- Markdown ----------
  // Client-side markdown → HTML. Handles headers, bold/italic, bullet
  // and numbered lists, paragraphs, inline + bare links. Deliberately
  // small: apps render LLM output that's mostly prose with occasional
  // lists; a full CommonMark parser would be overkill.
  function renderMarkdown(text) {
    if (!text) return '';

    // Extract inline links before escaping so URLs survive.
    var links = [];
    text = text.replace(/\[([^\]]+)\]\(([^)]+)\)/g, function(m, label, url) {
      var idx = links.length;
      links.push({label: label, url: url});
      return '%%LINK' + idx + '%%';
    });

    var html = escapeHtml(text);

    for (var i = 0; i < links.length; i++) {
      html = html.replace('%%LINK' + i + '%%', '<a href="' + links[i].url + '" target="_blank">' + escapeHtml(links[i].label) + '</a>');
    }

    // Convert markdown table blocks to HTML tables.
    // Pipes survive escapeHtml, so this runs on already-escaped content.
    html = (function(h) {
      var lines = h.split('\n');
      var out = [];
      var i = 0;
      while (i < lines.length) {
        var t = lines[i].trim();
        if (t.charAt(0) === '|' && t.charAt(t.length - 1) === '|') {
          var rows = [];
          while (i < lines.length) {
            var lt = lines[i].trim();
            if (lt.charAt(0) === '|' && lt.charAt(lt.length - 1) === '|') { rows.push(lt); i++; }
            else break;
          }
          var sep = -1;
          for (var j = 0; j < rows.length; j++) {
            if (/^\|[\s|\-:]+\|$/.test(rows[j])) { sep = j; break; }
          }
          var tbl = '<table>';
          if (sep > 0) tbl += '<thead>';
          for (var j = 0; j < rows.length; j++) {
            if (j === sep) { tbl += '</thead><tbody>'; continue; }
            var cells = rows[j].split('|').slice(1, -1);
            var tag = (sep > 0 && j < sep) ? 'th' : 'td';
            tbl += '<tr>';
            for (var k = 0; k < cells.length; k++) tbl += '<' + tag + '>' + cells[k].trim() + '</' + tag + '>';
            tbl += '</tr>';
          }
          if (sep >= 0) tbl += '</tbody>';
          tbl += '</table>';
          out.push(tbl);
        } else { out.push(lines[i]); i++; }
      }
      return out.join('\n');
    })(html);
    html = html.replace(/(^|[^"'>])(https?:\/\/[^\s<)]+)/gm, '$1<a href="$2" target="_blank">$2</a>');

    html = html.replace(/^##### (.+)$/gm, '<h5>$1</h5>');
    html = html.replace(/^#### (.+)$/gm, '<h4>$1</h4>');
    html = html.replace(/^### (.+)$/gm, '<h3>$1</h3>');
    html = html.replace(/^## (.+)$/gm, '<h2>$1</h2>');
    html = html.replace(/^# (.+)$/gm, '<h1>$1</h1>');
    html = html.replace(/\*\*(.+?)\*\*/g, '<strong>$1</strong>');
    html = html.replace(/\*(.+?)\*/g, '<em>$1</em>');

    var lines = html.split('\n');
    var out = [];
    var inUL = false, inOL = false;
    function closeLists() {
      if (inUL) { out.push('</ul>'); inUL = false; }
      if (inOL) { out.push('</ol>'); inOL = false; }
    }
    for (var li = 0; li < lines.length; li++) {
      var line = lines[li];
      var trimmed = line.trim();
      if (trimmed === '') { closeLists(); out.push('</p><p>'); continue; }
      var bm = trimmed.match(/^[-*]\s+(.*)/);
      if (bm) {
        if (inOL) { out.push('</ol>'); inOL = false; }
        if (!inUL) { out.push('</p><ul>'); inUL = true; }
        out.push('<li>' + bm[1] + '</li>');
        continue;
      }
      var nm = trimmed.match(/^\d+\.\s+(.*)/);
      if (nm) {
        if (inUL) { out.push('</ul>'); inUL = false; }
        if (!inOL) { out.push('</p><ol>'); inOL = true; }
        out.push('<li>' + nm[1] + '</li>');
        continue;
      }
      closeLists();
      if (out.length > 0) {
        var last = out[out.length - 1];
        if (last !== '</p><p>' && last !== '</ul>' && last !== '</ol>' && !last.match(/^<\/?(ul|ol|p)>/)) {
          out.push('<br>');
        }
      }
      out.push(trimmed);
    }
    closeLists();
    html = '<p>' + out.join('') + '</p>';
    html = html.replace(/<p>\s*<\/p>/g, '');
    html = html.replace(/<\/p><ul>/g, '<ul>');
    html = html.replace(/<\/p><ol>/g, '<ol>');
    html = html.replace(/<\/ul><p>/g, '</ul><p>');
    html = html.replace(/<\/ol><p>/g, '</ol><p>');
    html = html.replace(/<p>(<h[1-5]>)/g, '$1');
    html = html.replace(/(<\/h[1-5]>)<\/p>/g, '$1');
    html = html.replace(/(<\/h[1-5]>)<br>/g, '$1');
    html = html.replace(/<br>(<h[1-5]>)/g, '</p>$1');
    html = html.replace(/<p>\s*<\/p>/g, '');
    return html;
  }
  webui.renderMarkdown = renderMarkdown;
  if (typeof window !== 'undefined' && typeof window.renderMarkdown === 'undefined') {
    window.renderMarkdown = renderMarkdown;
  }

  webui.qs  = function(sel, root) { return (root || document).querySelector(sel); };
  webui.qsa = function(sel, root) { return Array.prototype.slice.call((root || document).querySelectorAll(sel)); };

  // ---------- shared history list ----------
  // webui.historyList(opts) renders a reusable history-view shell:
  // uppercase title row, Show-Archived + Select buttons (+ any extras),
  // search input, bulk action bar, and a list of .history-item entries.
  // The rendered HTML uses inline styles so per-app CSS
  // (`.history-btn`, `.history-item`, `#history-view.select-mode`,
  // etc.) keeps styling everything without modification.
  //
  // Items can be supplied as a flat list (opts.renderItem) or rendered
  // as a tree by the caller (opts.renderTree). In tree mode the caller
  // builds the full HTML inside the .whl-list element; the shared
  // component still wires click/select/archive/delete against every
  // `.history-item[data-id]` it finds in the container, so nested
  // items work the same as flat ones.
  //
  // Responsibilities owned by the component:
  //   - Select mode toggle (adds/removes `select-mode` class on the
  //     container so each app's existing #history-view.select-mode CSS
  //     fires without modification) with highlighted Select button
  //   - Show-Archived toggle
  //   - Live search filter (opts.searchMatch) or custom opts.onFilter
  //   - Bulk Archive / Delete with direct-DOM update — stays in select
  //     mode, Select button stays highlighted, so the user can keep
  //     picking more items
  //   - Click handler: toggles selection in select-mode, otherwise
  //     calls opts.onOpen(id)
  //
  // Returns an API object with `refresh()`, `setItems(new)`,
  // `close()`, `getSelected()`, `syncSelection()`.
  webui.historyList = function(opts) {
    var container = opts.container;
    if (!container) return null;
    var state = {
      items: opts.items || [],
      selected: Object.create(null),
      selectMode: false,
      showArchived: false,
      query: '',
    };
    var itemId = opts.itemId || function(it) { return it.ID || it.id; };
    var isArchived = opts.isArchived || function() { return false; };
    var searchMatch = opts.searchMatch || function(item, q) {
      return JSON.stringify(item).toLowerCase().indexOf(q) >= 0;
    };

    function render() {
      // Header: uppercase title on the left, Show Archived + Select
      // + any extra header buttons on the right. Inline styles keep
      // per-app CSS overrides working without modification.
      var h = '<div style="display:flex;justify-content:space-between;align-items:center;margin-bottom:0.5rem">'
        + '<span style="color:#8b949e;font-size:0.85rem;text-transform:uppercase;letter-spacing:0.1em">' + escapeHtml(opts.title || 'History') + '</span>'
        + '<div style="display:flex;gap:0.4rem">'
        + '<button class="history-btn whl-archive-toggle" type="button" style="font-size:0.75rem">Show Archived</button>'
        + '<button class="history-btn whl-select-toggle" type="button">Select</button>'
        + (opts.extraHeaderButtons || '')
        + '</div></div>';
      if (opts.searchable !== false) {
        h += '<div style="margin-bottom:0.75rem">'
          + '<input class="whl-search" type="text" placeholder="' + escapeHtml(opts.searchPlaceholder || 'Search...') + '" '
          + 'style="width:100%;padding:0.5rem 0.75rem;background:#161b22;border:1px solid #30363d;border-radius:6px;color:#c9d1d9;font-size:0.85rem">'
          + '</div>';
      }
      h += '<div class="whl-bulk-bar" style="display:none;background:#161b22;border:1px solid #30363d;border-radius:6px;padding:0.5rem;margin-bottom:0.75rem;gap:0.4rem;align-items:center">'
        + '<button class="whl-bulk-delete" type="button" '
        + 'style="padding:0.4rem 0.8rem;background:#da3633;color:#fff;border:none;border-radius:4px;cursor:pointer;font-size:0.8rem;margin-right:0.4rem">Delete selected</button>'
        + '<button class="whl-bulk-archive" type="button" '
        + 'style="padding:0.4rem 0.8rem;background:transparent;color:#c9d1d9;border:1px solid #30363d;border-radius:4px;cursor:pointer;font-size:0.8rem">Archive selected</button>'
        + '</div>';
      h += '<div class="whl-list"></div>';
      container.innerHTML = h;
      var listEl = container.querySelector('.whl-list');
      if (!state.items.length) {
        listEl.innerHTML = '<div style="color:#8b949e;text-align:center;padding:1rem">' + escapeHtml(opts.emptyMessage || 'Nothing here yet.') + '</div>';
      } else if (opts.renderTree) {
        // Tree-aware mode: the app builds its own HTML (e.g., nested
        // parent/child grouping with .thread-group wrappers). Each
        // nested `.history-item[data-id]` still gets the shared
        // click / select / archive / delete / filter wiring below.
        opts.renderTree(state.items, listEl);
      } else {
        var chunks = [];
        for (var i = 0; i < state.items.length; i++) {
          var item = state.items[i];
          var id = itemId(item);
          var archAttr = isArchived(item) ? ' data-archived="true"' : '';
          chunks.push('<div class="history-item" data-id="' + escapeHtml(String(id)) + '"' + archAttr + '>'
            + opts.renderItem(item) + '</div>');
        }
        listEl.innerHTML = chunks.join('');
      }
      wire();
      applyFilters();
      if (opts.onAfterRender) opts.onAfterRender(api);
    }

    function wire() {
      var archiveToggleBtn = container.querySelector('.whl-archive-toggle');
      var selectToggleBtn = container.querySelector('.whl-select-toggle');
      var searchInput = container.querySelector('.whl-search');
      var bulkArchiveBtn = container.querySelector('.whl-bulk-archive');
      var bulkDeleteBtn = container.querySelector('.whl-bulk-delete');
      if (archiveToggleBtn) archiveToggleBtn.onclick = toggleShowArchived;
      if (selectToggleBtn) selectToggleBtn.onclick = toggleSelectMode;
      if (searchInput) searchInput.oninput = function() {
        state.query = (this.value || '').toLowerCase().trim();
        applyFilters();
      };
      if (bulkArchiveBtn) bulkArchiveBtn.onclick = archiveSelected;
      if (bulkDeleteBtn) bulkDeleteBtn.onclick = deleteSelected;

      var items = container.querySelectorAll('.history-item[data-id]');
      for (var i = 0; i < items.length; i++) {
        (function(itemDiv) {
          itemDiv.onclick = function(e) {
            if (e.target.closest('button')) return;
            // Thread-toggle arrows (tree-view apps) manage their own
            // click via stopPropagation — this guard keeps an
            // accidental bubble from toggling selection.
            if (e.target.classList.contains('thread-toggle')) return;
            if (container.classList.contains('select-mode')) {
              toggleItemSelected(itemDiv);
            } else if (opts.onOpen) {
              opts.onOpen(itemDiv.getAttribute('data-id'));
            }
          };
        })(items[i]);
      }
    }

    function toggleItemSelected(itemDiv) {
      var id = itemDiv.getAttribute('data-id');
      if (itemDiv.classList.contains('hi-selected')) {
        itemDiv.classList.remove('hi-selected');
        delete state.selected[id];
      } else {
        itemDiv.classList.add('hi-selected');
        state.selected[id] = true;
      }
      if (opts.onSelectToggle) {
        opts.onSelectToggle(id, !!state.selected[id], itemDiv, container);
      }
      syncSelectedFromDom();
      updateBulkBar();
    }

    // syncSelectedFromDom rebuilds state.selected from the current DOM
    // `.hi-selected` set. Needed after opts.onSelectToggle performs
    // cascading selection (tree-view apps that select a parent's
    // descendants together) so the bulk bar count matches what's
    // visually highlighted.
    function syncSelectedFromDom() {
      state.selected = Object.create(null);
      var selNodes = container.querySelectorAll('.history-item.hi-selected[data-id]');
      for (var i = 0; i < selNodes.length; i++) {
        state.selected[selNodes[i].getAttribute('data-id')] = true;
      }
    }

    function toggleSelectMode() {
      state.selectMode = !state.selectMode;
      var btn = container.querySelector('.whl-select-toggle');
      // Toggle `select-mode` on the OUTER container (same element the
      // app's existing `#history-view.select-mode .history-item ...`
      // CSS targets). This keeps the app's per-page rules working
      // without modification.
      if (state.selectMode) {
        container.classList.add('select-mode');
        if (btn) btn.style.cssText = 'background:#58a6ff;color:#fff;border-color:#58a6ff';
      } else {
        container.classList.remove('select-mode');
        if (btn) btn.style.cssText = '';
        state.selected = Object.create(null);
        var selected = container.querySelectorAll('.history-item.hi-selected');
        for (var i = 0; i < selected.length; i++) selected[i].classList.remove('hi-selected');
      }
      updateBulkBar();
    }

    function toggleShowArchived() {
      state.showArchived = !state.showArchived;
      var btn = container.querySelector('.whl-archive-toggle');
      if (btn) btn.textContent = state.showArchived ? 'Hide Archived' : 'Show Archived';
      applyFilters();
    }

    function applyFilters() {
      var items = container.querySelectorAll('.history-item[data-id]');
      var q = state.query;
      for (var i = 0; i < items.length; i++) {
        var it = items[i];
        var id = it.getAttribute('data-id');
        var record = null;
        for (var j = 0; j < state.items.length; j++) {
          if (String(itemId(state.items[j])) === String(id)) { record = state.items[j]; break; }
        }
        // Show Archived ON  → show only archived items
        // Show Archived OFF → show only non-archived (active) items
        var isArchived = it.hasAttribute('data-archived');
        var showForArchive = state.showArchived ? isArchived : !isArchived;
        var showForQuery = !q || (record && searchMatch(record, q));
        it.style.display = (showForArchive && showForQuery) ? '' : 'none';
      }
      // Let the caller react to filter changes (e.g., show/hide thread
      // groupings in tree-view apps).
      if (opts.onFilter) opts.onFilter(q, state.showArchived, container);
    }

    function updateBulkBar() {
      var count = Object.keys(state.selected).length;
      var bar = container.querySelector('.whl-bulk-bar');
      var archiveBtn = container.querySelector('.whl-bulk-archive');
      var deleteBtn = container.querySelector('.whl-bulk-delete');
      if (bar) bar.style.display = (count > 0 && state.selectMode) ? 'flex' : 'none';
      if (archiveBtn) archiveBtn.textContent = 'Archive selected (' + count + ')';
      if (deleteBtn) deleteBtn.textContent = 'Delete selected (' + count + ')';
      if (opts.onSelectionChange) opts.onSelectionChange(Object.keys(state.selected));
    }

    function archiveSelected() {
      var ids = Object.keys(state.selected);
      if (ids.length === 0 || !opts.archiveURL) return;
      if (!confirm('Archive ' + ids.length + ' item' + (ids.length > 1 ? 's' : '') + '? (Toggle Show Archived to unarchive later.)')) return;
      Promise.all(ids.map(function(id) {
        return fetch(opts.archiveURL(id), {method: 'POST'})
          .then(function(r) { return r.json(); })
          .then(function(d) { return {id: id, archived: !!d.archived}; })
          .catch(function() { return {id: id, archived: null}; });
      })).then(function(results) {
        for (var i = 0; i < results.length; i++) {
          var r = results[i];
          if (r.archived === null) continue;
          var div = container.querySelector('.history-item[data-id="' + cssId(r.id) + '"]');
          if (!div) continue;
          if (r.archived) {
            div.setAttribute('data-archived', 'true');
            if (!state.showArchived) div.style.display = 'none';
          } else {
            div.removeAttribute('data-archived');
          }
          div.classList.remove('hi-selected');
        }
        state.selected = Object.create(null);
        updateBulkBar();
      });
    }

    function deleteSelected() {
      var ids = Object.keys(state.selected);
      if (ids.length === 0 || !opts.deleteURL) return;
      if (!confirm('Delete ' + ids.length + ' item' + (ids.length > 1 ? 's' : '') + '?')) return;
      Promise.all(ids.map(function(id) {
        return fetch(opts.deleteURL(id), {method: 'DELETE'}).catch(function() {});
      })).then(function() {
        for (var i = 0; i < ids.length; i++) {
          var div = container.querySelector('.history-item[data-id="' + cssId(ids[i]) + '"]');
          if (div) div.remove();
        }
        if (opts.onAfterDelete) opts.onAfterDelete(ids, container);
        state.selected = Object.create(null);
        updateBulkBar();
      });
    }

    // cssId escapes an id for use in a [data-id="..."] selector.
    // IDs are usually UUIDs with no special chars, but be defensive.
    function cssId(id) {
      return String(id).replace(/(["\\])/g, '\\$1');
    }

    var api = {
      refresh: render,
      setItems: function(newItems) { state.items = newItems || []; state.selected = Object.create(null); render(); },
      close: function() { container.innerHTML = ''; },
      getSelected: function() { return Object.keys(state.selected); },
      syncSelection: syncSelectedFromDom,
      exitSelectMode: function() { if (state.selectMode) toggleSelectMode(); },
    };
    render();
    return api;
  };

  // ---------- bootstrap ----------
  function init() {
    webui.checkAccess();
    refreshRibbon();
    setInterval(refreshRibbon, 10000);
  }

  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', init);
  } else {
    init();
  }
})();
