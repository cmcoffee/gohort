
  function mountComponent(cfg, parent, ctx) {
    if (!cfg) return;
    var fn = components[cfg.type];
    if (!fn) {
      parent.appendChild(el('div', {class: 'ui-card', text: 'Unknown component: ' + cfg.type}));
      return;
    }
    // ctx is the parent record (when mounted inside an Expand) — lets
    // nested components read the row data without a redundant fetch.
    parent.appendChild(fn(cfg, ctx));
  }

  // Public entry so app-specific JS (client actions, modals) can mount a
  // declared component into any host — e.g. drop a chip_picker into a
  // uiOpenSimpleModal body instead of hand-rolling the DOM.
  window.uiMountComponent = mountComponent;

  // --- Pull-to-refresh: shared across all Tables on the page -----------
  var ptrCallbacks = [];
  function setupPTR(cb) { ptrCallbacks.push(cb); }
  (function() {
    var indicator = el('div', {class: 'ui-ptr'}, [el('span', {class: 'ui-spinner'}), 'Refreshing…']);
    document.body.appendChild(indicator);
    var startY = 0, pulling = false, triggered = false;
    var THRESHOLD = 70;
    document.addEventListener('touchstart', function(e) {
      if (window.scrollY > 0) { pulling = false; return; }
      startY = e.touches[0].clientY; pulling = true; triggered = false;
    }, {passive: true});
    document.addEventListener('touchmove', function(e) {
      if (!pulling) return;
      var dy = e.touches[0].clientY - startY;
      if (dy > THRESHOLD && !triggered) {
        triggered = true; indicator.classList.add('show');
      }
    }, {passive: true});
    document.addEventListener('touchend', function() {
      if (triggered) {
        ptrCallbacks.forEach(function(cb){ cb(); });
        setTimeout(function(){ indicator.classList.remove('show'); }, 600);
      }
      pulling = false; triggered = false;
    }, {passive: true});
  })();

  // --- Page mount ------------------------------------------------------
  function mount() {
    var configEl = document.getElementById('ui-config');
    if (!configEl) return;
    var cfg;
    try { cfg = JSON.parse(configEl.textContent); }
    catch (e) {
      document.getElementById('ui-root').textContent = 'UI config parse error: ' + e.message;
      return;
    }

    var root = document.getElementById('ui-root');
    if (cfg.max_width) root.style.maxWidth = cfg.max_width;

    // Page header — back link + visible title. Renders above any
    // sticky bar so the back-arrow is the very first thing on the
    // page, easy to reach without scrolling.
    if (cfg.back_url || cfg.show_title || (cfg.nav && cfg.nav.length)) {
      var header = el('div', {class: 'ui-page-header'});
      if (cfg.back_url) {
        header.appendChild(el('a', {class: 'ui-back-link', href: cfg.back_url, title: 'Back'}, ['← Back']));
      }
      if (cfg.show_title && cfg.title) {
        header.appendChild(el('h1', {class: 'ui-page-title'}, [cfg.title]));
      }
      // Top tabs — Page.Nav rendered inline on the header row (same line as the
      // back link): a shared page menu for a multi-page app, active page
      // highlighted. Scrolls horizontally on narrow screens rather than wrapping.
      if (cfg.nav && cfg.nav.length) {
        var tabs = el('nav', {class: 'ui-page-tabs'});
        cfg.nav.forEach(function(it) {
          tabs.appendChild(el('a', {
            class: 'ui-page-tab' + (it.active ? ' active' : ''),
            href: it.url || '#',
          }, [it.label || '']));
        });
        header.appendChild(tabs);
      }
      // Live-sessions pill — polls /api/live every 10s and shows a
      // running/queued count badge with a click-through popover. Lets
      // operators see at a glance from any framework page that
      // background work is in flight, plus jump straight to it. Skipped on
      // public pages (cfg.public): an anonymous surface has no session, so the
      // poll would just 302 to login, and the cross-user view isn't for it.
      if (!cfg.public) {
      var liveWrap = el('div', {class: 'ui-live-pill-wrap'});
      // Pill content matches legacy: glowing dot + "LIVE" label.
      // The dropdown lists each session with its app + state, which
      // is where the count is visible — the pill itself stays terse
      // ("LIVE" reads at a glance, a number doesn't).
      var liveBtn = el('button', {class: 'ui-live-pill', title: 'Active sessions across all apps', style: 'display:none'},
        [el('span', {class: 'ui-live-dot'}), el('span', {class: 'ui-live-text'}, ['LIVE'])]);
      var liveMenu = el('div', {class: 'ui-live-menu', style: 'display:none'});
      liveWrap.appendChild(liveBtn);
      liveWrap.appendChild(liveMenu);
      header.appendChild(liveWrap);
      var liveItems = [];
      function renderLiveMenu() {
        liveMenu.innerHTML = '';
        if (!liveItems.length) {
          liveMenu.appendChild(el('div', {class: 'ui-live-empty'}, ['No active sessions.']));
          return;
        }
        liveItems.forEach(function(it) {
          var row = el('a', {
            class: 'ui-live-item' + (it.queued ? ' queued' : ' running'),
            href: it.url || '#',
          });
          row.appendChild(el('span', {class: 'ui-live-app'}, [it.app || '?']));
          row.appendChild(el('span', {class: 'ui-live-state'}, [it.queued ? 'Queued' : 'Running']));
          row.appendChild(el('span', {class: 'ui-live-label'}, [it.topic || it.label || 'Untitled']));
          liveMenu.appendChild(row);
        });
      }
      function refreshLive() {
        fetch('/api/live').then(function(r){ return r.json(); }).then(function(items) {
          items = (items || []).filter(function(it){ return !it.spawned; });
          liveItems = items;
          var n = items.length;
          if (n === 0) {
            liveBtn.style.display = 'none';
            liveMenu.style.display = 'none';
            return;
          }
          liveBtn.style.display = '';
          // Class encodes the state so CSS can paint the dot color —
          // green if anything is running, amber if all are queued.
          var anyRunning = items.some(function(it){ return !it.queued; });
          liveBtn.classList.toggle('running', anyRunning);
          liveBtn.classList.toggle('queued',  !anyRunning);
          if (liveMenu.style.display !== 'none') renderLiveMenu();
        }).catch(function(){});
      }
      liveBtn.addEventListener('click', function(ev) {
        ev.stopPropagation();
        if (liveMenu.style.display === 'none') {
          renderLiveMenu();
          liveMenu.style.display = '';
        } else {
          liveMenu.style.display = 'none';
        }
      });
      document.addEventListener('click', function(ev) {
        if (liveMenu.style.display === 'none') return;
        if (!liveWrap.contains(ev.target)) liveMenu.style.display = 'none';
      });
      refreshLive();
      setInterval(refreshLive, 10000);
      } // end if(!cfg.public) — no live-sessions pill on public pages
      // Update the document title in case the rendered title differs.
      if (cfg.title) document.title = cfg.title;
      // Insert the header OUTSIDE #ui-root (a sibling above it) so the bar spans
      // the full viewport width while the content column below stays centered at
      // cfg.max_width. (Appending into root would constrain the bar to the narrow
      // column.)
      if (root.parentNode) root.parentNode.insertBefore(header, root);
      else root.appendChild(header);
    }

    if (cfg.sticky) mountComponent(cfg.sticky, root);

    // Section layout. Three modes, combinable:
    //  - tabbed (cfg.tabbed): a top button bar of the distinct section
    //    groups; each group is a panel shown one at a time. A panel is
    //    itself a grid when cfg.grid.
    //  - grid (cfg.grid): one responsive 2-col grid (1 col on mobile);
    //    Wide sections span full width.
    //  - plain: stacked directly on root.
    var inGrid = !!cfg.grid;
    var tabbed = !!cfg.tabbed;
    var sectionsHost = root;        // non-tabbed host
    var groupHosts = {};            // group name -> mount host (tabbed)
    var secNav = !!cfg.section_nav; // left-rail sub-nav of a group's sections
    // buildSecNav renders a left rail of section titles into mountEl; one
    // section is shown at a time. Each section stashes its own mount host
    // (s.__host) so hostForSection routes to the right sub-panel. Used both
    // inside a tab (a group's sections) and at page level on a non-tabbed page
    // (all sections form a single rail).
    function buildSecNav(mountEl, secs) {
      var rail = el('div', {class: 'ui-secnav-rail'});
      var content = el('div', {class: 'ui-secnav-content'});
      mountEl.appendChild(el('div', {class: 'ui-secnav'}, [rail, content]));
      var subPanels = [];
      secs.forEach(function(s, si) {
        var sp = el('div', {class: 'ui-secnav-panel' + (si === 0 ? '' : ' ui-tab-hidden')});
        if (inGrid) { var sg = el('div', {class: 'ui-section-grid'}); sp.appendChild(sg); s.__host = sg; }
        else { s.__host = sp; }
        content.appendChild(sp);
        subPanels.push(sp);
        var ib = el('button', {type: 'button', class: 'ui-secnav-item' + (si === 0 ? ' active' : '')}, [s.title || ('Section ' + (si + 1))]);
        ib.addEventListener('click', function() {
          for (var k = 0; k < subPanels.length; k++) subPanels[k].classList.toggle('ui-tab-hidden', k !== si);
          var items = rail.querySelectorAll('.ui-secnav-item');
          for (var m = 0; m < items.length; m++) items[m].classList.remove('active');
          ib.classList.add('active');
        });
        rail.appendChild(ib);
      });
    }
    if (tabbed) {
      var order = [], seenG = {}, secByGroup = {};
      (cfg.sections || []).forEach(function(s) {
        var g = s.group || 'General';
        if (!seenG[g]) { seenG[g] = true; order.push(g); secByGroup[g] = []; }
        secByGroup[g].push(s);
      });
      var tabbar = el('div', {class: 'ui-tabbar'});
      root.appendChild(tabbar);
      var panels = [];
      order.forEach(function(g, idx) {
        var panel = el('div', {class: 'ui-tabpanel' + (idx === 0 ? '' : ' ui-tab-hidden')});
        if (secNav && secByGroup[g].length > 1) {
          buildSecNav(panel, secByGroup[g]);
        } else {
          var host = panel;
          if (inGrid) { host = el('div', {class: 'ui-section-grid'}); panel.appendChild(host); }
          groupHosts[g] = host;
        }
        panels.push(panel);
        var btn = el('button', {type: 'button', class: 'ui-tab' + (idx === 0 ? ' active' : '')}, [g]);
        btn.addEventListener('click', function() {
          for (var i = 0; i < panels.length; i++) panels[i].classList.toggle('ui-tab-hidden', i !== idx);
          var tabs = tabbar.querySelectorAll('.ui-tab');
          for (var j = 0; j < tabs.length; j++) tabs[j].classList.remove('active');
          btn.classList.add('active');
        });
        tabbar.appendChild(btn);
        root.appendChild(panel);
      });
    } else if (secNav && (cfg.sections || []).length > 1) {
      // Page-level side-nav: no top tabs (a single conceptual area), just one
      // rail of all sections. Fits a flat management surface better than a long
      // scroll of stacked panels.
      buildSecNav(root, cfg.sections || []);
    } else if (inGrid) {
      sectionsHost = el('div', {class: 'ui-section-grid'});
      root.appendChild(sectionsHost);
    }
    function hostForSection(s) {
      if (s.__host) return s.__host;
      if (tabbed) return groupHosts[s.group || 'General'] || sectionsHost;
      return sectionsHost;
    }
    (cfg.sections || []).forEach(function(s) {
      var host = hostForSection(s);
      // NoChrome sections skip the card wrapper — body mounts directly
      // with no padding/bg/border. Used when the contained component
      // (e.g. ChatPanel) manages its own layout and a card would just
      // create double-nested boxes. In grid mode they ride a full-width
      // slot so page order is preserved.
      if (s.no_chrome) {
        if (inGrid) {
          var ncWrap = el('div', {class: 'ui-section-wide'});
          if (s.body) mountComponent(s.body, ncWrap);
          host.appendChild(ncWrap);
        } else if (s.body) {
          mountComponent(s.body, host);
        }
        return;
      }
      var section = el('div', {class: 'ui-section'});
      // Collapsible — when the section is declared with Collapsed:true
      // and HAS a title, render the title bar clickable with a caret
      // that hides/shows the subtitle + body. Without a title there's
      // nothing to click, so the flag is silently ignored.
      var collapsed = !!s.collapsed && !!s.title;
      var caret = null;
      var inner = el('div', {class: 'ui-section-inner'});
      if (s.title) {
        var headerWrap = el('div', {class: 'ui-section-h'}, [
          el('span', {text: s.title}),
          el('span', {class: 'ui-section-h-r'}),
        ]);
        if (collapsed) {
          headerWrap.style.cursor = 'pointer';
          headerWrap.style.userSelect = 'none';
          caret = document.createElement('span');
          caret.style.cssText = 'margin-right:0.4rem;display:inline-block;color:var(--text-mute);transition:transform 0.15s';
          caret.textContent = String.fromCharCode(9656); // ▸
          headerWrap.insertBefore(caret, headerWrap.firstChild);
        }
        section.appendChild(headerWrap);
        if (collapsed) {
          headerWrap.addEventListener('click', function(ev) {
            // Ignore clicks on the saving-indicator slot (.ui-section-h-r)
            // and any interactive controls a future caller might land there.
            if (ev.target && ev.target.closest && ev.target.closest('.ui-section-h-r')) return;
            var open = inner.style.display === 'none';
            inner.style.display = open ? '' : 'none';
            caret.style.transform = open ? 'rotate(90deg)' : '';
          });
        }
      }
      if (s.subtitle) inner.appendChild(el('div', {class: 'ui-section-sub'}, [s.subtitle]));
      if (s.body) mountComponent(s.body, inner);
      if (collapsed) inner.style.display = 'none';
      section.appendChild(inner);
      if (inGrid && s.wide) section.classList.add('ui-section-wide');
      host.appendChild(section);
    });

    // Masonry packing for grid sections. Plain CSS grid aligns every row to its
    // tallest card, leaving holes under shorter cards (the "missing puzzle pieces"
    // look). We give the grid a fine row track (the .ui-masonry CSS above) and set
    // each card's row span to ceil(height / track), so cards pack directly under
    // the one above. Only at >=2 columns; single-column (mobile) clears the spans.
    if (inGrid) {
      var masonryGrids = Array.prototype.slice.call(root.querySelectorAll('.ui-section-grid'));
      masonryGrids.forEach(function(g) { g.classList.add('ui-masonry'); });
      var layoutMasonry = function(grid) {
        if (grid.offsetParent === null) return; // hidden (inactive tab) — reruns when shown
        var cs = getComputedStyle(grid);
        var cols = cs.gridTemplateColumns.split(' ').filter(Boolean).length;
        var kids = Array.prototype.slice.call(grid.children);
        if (cols < 2) { kids.forEach(function(c) { c.style.gridRowEnd = ''; }); return; }
        var rowH = parseFloat(cs.gridAutoRows) || 1;
        var gap = parseFloat(cs.rowGap) || 0;
        // Reset, measure all, then assign — avoids interleaved read/write thrash
        // and the cards never paint mid-pass (one synchronous JS task).
        kids.forEach(function(c) { c.style.gridRowEnd = ''; });
        var spans = kids.map(function(c) {
          return Math.max(1, Math.ceil((c.getBoundingClientRect().height + gap) / (rowH + gap)));
        });
        kids.forEach(function(c, i) { c.style.gridRowEnd = 'span ' + spans[i]; });
      };
      var relayoutMasonry = function() { masonryGrids.forEach(layoutMasonry); };
      requestAnimationFrame(relayoutMasonry); // initial pass once layout settles
      var mrT = null;
      window.addEventListener('resize', function() {
        if (mrT) clearTimeout(mrT);
        mrT = setTimeout(relayoutMasonry, 120); // column count flips at the breakpoint
      });
      // Recompute when a card's own height changes — async Table loads, ShowWhen
      // toggles, collapsibles, and tab show/hide (display:none -> shown fires it).
      if (window.ResizeObserver) {
        var moT = null;
        var mo = new ResizeObserver(function() {
          if (moT) clearTimeout(moT);
          moT = setTimeout(relayoutMasonry, 60);
        });
        masonryGrids.forEach(function(grid) {
          Array.prototype.forEach.call(grid.children, function(c) { mo.observe(c); });
        });
      }
    }

    if (cfg.footer) {
      var footer = el('div', {class: 'ui-footer'});
      if (cfg.footer_url) footer.appendChild(el('a', {class: 'ui-footer-link', href: cfg.footer_url}, [cfg.footer]));
      else footer.appendChild(el('span', {class: 'ui-footer-link'}, [cfg.footer]));
      root.appendChild(footer);
    }
  }

  if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', mount);
  else mount();
})();
