
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

  // --- Viewport measurement --------------------------------------------
  // Full-height panels (chat, agent, workbench, article/code editor) size
  // themselves as "the viewport minus the page chrome". They used to subtract
  // a hardcoded guess for that chrome — 70px on desktop, 120px on mobile —
  // and the guess is wrong on a phone: a 44px tap-target back link plus the
  // root's top and bottom padding plus the home-indicator safe area add up
  // past 120px, so the page overflowed by a dozen-odd pixels and the composer
  // sat just below the fold. That's the "input is too low" feel.
  //
  // Measure it instead. Two custom properties on <html>:
  //   --ui-chrome-h  real height of the page header + #ui-root's vertical
  //                  padding, i.e. everything a full-height panel shares the
  //                  viewport with.
  //   --ui-vh        the height actually visible right now. visualViewport
  //                  tracks the on-screen keyboard, so opening it shrinks the
  //                  panel rather than shoving the composer under the keys.
  //
  // The CSS keeps the old constants as var() fallbacks, so the first paint —
  // before this runs — looks exactly like it does today.
  function syncViewport() {
    var st = document.documentElement.style;
    var chrome = 0;
    var hdr = document.querySelector('.ui-page-header');
    if (hdr) chrome += hdr.getBoundingClientRect().height;
    var root = document.getElementById('ui-root');
    if (root) {
      var cs = getComputedStyle(root);
      chrome += (parseFloat(cs.paddingTop) || 0) + (parseFloat(cs.paddingBottom) || 0);
    }
    // Pinch-zoom also shrinks visualViewport. Sizing to it then would collapse
    // the panel to the zoomed window, so only trust it at natural scale.
    var vv = window.visualViewport;
    var vh = (vv && vv.scale && vv.scale <= 1.01) ? vv.height : window.innerHeight;
    // Round AWAY from a fit — ceil the chrome, floor the viewport — so a
    // fractional device pixel can only ever leave a hairline gap under the
    // panel, never overflow the page and start the scroll that put the
    // composer out of reach in the first place.
    st.setProperty('--ui-chrome-h', Math.ceil(chrome) + 'px');
    st.setProperty('--ui-vh', Math.floor(vh) + 'px');
  }
  // Coalesce bursts (a keyboard opening fires several resizes) into one pass
  // on the next frame — the measurement reads layout, so batching it keeps
  // the read out of the middle of the browser's own resize work.
  var vpPending = false;
  function scheduleViewportSync() {
    if (vpPending) return;
    vpPending = true;
    requestAnimationFrame(function() { vpPending = false; syncViewport(); });
  }
  window.uiSyncViewport = scheduleViewportSync;
  (function() {
    window.addEventListener('resize', scheduleViewportSync);
    window.addEventListener('orientationchange', scheduleViewportSync);
    if (window.visualViewport) {
      window.visualViewport.addEventListener('resize', scheduleViewportSync);
    }
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
      // Left group = column 1 of the header grid (back link + title). Bundling
      // them in their own track lets the title truncate within it while the
      // tabs stay centered in column 2 — the two can never overlap.
      var headerLeft = el('div', {class: 'ui-page-header-left'});
      if (cfg.back_url) {
        headerLeft.appendChild(el('a', {class: 'ui-back-link', href: cfg.back_url, title: 'Back'}, ['← Back']));
      }
      if (cfg.show_title && cfg.title) {
        // title attr = full name, so a name truncated by the ellipsis in a
        // narrow column is still readable on hover.
        headerLeft.appendChild(el('h1', {class: 'ui-page-title', title: cfg.title}, [cfg.title]));
      }
      header.appendChild(headerLeft);
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
      // Hidden with VISIBILITY, not display: the pill is taller than the rest
      // of the header, so removing it from layout let the header collapse and
      // every page below it jumped a few pixels each time work started or
      // finished. Keeping the box reserved means appearing costs nothing but
      // the paint. Nothing sits to its right, so the reserved width is free.
      var liveBtn = el('button', {class: 'ui-live-pill', title: 'Active sessions across all apps', style: 'visibility:hidden'},
        [el('span', {class: 'ui-live-dot'}), el('span', {class: 'ui-live-text'}, ['Live'])]);
      var liveMenu = el('div', {class: 'ui-live-menu', style: 'display:none'});
      liveWrap.appendChild(liveBtn);
      liveWrap.appendChild(liveMenu);
      header.appendChild(liveWrap);
      var liveItems = [];
      // The server resolves each entry's destination and applies the
      // viewer's app access, so an empty href here means "no way back" —
      // either the work has no owning page (an agent run, a queued task)
      // or access says no. Both land on the framework's live view.
      function liveHref(it) {
        return it.href || cfg.live_url || '#';
      }
      function renderLiveMenu() {
        liveMenu.innerHTML = '';
        if (!liveItems.length) {
          liveMenu.appendChild(el('div', {class: 'ui-live-empty'}, ['No active sessions.']));
          return;
        }
        liveItems.forEach(function(it) {
          var row = el('a', {
            class: 'ui-live-item' + (it.queued ? ' queued' : ' running'),
            // Prefer going back to the work itself (the server-resolved
            // href), falling back to the framework-supplied live view when
            // the entry offers nowhere to go. core/ui names no specific app
            // either way. '#' when neither is configured.
            href: liveHref(it),
          });
          row.appendChild(el('span', {class: 'ui-live-app'}, [it.app || '?']));
          // Background work says so here rather than reading "Running" like
          // anything else — the state column is the one place a person looks
          // to find out what kind of thing this is.
          var state = it.queued ? 'Queued' : (it.background ? 'Background' : 'Running');
          row.appendChild(el('span', {class: 'ui-live-state' + (it.background && !it.queued ? ' background' : '')}, [state]));
          row.appendChild(el('span', {class: 'ui-live-label'}, [it.topic || it.label || 'Untitled']));
          // The provider has been sending a status all along ("scheduled ·
          // round 2 · web_search") and this menu never rendered it, so the one
          // field that said what the work was DOING was fetched every ten
          // seconds and dropped. Optional: not every provider sets one.
          if (it.status) {
            row.appendChild(el('span', {class: 'ui-live-status'}, [it.status]));
          }
          // A way to stop work that outlives the turn that started it. Only
          // when the entry declares one: most rows cannot be stopped from
          // here, and a button that does nothing is worse than none. core/ui
          // learns only that an endpoint exists and posts to it; which app,
          // which run, and whether this viewer may is all resolved server-side.
          if (it.cancel_url) {
            var stop = el('button', {class: 'ui-live-stop', title: 'Stop this'}, ['Stop']);
            stop.addEventListener('click', function(ev) {
              // The row is a link. Without this, stopping the work also
              // navigates away from the page you were reading.
              ev.preventDefault();
              ev.stopPropagation();
              stop.disabled = true;
              stop.textContent = 'Stopping…';
              fetch(it.cancel_url, {method: 'POST'}).then(function() {
                refreshLive();
              }).catch(function() {
                stop.disabled = false;
                stop.textContent = 'Stop';
              });
            });
            row.appendChild(stop);
          }
          liveMenu.appendChild(row);
        });
      }
      // liveTitle says the counts in words. Built as a list so the sentence
      // stays true when a category is empty — "2 running" rather than
      // "2 running and 0 in the background", which reads like a report on
      // something that is not happening.
      function liveTitle(fg, bg, queued) {
        var parts = [];
        if (fg) parts.push(fg + (fg === 1 ? ' session' : ' sessions') + ' you started');
        if (bg) parts.push(bg + ' running in the background');
        if (queued) parts.push(queued + ' queued');
        return parts.length ? parts.join(', ') : 'Active sessions across all apps';
      }
      function refreshLive() {
        fetch('/api/live').then(function(r){ return r.json(); }).then(function(items) {
          items = (items || []).filter(function(it){ return !it.spawned; });
          liveItems = items;
          var n = items.length;
          if (n === 0) {
            liveBtn.style.visibility = 'hidden';
            liveMenu.style.display = 'none';
            return;
          }
          liveBtn.style.visibility = 'visible';
          // Class encodes the state so CSS can paint the dot. Four, because
          // the two kinds of activity are INDEPENDENT and both are worth
          // seeing:
          //
          //   running     green          only work you started
          //   background  indigo         only work that started on its own
          //   split       half and half  both at once
          //   queued      amber          nothing has started yet
          //
          // A precedence rule was the first shape and it was wrong: with
          // foreground winning, a scheduled job firing while you had any
          // thread open was invisible — which is precisely the case the
          // background color was added for. A split dot answers both
          // questions at once instead of ranking them.
          var running = items.filter(function(it){ return !it.queued; });
          var fg = running.filter(function(it){ return !it.background; }).length;
          var bg = running.length - fg;
          liveBtn.classList.toggle('running', fg > 0 && bg === 0);
          liveBtn.classList.toggle('background', bg > 0 && fg === 0);
          liveBtn.classList.toggle('split', fg > 0 && bg > 0);
          liveBtn.classList.toggle('queued', running.length === 0);
          // The tooltip carries what no color can: a dot is not readable to
          // everyone, and half a dot is less readable than a whole one.
          liveBtn.title = liveTitle(fg, bg, items.length - running.length);
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
      // A 10s poll is fine for background work nobody is watching, but it is
      // the wrong clock for work the user just started: asking a question and
      // watching the pill sit idle for most of the answer reads as the pill
      // being broken. Any surface that knows a run began can say so — the poll
      // stays as the backstop that catches everything else (another tab, a
      // schedule firing, a run that ended elsewhere).
      //
      // Two follow-ups because the race cuts both ways: the request may not
      // have reached the server yet when the first refresh lands, and a very
      // short turn may already be over by the second.
      window.uiRefreshLive = function() {
        refreshLive();
        setTimeout(refreshLive, 900);
        setTimeout(refreshLive, 2500);
      };
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
    // secnavSlug names a section for the URL: "Try it" → "try-it". The
    // same transform any server code linking INTO a page must apply
    // (see Go's ui.SectionSlug), which is what makes a graph node or a
    // shared link able to say "#verify" and land on the verify section.
    function secnavSlug(title) {
      return String(title || '').toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-+|-+$/g, '');
    }
    function buildSecNav(mountEl, secs) {
      var rail = el('div', {class: 'ui-secnav-rail'});
      var content = el('div', {class: 'ui-secnav-content'});
      mountEl.appendChild(el('div', {class: 'ui-secnav'}, [rail, content]));
      var subPanels = [], items = [], slugs = [];
      function activate(si) {
        for (var k = 0; k < subPanels.length; k++) subPanels[k].classList.toggle('ui-tab-hidden', k !== si);
        for (var m = 0; m < items.length; m++) items[m].classList.toggle('active', m === si);
      }
      secs.forEach(function(s, si) {
        var sp = el('div', {class: 'ui-secnav-panel' + (si === 0 ? '' : ' ui-tab-hidden')});
        if (inGrid) { var sg = el('div', {class: 'ui-section-grid'}); sp.appendChild(sg); s.__host = sg; }
        else { s.__host = sp; }
        content.appendChild(sp);
        subPanels.push(sp);
        slugs.push(secnavSlug(s.title));
        var ib = el('button', {type: 'button', class: 'ui-secnav-item' + (si === 0 ? ' active' : '')}, [s.title || ('Section ' + (si + 1))]);
        ib.addEventListener('click', function() {
          activate(si);
          // The hash is the address of the open section, so a link can
          // carry someone to it and Back walks the trail. replaceState
          // when clearing to the first section would be nicer still, but
          // a plain hash set keeps the behaviour observable.
          if (slugs[si]) window.location.hash = slugs[si];
        });
        items.push(ib);
        rail.appendChild(ib);
      });
      // Deep-linking: land on (or be sent to) #<slug>. hashchange covers
      // both a link clicked INSIDE the page (a graph node) and the back
      // button walking earlier sections.
      function activateHash() {
        var want = window.location.hash.replace(/^#/, '');
        if (!want) { activate(0); return; }
        var si = slugs.indexOf(secnavSlug(want));
        if (si >= 0) activate(si);
      }
      window.addEventListener('hashchange', activateHash);
      activateHash();
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
    // bareSectionHead writes a no-chrome section's title/subtitle above its
    // body. No card, no padding, no wrapper around the body — the panel keeps
    // managing its own layout, which is the whole reason it asked for
    // no_chrome; it just stops being the one section kind whose heading is
    // silently discarded.
    //
    // Nothing renders when there is no title and no subtitle, so every existing
    // no-chrome section (which sets neither) is untouched.
    function bareSectionHead(s, into) {
      if (!s.title && !s.subtitle) return;
      var head = el('div', {class: 'ui-section-bare-head'});
      if (s.title) head.appendChild(el('div', {class: 'ui-section-h'}, [el('span', {text: s.title})]));
      if (s.subtitle) head.appendChild(el('div', {class: 'ui-section-sub'}, [s.subtitle]));
      into.appendChild(head);
    }
    // markMounted stamps every element a section mounted with data-ui-section,
    // so "how many sections actually rendered?" has ONE answer for chromed and
    // no-chrome sections alike. Anything reading the page from outside — a
    // headless render check, a screenshot tool — can count
    // '.ui-section,[data-ui-section]' instead of guessing at each panel's own
    // root class and reporting a working page as blank.
    function markMounted(nodes) {
      (nodes || []).forEach(function(n) {
        if (n && n.nodeType === 1 && !n.hasAttribute('data-ui-section')) {
          n.setAttribute('data-ui-section', '');
        }
      });
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
          bareSectionHead(s, ncWrap);
          if (s.body) mountComponent(s.body, ncWrap);
          markMounted([ncWrap]);
          host.appendChild(ncWrap);
        } else if (s.body) {
          bareSectionHead(s, host);
          // Mount directly, then MARK what landed. A no-chrome section has no
          // .ui-section card by design, so from outside the page it used to be
          // invisible — and a page built only of them (chat, workbench,
          // pipeline) counted zero sections and read as blank to anything
          // inspecting the DOM. The marker is inert: no class, no style, no
          // wrapper element that could break a panel's 100%-height layout.
          var before = host.childNodes.length;
          mountComponent(s.body, host);
          markMounted(Array.prototype.slice.call(host.childNodes, before));
        }
        return;
      }
      var section = el('div', {class: 'ui-section'});
      markMounted([section]);
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
      // Cap the CARD, not just its contents. A wide section spans every
      // grid column; on a large display that can be more width than the
      // body has anything to say. Capped, the card keeps its own edge and
      // stays left-aligned in the slot.
      if (s.max_width) section.style.maxWidth = s.max_width;
      host.appendChild(section);
    });

    // A full-height panel is a two-column layout that owns the viewport: a
    // rail beside a working column. Every hand-written page that hosts one sets
    // max_width 100%, arrived at independently each time — which is the tell
    // that the requirement belongs to the PANEL rather than to each page.
    //
    // A STORED page cannot make that call reliably. Its width was decided when
    // it was authored, so a page whose panel arrived later opens in a narrow
    // column with a sidebar eating a quarter of it, and the only repair is to
    // re-author it. Deciding it here, from what actually mounted, fixes the
    // pages already written and means an author cannot get it wrong.
    //
    // Keyed on the panel roots, deliberately not on "has any section": a
    // no-chrome section holding ordinary content keeps the column it asked for.
    if (root.querySelector('.ui-chat, .ui-agent, .ui-pl, .ui-wb, .ui-cw, .ui-tw')) {
      root.style.maxWidth = '100%';
    }

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

    // Chrome is in the DOM now — measure it. Re-measure whenever the header's
    // own height changes: the nav tabs wrap to a second row on a narrow phone,
    // and a late webfont swap shifts it by a pixel or two.
    syncViewport();
    var hdrEl = document.querySelector('.ui-page-header');
    if (hdrEl && window.ResizeObserver) {
      new ResizeObserver(scheduleViewportSync).observe(hdrEl);
    }
  }

  if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', mount);
  else mount();
})();
