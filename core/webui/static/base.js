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
      if (!items || items.length === 0) {
        el.style.display = 'none';
        box.innerHTML = '';
        return;
      }
      // Sort: parent processes first, child processes after.
      var parentApps = {'Auto Blog': 1, 'Blogger': 2, 'Deep Research': 3, 'Debate': 4};
      items.sort(function(a, b) {
        return (parentApps[a.app] || 99) - (parentApps[b.app] || 99);
      });
      var html = '';
      for (var i = 0; i < items.length; i++) {
        var it = items[i];
        var badge = it.queued
          ? '<span class="badge q">Queued</span>'
          : '<span class="badge run">Running</span>';
        var app = it.app ? '<span class="badge">' + escapeHtml(it.app) + '</span>' : '';
        var url = it.url || '';
        if (!url) {
          var path = it.path || '';
          url = path + '/';
          if (path.indexOf('?') >= 0) {
            url = path;
          } else if (it.id) {
            var key = it.app === 'Debate' ? 'debate' : (it.app === 'Deep Research' ? 'research' : '');
            if (key) url = path + '/?' + key + '=' + encodeURIComponent(it.id);
          }
        }
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

  webui.qs  = function(sel, root) { return (root || document).querySelector(sel); };
  webui.qsa = function(sel, root) { return Array.prototype.slice.call((root || document).querySelectorAll(sel)); };

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
