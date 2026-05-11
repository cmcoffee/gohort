package ui

// runtimeCSS — theme tokens + every component's static styling.
// Mobile-first, with safe-area insets, 44px minimum tap targets, and
// the Blackboard color scheme as the default theme.
const runtimeCSS = `
:root[data-theme="blackboard"] {
  --bg-0: #0c1424;       /* page background — deepest navy */
  --bg-1: #142037;       /* card / row background */
  --bg-2: #1c2a45;       /* elevated surfaces */
  --text:     #f0e9c8;   /* warm cream */
  --text-hi:  #ffffff;
  --text-mute:#9aa3b8;
  --border:   #2a3a5e;
  --accent:   #d4a657;   /* warm amber, plays nice with cream */
  --accent-hi:#f0c878;
  --danger:   #f85149;
  --success:  #56d364;
  --tap: 44px;
}
:root[data-theme="github-dark"] {
  --bg-0: #0d1117;
  --bg-1: #161b22;
  --bg-2: #21262d;
  --text: #c9d1d9;
  --text-hi: #f0f6fc;
  --text-mute: #8b949e;
  --border: #30363d;
  --accent: #4f8cff;
  --accent-hi: #79c0ff;
  --danger: #f85149;
  --success: #56d364;
  --tap: 44px;
}

* { box-sizing: border-box; }
html, body {
  margin: 0; padding: 0;
  background: var(--bg-0); color: var(--text);
  font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", "Helvetica Neue", Arial, sans-serif;
  -webkit-font-smoothing: antialiased; -moz-osx-font-smoothing: grayscale;
  overscroll-behavior-y: contain;
}
body { min-height: 100vh; min-height: 100dvh; }

#ui-root {
  margin: 0 auto;
  padding: 0.6rem;
  padding-top: calc(0.6rem + env(safe-area-inset-top, 0px));
  padding-bottom: calc(1.5rem + env(safe-area-inset-bottom, 0px));
}

/* --- Page header (back link + visible title) --- */
.ui-page-header {
  display: flex; align-items: center; gap: 0.6rem;
  padding: 0.2rem 0 0.4rem;
  border-bottom: 1px solid var(--border);
  margin-bottom: 0.5rem;
}
.ui-back-link {
  display: inline-flex; align-items: center;
  padding: 0.2rem 0.5rem;
  font-size: 0.8rem; color: var(--text-mute); text-decoration: none;
  border: 1px solid var(--border); border-radius: 6px;
  background: var(--bg-1);
  -webkit-tap-highlight-color: transparent;
  transition: color 0.1s ease, border-color 0.1s ease, transform 0.05s ease;
}
/* Mobile: keep a touch-friendly tap target. */
@media (max-width: 700px) {
  .ui-back-link { min-height: var(--tap); padding: 0.45rem 0.7rem; font-size: 0.9rem; }
}
.ui-back-link:hover { color: var(--text); border-color: var(--text-mute); }
.ui-back-link:active { transform: scale(0.97); }
/* Live-sessions pill — pinned to the right of the page header.
 * Hidden when no sessions are active so the header stays clean
 * during normal use; pulses with a colored dot when something is
 * running. Click → dropdown listing each session with click-through
 * to its app page. */
.ui-live-pill-wrap { position: relative; margin-left: auto; }
.ui-live-pill {
  display: inline-flex; align-items: center; gap: 0.45rem;
  padding: 0.32rem 0.7rem; border-radius: 999px;
  background: var(--bg-1); color: #3fb950;
  border: 1px solid #3fb950;
  font-size: 0.72rem; font-weight: 700;
  letter-spacing: 0.08em;
  cursor: pointer;
  -webkit-tap-highlight-color: transparent;
  font-family: inherit;
}
.ui-live-pill.queued {
  color: #e3b341;
  border-color: #e3b341;
}
.ui-live-pill:hover { filter: brightness(1.15); }
.ui-live-text { font-weight: 700; }
.ui-live-dot {
  width: 9px; height: 9px; border-radius: 50%;
  background: var(--text-mute);
}
.ui-live-pill.running .ui-live-dot {
  background: #3fb950;
  box-shadow: 0 0 6px rgba(63, 185, 80, 0.85);
  animation: ui-live-pulse 1.6s ease-in-out infinite;
}
.ui-live-pill.queued .ui-live-dot {
  background: #e3b341;
  box-shadow: 0 0 6px rgba(227, 179, 65, 0.7);
}
/* Pulse the glow ring outward — the inner shadow stays put while
 * the outer ring expands and fades, so the dot reads as "alive"
 * instead of just colored. Matches the legacy live-status pill
 * the user remembered. */
@keyframes ui-live-pulse {
  0%, 100% {
    box-shadow:
      0 0 6px rgba(63, 185, 80, 0.85),
      0 0 0 0 rgba(63, 185, 80, 0.5);
  }
  50% {
    box-shadow:
      0 0 6px rgba(63, 185, 80, 0.85),
      0 0 0 7px rgba(63, 185, 80, 0);
  }
}
.ui-live-menu {
  position: absolute; right: 0; top: calc(100% + 6px); z-index: 50;
  min-width: 320px; max-width: 480px;
  background: var(--bg-1); border: 1px solid var(--border);
  border-radius: 8px; box-shadow: 0 4px 14px rgba(0,0,0,0.35);
  padding: 0.3rem; max-height: 60vh; overflow-y: auto;
}
.ui-live-empty {
  font-size: 0.82rem; color: var(--text-mute);
  padding: 0.5rem 0.6rem; text-align: center;
}
.ui-live-item {
  display: flex; align-items: center; gap: 0.5rem;
  padding: 0.5rem 0.6rem; border-radius: 6px;
  text-decoration: none; color: var(--text);
  font-size: 0.85rem;
}
.ui-live-item:hover { background: var(--bg-2); }
.ui-live-app {
  font-size: 0.7rem; font-weight: 600;
  text-transform: uppercase; letter-spacing: 0.04em;
  color: var(--text-mute); flex-shrink: 0;
}
.ui-live-state {
  font-size: 0.65rem; font-weight: 700;
  padding: 0.1rem 0.4rem; border-radius: 999px;
  text-transform: uppercase; letter-spacing: 0.04em;
  flex-shrink: 0;
}
.ui-live-item.running .ui-live-state { background: rgba(63, 185, 80, 0.18); color: #3fb950; }
.ui-live-item.queued .ui-live-state  { background: rgba(227, 179, 65, 0.18); color: #e3b341; }
.ui-live-label {
  flex: 1; min-width: 0;
  white-space: nowrap; overflow: hidden; text-overflow: ellipsis;
}
.ui-page-title {
  margin: 0; flex: 1;
  font-family: 'Orbitron', -apple-system, BlinkMacSystemFont, 'Segoe UI', Helvetica, sans-serif;
  font-weight: 700; font-size: 1.1rem; letter-spacing: 0.05em; line-height: 1;
  /* Metallic gradient — chrome-on-charcoal wordmark matching the
     legacy app-title look so framework pages don't appear downgraded
     vs. apps still serving hand-rolled chrome. */
  background: linear-gradient(180deg, #f0f6fc 0%, #30363d 100%);
  -webkit-background-clip: text;
  -webkit-text-fill-color: transparent;
  background-clip: text;
  white-space: nowrap;
}

/* --- Section --- */
.ui-section {
  background: var(--bg-1); border: 1px solid var(--border); border-radius: 12px;
  padding: 0.6rem 0.8rem; margin-bottom: 0.8rem;
}
.ui-section-h {
  font-size: 0.85rem; margin: 0 0 0.5rem 0;
  color: var(--text-mute); text-transform: uppercase; letter-spacing: 0.04em;
  display: flex; align-items: center; justify-content: space-between; gap: 0.5rem;
  flex-wrap: wrap;
  word-break: break-word;
}
.ui-section-h-r { font-size: 0.7rem; text-transform: none; letter-spacing: 0; font-weight: normal; }
.ui-section-sub {
  font-size: 0.8rem; color: var(--text-mute);
  margin: -0.3rem 0 0.6rem 0;
  line-height: 1.45;
  /* Long subtitles wrap onto multiple lines; never truncate. */
  white-space: normal; overflow-wrap: break-word;
}

/* --- PanicBar (sticky top) --- */
.ui-panic-bar {
  position: sticky; top: 0; z-index: 10;
  margin: -0.6rem -0.6rem 0.8rem; padding: 0.6rem;
  padding-top: calc(0.6rem + env(safe-area-inset-top, 0px));
  background: rgba(12, 20, 36, 0.85); backdrop-filter: blur(8px);
  border-bottom: 1px solid var(--border);
}
.ui-panic-btn {
  display: block; width: 100%; min-height: var(--tap);
  background: linear-gradient(180deg, #8b2222, #6e1c1c); color: #fff;
  border: 1px solid var(--danger); border-radius: 10px;
  padding: 0.85rem; font-size: 1rem; font-weight: 600; letter-spacing: 0.02em;
  cursor: pointer; -webkit-tap-highlight-color: transparent;
  transition: transform 0.05s ease, background 0.1s ease;
}
.ui-panic-btn:active { background: var(--danger); transform: scale(0.98); }
.ui-panic-status { font-size: 0.8rem; color: var(--text-mute); text-align: center; display: block; margin-top: 0.3rem; min-height: 1em; }

/* --- iOS-style switch --- */
.ui-switch {
  -webkit-appearance: none; appearance: none;
  position: relative; flex-shrink: 0;
  width: 52px; height: 32px; border-radius: 16px;
  background: var(--border); border: none;
  cursor: pointer; outline: none;
  transition: background 0.15s ease;
}
.ui-switch::before {
  content: ''; position: absolute; left: 2px; top: 2px;
  width: 28px; height: 28px; border-radius: 50%; background: #fff;
  box-shadow: 0 1px 3px rgba(0,0,0,0.3);
  transition: transform 0.15s ease;
}
.ui-switch:checked { background: var(--success); }
.ui-switch:checked::before { transform: translateX(20px); }
.ui-switch:disabled { opacity: 0.5; cursor: not-allowed; }

/* --- ToggleGroup row --- */
/* Padding is 0 horizontal so label text shares its left edge with the
 * section header above, and the switch sits flush with the section's
 * right content edge. Eliminates the "wandering text" look where each
 * row's label starts a few pixels right of where the header started. */
.ui-toggle-row {
  display: flex; align-items: center; justify-content: space-between;
  gap: 0.7rem; padding: 0.65rem 0; min-height: var(--tap);
  line-height: 1.3;
  border-bottom: 1px solid var(--border);
  -webkit-tap-highlight-color: transparent; cursor: pointer; user-select: none;
}
.ui-toggle-row:last-child { border-bottom: none; }
.ui-toggle-row:active { background: rgba(255,255,255,0.02); }
.ui-toggle-row .ui-toggle-label { font-size: 0.95rem; color: var(--text); flex: 1; min-width: 0; padding-right: 0.5rem; }
.ui-toggle-row .ui-toggle-help { display: block; font-size: 0.78rem; color: var(--text-mute); margin-top: 0.15rem; }

/* --- Table --- */
.ui-table-list { display: flex; flex-direction: column; gap: 0.5rem; }
.ui-table-row {
  background: var(--bg-2); border: 1px solid var(--border); border-radius: 10px;
  padding: 0.6rem 0.7rem;
  display: flex; align-items: center; gap: 0.7rem;
  /* Single-line layout on desktop — cells ellipsize when crowded.
   * Wrapping caused inconsistent placement (long topics pushed
   * action buttons to a second line, short topics didn't). The
   * 800px media query below handles narrow viewports separately
   * by stacking the row as a column. */
  flex-wrap: nowrap;
  min-height: var(--tap);
}
.ui-row-cells {
  display: flex; align-items: center; gap: 0.7rem;
  flex: 1 1 auto; min-width: 0;
}
.ui-row-actions {
  display: flex; align-items: center; gap: 0.5rem;
  flex-wrap: wrap; /* buttons wrap as a group, never one-per-line */
  flex-shrink: 0;
  /* Pin to the right edge of the row. The cells container takes
   * remaining width via flex:1, so actions always end up flush right
   * regardless of how much cell content there is. */
  margin-left: auto;
  justify-content: flex-end;
}
.ui-table-cell { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-size: 0.95rem; }
.ui-table-cell.mute { color: var(--text-mute); font-size: 0.78rem; }
.ui-table-empty { color: var(--text-mute); font-size: 0.9rem; padding: 0.5rem 0; }

/* Narrow viewport: row goes column, cells stack vertically (display:
 * block — more predictable than nested flex), actions stay a single
 * horizontal wrapped strip. Avoids the empty-content collapse bug
 * that hit on phone Chrome where width:100% on flex children inside
 * a flex-column container rendered as 0-height. */
@media (max-width: 800px) {
  .ui-table-row {
    flex-direction: column; align-items: stretch;
    gap: 0.5rem;
  }
  .ui-row-cells {
    display: block;
    flex: none; min-width: 0;
  }
  .ui-row-cells > .ui-table-cell {
    display: block;
    width: auto;
    margin: 0 0 0.25rem 0;
    white-space: normal; word-break: break-word;
    overflow: visible; /* keep ellipsis off when wrapping is on */
    text-overflow: clip;
  }
  .ui-row-cells > .ui-table-cell:last-child { margin-bottom: 0; }
  .ui-table-cell.mute { font-size: 0.8rem; }
  .ui-row-actions {
    display: flex; flex-wrap: wrap;
    flex-direction: row;
    margin-left: 0; justify-content: flex-start;
    gap: 0.4rem;
  }
}

.ui-row-btn {
  flex-shrink: 0; min-width: var(--tap); min-height: var(--tap);
  padding: 0.45rem 0.7rem; font-size: 0.8rem;
  background: var(--bg-1); color: var(--text); border: 1px solid var(--border);
  border-radius: 8px; cursor: pointer; -webkit-tap-highlight-color: transparent;
  transition: transform 0.05s ease, background 0.1s ease;
}
.ui-row-btn:active { background: var(--bg-0); transform: scale(0.96); }
.ui-row-toggle-pair { display: inline-flex; align-items: center; gap: 0.4rem; flex-shrink: 0; }
.ui-row-toggle-label { font-size: 0.78rem; color: var(--text-mute); text-transform: uppercase; letter-spacing: 0.04em; }
.ui-row-select, .ui-row-number {
  flex-shrink: 0;
  padding: 0.35rem 0.45rem;
  background: var(--bg-1); color: var(--text);
  border: 1px solid var(--border); border-radius: 6px;
  font-size: 0.85rem; font-family: inherit; outline: none;
  -webkit-appearance: none; appearance: none;
}
.ui-row-select:focus, .ui-row-number:focus { border-color: var(--accent); }
.ui-row-select:disabled { opacity: 0.5; cursor: not-allowed; }
.ui-row-number { width: 6rem; text-align: right; }
.ui-row-btn.danger  { color: var(--danger); border-color: var(--danger); }
.ui-row-btn.warning { color: #d29922; border-color: #d29922; }
.ui-row-btn.success { color: var(--success); border-color: var(--success); }
/* Compact: trim padding so an emoji or 1-3 letter label sits in a
 * ~36x36 box. Used for secondary row buttons on narrow screens where
 * the standard 44px-min target would push the auto-reply switch and
 * row name off the visible width. */
.ui-row-btn.compact { min-width: 36px; min-height: 36px; padding: 0.3rem 0.4rem; font-size: 0.95rem; }
/* Stack composes multiple components in one expand panel. */
.ui-stack { display: flex; flex-direction: column; gap: 0.6rem; }
/* Badge — colored pill for boolean/state indicators in table rows. */
.ui-badge {
  display: inline-block; padding: 0.1rem 0.55rem;
  border-radius: 999px; font-size: 0.7rem; font-weight: 600;
  text-transform: uppercase; letter-spacing: 0.04em;
  border: 1px solid var(--border); background: var(--bg-0);
}
.ui-badge.success { color: #56d364; border-color: #2e5e3a; background: #122017; }
.ui-badge.warning { color: #d29922; border-color: #463b1f; background: #2a2218; }
.ui-badge.danger  { color: var(--danger); border-color: #5a2929; background: #2a1b1b; }
.ui-badge.mute    { color: var(--text-mute); border-color: var(--border); background: var(--bg-0); }

/* --- Expand panel --- */
.ui-expand {
  background: var(--bg-2); border: 1px solid var(--border); border-radius: 10px;
  padding: 0.6rem 0.7rem; margin-top: 0.5rem;
  animation: ui-slide 0.2s ease-out;
}
@keyframes ui-slide { from { opacity: 0; transform: translateY(-4px); } to { opacity: 1; transform: translateY(0); } }

/* --- HistoryPanel --- */
.ui-history { display: flex; flex-direction: column; gap: 0.4rem; max-height: 60vh; overflow-y: auto; }
.ui-history-h { font-size: 0.7rem; color: var(--text-mute); text-transform: uppercase; letter-spacing: 0.04em; padding-bottom: 0.3rem; border-bottom: 1px solid var(--border); }
.ui-history-msg { padding: 0.4rem 0.5rem; border-radius: 8px; font-size: 0.88rem; line-height: 1.4; background: var(--bg-1); }
.ui-history-msg.ai { border-left: 3px solid var(--accent); }
.ui-history-who { font-size: 0.7rem; color: var(--text-mute); margin-bottom: 0.15rem; }
.ui-history-body { color: var(--text); white-space: pre-wrap; word-break: break-word; }
.ui-history-empty { color: var(--text-mute); font-size: 0.85rem; }

/* --- KeyManager (API keys list + create-once-show-secret + delete) --- */
.ui-keys { display: flex; flex-direction: column; gap: 0.5rem; }
.ui-keys-actions { display: flex; justify-content: flex-end; }
.ui-keys-new {
  background: transparent; color: var(--accent); border: 1px solid var(--accent);
  border-radius: 6px; padding: 0.4rem 0.85rem; font-size: 0.8rem; cursor: pointer;
  -webkit-tap-highlight-color: transparent;
}
.ui-keys-new:hover { background: var(--bg-2); }
.ui-keys-form {
  display: flex; gap: 0.4rem; flex-wrap: wrap; align-items: center;
  background: var(--bg-2); border: 1px solid var(--border); border-radius: 8px;
  padding: 0.5rem 0.6rem;
}
.ui-keys-form input {
  flex: 1; min-width: 12rem;
  padding: 0.45rem 0.6rem; border-radius: 6px;
  background: var(--bg-0); color: var(--text);
  border: 1px solid var(--border); font-size: 0.85rem;
}
.ui-keys-form input:focus { outline: none; border-color: var(--accent); }
.ui-keys-form button {
  padding: 0.45rem 0.85rem; border-radius: 6px; font-size: 0.8rem; cursor: pointer;
  background: var(--accent); color: var(--text-on-accent, #1a1a1a);
  border: 1px solid var(--accent); font-weight: 600;
}
.ui-keys-form button.secondary { background: transparent; color: var(--text-mute); border-color: var(--border); font-weight: normal; }
.ui-keys-revealed {
  background: rgba(212, 166, 87, 0.12);
  border: 1px solid var(--accent);
  border-radius: 8px; padding: 0.7rem 0.8rem;
  display: flex; flex-direction: column; gap: 0.4rem;
}
.ui-keys-revealed-h { font-size: 0.78rem; color: var(--accent); font-weight: 600; }
.ui-keys-revealed-hint { font-size: 0.78rem; color: var(--text-mute); }
.ui-keys-revealed-secret {
  font-family: ui-monospace, "SF Mono", Menlo, monospace;
  font-size: 0.85rem; word-break: break-all; user-select: all;
  background: var(--bg-0); border: 1px solid var(--border);
  border-radius: 6px; padding: 0.5rem 0.6rem;
}
.ui-keys-revealed-row { display: flex; gap: 0.4rem; align-items: center; }
.ui-keys-revealed-row button {
  padding: 0.4rem 0.75rem; border-radius: 6px; font-size: 0.78rem; cursor: pointer;
  background: var(--bg-1); color: var(--text); border: 1px solid var(--border);
}
.ui-keys-revealed-row button.copied { background: #2da44e; color: #fff; border-color: #2da44e; }
.ui-keys-list {
  display: flex; flex-direction: column;
  border: 1px solid var(--border); border-radius: 8px; overflow: hidden;
}
.ui-keys-row {
  display: flex; align-items: center; gap: 0.6rem;
  padding: 0.55rem 0.7rem;
  border-bottom: 1px solid var(--border);
}
.ui-keys-row:last-child { border-bottom: none; }
.ui-keys-row-name { flex: 1; min-width: 0; color: var(--text); font-size: 0.9rem; font-weight: 600; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.ui-keys-row-meta { font-size: 0.75rem; color: var(--text-mute); white-space: nowrap; }
.ui-keys-row-del {
  background: transparent; color: var(--text-mute);
  border: 1px solid var(--border); border-radius: 6px;
  width: 1.8rem; height: 1.8rem; padding: 0; cursor: pointer;
  font-size: 1rem; line-height: 1;
}
.ui-keys-row-del:hover { color: var(--danger, #f85149); border-color: var(--danger, #f85149); }
.ui-keys-empty { color: var(--text-mute); font-size: 0.85rem; padding: 0.5rem 0.2rem; }

/* --- MemberEditor (group-chat participants list) --- */
.ui-mem { display: flex; flex-direction: column; gap: 0.5rem; }
.ui-mem-row {
  display: grid;
  grid-template-columns: 1fr 1fr 1fr auto;
  gap: 0.4rem;
  align-items: center;
  padding: 0.4rem;
  background: var(--bg-2);
  border: 1px solid var(--border);
  border-radius: 6px;
}
@media (max-width: 700px) {
  .ui-mem-row {
    grid-template-columns: 1fr 1fr auto;
  }
  .ui-mem-row > .ui-mem-aliases { grid-column: 1 / 3; }
}
.ui-mem-row input {
  padding: 0.35rem 0.5rem;
  background: var(--bg-0); color: var(--text);
  border: 1px solid var(--border); border-radius: 4px;
  font-size: 0.82rem; min-width: 0;
}
.ui-mem-row input:focus { outline: none; border-color: var(--accent); }
.ui-mem-row input::placeholder { color: var(--text-mute); }
.ui-mem-del {
  background: transparent; color: var(--text-mute);
  border: 1px solid var(--border); border-radius: 4px;
  width: 1.8rem; height: 1.8rem; padding: 0; cursor: pointer;
  font-size: 0.95rem; line-height: 1;
}
.ui-mem-del:hover { color: var(--danger, #f85149); border-color: var(--danger, #f85149); }
.ui-mem-add {
  align-self: flex-start;
  background: transparent; color: var(--accent); border: 1px solid var(--accent);
  border-radius: 6px; padding: 0.35rem 0.7rem; font-size: 0.78rem; cursor: pointer;
}
.ui-mem-add:hover { background: var(--bg-2); }
.ui-mem-empty { color: var(--text-mute); font-size: 0.82rem; padding: 0.3rem 0.2rem; }
.ui-mem-aliasrow {
  display: flex; flex-direction: column; gap: 0.3rem;
  margin-top: 0.4rem; padding: 0.5rem 0.6rem;
  background: var(--bg-1); border: 1px solid var(--border); border-radius: 6px;
}
.ui-mem-aliasrow label { font-size: 0.78rem; color: var(--text-mute); }
.ui-mem-aliasrow input {
  padding: 0.4rem 0.55rem;
  background: var(--bg-0); color: var(--text);
  border: 1px solid var(--border); border-radius: 4px;
  font-size: 0.83rem; font-family: ui-monospace, "SF Mono", Menlo, monospace;
}
.ui-mem-aliasrow input:focus { outline: none; border-color: var(--accent); }

/* --- ChipPicker --- */
.ui-chips { display: flex; flex-wrap: wrap; gap: 0.4rem; }
.ui-chip {
  font-size: 0.8rem; padding: 0.45rem 0.7rem; border-radius: 18px;
  background: var(--bg-1); color: var(--text-mute); border: 1px solid var(--border);
  cursor: pointer; user-select: none; min-height: 32px;
  -webkit-tap-highlight-color: transparent; transition: transform 0.05s ease;
}
.ui-chip:active { transform: scale(0.94); }
.ui-chip.on { background: var(--accent); color: #1a1a1a; border-color: var(--accent); }

/* --- Pull-to-refresh --- */
.ui-ptr {
  position: fixed; top: env(safe-area-inset-top, 0px); left: 0; right: 0;
  background: var(--bg-1); border-bottom: 1px solid var(--border);
  text-align: center; padding: 0.5rem; font-size: 0.85rem; color: var(--text-mute);
  transform: translateY(-100%); transition: transform 0.2s ease;
  z-index: 20;
}
.ui-ptr.show { transform: translateY(0); }
.ui-spinner {
  display: inline-block; width: 12px; height: 12px;
  border: 2px solid var(--border); border-top-color: var(--accent);
  border-radius: 50%; animation: ui-spin 0.7s linear infinite;
  vertical-align: middle; margin-right: 0.4rem;
}
@keyframes ui-spin { to { transform: rotate(360deg); } }

/* --- Footer --- */
.ui-footer { margin-top: 1rem; text-align: center; }
.ui-footer-link { display: inline-block; min-height: var(--tap); padding: 0.7rem 1rem; font-size: 0.9rem; color: var(--text-mute); text-decoration: none; }
.ui-footer-link:active { color: var(--text); }

/* --- FormPanel --- */
.ui-form-field { padding: 0.5rem 0; border-bottom: 1px solid var(--border); }
.ui-form-field:last-child { border-bottom: none; }
.ui-form-label { display: block; font-size: 0.78rem; color: var(--text-mute); text-transform: uppercase; letter-spacing: 0.04em; margin-bottom: 0.3rem; }
.ui-form-input,
.ui-form-textarea,
.ui-form-select {
  width: 100%; box-sizing: border-box;
  padding: 0.55rem 0.65rem;
  background: var(--bg-2); border: 1px solid var(--border); border-radius: 8px;
  color: var(--text); font-size: 0.95rem; font-family: inherit;
  outline: none; transition: border-color 0.1s ease;
  -webkit-appearance: none; appearance: none;
}
.ui-form-input:focus,
.ui-form-textarea:focus,
.ui-form-select:focus { border-color: var(--accent); }
.ui-form-textarea { resize: vertical; min-height: calc(var(--tap) * 1.5); line-height: 1.4; }
.ui-form-help { display: block; font-size: 0.75rem; color: var(--text-mute); margin-top: 0.3rem; }

/* --- PipelineWatchPanel — live pipeline-follow view --- */
.ui-watch { display: flex; flex-direction: column; gap: 0; }
.ui-watch-header {
  padding: 0.75rem 1rem;
  background: var(--bg-1);
  border: 1px solid var(--border);
  border-radius: 6px 6px 0 0;
}
.ui-watch-app {
  font-size: 0.85rem; color: var(--text-mute);
  margin-bottom: 0.3rem; text-transform: uppercase; letter-spacing: 0.05em;
}
.ui-watch-title-row {
  display: flex; justify-content: space-between; align-items: center;
  margin-bottom: 0.3rem;
}
.ui-watch-title {
  font-size: 1.05rem; color: var(--text-hi); font-weight: 600;
}
.ui-watch-cancel {
  padding: 0.3rem 0.8rem; background: #da3633;
  color: #fff; border: none; border-radius: 6px;
  font-size: 0.8rem; cursor: pointer;
}
.ui-watch-cancel:hover { filter: brightness(1.1); }
.ui-watch-topic {
  color: var(--text-mute); font-size: 0.9rem;
  white-space: normal; word-break: break-word;
}
.ui-watch-spinner {
  display: inline-block; width: 12px; height: 12px;
  border: 2px solid var(--border); border-top-color: var(--accent);
  border-radius: 50%; animation: ui-spin 0.8s linear infinite;
  vertical-align: middle; margin-right: 0.4rem;
}
@keyframes ui-spin { to { transform: rotate(360deg); } }
.ui-watch-stages {
  display: flex; flex-wrap: wrap; gap: 0.5rem;
  padding: 0.5rem 1rem;
  background: var(--bg-2);
  border-left: 1px solid var(--border);
  border-right: 1px solid var(--border);
}
.ui-watch-pill {
  padding: 0.2rem 0.6rem;
  border-radius: 12px;
  background: var(--bg-1);
  border: 1px solid var(--border);
  color: var(--text-mute);
  font-size: 0.78rem;
}
.ui-watch-pill.active {
  border-color: var(--accent); color: var(--accent);
}
.ui-watch-pill.done {
  border-color: #3fb950; color: #3fb950;
}
.ui-watch-pill.error {
  border-color: var(--danger, #f85149); color: var(--danger, #f85149);
}
.ui-watch-status {
  flex: 1; overflow-y: auto; padding: 1rem;
  background: var(--bg-0);
  border: 1px solid var(--border);
  border-top: none;
  min-height: 12rem;
  max-height: 60vh;
}
.ui-watch-status-msg {
  color: var(--text-mute); font-size: 0.88rem;
  padding: 0.3rem 0;
}
.ui-watch-status-msg:last-child { color: var(--text); }
.ui-watch-draft {
  padding: 0.75rem 1rem;
  border-left: 1px solid var(--border);
  border-right: 1px solid var(--border);
  background: var(--bg-1);
}
.ui-watch-draft summary {
  cursor: pointer; color: var(--text-mute); font-size: 0.85rem;
  user-select: none;
}
.ui-watch-draft-body {
  margin-top: 0.7rem; color: var(--text);
  font-size: 0.9rem; line-height: 1.6;
}
.ui-watch-article {
  padding: 1.5rem 2rem;
  background: var(--bg-0);
  border: 1px solid var(--border);
  border-top: none;
}
.ui-watch-article-title {
  font-size: 1.4rem; color: var(--text-hi); margin: 0 0 1rem;
}
.ui-watch-article-body {
  color: var(--text); line-height: 1.7; font-size: 0.95rem;
}
.ui-watch-article-body h2 { font-size: 1.15rem; margin: 1.5rem 0 0.4rem; color: var(--text-hi); }
.ui-watch-article-body h3 { font-size: 1.02rem; margin: 1.2rem 0 0.35rem; color: var(--text-hi); }
.ui-watch-article-body p  { margin: 0.6rem 0; }
.ui-watch-done-actions {
  display: flex; gap: 0.5rem; flex-wrap: wrap; justify-content: center;
  padding: 1rem;
  border: 1px solid var(--border); border-top: none;
  background: var(--bg-1);
  border-radius: 0 0 6px 6px;
}
.ui-watch-action {
  padding: 0.5rem 1rem;
  background: var(--bg-2);
  border: 1px solid var(--border);
  border-radius: 6px;
  color: var(--text);
  font-size: 0.88rem;
  text-decoration: none;
  cursor: pointer;
}
.ui-watch-action:hover { border-color: var(--accent); }
.ui-watch-action.primary {
  background: var(--accent); color: #fff; border-color: var(--accent);
}
.ui-watch-action.danger {
  background: var(--danger, #da3633); color: #fff; border-color: var(--danger, #da3633);
}

/* --- ApiKeyPanel — single rotating API key with Copy/Generate --- */
.ui-apikey { display: flex; gap: 0.4rem; align-items: center; }
.ui-apikey-input {
  flex: 1; min-width: 0;
  padding: 0.4rem 0.55rem;
  border-radius: 6px;
  background: var(--bg-2);
  border: 1px solid var(--border);
  color: var(--text);
  font-family: monospace;
  font-size: 0.82rem;
  outline: none;
}
.ui-apikey-btn {
  flex-shrink: 0;
  padding: 0.4rem 0.7rem;
  border-radius: 6px;
  background: var(--bg-1);
  border: 1px solid var(--border);
  color: var(--text);
  font-size: 0.82rem;
  cursor: pointer;
}
.ui-apikey-btn:hover:not(:disabled) { border-color: var(--accent); }
.ui-apikey-btn:disabled { opacity: 0.5; cursor: not-allowed; }

/* --- SuggestPanel — LLM-backed topic suggestion list --- */
.ui-suggest { display: flex; flex-direction: column; gap: 0.5rem; }
.ui-suggest-controls { display: flex; gap: 0.5rem; align-items: center; }
.ui-suggest-direction { flex: 1; min-width: 0; }
.ui-suggest-btn { white-space: nowrap; }
.ui-suggest-list { display: flex; flex-direction: column; gap: 0.4rem; }
.ui-suggest-loading, .ui-suggest-empty {
  font-size: 0.85rem; color: var(--text-mute); padding: 0.4rem 0.2rem;
}
.ui-suggest-item {
  display: flex; align-items: center; gap: 0.5rem;
  padding: 0.55rem 0.7rem;
  background: var(--bg-1);
  border: 1px solid var(--border);
  border-radius: 6px;
}
.ui-suggest-item:hover { border-color: var(--accent); background: var(--bg-2); }
.ui-suggest-item-body { flex: 1; min-width: 0; }
.ui-suggest-item-q {
  color: var(--text); font-size: 0.9rem; font-weight: 500;
  white-space: normal; word-break: break-word;
}
.ui-suggest-item-hook {
  color: var(--text-mute); font-size: 0.8rem; margin-top: 0.15rem;
  white-space: normal; word-break: break-word;
}
.ui-suggest-secondary {
  flex-shrink: 0;
  font-size: 0.8rem;
  padding: 0.25rem 0.6rem;
}

/* --- FormField type="tags" — chip-style array editor --- */
.ui-tags {
  display: flex; flex-wrap: wrap; gap: 0.35rem;
  align-items: center;
  padding: 0.4rem 0.5rem;
  border-radius: 6px;
  background: var(--bg-2); border: 1px solid var(--border);
  min-height: calc(var(--tap) * 0.9);
}
.ui-tag {
  display: inline-flex; align-items: center; gap: 0.3rem;
  padding: 0.15rem 0.5rem;
  border-radius: 999px;
  background: var(--bg-1); border: 1px solid var(--border);
  color: var(--text); font-size: 0.82rem;
  white-space: nowrap;
}
.ui-tag-del {
  background: transparent; border: none; color: var(--text-mute);
  padding: 0; margin: 0; cursor: pointer;
  font-size: 1rem; line-height: 1;
}
.ui-tag-del:hover { color: var(--danger, #f85149); }
.ui-tag-input {
  flex: 1; min-width: 6rem;
  background: transparent; border: none;
  color: var(--text); font-size: 0.85rem; font-family: inherit;
  outline: none; padding: 0.1rem 0.2rem;
}
.ui-tags-empty {
  font-size: 0.78rem; color: var(--text-mute);
  padding: 0.1rem 0.2rem;
}

/* --- FormField type="rules" — list editor for line-separated rules --- */
.ui-rules { display: flex; flex-direction: column; gap: 0.35rem; }
.ui-rules-row {
  display: flex; align-items: center; gap: 0.4rem;
}
.ui-rules-num {
  flex: 0 0 auto; min-width: 1.4rem; text-align: right;
  font-size: 0.78rem; color: var(--text-mute); font-variant-numeric: tabular-nums;
}
.ui-rules-input {
  flex: 1; min-width: 0;
  padding: 0.4rem 0.55rem; border-radius: 6px;
  background: var(--bg-2); border: 1px solid var(--border);
  color: var(--text); font-size: 0.88rem; font-family: inherit;
  outline: none; transition: border-color 0.1s ease;
}
.ui-rules-input:focus { border-color: var(--accent); }
.ui-rules-del {
  flex: 0 0 auto;
  background: transparent; color: var(--text-mute);
  border: 1px solid var(--border); border-radius: 6px;
  width: 1.7rem; height: 1.7rem; padding: 0; cursor: pointer;
  font-size: 0.95rem; line-height: 1;
}
.ui-rules-del:hover { color: var(--danger, #f85149); border-color: var(--danger, #f85149); }
.ui-rules-add {
  align-self: flex-start;
  background: transparent; color: var(--accent); border: 1px dashed var(--accent);
  border-radius: 6px; padding: 0.35rem 0.7rem; font-size: 0.78rem; cursor: pointer;
  margin-top: 0.2rem;
}
.ui-rules-add:hover { background: var(--bg-2); }
.ui-rules-empty { font-size: 0.8rem; color: var(--text-mute); padding: 0.2rem 0; }
/* Chips row above a form input — applied to FormField with
   chips_source set. Each chip = preset; click applies to the input. */
.ui-form-chips {
  display: flex; flex-wrap: wrap; gap: 0.3rem; margin-bottom: 0.4rem;
}
.ui-form-chip {
  font-size: 0.72rem; padding: 0.2rem 0.55rem; border-radius: 999px;
  border: 1px solid var(--border); background: var(--bg-0); color: var(--text-mute);
  cursor: pointer; white-space: nowrap;
  -webkit-tap-highlight-color: transparent;
  user-select: none;
}
.ui-form-chip:hover { color: var(--text); border-color: var(--accent); }
.ui-form-chip-add { color: var(--accent); border-color: var(--accent); }
.ui-form-chips-loading { color: var(--text-mute); font-size: 0.72rem; padding: 0.2rem 0.55rem; }
/* Form chip create modal — AI Assist + Save flow. */
.ui-form-modal-overlay {
  position: fixed; inset: 0; z-index: 1100;
  background: rgba(0,0,0,0.55);
  display: flex; align-items: center; justify-content: center;
  padding: 1rem;
}
.ui-form-modal {
  background: var(--bg-1); border: 1px solid var(--border); border-radius: 10px;
  width: 100%; max-width: 520px;
  display: flex; flex-direction: column; gap: 0.4rem;
  padding: 1.2rem;
  box-shadow: 0 10px 30px rgba(0,0,0,0.5);
}
.ui-form-modal-h { font-size: 1rem; font-weight: 600; color: var(--text); }
.ui-form-modal-hint { font-size: 0.78rem; color: var(--text-mute); }
.ui-form-modal-actions { display: flex; gap: 0.4rem; align-items: center; margin-top: 0.6rem; }
.ui-form-saving { font-size: 0.7rem; color: var(--text-mute); margin-left: 0.4rem; opacity: 0; transition: opacity 0.15s ease; }
.ui-form-saving.show { opacity: 1; }

/* --- DisplayPanel --- */
.ui-display { display: flex; flex-direction: column; }
.ui-display-row {
  display: flex; align-items: center; justify-content: space-between;
  gap: 0.7rem; padding: 0.45rem 0; border-bottom: 1px solid var(--border);
}
.ui-display-row:last-child { border-bottom: none; }
.ui-display-label { font-size: 0.85rem; color: var(--text-mute); }
.ui-display-value { font-size: 0.9rem; color: var(--text); text-align: right; word-break: break-all; }
.ui-display-value.mono { font-family: ui-monospace, "SF Mono", Menlo, monospace; font-size: 0.82rem; }

/* --- BarChart --- */
.ui-chart { width: 100%; }
.ui-chart-svg { display: block; max-width: 100%; }
.ui-chart-axis { stroke: var(--border); stroke-width: 1; }
.ui-chart-grid { stroke: var(--border); stroke-width: 1; stroke-dasharray: 2 3; opacity: 0.5; }
.ui-chart-axis-label { font-size: 10px; fill: var(--text-mute); font-family: ui-monospace, monospace; }
.ui-chart-bar { fill: var(--accent); transition: fill 0.1s ease; }
.ui-chart-bar:hover { fill: var(--accent-hi); }
.ui-chart-empty { color: var(--text-mute); padding: 1rem 0; text-align: center; }
.ui-chart-summary { font-size: 0.8rem; color: var(--text-mute); margin-top: 0.5rem; text-align: right; }
.ui-chart-tip {
  position: absolute; z-index: 5;
  background: var(--bg-1); color: var(--text);
  border: 1px solid var(--border); border-radius: 8px;
  padding: 0.5rem 0.7rem; font-size: 0.82rem;
  white-space: nowrap; pointer-events: none;
  box-shadow: 0 4px 14px rgba(0,0,0,0.5);
  min-width: 12rem;
}
.ui-chart-tip-h { font-weight: 600; color: var(--text-hi); margin-bottom: 0.25rem; }
.ui-chart-tip-y { font-size: 1.05rem; color: var(--accent-hi); margin-bottom: 0.4rem; }
.ui-chart-tip-rows {
  border-top: 1px solid var(--border);
  padding-top: 0.4rem;
  display: grid; grid-template-columns: auto 1fr; gap: 0.15rem 0.7rem;
  font-family: ui-monospace, "SF Mono", Menlo, monospace; font-size: 0.75rem;
}
.ui-chart-tip-row { display: contents; }
.ui-chart-tip-label { color: var(--text-mute); }
.ui-chart-tip-val { color: var(--text); text-align: right; }
.ui-chart-tip-val.mono { font-family: ui-monospace, "SF Mono", Menlo, monospace; }

/* --- ActionList --- */
.ui-actionlist { display: flex; flex-direction: column; gap: 0.4rem; }
.ui-actionlist-row {
  display: flex; align-items: center; gap: 0.7rem;
  padding: 0.55rem 0.5rem;
  background: var(--bg-2); border: 1px solid var(--border); border-radius: 8px;
}
.ui-actionlist-text { flex: 1; min-width: 0; }
.ui-actionlist-label { font-size: 0.92rem; color: var(--text); }
.ui-actionlist-desc  { font-size: 0.78rem; color: var(--text-mute); margin-top: 0.15rem; }
.ui-actionlist-status { font-size: 0.78rem; color: var(--text-mute); min-width: 4rem; text-align: right; }
.ui-actionlist-empty { color: var(--text-mute); font-size: 0.9rem; padding: 0.5rem 0; }

/* --- JSONView --- */
.ui-jsonview-h { font-size: 0.7rem; color: var(--text-mute); text-transform: uppercase; letter-spacing: 0.04em; padding-bottom: 0.3rem; border-bottom: 1px solid var(--border); margin-bottom: 0.4rem; }
.ui-jsonview-body {
  background: var(--bg-0); color: var(--text);
  border: 1px solid var(--border); border-radius: 6px;
  padding: 0.55rem 0.7rem; margin: 0;
  font-family: ui-monospace, "SF Mono", Menlo, monospace; font-size: 0.78rem;
  line-height: 1.4;
  max-height: 40vh; overflow: auto;
  white-space: pre; word-break: normal;
}

/* --- ChatPanel ---
 * Desktop: two-column grid (sidebar + main). Sidebar always visible inline.
 * Mobile (≤700px): sidebar becomes a slide-in drawer with backdrop. Main
 * column gets a compact header bar with hamburger + session title +
 * new-chat shortcut. Matches the Claude mobile pattern: full-width chat,
 * sessions hidden behind a tap. */
.ui-chat {
  display: grid; grid-template-columns: 240px 1fr;
  gap: 1rem;
  /* Reserve only the actual page-header + root padding (~70px after
   * the header shrink) so the chat fills the rest of the viewport
   * instead of leaving a phantom gap at the bottom. Matches techwriter
   * / codewriter / agent-loop panels. min-height keeps mobile usable
   * when 100dvh is unreliable mid-keyboard. */
  height: calc(100vh - 70px);
  height: calc(100dvh - 70px);
  min-height: 400px;
  position: relative;
}
@media (min-width: 1100px) {
  /* Wider rail on big screens — fits the verdict snippet + pills on
   * one row each, makes session titles less likely to truncate. */
  .ui-chat { grid-template-columns: 280px 1fr; }
}
@media (min-width: 1500px) {
  .ui-chat { grid-template-columns: 320px 1fr; }
}
/* Collapsed state — hide the sessions sidebar, reflow the grid so
 * the main column fills the available width minus a small gutter
 * for the floating expand tab. Mirrors techwriter / codewriter. */
.ui-chat.side-collapsed { grid-template-columns: 1fr; }
.ui-chat.side-collapsed .ui-chat-side { display: none; }
.ui-chat.side-collapsed .ui-tw-expand { display: block; }
.ui-chat.side-collapsed .ui-chat-main { margin-left: 32px; }
.ui-chat-side {
  background: var(--bg-1); border: 1px solid var(--border); border-radius: 10px;
  display: flex; flex-direction: column; overflow: hidden;
}
.ui-chat-mobile-hdr { display: none; }
.ui-chat-backdrop { display: none; }

@media (max-width: 700px) {
  /* Mobile layout — single column, full-screen-ish chat. */
  .ui-chat {
    grid-template-columns: 1fr;
    /* Use dvh when available so iOS Safari's URL bar doesn't subtract
     * twice when collapsing. Fallback to vh for older browsers. */
    height: calc(100vh - 120px);
    height: calc(100dvh - 120px);
    min-height: 0;
  }
  /* Sidebar becomes a full-screen drawer that slides in over the chat —
   * on a phone the history list is the only useful thing while it's
   * open, so taking the whole viewport gives users far more rows to
   * scan than an 80%-wide partial drawer. */
  .ui-chat-side {
    position: absolute; top: 0; left: 0; bottom: 0; right: 0; z-index: 30;
    width: 100%; max-width: none;
    transform: translateX(-105%);
    transition: transform 0.22s ease;
    border-radius: 0;
    box-shadow: none;
  }
  .ui-chat-side.open { transform: translateX(0); }
  /* Backdrop dims the main column while drawer is open. */
  .ui-chat-backdrop {
    display: block;
    position: absolute; inset: 0; z-index: 25;
    background: rgba(0,0,0,0.5);
    opacity: 0; pointer-events: none;
    transition: opacity 0.22s ease;
  }
  .ui-chat-backdrop.show { opacity: 1; pointer-events: auto; }
  /* Mobile header — hamburger + title + new. */
  .ui-chat-mobile-hdr {
    display: flex; align-items: center; gap: 0.6rem;
    padding: 0.5rem 0.6rem;
    background: var(--bg-2); border-bottom: 1px solid var(--border);
  }
  .ui-chat-hamburger {
    background: transparent; color: var(--text);
    border: 1px solid var(--border); border-radius: 8px;
    min-width: 40px; min-height: 40px; padding: 0;
    font-size: 1.05rem; cursor: pointer;
    -webkit-tap-highlight-color: transparent;
  }
  .ui-chat-hamburger:active { background: var(--bg-1); }
  .ui-chat-mobile-title {
    flex: 1; min-width: 0;
    font-size: 0.95rem; font-weight: 600; color: var(--text);
    white-space: nowrap; overflow: hidden; text-overflow: ellipsis;
  }
  /* Trim main-column chrome on mobile so the chat dominates the viewport. */
  .ui-chat-modes { padding: 0.4rem 0.6rem; }
  .ui-chat-thread { padding: 0.6rem 0.7rem; }
  .ui-chat-input-area { padding: 0.5rem; padding-bottom: calc(0.5rem + env(safe-area-inset-bottom, 0px)); }
}
.ui-chat-side-h {
  display: flex; align-items: center; gap: 0.4rem;
  padding: 0.5rem 0.7rem;
  font-size: 0.8rem; color: var(--text-mute); text-transform: uppercase; letter-spacing: 0.04em;
  border-bottom: 1px solid var(--border);
}
/* Stretch the "Sessions" label so action buttons hug the right edge —
 * Select sits just left of + New, with Mobile-only × close on the far
 * left of the header. */
.ui-chat-side-h > span:first-child,
.ui-chat-side-h > .ui-chat-side-h-label { flex: 1; min-width: 0; }
.ui-chat-new {
  background: transparent; color: var(--accent); border: 1px solid var(--accent);
  border-radius: 6px; padding: 0.2rem 0.55rem; font-size: 0.75rem; cursor: pointer;
  -webkit-tap-highlight-color: transparent;
}
.ui-chat-new:hover { background: var(--bg-2); }
.ui-chat-new.active { background: var(--accent); color: var(--text-on-accent, #fff); }

/* Secondary sidebar button — same shape as .ui-chat-new but neutral
   color so it doesn't compete with the primary "New" action. Used
   for Select (matches header bg in default state, fills with accent
   when active so the click → mode-on transition is visible). */
.ui-chat-side-btn {
  background: transparent; color: var(--text-mute);
  border: 1px solid var(--border); border-radius: 6px;
  padding: 0.2rem 0.55rem; font-size: 0.75rem; cursor: pointer;
  -webkit-tap-highlight-color: transparent;
}
.ui-chat-side-btn:hover { color: var(--text); border-color: var(--text-mute); }
.ui-chat-side-btn.active {
  background: var(--accent); color: var(--text-on-accent, #fff);
  border-color: var(--accent);
}
.ui-chat-side-list { flex: 1; overflow-y: auto; padding: 0.2rem 0.35rem; }
/* Sidebar search — sits between the header and list, filters
   visible session rows by substring on title + meta text. */
.ui-chat-side-search {
  margin: 0.4rem 0.5rem;
  padding: 0.35rem 0.6rem;
  background: var(--bg-0); color: var(--text);
  border: 1px solid var(--border); border-radius: 6px;
  font-size: 0.78rem; font-family: inherit;
  -webkit-appearance: none;
}
.ui-chat-side-search:focus { outline: none; border-color: var(--accent); }
.ui-chat-side-search::placeholder { color: var(--text-mute); }
.ui-chat-side-item {
  position: relative;
  padding: 0.3rem 0.6rem; padding-right: 1.6rem;
  border-radius: 6px; cursor: pointer; user-select: none;
  margin-bottom: 0.1rem;
}
.ui-chat-side-text { flex: 1; min-width: 0; overflow: hidden; }
.ui-chat-side-item { display: flex; align-items: flex-start; gap: 0.5rem; }

/* --- Bulk-select pill + selected state ---
 * The pill toggles the side list into "select mode". While active,
 * tapping items toggles them into a selection (with a colored
 * highlight) instead of opening them. A second action bar at the
 * bottom shows Select-all / Delete once anything is checked. */
.ui-bulk-bar {
  display: flex; align-items: center; gap: 0.4rem;
  padding: 0.4rem 0.5rem;
  margin-bottom: 0.4rem;
}
.ui-bulk-bar.bottom { margin: 0.4rem 0 0; border-top: 1px solid var(--border); padding-top: 0.5rem; }
.ui-bulk-bar .ui-row-btn { padding: 0.3rem 0.65rem; font-size: 0.78rem; min-height: 0; min-width: 0; }
.ui-bulk-pill {
  background: transparent; color: var(--text-mute);
  border: 1px solid var(--border); border-radius: 999px;
  padding: 0.3rem 0.85rem; font-size: 0.78rem; cursor: pointer;
  -webkit-tap-highlight-color: transparent;
  transition: color 0.1s ease, border-color 0.1s ease, background 0.1s ease;
}
.ui-bulk-pill:hover { color: var(--text); border-color: var(--text-mute); }
.ui-bulk-pill.active {
  color: #1a1a1a; background: var(--accent); border-color: var(--accent);
  font-weight: 600;
}

/* Selectable items show a small left indicator + cursor change so
 * the user can tell which list rows are tappable in select mode.
 * Padding-left includes the indicator's full footprint plus extra
 * breathing room so the article/session title never collides with
 * the checkbox. */
.ui-chat-side-item.selectable {
  position: relative; cursor: pointer;
  padding-left: 2.2rem;
  transition: background 0.1s ease, border-color 0.1s ease;
}
.ui-chat-side-item.selectable::before {
  content: ''; position: absolute; left: 0.7rem; top: 50%; transform: translateY(-50%);
  width: 16px; height: 16px; border-radius: 4px;
  border: 1.5px solid var(--text-mute);
  background: transparent;
  transition: all 0.1s ease;
}
.ui-chat-side-item.selected {
  background: rgba(212, 166, 87, 0.12); /* amber-tinted on Blackboard */
  border-radius: 6px;
}
.ui-chat-side-item.selected::before {
  background: var(--accent);
  border-color: var(--accent);
  /* Tiny check mark via inline SVG-ish trick: use a unicode check. */
  content: '✓';
  color: #1a1a1a; font-weight: 700; font-size: 11px;
  display: flex; align-items: center; justify-content: center;
  line-height: 1;
}
.ui-chat-side-item:hover { background: var(--bg-2); }
.ui-chat-side-item.active { background: var(--bg-2); border-left: 2px solid var(--accent); padding-left: calc(0.6rem - 2px); }
/* When the active item is also selectable (bulk-select mode is on),
 * the checkbox indicator needs its 2.2rem indent — restore it instead
 * of letting .active's narrower padding stack the title under the
 * checkbox. -2px keeps the active border accounted for. */
.ui-chat-side-item.active.selectable { padding-left: calc(2.2rem - 2px); }
.ui-chat-side-title {
  font-size: 0.85rem; color: var(--text); line-height: 1.3;
  /* Allow up to 3 lines before truncating — single-line ellipsis
   * truncated too much information from long session titles get
   * truncated. -webkit-line-clamp is widely supported and
   * preserves the ellipsis at the cap point. */
  display: -webkit-box;
  -webkit-line-clamp: 3;
  -webkit-box-orient: vertical;
  overflow: hidden;
  word-break: break-word;
}
.ui-chat-side-meta { font-size: 0.7rem; color: var(--text-mute); margin-top: 0.05rem; }
.ui-chat-side-del {
  position: absolute; right: 0.4rem; top: 50%; transform: translateY(-50%);
  background: transparent; color: var(--text-mute);
  border: none; border-radius: 4px;
  width: 1.4rem; height: 1.4rem; cursor: pointer;
  font-size: 1rem; line-height: 1;
}
.ui-chat-side-del:hover { color: var(--danger); background: var(--bg-2); }
.ui-chat-side-ren {
  position: absolute; right: 2rem; top: 50%; transform: translateY(-50%);
  background: transparent; color: var(--text-mute);
  border: none; border-radius: 4px;
  width: 1.4rem; height: 1.4rem; cursor: pointer;
  font-size: 0.9rem; line-height: 1;
}
.ui-chat-side-ren:hover { color: var(--text-hi); background: var(--bg-2); }
/* Rows that carry a rename button (✎) need extra right padding
 * so the title doesn't run under both ✎ and × buttons. */
.ui-chat-side-item.ui-chat-side-item-renable { padding-right: 3.2rem; }
/* Drawer-close button — hidden on desktop where the sidebar is inline,
 * visible on mobile where the drawer covers the whole viewport and the
 * backdrop tap-target sits behind it. */
.ui-chat-side-close { display: none; }
@media (max-width: 700px) {
  .ui-chat-side-close {
    display: inline-flex; align-items: center; justify-content: center;
    background: transparent; color: var(--text-mute);
    border: 1px solid var(--border); border-radius: 6px;
    width: 1.7rem; height: 1.7rem; padding: 0;
    font-size: 1rem; line-height: 1; cursor: pointer;
    -webkit-tap-highlight-color: transparent;
  }
  .ui-chat-side-close:active { background: var(--bg-2); }
}

.ui-chat-main {
  background: var(--bg-1); border: 1px solid var(--border); border-radius: 10px;
  display: flex; flex-direction: column; overflow: hidden;
}
.ui-chat-thread {
  flex: 1; overflow-y: auto;
  padding: 0.8rem 1rem;
  display: flex; flex-direction: column; gap: 0.7rem;
}
.ui-chat-empty { color: var(--text-mute); text-align: center; padding: 2rem 1rem; font-size: 0.9rem; }
.ui-chat-msg {
  max-width: 85%;
  padding: 0.6rem 0.85rem; border-radius: 10px;
  font-size: 0.92rem; line-height: 1.5; white-space: pre-wrap; word-wrap: break-word;
}
.ui-chat-msg.user { background: var(--bg-2); align-self: flex-end; border: 1px solid var(--border); }
.ui-chat-msg.assistant { background: var(--bg-0); align-self: flex-start; border: 1px solid var(--border); border-left: 3px solid var(--accent); }
.ui-chat-actions {
  display: flex; gap: 0.4rem;
  margin-top: 0.4rem; padding-top: 0.4rem;
  border-top: 1px dashed var(--border);
}
.ui-chat-edit-bar { margin-top: 0.4rem; display: flex; flex-direction: column; gap: 0.4rem; }
.ui-chat-edit-ta {
  width: 100%; box-sizing: border-box; min-height: 60px;
  padding: 0.5rem 0.7rem; resize: vertical;
  background: var(--bg-0); color: var(--text);
  border: 1px solid var(--border); border-radius: 6px;
  font-family: inherit; font-size: 0.92rem; line-height: 1.4; outline: none;
}
.ui-chat-edit-ta:focus { border-color: var(--accent); }
.ui-chat-edit-actions { display: flex; gap: 0.4rem; }
.ui-chat-round-stats {
  margin-top: 0.4rem;
  font-family: ui-monospace, "SF Mono", Menlo, monospace;
  font-size: 0.7rem; color: var(--text-mute);
  letter-spacing: 0.02em;
  opacity: 0.75;
}
.ui-chat-stats-bar {
  padding: 0.35rem 0.9rem;
  border-top: 1px solid var(--border);
  font-family: ui-monospace, "SF Mono", Menlo, monospace;
  font-size: 0.72rem; color: var(--text-mute);
  background: var(--bg-2);
  letter-spacing: 0.02em;
}
.ui-chat-act {
  background: transparent; color: var(--text-mute); border: 1px solid var(--border);
  border-radius: 5px; padding: 0.2rem 0.55rem; font-size: 0.72rem; cursor: pointer;
}
.ui-chat-act:hover { color: var(--text); border-color: var(--text-mute); }

.ui-chat-tools-toggle {
  display: inline-flex; align-items: center; gap: 0.3rem;
  margin-top: 0.4rem; padding: 0.25rem 0.55rem;
  background: var(--bg-2); color: var(--text-mute);
  border: 1px solid var(--border); border-radius: 999px;
  font-size: 0.72rem; cursor: pointer;
  font-family: inherit;
  -webkit-tap-highlight-color: transparent;
}
.ui-chat-tools-toggle:hover { color: var(--text); border-color: var(--accent); }
.ui-chat-tools-toggle.open { background: var(--bg-1); color: var(--text); }
.ui-chat-tools-panel {
  margin-top: 0.4rem; padding: 0.4rem;
  background: var(--bg-1); border: 1px solid var(--border); border-radius: 6px;
  display: flex; flex-direction: column; gap: 0.3rem;
}
.ui-chat-tool {
  background: var(--bg-2); border: 1px solid var(--border); border-radius: 6px;
  font-size: 0.78rem; color: var(--text-mute);
  font-family: ui-monospace, monospace;
}
.ui-chat-tool-summary {
  list-style: none; cursor: pointer; user-select: none;
  padding: 0.35rem 0.65rem;
}
.ui-chat-tool-summary::-webkit-details-marker { display: none; }
.ui-chat-tool-summary::before {
  content: '▸'; display: inline-block; margin-right: 0.35rem;
  color: var(--text-mute); font-size: 0.7rem;
  transition: transform 0.12s ease;
}
.ui-chat-tool[open] > .ui-chat-tool-summary::before { transform: rotate(90deg); }
.ui-chat-tool-summary:hover { background: var(--bg-1); }
.ui-chat-tool-name { color: var(--accent); font-weight: 600; margin-right: 0.5rem; }
.ui-chat-tool-args { color: var(--text-mute); font-size: 0.72rem; }
.ui-chat-tool-body { padding: 0 0.5rem 0.5rem 0.5rem; }
.ui-chat-tool-empty { color: var(--text-mute); font-style: italic; padding: 0.3rem 0; }
.ui-chat-tool-result {
  align-self: flex-start; max-width: 100%;
  background: var(--bg-0); border: 1px solid var(--border); border-radius: 4px;
  padding: 0.45rem 0.65rem; margin: 0;
  font-family: ui-monospace, monospace; font-size: 0.75rem;
  color: var(--text-mute); max-height: 320px; overflow: auto;
  white-space: pre-wrap; word-break: break-word;
}
.ui-chat-status { align-self: center; font-size: 0.78rem; color: var(--text-mute); font-style: italic; padding: 0.2rem 0; }
.ui-chat-typing {
  display: inline-flex; align-items: center; gap: 4px;
  padding: 0.1rem 0.1rem;
}
.ui-chat-typing > span {
  width: 7px; height: 7px; border-radius: 50%;
  background: var(--text-mute);
  animation: ui-typing 1.2s ease-in-out infinite;
}
.ui-chat-typing > span:nth-child(2) { animation-delay: 0.15s; }
.ui-chat-typing > span:nth-child(3) { animation-delay: 0.3s; }
@keyframes ui-typing {
  0%, 60%, 100% { transform: translateY(0);   opacity: 0.4; }
  30%           { transform: translateY(-3px); opacity: 1;   }
}
.ui-chat-error { align-self: center; color: var(--danger); font-size: 0.85rem; padding: 0.4rem 0.6rem; }

.ui-chat-modesbar {
  display: flex; flex-wrap: wrap; align-items: center; gap: 0.6rem;
  padding: 0.5rem 0.7rem;
  border-bottom: 1px solid var(--border);
  background: var(--bg-2);
}
.ui-chat-modes { display: flex; flex-wrap: wrap; gap: 0.4rem; }
.ui-chat-empty-hint {
  margin-left: auto; color: var(--text-mute);
  font-size: 0.78rem; font-style: italic;
}
/* Tools badge — expandable "🔧 N tools" pill on the right of the
   modes bar. Click expands a popover listing every tool the LLM
   can call (name + description). */
.ui-chat-tools-wrap { position: relative; margin-left: auto; }
/* When the empty hint is showing, tools-wrap sits right after it on
   the right edge — no extra auto-margin needed (the hint already has
   margin-left:auto pushing the cluster right). Keep a small gap. */
.ui-chat-empty-hint:not([style*="display: none"]) + .ui-chat-tools-wrap {
  margin-left: 0.5rem;
}
.ui-chat-tools-badge {
  background: transparent; color: var(--text-mute);
  border: 1px solid var(--border); border-radius: 999px;
  padding: 0.2rem 0.6rem; font-size: 0.72rem; cursor: pointer;
  font-family: inherit;
  -webkit-tap-highlight-color: transparent;
}
.ui-chat-tools-badge:hover { color: var(--text); border-color: var(--accent); }
.ui-chat-tools-popover {
  position: absolute; right: 0; top: calc(100% + 4px); z-index: 60;
  min-width: 320px; max-width: 480px; max-height: 60vh; overflow-y: auto;
  background: var(--bg-1); border: 1px solid var(--border); border-radius: 6px;
  box-shadow: 0 4px 14px rgba(0,0,0,0.35);
  padding: 0.4rem;
  display: flex; flex-direction: column; gap: 0.3rem;
}
.ui-chat-tools-row {
  padding: 0.35rem 0.45rem; border-radius: 4px;
  background: var(--bg-2); border: 1px solid var(--border);
}
.ui-chat-tools-name {
  font-family: ui-monospace, monospace; font-size: 0.78rem;
  font-weight: 600; color: var(--accent);
}
.ui-chat-tools-desc {
  font-size: 0.72rem; color: var(--text-mute);
  line-height: 1.35; margin-top: 0.15rem;
}
.ui-chat-tools-loading { color: var(--text-mute); font-size: 0.78rem; padding: 0.5rem; }
@media (max-width: 600px) {
  .ui-chat-tools-popover {
    position: fixed; left: 0.5rem; right: 0.5rem; top: auto; bottom: 4rem;
    min-width: 0; max-width: none;
  }
}

/* --- PipelinePanel --- */
.ui-pl-form {
  padding: 0.7rem 0.9rem; border-bottom: 1px solid var(--border);
  background: var(--bg-2);
  display: flex; flex-direction: column; gap: 0.5rem;
}
.ui-pl-formrow { display: flex; flex-direction: column; gap: 0.2rem; }
.ui-pl-formlabel { font-size: 0.72rem; color: var(--text-mute); text-transform: uppercase; letter-spacing: 0.04em; }

/* Toggle row — laid out horizontally as a compact pill instead of
 * the default full-width stacked input. Hugs its content so the
 * label + switch don't dominate a form whose primary input is a
 * textarea. */
.ui-pl-formrow-toggle {
  flex-direction: row;
  align-items: center;
  align-self: flex-start;
  gap: 0.5rem;
  padding: 0.25rem 0.6rem 0.25rem 0.7rem;
  background: var(--bg-1);
  border: 1px solid var(--border);
  border-radius: 999px;
}
.ui-pl-formrow-toggle .ui-pl-formlabel {
  font-size: 0.74rem;
  text-transform: none;
  letter-spacing: 0;
  color: var(--text);
}
/* Small switch variant for inline use. Trimmed to ~70% of the
 * default switch footprint so a row of action buttons + toggle
 * doesn't tower over plain icons. */
.ui-switch.ui-switch-sm {
  width: 34px; height: 20px; border-radius: 10px;
}
.ui-switch.ui-switch-sm::before {
  width: 16px; height: 16px;
}
.ui-switch.ui-switch-sm:checked::before {
  transform: translateX(14px);
}
.ui-pl-input {
  font-family: inherit; font-size: 0.9rem;
  background: var(--bg-1); color: var(--text);
  border: 1px solid var(--border); border-radius: 6px;
  padding: 0.45rem 0.6rem;
}
.ui-pl-input:focus { outline: none; border-color: var(--accent); }
.ui-pl-textarea { resize: vertical; min-height: 3rem; }
.ui-pl-formactions {
  display: flex; justify-content: flex-end; gap: 0.4rem;
  padding-top: 0.2rem; align-items: center;
}
.ui-pl-prefill-wrap { position: relative; }
.ui-pl-prefill-menu {
  position: absolute; right: 0; top: calc(100% + 4px); z-index: 50;
  min-width: 360px; max-width: 560px;
  background: var(--bg-1); border: 1px solid var(--border); border-radius: 8px;
  box-shadow: 0 4px 14px rgba(0,0,0,0.35);
  max-height: 60vh; overflow-y: auto;
  padding: 0.4rem;
}
/* Suggest button — AI-styled with sparkle icon. Compact on mobile,
 * more prominent on desktop so it reads as a "magic helper" call to
 * action rather than a generic toolbar button. */
.ui-pl-prefill-btn {
  display: inline-flex; align-items: center; gap: 0.4rem;
  background: linear-gradient(135deg, rgba(212, 166, 87, 0.18), rgba(212, 166, 87, 0.06));
  color: var(--accent); border: 1px solid var(--accent);
  font-weight: 600;
}
.ui-pl-prefill-btn:hover:not(:disabled):not(.is-disabled) {
  background: linear-gradient(135deg, rgba(212, 166, 87, 0.28), rgba(212, 166, 87, 0.12));
  filter: none;
}
.ui-pl-prefill-icon {
  display: inline-block; font-size: 0.95em;
  /* Subtle pulse so the icon reads as an active AI affordance. */
  animation: ui-pl-sparkle 2.4s ease-in-out infinite;
}
@keyframes ui-pl-sparkle {
  0%, 100% { opacity: 1; transform: scale(1); }
  50%      { opacity: 0.7; transform: scale(0.9); }
}
@media (min-width: 700px) {
  .ui-pl-prefill-btn {
    padding: 0.55rem 1.1rem;
    font-size: 0.95rem;
    letter-spacing: 0.01em;
  }
  .ui-pl-prefill-icon { font-size: 1.05em; }
}
.ui-pl-prefill-item {
  padding: 0.65rem 0.8rem; border-radius: 6px; cursor: pointer;
  color: var(--text); line-height: 1.4;
}
.ui-pl-prefill-item + .ui-pl-prefill-item { margin-top: 0.25rem; }
.ui-pl-prefill-item:hover { background: var(--bg-2); }
.ui-pl-prefill-item:hover .ui-pl-prefill-item-q { color: var(--accent); }
.ui-pl-prefill-item-q {
  color: var(--text); font-size: 1rem; line-height: 1.4; font-weight: 500;
}
.ui-pl-prefill-item-hook {
  color: var(--text-mute); font-size: 0.85rem; line-height: 1.5;
  margin-top: 0.3rem;
}
/* On narrow viewports the popover would overflow off-screen
   if anchored to the button's right edge, and the min-width
   forces it wider than the viewport. Switch to a fixed-position
   strip with viewport-relative width so suggestions are always
   reachable. */
@media (max-width: 600px) {
  .ui-pl-prefill-menu {
    position: fixed;
    left: 0.5rem; right: 0.5rem; top: auto; bottom: 4rem;
    min-width: 0; max-width: none;
    max-height: 50vh;
  }
  .ui-pl-prefill-item { padding: 0.75rem 0.8rem; }
  .ui-pl-prefill-item-q { font-size: 1.02rem; }
  .ui-pl-prefill-item-hook { font-size: 0.88rem; }
}
.ui-pl-btn {
  font-family: inherit; font-size: 0.85rem;
  padding: 0.4rem 0.85rem; border-radius: 6px; cursor: pointer;
  border: 1px solid var(--border); background: var(--bg-1); color: var(--text);
  -webkit-tap-highlight-color: transparent;
}
.ui-pl-btn:disabled,
.ui-pl-btn.is-disabled { opacity: 0.4; cursor: not-allowed; pointer-events: none; }
.ui-pl-btn.primary { background: var(--accent); color: var(--text-on-accent, #fff); border-color: var(--accent); }
.ui-pl-btn.danger { background: transparent; color: var(--danger, #f85149); border-color: var(--danger, #f85149); }
.ui-pl-btn.secondary { background: var(--bg-2); }
.ui-pl-btn:hover:not(:disabled):not(.is-disabled) { filter: brightness(1.1); }

.ui-pl-actions {
  padding: 0.5rem 0.9rem;
  border-bottom: 1px solid var(--border); background: var(--bg-2);
  display: flex; flex-wrap: wrap; gap: 0.4rem; align-items: center;
}
/* Stream-modal — used by PipelineAction.Method=="modal" for things
   like Generate Report. Styled to match the legacy report
   modal: roomy content, big metallic h1, blue h2 with bottom rule. */
.ui-pl-modal-overlay {
  position: fixed; inset: 0; z-index: 1000;
  background: rgba(0,0,0,0.85);
  overflow-y: auto; padding: 2rem 1rem;
  display: flex; justify-content: center;
}
.ui-pl-modal {
  position: relative;
  background: var(--bg-0); border: 1px solid var(--border);
  border-radius: 12px; box-shadow: 0 12px 40px rgba(0,0,0,0.5);
  width: 100%; max-width: 800px; height: fit-content;
  padding: 2rem;
  display: flex; flex-direction: column; gap: 0.4rem;
}
.ui-pl-modal-h {
  display: flex; align-items: center; gap: 0.5rem;
  padding: 0 2.5rem 0 0; /* leave space for the absolute × */
  margin-bottom: 0.5rem;
}
.ui-pl-modal-title {
  font-size: 0.78rem; font-weight: 700; color: var(--text-mute);
  text-transform: uppercase; letter-spacing: 0.06em;
  flex-shrink: 0;
}
.ui-pl-modal-h-actions {
  display: flex; flex-wrap: wrap; gap: 0.35rem;
  margin-left: auto;
}
.ui-pl-modal-h-actions .ui-pl-btn { font-size: 0.75rem; padding: 0.3rem 0.7rem; }
.ui-pl-modal-close {
  position: absolute; top: 1rem; right: 1rem;
  background: transparent; border: 1px solid var(--border);
  color: var(--text-mute); font-size: 1.2rem; cursor: pointer;
  width: 2rem; height: 2rem; border-radius: 6px; line-height: 1;
  display: flex; align-items: center; justify-content: center;
  padding: 0;
}
.ui-pl-modal-close:hover { background: var(--bg-2); color: var(--text); }
.ui-pl-modal-body {
  font-size: 0.92rem; line-height: 1.7; color: var(--text);
}
/* Related-action popover items — the "click a topic to drill in"
 * style the legacy panels had inline. Each entry is a clickable card
 * with the question on top and an optional reason below. */
.ui-pl-related-h {
  font-size: 0.72rem; color: var(--text-mute);
  text-transform: uppercase; letter-spacing: 0.05em;
  margin: 0.6rem 0 0.4rem;
}
.ui-pl-related-h:first-child { margin-top: 0; }
.ui-pl-related-item {
  padding: 0.55rem 0.7rem;
  border: 1px solid var(--border); border-radius: 6px;
  margin-bottom: 0.4rem; cursor: pointer;
  transition: border-color 0.1s ease, background 0.1s ease;
  background: var(--bg-1);
}
.ui-pl-related-item:hover {
  border-color: var(--accent); background: var(--bg-2);
}
.ui-pl-related-item.accent { border-left: 3px solid var(--accent); }
/* Variant accents — match the legacy panel where unanswered
 * questions surfaced in amber (gap, needs follow-up) and previously-
 * answered questions surfaced in green (already covered, click to
 * open the existing report). The colored left rule + matching header
 * + matching reason-text color make the three groups scannable at
 * a glance instead of three identical white-bordered card stacks. */
.ui-pl-related-item.warning {
  border-left: 3px solid #d29922;
}
.ui-pl-related-item.warning:hover { border-color: #d29922; }
.ui-pl-related-item.success {
  border-left: 3px solid #3fb950;
}
.ui-pl-related-item.success:hover { border-color: #3fb950; }
.ui-pl-related-h.warning { color: #d29922; }
.ui-pl-related-h.success { color: #3fb950; }
.ui-pl-related-item.warning .ui-pl-related-reason { color: #d29922; }
.ui-pl-related-item.success .ui-pl-related-reason { color: #3fb950; }
.ui-pl-related-q { color: var(--text); font-size: 0.92rem; line-height: 1.4; }
.ui-pl-related-reason {
  color: var(--text-mute); font-size: 0.8rem;
  line-height: 1.45; margin-top: 0.2rem;
}
.ui-pl-related-hint {
  display: inline-block; margin-top: 0.3rem;
  font-size: 0.7rem; color: var(--accent);
  background: rgba(212, 166, 87, 0.12);
  padding: 0.1rem 0.45rem; border-radius: 999px;
}
/* Headline: Orbitron metallic gradient, matches legacy report h1. */
.ui-pl-modal-headline {
  font-family: 'Orbitron', -apple-system, BlinkMacSystemFont, 'Segoe UI', Helvetica, sans-serif;
  font-size: 1.5rem; line-height: 1.3; text-align: center;
  text-wrap: balance; margin: 0 0 0.5rem;
  background: linear-gradient(180deg, #f0f6fc 0%, #30363d 100%);
  -webkit-background-clip: text; -webkit-text-fill-color: transparent;
  background-clip: text;
}
.ui-pl-modal-sub {
  color: var(--text-mute); font-size: 0.85rem;
  margin-bottom: 0.2rem; text-align: center;
}
.ui-pl-modal-content { margin-top: 1rem; }
.ui-pl-modal-content p { margin: 0.5rem 0; }
.ui-pl-modal-content h1 {
  font-family: 'Orbitron', sans-serif;
  color: var(--text); font-size: 1.25rem; margin: 1rem 0 0.4rem;
}
.ui-pl-modal-content h2 {
  color: var(--accent); font-size: 1.1rem; margin: 1.5rem 0 0.5rem;
  border-bottom: 1px solid var(--border); padding-bottom: 0.3rem;
  text-transform: none; letter-spacing: normal; font-weight: 600;
}
.ui-pl-modal-content h2:first-child { margin-top: 0; }
.ui-pl-modal-content h3 {
  color: var(--text); font-size: 0.98rem; margin: 1rem 0 0.3rem;
  font-weight: 600; text-transform: none; letter-spacing: normal;
}
.ui-pl-modal-content ol, .ui-pl-modal-content ul {
  margin: 0.5rem 0; padding-left: 1.5rem;
}
.ui-pl-modal-content li { margin-bottom: 0.3rem; }
.ui-pl-modal-content strong { color: var(--text); }
.ui-pl-modal-content a { color: var(--accent); text-decoration: none; word-break: break-all; }
.ui-pl-modal-content a:hover { text-decoration: underline; }
.ui-pl-modal-content code {
  background: var(--bg-2); padding: 0.1rem 0.3rem; border-radius: 3px;
  font-size: 0.85em;
}
.ui-pl-modal-status {
  display: flex; gap: 0.4rem; align-items: center; justify-content: center;
  padding: 1rem; color: var(--text-mute); font-style: italic;
}
.ui-pl-actions .ui-pl-btn { font-size: 0.78rem; padding: 0.3rem 0.7rem; }
.ui-pl-btn-cancel { margin-left: auto !important; }
/* Inline feedback flash — the Copy Link button briefly turns green
   on success so the user gets confirmation without a corner toast. */
.ui-pl-btn.copied { background: #2da44e !important; color: #fff !important; border-color: #2da44e !important; }
/* Sidebar session-row meta fields: verdict snippet, confidence pill,
   winning-side pill. Compact, scannable. */
.ui-pl-side-metaline {
  font-size: 0.7rem; color: var(--text-mute); line-height: 1.3;
  margin-top: 0.08rem;
  /* Multi-line up to 3 lines so verdict snippets don't get
   * truncated to a useless half-sentence. line-clamp keeps the cap
   * sensible — anything longer collapses with an ellipsis. */
  display: -webkit-box;
  -webkit-line-clamp: 3;
  -webkit-box-orient: vertical;
  overflow: hidden;
  word-break: break-word;
}
.ui-pl-side-metalabel { color: var(--text-mute); font-weight: 600; }
.ui-pl-side-pillrow {
  display: flex; flex-wrap: wrap; gap: 0.25rem; align-items: center;
  margin-top: 0.1rem;
}
.ui-pl-side-pillrow .ui-chat-side-meta { margin-top: 0; }
.ui-pl-side-pill {
  display: inline-block; font-size: 0.6rem; font-weight: 700;
  padding: 0.02rem 0.3rem; border-radius: 999px;
  background: var(--bg-2); color: var(--text-mute);
  border: 1px solid var(--border);
  text-transform: uppercase; letter-spacing: 0.04em;
  white-space: nowrap; line-height: 1.4;
}
.ui-pl-session-title {
  padding: 0.55rem 0.95rem 0.45rem;
  border-bottom: 1px solid var(--border); background: var(--bg-1);
  font-size: 1rem; font-weight: 600; color: var(--text);
  line-height: 1.35;
}

.ui-pl-transcript {
  flex: 1; min-height: 0; overflow-y: auto; padding: 0.6rem 0.9rem;
  display: flex; flex-direction: column; gap: 0.35rem;
}
/* Don't let blocks shrink below their content height in the
 * column-flex transcript. Without flex-shrink: 0, a tall block
 * (e.g. the final synthesized report) gets squeezed by other
 * children, and combined with .ui-pl-block's overflow:hidden
 * the overflow gets clipped — that's the "report cuts off" bug. */
.ui-pl-block { flex-shrink: 0; }
.ui-pl-block {
  background: var(--bg-1); border: 1px solid var(--border); border-radius: 8px;
  overflow: hidden;
}
.ui-pl-block-h {
  padding: 0.4rem 0.7rem;
  background: var(--bg-2); border-bottom: 1px solid var(--border);
  font-weight: 600; font-size: 0.85rem; color: var(--text);
}
.ui-pl-block-body {
  padding: 0.6rem 0.8rem;
  font-size: 0.92rem; line-height: 1.55;
  color: var(--text);
  white-space: pre-wrap; word-break: break-word;
}
.ui-pl-block-body p { margin: 0.4rem 0; }
.ui-pl-block-body h1, .ui-pl-block-body h2, .ui-pl-block-body h3,
.ui-pl-block-body h4, .ui-pl-block-body h5, .ui-pl-block-body h6 {
  color: var(--text); font-weight: 600;
  text-transform: none; letter-spacing: normal;
}
/* Section-header scale — h1/h2 are the "section title" tier (larger
 * so they stand out as breaks); h3-h6 step down for headings inside
 * the section body. */
.ui-pl-block-body h1 { font-size: 1.15rem; margin: 1.6rem 0 0.5rem; }
.ui-pl-block-body h2 { font-size: 1.05rem; margin: 1.6rem 0 0.5rem; }
.ui-pl-block-body h3 { font-size: 0.95rem; margin: 1.3rem 0 0.4rem; }
.ui-pl-block-body h4 { font-size: 0.9rem;  margin: 1.1rem 0 0.35rem; }
.ui-pl-block-body h5,
.ui-pl-block-body h6 { font-size: 0.85rem; margin: 0.9rem 0 0.3rem; }

/* Status with spinner — live + done variants. The active state
 * uses a brighter foreground + accent-tinted border so the
 * "something is happening" indicator stands out against the
 * surrounding read-only blocks. The done variant fades back to
 * muted styling once the spinner stops, keeping the trail of
 * earlier statuses unobtrusive. */
.ui-pl-status {
  align-self: center;
  display: inline-flex; align-items: center; gap: 0.55rem;
  font-size: 0.82rem; color: var(--text); font-weight: 500;
  padding: 0.3rem 0.75rem; border-radius: 999px;
  background: var(--bg-2);
  border: 1px solid var(--accent);
  box-shadow: 0 0 0 3px rgba(212, 166, 87, 0.08);
}
.ui-pl-status-done {
  color: var(--text-mute); font-weight: normal;
  border-color: var(--border);
  box-shadow: none;
  opacity: 0.55;
}
.ui-pl-status-done .ui-pl-spinner { display: none; }

/* Generic spinning indicator — used by status pills, streaming
 * cards, and any "work in progress" affordance. Inline-block so it
 * sits beside text without breaking the line flow. The accent
 * border + subtle halo make it the focal point of whatever row
 * contains it. */
.ui-pl-spinner {
  display: inline-block; width: 0.85rem; height: 0.85rem;
  border: 2px solid var(--accent);
  border-right-color: transparent;
  border-radius: 50%;
  animation: ui-pl-spin 0.9s linear infinite;
  box-shadow: 0 0 0 1px rgba(212, 166, 87, 0.18);
}
@keyframes ui-pl-spin { to { transform: rotate(360deg); } }

/* Generic expandable card — collapsible "show details" panel. */
.ui-pl-expandable {
  margin-top: 0.6rem; padding: 0.5rem 0.75rem;
  background: var(--bg-0); border: 1px solid var(--border); border-radius: 6px;
  cursor: pointer;
}
.ui-pl-expandable-h {
  display: flex; justify-content: space-between; align-items: flex-start;
  gap: 0.6rem;
  font-size: 0.8rem;
}
.ui-pl-expandable-title {
  flex: 1; min-width: 0;
  font-weight: 700; color: var(--accent);
  white-space: normal; word-break: break-word;
}
.ui-pl-expandable-subtitle {
  margin-top: 0.2rem;
  font-weight: 500; color: var(--text-mute);
  font-size: 0.78rem;
}
.ui-pl-expandable-toggle {
  flex-shrink: 0;
  font-size: 0.7rem; color: var(--text-mute);
}
.ui-pl-expandable.expanded .ui-pl-expandable-toggle::after { content: ' ▾'; }
.ui-pl-expandable-body { display: none; margin-top: 0.5rem; font-size: 0.85rem; line-height: 1.5; color: var(--text); }
.ui-pl-expandable.expanded .ui-pl-expandable-body { display: block; }
.ui-pl-expandable-body h1, .ui-pl-expandable-body h2, .ui-pl-expandable-body h3 {
  font-size: 0.92rem; font-weight: 600; margin: 0.6rem 0 0.25rem;
  color: var(--text); text-transform: none; letter-spacing: normal;
}
.ui-pl-expandable-body p { margin: 0.3rem 0; }
.ui-pl-expandable-body code { background: var(--bg-2); padding: 0.05rem 0.25rem; border-radius: 3px; }

.ui-chat-field {
  display: inline-flex; align-items: center; gap: 0.3rem;
  font-size: 0.75rem; color: var(--text-mute);
}
.ui-chat-field-label { white-space: nowrap; }
.ui-chat-field-input {
  background: var(--bg-1); color: var(--text);
  border: 1px solid var(--border); border-radius: 4px;
  padding: 0.15rem 0.35rem; font-size: 0.75rem;
  font-family: inherit;
}
.ui-chat-field-input[type="number"] { width: 4rem; }
.ui-chat-mode {
  background: transparent; color: var(--text-mute);
  border: 1px solid var(--border); border-radius: 6px;
  padding: 0.25rem 0.6rem; font-size: 0.75rem; cursor: pointer;
  transition: color 0.1s ease, border-color 0.1s ease;
  -webkit-tap-highlight-color: transparent;
}
.ui-chat-mode:hover { color: var(--text); border-color: var(--text-mute); }
.ui-chat-mode.active { color: var(--accent); border-color: var(--accent); }

.ui-chat-input-area {
  display: flex; gap: 0.5rem; padding: 0.6rem; border-top: 1px solid var(--border);
}
.ui-chat-iconbtn {
  background: transparent; color: var(--text-mute);
  border: 1px solid var(--border); border-radius: 8px;
  min-width: 40px; min-height: 40px; padding: 0 0.6rem;
  font-size: 1.1rem; cursor: pointer;
  -webkit-tap-highlight-color: transparent; user-select: none;
}
.ui-chat-iconbtn:hover { color: var(--text); border-color: var(--text-mute); }
.ui-chat-iconbtn.active { color: var(--accent); border-color: var(--accent); }
.ui-chat-iconbtn.recording {
  background: var(--danger); color: #fff; border-color: var(--danger);
  animation: ui-mic-pulse 1s ease-in-out infinite;
}
@keyframes ui-mic-pulse { 0%,100% { box-shadow: 0 0 0 0 rgba(248,81,73,0.6); } 50% { box-shadow: 0 0 0 6px rgba(248,81,73,0); } }

.ui-chat-msg-body p:first-child { margin-top: 0; }
.ui-chat-msg-body p:last-child { margin-bottom: 0; }
.ui-chat-msg-body pre {
  background: var(--bg-2); border: 1px solid var(--border); border-radius: 6px;
  padding: 0.5rem 0.7rem; margin: 0.4rem 0;
  font-family: ui-monospace, monospace; font-size: 0.82rem;
  overflow-x: auto;
}
.ui-chat-msg-body code {
  background: var(--bg-2); padding: 0.05rem 0.3rem; border-radius: 4px;
  font-family: ui-monospace, monospace; font-size: 0.85em;
}
.ui-chat-msg-body pre code { background: transparent; padding: 0; }
.ui-chat-msg-body h1, .ui-chat-msg-body h2, .ui-chat-msg-body h3 {
  margin: 0.6rem 0 0.3rem; font-weight: 600;
}
.ui-chat-msg-body h1 { font-size: 1.1rem; }
.ui-chat-msg-body h2 { font-size: 1rem; }
.ui-chat-msg-body h3 { font-size: 0.95rem; color: var(--text-mute); }
.ui-chat-msg-body ul { margin: 0.4rem 0; padding-left: 1.3rem; }
.ui-chat-msg-body li { margin: 0.15rem 0; }
.ui-chat-msg-body a { color: var(--accent-hi); text-decoration: underline; }
.ui-chat-input {
  flex: 1; min-height: 40px; max-height: 200px; resize: none;
  padding: 0.5rem 0.7rem;
  background: var(--bg-0); color: var(--text);
  border: 1px solid var(--border); border-radius: 8px;
  font-family: inherit; font-size: 0.92rem; line-height: 1.4; outline: none;
}
.ui-chat-input:focus { border-color: var(--accent); }
.ui-chat-send, .ui-chat-cancel {
  padding: 0 1.1rem; min-height: 40px;
  border: 1px solid var(--accent); border-radius: 8px;
  background: var(--accent); color: #1a1a1a; font-weight: 600;
  cursor: pointer; -webkit-tap-highlight-color: transparent;
}
.ui-chat-send:disabled { opacity: 0.5; cursor: not-allowed; }
.ui-chat-cancel { background: transparent; color: var(--danger); border-color: var(--danger); }

/* --- CodeWriterPanel ---
 * Reuses the .ui-tw two-pane grid (sidebar + main) and adds the
 * codewriter toolbar + monospace code editor inside the main column.
 * Snippet titles are typically short ("list users", "backup db") so
 * the sidebar runs narrower than techwriter's article list. */
/* Halved sidebar width vs. techwriter (which inherits .ui-tw). The
 * compound .ui-cw.ui-tw selector beats .ui-tw on specificity so this
 * actually wins regardless of stylesheet ordering. Snippet titles
 * have ellipsis + hover tooltip so a narrow rail still works. */
.ui-cw.ui-tw { grid-template-columns: 175px 1fr; }
@media (min-width: 1100px) {
  .ui-cw.ui-tw { grid-template-columns: 215px 1fr; }
}
/* Mobile — collapse to single column so the editor pane isn't
 * squashed against a 175px sidebar. Matches techwriter's mobile
 * rule; specificity (2 classes) must match the base selector
 * for the override to apply. */
@media (max-width: 700px) {
  .ui-cw.ui-tw { grid-template-columns: 1fr; }
}

/* AgentLoopPanel — optional list rail (sessions/workspaces/...) +
 * conversation pane + activity pane (optionally split with a
 * terminal below). Reuses .ui-chat-side (list rail) styling so
 * apps that mix chat and agent surfaces look consistent.
 *
 * Layout matrix:
 *   - hasList + !sideCollapsed: 2-col grid (rail + main)
 *   - hasList + sideCollapsed:  1-col grid; floating expand tab
 *   - !hasList:                 1-col grid (no rail at all)
 */
.ui-agent {
  display: flex; flex-direction: column;
  gap: 0.6rem; position: relative;
  /* height (not min-height) so the panel is capped at viewport
   * size. Without the cap, a long workspace replay grows the
   * whole page taller than the viewport and the inner convo log's
   * overflow-y never engages — you'd scroll the page instead of
   * the conversation. Same pattern .ui-chat uses. */
  height: calc(100vh - 70px);
}
@media (max-width: 900px) {
  .ui-agent { height: calc(100vh - 120px); }
}
.ui-agent-topbar {
  display: flex; flex-direction: column;
  flex: 0 0 auto;
}
.ui-agent-grid {
  display: grid; grid-template-columns: 240px 1fr;
  gap: 1rem;
  flex: 1 1 auto; min-height: 0;
  position: relative;
}
@media (min-width: 900px)  { .ui-agent-grid { grid-template-columns: 280px 1fr; } }
@media (min-width: 1200px) { .ui-agent-grid { grid-template-columns: 320px 1fr; } }
.ui-agent.ui-agent-no-list  .ui-agent-grid,
.ui-agent.side-collapsed    .ui-agent-grid {
  /* Collapsed: 1-col grid. The expand tab is positioned absolutely
   * over the top-left corner; no column reservation needed. */
  grid-template-columns: 1fr;
}
.ui-agent.side-collapsed .ui-chat-side { display: none; }
@media (max-width: 900px) {
  .ui-agent-grid { grid-template-columns: 1fr; }
  .ui-agent .ui-chat-side { display: none; }
  .ui-agent .ui-chat-side.open { display: flex; }
  .ui-agent .ui-agent-main { margin-left: 0; }
  .ui-agent .ui-agent-expand { display: none; }
}
.ui-agent-collapse, .ui-agent-expand {
  background: transparent; color: var(--text-mute);
  border: 1px solid var(--border); border-radius: 3px;
  width: 26px; height: 24px; font-size: 0.95rem; line-height: 1;
  cursor: pointer; padding: 0;
  display: inline-flex; align-items: center; justify-content: center;
}
.ui-agent-collapse:hover, .ui-agent-expand:hover {
  color: var(--text-hi); border-color: var(--accent);
}
.ui-agent-expand {
  /* Positioned over the top-left of the main column (inside the
   * grid row, so it sits below the full-width topbar and never
   * overlaps the toolbar's buttons). Only visible when the rail
   * is collapsed; the in-rail ☰ does the reverse direction. */
  position: absolute; left: 4px; top: 6px;
  display: none; z-index: 2;
}
.ui-agent.side-collapsed .ui-agent-expand { display: block; }
.ui-agent-main {
  display: flex; flex-direction: column;
  background: var(--bg-1); border: 1px solid var(--border); border-radius: 6px;
  overflow: hidden;
  min-height: 0;
}
.ui-agent-status {
  padding: 0.4rem 0.7rem;
  background: var(--bg-2); color: var(--text-mute);
  font-size: 0.78rem; border-bottom: 1px solid var(--border);
}
.ui-agent-actions {
  display: flex; gap: 0.4rem; padding: 0.5rem 0.7rem;
  border-bottom: 1px solid var(--border); background: var(--bg-2);
  /* Wrap to a second/third row instead of running off-screen on
   * narrow viewports. Horizontal-scroll is the other option but
   * action buttons that need a tap discovery aren't great hidden
   * behind a scroll. */
  flex-wrap: wrap;
}
@media (max-width: 700px) {
  .ui-agent-actions { padding: 0.4rem 0.5rem; gap: 0.3rem; }
  .ui-agent-actions .ui-row-btn { font-size: 0.78rem; padding: 0.3rem 0.55rem; }
}
.ui-agent-split {
  display: flex; flex: 1 1 auto; min-height: 0;
}
.ui-agent-convo {
  flex: 1 1 60%;
  display: flex; flex-direction: column;
  min-width: 0; min-height: 0;
  /* Darker output canvas — matches the Activity pane's bg-0 so
   * the conversation surface reads like a console / editor body
   * rather than a card. Message bubbles still ride on bg-2,
   * giving them a clear lift off the canvas. */
  background: var(--bg-0);
}
.ui-agent-convo-log {
  flex: 1 1 auto; overflow-y: auto;
  padding: 0.8rem; display: flex; flex-direction: column; gap: 0.6rem;
}
.ui-agent-empty {
  color: var(--text-mute); font-style: italic; text-align: center;
  padding: 1.5rem 0;
}
.ui-agent-msg {
  max-width: 92%; padding: 0.55rem 0.85rem;
  border-radius: 6px; line-height: 1.45;
  word-wrap: break-word; overflow-wrap: anywhere;
  border: 1px solid var(--border);
}
/* User bubble — elevated bg + accent stripe on the right edge
 * (mirrors the assistant-stripe pattern in ChatPanel). The right
 * stripe pairs with the right-aligned layout so the eye reads
 * "this is your message" at a glance. */
.ui-agent-msg-user {
  background: var(--bg-2);
  align-self: flex-end;
  border-right: 3px solid var(--accent);
  color: var(--text-hi);
}
/* Mid-flight interjection — dimmer until the agent drains it, then
 * solidifies. The .consumed modifier is added by the app's
 * notes_consumed handler when the orchestrator picks it up. */
.ui-agent-msg-user.ui-agent-interjection {
  opacity: 0.7;
  font-style: italic;
}
.ui-agent-msg-user.ui-agent-interjection.consumed {
  opacity: 1; font-style: normal;
}
.ui-agent-msg-user.ui-agent-interjection-failed {
  border-right-color: var(--danger);
  opacity: 0.5;
}
/* Assistant bubble — flat panel against the dark canvas. The
 * absence of a colored stripe lets long replies (markdown, code
 * fences) read calmly without competing with the user bubble. */
.ui-agent-msg-assistant {
  background: var(--bg-1);
  align-self: flex-start;
}
.ui-agent-msg-system    {
  background: transparent; color: var(--text-mute); font-style: italic;
  align-self: center; font-size: 0.85rem;
}
.ui-agent-msg-body p:first-child { margin-top: 0; }
.ui-agent-msg-body p:last-child  { margin-bottom: 0; }
.ui-agent-msg-body pre {
  background: var(--bg-0); padding: 0.4rem 0.6rem;
  border-radius: 4px; overflow-x: auto;
}

.ui-agent-divider {
  flex: 0 0 4px; cursor: col-resize;
  background: var(--border);
}
.ui-agent-divider:hover { background: var(--accent); }

.ui-agent-right {
  flex: 1 1 40%;
  display: flex; flex-direction: column;
  background: var(--bg-0); min-width: 220px; min-height: 0;
}
.ui-agent-hdivider {
  flex: 0 0 4px; cursor: row-resize;
  background: var(--border);
}
.ui-agent-hdivider:hover { background: var(--accent); }
.ui-agent-terminal {
  flex: 1 1 40%; min-height: 100px;
  display: flex; flex-direction: column;
  background: var(--bg-0);
}
.ui-agent-terminal-h {
  padding: 0.4rem 0.7rem; border-bottom: 1px solid var(--border);
  font-size: 0.78rem; font-weight: 600; color: var(--text-hi);
  background: var(--bg-2);
}
.ui-agent-terminal-body {
  flex: 1 1 auto; overflow: hidden;
  background: #0d1117; color: #e6edf3;
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
  font-size: 0.78rem;
  min-height: 0;
  position: relative;
}
/* xterm.js fills its host; ensure the viewport claims the pane's
 * full height so cursor + scrollback aren't clipped. */
.ui-agent-terminal-body .xterm { height: 100%; }
.ui-agent-terminal-body .xterm-viewport { overflow-y: hidden !important; }
.ui-agent-terminal-placeholder {
  color: var(--text-mute); font-style: italic;
  padding: 0.4rem 0.6rem;
}

.ui-agent-activity {
  flex: 1 1 60%;
  display: flex; flex-direction: column;
  min-height: 0;
}
.ui-agent-activity.collapsed { flex: 0 0 0; min-width: 0; overflow: hidden; }
.ui-agent-activity-h {
  padding: 0.4rem 0.7rem; border-bottom: 1px solid var(--border);
  font-size: 0.78rem; font-weight: 600; color: var(--text-hi);
  background: var(--bg-2);
}
.ui-agent-activity-log {
  flex: 1 1 auto; overflow-y: auto; padding: 0.5rem 0.7rem;
  display: flex; flex-direction: column; gap: 0.35rem;
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
  font-size: 0.78rem;
}
.ui-agent-act {
  padding: 0.25rem 0.4rem; border-radius: 3px;
  white-space: pre-wrap; word-break: break-word;
  /* Each activity row keeps its natural size. Without this, the
   * column's default flex-shrink:1 squashes earlier rows as new
   * ones arrive — and once squashed, the collapsed/expanded
   * max-height rules can't enlarge them again. */
  flex: 0 0 auto;
}
.ui-agent-act-status { color: var(--text-mute); }
.ui-agent-act-cmd {
  background: var(--bg-2); color: var(--text-hi);
  border-left: 2px solid var(--accent);
}
.ui-agent-act-output {
  background: var(--bg-1); color: var(--text);
  max-height: 10em; overflow: hidden; cursor: pointer;
  position: relative;
  padding-bottom: 1.3rem;
}
/* Bottom strip always visible — "▾ Show more / ▴ Show less" so
 * the user sees the affordance even when the row is short.
 * pointer-events:none so clicks pass through to the parent div
 * (the click handler is attached to the row, not the pseudo). */
.ui-agent-act-output::after {
  position: absolute; left: 0; right: 0; bottom: 0;
  padding: 0.15rem 0.5rem;
  background: var(--bg-2); color: var(--accent);
  font-size: 0.72rem; text-align: center;
  border-top: 1px solid var(--border);
  font-family: system-ui, -apple-system, sans-serif;
  pointer-events: none;
}
.ui-agent-act-output.collapsed::after  { content: '▾  show more  ▾'; }
.ui-agent-act-output:not(.collapsed)   { max-height: none; }
.ui-agent-act-output:not(.collapsed)::after { content: '▴  show less  ▴'; }
.ui-agent-act-output:hover::after { background: var(--bg-1); }
/* Short outputs that fit within the cap: no truncation, no show
 * more, no pointer cursor — the affordance is a lie when the row
 * already shows everything. */
.ui-agent-act-output.no-truncate {
  max-height: none;
  cursor: default;
  padding-bottom: 0.25rem;
}
.ui-agent-act-output.no-truncate::after { display: none; }
.ui-agent-act-watch { color: var(--text-mute); display: flex; gap: 0.3rem; align-items: center; }
.ui-agent-act-error { color: var(--danger); }

.ui-agent-confirm {
  background: var(--warn-mute, #5a4a1a);
  border: 1px solid var(--warn, #d4a017);
  border-radius: 4px; padding: 0.5rem 0.6rem;
  margin: 0.3rem 0;
}
.ui-agent-confirm-prompt { font-weight: 600; color: var(--text-hi); margin-bottom: 0.3rem; }
.ui-agent-confirm-detail { color: var(--text-mute); font-size: 0.75rem; margin-bottom: 0.4rem; }
.ui-agent-confirm-btns  { display: flex; gap: 0.4rem; flex-wrap: wrap; }

.ui-agent-extras {
  display: flex; gap: 0.6rem; padding: 0.4rem 0.7rem;
  background: var(--bg-2);
  flex-wrap: wrap; align-items: center;
}
.ui-agent-extras-label {
  display: flex; align-items: center; gap: 0.35rem;
  font-size: 0.78rem; color: var(--text-mute);
}
.ui-agent-extras-label select, .ui-agent-extras-label input {
  min-width: 12rem;
}
.ui-agent-input-row {
  display: flex; gap: 0.4rem; align-items: stretch;
  padding: 0.5rem 0.7rem; border-top: 1px solid var(--border);
  background: var(--bg-1);
}
.ui-agent-input {
  flex: 1 1 auto; resize: vertical; min-height: 2.2rem;
  background: var(--bg-0); color: var(--text);
  border: 1px solid var(--border); border-radius: 4px;
  padding: 0.4rem 0.55rem; font: inherit;
}
.ui-agent-spinner { display: flex; align-items: center; padding: 0 0.4rem; }
.ui-agent-attach-strip {
  padding: 0.3rem 0.7rem; border-top: 1px solid var(--border);
  background: var(--bg-2); display: flex; gap: 0.4rem; flex-wrap: wrap;
  font-size: 0.78rem;
}
.ui-agent-attach-chip {
  background: var(--bg-1); border: 1px solid var(--border);
  border-radius: 12px; padding: 0.15rem 0.45rem;
  display: inline-flex; align-items: center; gap: 0.3rem;
}
.ui-agent-attach-x {
  background: none; border: 0; color: var(--text-mute); cursor: pointer;
  font-size: 1rem; line-height: 1; padding: 0;
}
.ui-agent-attach-x:hover { color: var(--danger); }
@media (min-width: 1500px) {
  .ui-cw.ui-tw { grid-template-columns: 240px 1fr; }
}
/* Compact snippet rows — title-only, ~half the height of a default
 * sidebar item. Full name + lang + date land in the row's title=
 * attribute so a hover tooltip surfaces them when the text ellipsizes
 * at the narrower sidebar width. */
.ui-cw .ui-chat-side-item {
  padding: 0.15rem 0.5rem; padding-right: 1.4rem;
  margin-bottom: 0.05rem;
  align-items: center;
}
.ui-cw .ui-chat-side-title {
  font-size: 0.85rem; line-height: 1.3;
}
.ui-cw .ui-chat-side-del {
  width: 1.2rem; height: 1.2rem; font-size: 0.85rem;
}
.ui-cw-main { background: var(--bg-1); }
.ui-cw-toolbar {
  display: flex; align-items: center; gap: 0.5rem;
  padding: 0.55rem 0.8rem; border-bottom: 1px solid var(--border);
  flex-wrap: wrap;
}
.ui-cw-name {
  flex: 1; min-width: 12rem;
  background: transparent; color: var(--text);
  border: none; outline: none;
  font-size: 1.0rem; font-weight: 600; font-family: inherit;
  padding: 0.3rem 0;
  border-bottom: 1px solid transparent;
  transition: border-color 0.15s ease;
}
.ui-cw-name:focus { border-bottom-color: var(--accent); }
.ui-cw-name::placeholder { color: var(--text-mute); font-weight: normal; }
.ui-cw-lang {
  background: var(--bg-0); color: var(--text);
  border: 1px solid var(--border); border-radius: 6px;
  padding: 0.35rem 0.55rem; font-size: 0.82rem; font-family: inherit;
  outline: none;
}
.ui-cw-lang:focus { border-color: var(--accent); }
.ui-cw-toolbar .ui-row-btn { padding: 0.4rem 0.85rem; min-height: 0; min-width: 0; font-size: 0.82rem; }
.ui-cw-toolbar .ui-row-btn.primary {
  background: var(--accent); color: var(--text-on-accent, #1a1a1a);
  border-color: var(--accent); font-weight: 600;
}
.ui-cw-toolbar .ui-row-btn.copied {
  background: #2da44e; color: #fff; border-color: #2da44e;
}
.ui-cw-saved {
  font-size: 0.72rem; color: var(--text-mute); white-space: nowrap;
  align-self: center;
}
/* Revision navigation group — pinned next to the snippet name. The
 * renderer toggles inline display:none/inline-flex via updateRevNav;
 * default in CSS is inline-flex so the layout intent is preserved
 * (don't set display:none here — clearing the inline override would
 * fall back to it and the group would never re-appear). */
.ui-cw-rev-group {
  align-items: center; gap: 0.25rem;
  padding: 0 0.4rem; margin: 0 0.2rem;
  border-left: 1px solid var(--border);
  border-right: 1px solid var(--border);
}
.ui-cw-rev-btn {
  padding: 0.25rem 0.5rem !important; min-height: 0; min-width: 0;
  font-size: 0.78rem;
}
.ui-cw-rev-btn:disabled { opacity: 0.4; cursor: not-allowed; }
.ui-cw-rev-ind {
  font-size: 0.72rem; color: var(--text-mute);
  font-variant-numeric: tabular-nums;
  white-space: nowrap;
  min-width: 4.5rem; text-align: center;
}
.ui-cw-rev-mark {
  padding: 0.25rem 0.55rem !important; font-size: 0.72rem;
  background: transparent; color: var(--accent); border-color: var(--accent);
}
.ui-cw-rev-mark:hover { background: var(--bg-2); }
.ui-cw-body {
  flex: 1 1 auto; min-height: 0;
  display: flex; flex-direction: row;
}
.ui-cw-editor-wrap {
  flex: 1 1 auto; min-width: 0; min-height: 0;
  display: flex; position: relative;
}
.ui-cw-editor {
  flex: 1; width: 100%; min-height: 0;
  resize: none; outline: none;
  background: var(--bg-0); color: var(--text);
  border: none;
  padding: 0.85rem 1rem;
  font-family: ui-monospace, "SF Mono", Menlo, Consolas, monospace;
  font-size: 0.88rem; line-height: 1.5; tab-size: 2;
}

/* Chat pane on the right side of the body row. Matches the tw-asst
 * pattern: header + scrollable transcript + sticky input area. */
.ui-cw-chat {
  /* flex-basis auto so inline width set by the chat resizer takes
   * effect — fixed flex-basis would clobber the inline value and
   * the drag would visually do nothing. */
  flex: 0 0 auto;
  width: 360px; min-width: 240px;
  display: flex; flex-direction: column;
  border-left: 1px solid var(--border);
  background: var(--bg-1);
  min-height: 0;
}
@media (max-width: 900px) {
  .ui-cw-body { flex-direction: column; }
  .ui-cw-chat {
    flex: 0 0 auto; max-width: none;
    border-left: none; border-top: 1px solid var(--border);
    max-height: 45%;
  }
}
.ui-cw-chat-h {
  display: flex; align-items: center; justify-content: space-between;
  padding: 0.45rem 0.7rem;
  font-size: 0.78rem; color: var(--text-mute);
  text-transform: uppercase; letter-spacing: 0.04em;
  border-bottom: 1px solid var(--border);
}
.ui-cw-chat-h .ui-row-btn { padding: 0.25rem 0.55rem; font-size: 0.72rem; min-height: 0; min-width: 0; }
.ui-cw-chat-msgs {
  flex: 1 1 auto; min-height: 0;
  overflow-y: auto;
  padding: 0.75rem;
  display: flex; flex-direction: column; gap: 0.55rem;
}
.ui-cw-msg {
  max-width: 95%;
  padding: 0.5rem 0.7rem;
  border-radius: 10px;
  font-size: 0.86rem; line-height: 1.45;
  background: var(--bg-2); color: var(--text);
}
.ui-cw-msg.user {
  align-self: flex-end;
  background: var(--accent); color: var(--text-on-accent, #1a1a1a);
  border-radius: 10px 10px 2px 10px;
}
.ui-cw-msg.assistant {
  align-self: flex-start;
  border-radius: 10px 10px 10px 2px;
}
.ui-cw-msg p { margin: 0 0 0.4rem 0; }
.ui-cw-msg p:last-child { margin-bottom: 0; }
.ui-cw-msg pre {
  background: var(--bg-0);
  border: 1px solid var(--border);
  border-radius: 6px;
  padding: 0.5rem 0.65rem;
  font-family: ui-monospace, "SF Mono", Menlo, Consolas, monospace;
  font-size: 0.8rem; line-height: 1.45;
  overflow-x: auto;
  white-space: pre;
  margin: 0.2rem 0;
}
.ui-cw-mode-tag {
  display: inline-block;
  font-size: 0.62rem; font-weight: 700;
  padding: 0.05rem 0.4rem;
  border-radius: 999px;
  background: var(--bg-1); color: var(--text-mute);
  text-transform: uppercase; letter-spacing: 0.04em;
  margin-right: 0.3rem;
}
.ui-cw-msg.user .ui-cw-mode-tag {
  background: rgba(0,0,0,0.15); color: rgba(0,0,0,0.7);
}
.ui-cw-spinner {
  display: inline-block; width: 8px; height: 8px;
  border-radius: 50%;
  background: var(--text-mute);
  animation: ui-cw-blink 1s ease-in-out infinite;
}
@keyframes ui-cw-blink { 50% { opacity: 0.2; } }
.ui-cw-err { color: var(--danger, #f85149); }
.ui-cw-diff-summary {
  margin-top: 0.4rem;
  font-size: 0.78rem; color: var(--text-mute);
  font-style: italic;
}
.ui-cw-chat-input-area {
  border-top: 1px solid var(--border);
  padding: 0.55rem; display: flex; gap: 0.4rem; flex-wrap: wrap;
}
.ui-cw-chat-input {
  flex: 1 1 100%;
  background: var(--bg-0); color: var(--text);
  border: 1px solid var(--border); border-radius: 6px;
  padding: 0.5rem 0.6rem;
  font-family: inherit; font-size: 0.85rem; line-height: 1.4;
  resize: vertical; outline: none;
}
.ui-cw-chat-input:focus { border-color: var(--accent); }
.ui-cw-chat-input-area .ui-row-btn { flex: 1 1 auto; }
.ui-cw-chat-input-area .ui-row-btn.primary {
  background: var(--accent); color: var(--text-on-accent, #1a1a1a);
  border-color: var(--accent); font-weight: 600;
}

/* Editor wrap stacks: [code editor (flex)] over [context section]. */
.ui-cw-editor-wrap { flex-direction: column; }

/* Resize handles. Vertical (chat-resizer) is a 4px column between
 * editor and chat pane; horizontal (ctx-resizer) is a 4px row between
 * the code editor and the context section. Hover and active states
 * tint the bar with the accent color so the drag affordance is
 * obvious. */
.ui-cw-chat-resizer {
  flex: 0 0 5px;
  cursor: col-resize;
  background: var(--border);
  transition: background 0.1s ease;
  align-self: stretch;
}
.ui-cw-chat-resizer:hover,
.ui-cw-chat-resizer.dragging { background: var(--accent); }
.ui-cw-ctx-resizer {
  flex: 0 0 5px;
  cursor: row-resize;
  background: var(--border);
  transition: background 0.1s ease;
  width: 100%;
}
.ui-cw-ctx-resizer:hover,
.ui-cw-ctx-resizer.dragging { background: var(--accent); }
@media (max-width: 900px) {
  /* On stacked mobile layout the chat resizer would resize the wrong
   * axis (width while everything is column-flowed). Hide it. */
  .ui-cw-chat-resizer { display: none; }
}

/* Collapsible context section under the editor. Header bar acts as the
 * toggle; pane underneath holds a second textarea for reference docs. */
.ui-cw-ctx-section {
  display: flex; flex-direction: column;
  border-top: 1px solid var(--border);
  background: var(--bg-1);
  flex: 0 0 auto; max-height: 35%;
}
.ui-cw-ctx-section.ui-cw-ctx-pane-only-collapsed { /* placeholder */ }
.ui-cw-ctx-toggle {
  display: flex; align-items: center; gap: 0.4rem;
  padding: 0.4rem 0.7rem;
  font-size: 0.78rem; color: var(--text-mute);
  cursor: pointer; user-select: none;
  border-bottom: 1px solid transparent;
}
.ui-cw-ctx-toggle:hover { color: var(--text); }
.ui-cw-ctx-arrow {
  display: inline-block; transition: transform 0.15s ease;
  font-size: 0.7rem;
}
.ui-cw-ctx-arrow.open { transform: rotate(90deg); }
.ui-cw-ctx-actions { display: inline-flex; align-items: center; gap: 0.35rem; margin-left: auto; }
.ui-cw-ctx-btn {
  background: transparent; color: var(--text-mute);
  border: 1px solid var(--border); border-radius: 4px;
  padding: 0.2rem 0.5rem; font-size: 0.72rem; cursor: pointer;
  font-family: inherit;
}
.ui-cw-ctx-btn:hover { color: var(--text); border-color: var(--text-mute); }
.ui-cw-ctx-current { font-size: 0.72rem; color: var(--accent); margin-left: 0.4rem; font-style: italic; }
.ui-cw-ctx-pane {
  display: none; flex: 1 1 auto; min-height: 0;
  border-top: 1px solid var(--border);
}
.ui-cw-ctx-pane.open { display: flex; }
.ui-cw-ctx-editor {
  flex: 1; width: 100%; min-height: 80px;
  resize: none; outline: none;
  background: var(--bg-0); color: var(--text);
  border: none;
  padding: 0.55rem 0.75rem;
  font-family: ui-monospace, "SF Mono", Menlo, Consolas, monospace;
  font-size: 0.82rem; line-height: 1.45;
}

/* --- CodeWriterPanel modal (variables / values / contexts) --- */
.ui-cw-modal-overlay {
  display: none;
  position: fixed; inset: 0; z-index: 1000;
  background: rgba(0,0,0,0.55); backdrop-filter: blur(4px);
  align-items: center; justify-content: center;
  padding: 1rem;
}
.ui-cw-modal-overlay.open { display: flex; }
.ui-cw-modal-box {
  background: var(--bg-1); border: 1px solid var(--border);
  border-radius: 10px;
  padding: 1.1rem 1.2rem;
  width: 100%; max-width: 540px; max-height: 85vh;
  overflow-y: auto;
  display: flex; flex-direction: column; gap: 0.5rem;
  box-shadow: 0 12px 32px rgba(0,0,0,0.5);
}
.ui-cw-modal-box h3 {
  margin: 0 0 0.3rem 0; font-size: 1.05rem; color: var(--text);
}
.ui-cw-modal-desc { font-size: 0.82rem; color: var(--text-mute); margin-bottom: 0.3rem; }
.ui-cw-modal-box label {
  display: block; font-size: 0.72rem; color: var(--text-mute);
  text-transform: uppercase; letter-spacing: 0.04em;
  margin: 0.4rem 0 0.2rem;
}
.ui-cw-modal-box input[type="text"] {
  width: 100%; box-sizing: border-box;
  padding: 0.45rem 0.6rem;
  background: var(--bg-0); color: var(--text);
  border: 1px solid var(--border); border-radius: 6px;
  font-family: inherit; font-size: 0.85rem;
  outline: none;
}
.ui-cw-modal-box input[type="text"]:focus { border-color: var(--accent); }
.ui-cw-modal-box input.mono {
  font-family: ui-monospace, "SF Mono", Menlo, Consolas, monospace;
  font-size: 0.82rem;
}
.ui-cw-modal-btns {
  display: flex; gap: 0.45rem; justify-content: flex-end;
  margin-top: 0.6rem;
}
.ui-cw-modal-btns .ui-row-btn.primary {
  background: var(--accent); color: var(--text-on-accent, #1a1a1a);
  border-color: var(--accent); font-weight: 600;
}

.ui-cw-var-inputs { display: flex; flex-direction: column; gap: 0.5rem; }
.ui-cw-var-row {
  display: grid;
  grid-template-columns: 7rem 1fr;
  gap: 0.4rem 0.5rem;
  align-items: center;
}
.ui-cw-var-row label {
  margin: 0; padding: 0; text-transform: none; letter-spacing: 0;
  font-size: 0.85rem; color: var(--text);
  font-family: ui-monospace, "SF Mono", Menlo, Consolas, monospace;
}
.ui-cw-var-picker {
  grid-column: 2;
  background: var(--bg-2); color: var(--text-mute);
  border: 1px solid var(--border); border-radius: 6px;
  padding: 0.3rem 0.45rem; font-size: 0.78rem; font-family: inherit;
  outline: none;
}
.ui-cw-var-picker:focus { border-color: var(--accent); }

.ui-cw-list { display: flex; flex-direction: column; gap: 0.35rem; }
.ui-cw-list-row {
  display: flex; align-items: center; gap: 0.5rem;
  padding: 0.5rem 0.6rem;
  background: var(--bg-2); border: 1px solid var(--border); border-radius: 6px;
}
.ui-cw-list-info { flex: 1; min-width: 0; overflow: hidden; }
.ui-cw-list-title {
  font-size: 0.88rem; color: var(--text); font-weight: 600;
  overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
}
.ui-cw-list-meta {
  font-size: 0.74rem; color: var(--text-mute);
  overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
}
.ui-cw-list-meta.mono {
  font-family: ui-monospace, "SF Mono", Menlo, Consolas, monospace;
}
.ui-cw-list-btn {
  background: transparent; color: var(--text-mute);
  border: 1px solid var(--border); border-radius: 6px;
  padding: 0.3rem 0.55rem; font-size: 0.78rem; cursor: pointer;
  font-family: inherit; flex-shrink: 0;
}
.ui-cw-list-btn:hover { color: var(--text); border-color: var(--text-mute); }
.ui-cw-list-btn.danger { width: 1.8rem; height: 1.8rem; padding: 0; }
.ui-cw-list-btn.danger:hover { color: var(--danger, #f85149); border-color: var(--danger, #f85149); }
.ui-cw-empty {
  font-size: 0.85rem; color: var(--text-mute);
  padding: 0.4rem 0.2rem; text-align: center;
}

/* --- ArticleEditor --- */
.ui-tw {
  display: grid; grid-template-columns: 175px 1fr;
  gap: 1rem;
  /* Subtract just the page header (back link + title) so the editor
   * uses essentially every available pixel. dvh handles iOS Safari's
   * collapsing URL bar; vh fallback for everything else.
   * Sidebar widths match codewriter — narrower than the original
   * 280/340/380 because article rows are title-only with hover
   * tooltip, no body preview. */
  height: calc(100vh - 70px);
  height: calc(100dvh - 70px);
  min-height: 500px;
  position: relative;
}
@media (min-width: 1100px) {
  .ui-tw { grid-template-columns: 215px 1fr; }
}
@media (min-width: 1500px) {
  .ui-tw { grid-template-columns: 240px 1fr; }
}
/* Compact title-only rows (mirrors .ui-cw treatment). Drops the
 * date meta line and tightens padding so more articles fit on one
 * screen. Full title + date land in the row's title attribute. */
.ui-tw .ui-chat-side-item {
  padding: 0.15rem 0.5rem; padding-right: 1.4rem;
  margin-bottom: 0.05rem;
  align-items: center;
}
.ui-tw .ui-chat-side-title {
  font-size: 0.85rem; line-height: 1.3;
}
.ui-tw .ui-chat-side-meta { display: none; }
.ui-tw .ui-chat-side-del {
  width: 1.2rem; height: 1.2rem; font-size: 0.85rem;
}
.ui-tw-side {
  background: var(--bg-1); border: 1px solid var(--border); border-radius: 10px;
  display: flex; flex-direction: column; overflow: hidden;
  transition: opacity 0.18s ease;
}
.ui-tw-side-h {
  display: flex; align-items: center; gap: 0.4rem;
  padding: 0.5rem 0.7rem;
  font-size: 0.8rem; color: var(--text-mute); text-transform: uppercase; letter-spacing: 0.04em;
  border-bottom: 1px solid var(--border);
}
.ui-tw-side-h > span:first-child { flex: 1; }
.ui-tw-collapse {
  background: transparent; color: var(--text-mute);
  border: 1px solid var(--border); border-radius: 6px;
  width: 26px; height: 26px; padding: 0; cursor: pointer;
  font-size: 0.95rem; line-height: 1;
}
.ui-tw-collapse:hover { color: var(--text); border-color: var(--text-mute); }
@media (max-width: 700px) { .ui-tw-collapse { display: none; } }
.ui-tw-side-list { flex: 1; overflow-y: auto; padding: 0.3rem 0.4rem; }

/* Floating expand tab — only visible when the sidebar is collapsed.
 * Sits flush against the left edge of the main pane so the user can
 * always reopen the list without digging through menus. */
.ui-tw-expand {
  display: none;
  position: absolute; top: 0.5rem; left: 0; z-index: 5;
  background: var(--bg-2); color: var(--text-mute);
  border: 1px solid var(--border); border-left: none;
  border-radius: 0 8px 8px 0;
  width: 26px; height: 36px; padding: 0; cursor: pointer;
  font-size: 1rem; line-height: 1;
}
.ui-tw-expand:hover { color: var(--text); }

/* Collapsed state — sidebar slides out and the grid reflows so the
 * main pane fills the entire width minus a small reserved gutter for
 * the floating expand tab. Without the gutter, the tab overlaps the
 * title input on the left edge of the editor. */
.ui-tw.side-collapsed { grid-template-columns: 1fr; }
.ui-tw.side-collapsed .ui-tw-side { display: none; }
.ui-tw.side-collapsed .ui-tw-expand { display: block; }
.ui-tw.side-collapsed .ui-tw-main { margin-left: 32px; }

.ui-tw-main {
  background: var(--bg-1); border: 1px solid var(--border); border-radius: 10px;
  display: flex; flex-direction: column;
  overflow: hidden; min-height: 0;
  position: relative; /* Anchor the revisions / rules slide-in panels. */
}
.ui-tw-titlebar {
  display: flex; align-items: center; gap: 0.5rem;
  padding: 0.55rem 0.8rem; border-bottom: 1px solid var(--border);
  flex-wrap: wrap;
}
.ui-tw-titlebar .ui-row-btn { padding: 0.4rem 0.85rem; min-height: 0; min-width: 0; font-size: 0.82rem; }

/* "More" menu — anchored popover that holds less-frequent actions
 * (Suggest title, Generate image, etc) so the titlebar doesn't get
 * crowded. Shows below + right-aligned to the trigger button. */
.ui-tw-extras-wrap { position: relative; display: inline-flex; }
.ui-tw-extras-menu {
  position: absolute; top: calc(100% + 4px); right: 0;
  min-width: 180px;
  background: var(--bg-1); border: 1px solid var(--border);
  border-radius: 8px;
  box-shadow: 0 4px 12px rgba(0,0,0,0.4);
  padding: 0.25rem; z-index: 50;
  display: flex; flex-direction: column;
}
.ui-tw-extras-item {
  background: transparent; border: none;
  color: var(--text); text-align: left;
  padding: 0.45rem 0.7rem; border-radius: 6px;
  font-family: inherit; font-size: 0.85rem; cursor: pointer;
  white-space: nowrap;
}
.ui-tw-extras-item:hover { background: var(--bg-2); color: var(--text-hi); }
.ui-tw-title {
  flex: 1; min-width: 0;
  background: transparent; color: var(--text-hi);
  border: none; outline: none;
  font-size: 1.05rem; font-weight: 600; font-family: inherit;
  padding: 0.3rem 0;
  border-bottom: 1px solid transparent;
  transition: border-color 0.15s ease;
}
.ui-tw-title:focus { border-bottom-color: var(--accent); }
.ui-tw-saved { font-size: 0.72rem; color: var(--text-mute); white-space: nowrap; flex-shrink: 0; }
.ui-tw-body {
  width: 100%; min-height: 200px;
  flex: 1 1 auto; /* absorb remaining vertical space inside the flex-column main */
  padding: 0.8rem 1rem;
  background: var(--bg-0); color: var(--text);
  border: none; outline: none; resize: none;
  font-family: ui-monospace, "SF Mono", Menlo, monospace;
  font-size: 0.9rem; line-height: 1.55;
  box-sizing: border-box;
}

.ui-tw-asst {
  display: flex; flex-direction: column;
  border-top: 1px solid var(--border);
  background: var(--bg-2);
  max-height: 35%; /* default cap; overridden by inline style when the user drag-resizes */
  flex-shrink: 0;
}
/* Drag handle between editor body and assistant pane. Six pixels
 * tall is enough to grab without being visually noisy. Hover-and
 * active states use the accent color so the handle reads as
 * interactive without a label. */
.ui-tw-resizer {
  height: 6px; flex-shrink: 0;
  background: var(--border);
  cursor: ns-resize;
  transition: background 0.1s ease;
  position: relative;
}
.ui-tw-resizer:hover, .ui-tw-resizer:active { background: var(--accent); }
/* A subtle 3-dot grip indicator centered on the handle so users
 * realize it can be dragged (vs an inert separator line). */
.ui-tw-resizer::before {
  content: ''; position: absolute; left: 50%; top: 50%;
  transform: translate(-50%, -50%);
  width: 30px; height: 2px;
  background: rgba(255, 255, 255, 0.18);
  border-radius: 1px;
}
.ui-tw-resizer:hover::before { background: rgba(0, 0, 0, 0.4); }
.ui-tw-asst-thread {
  flex: 1; overflow-y: auto; padding: 0.5rem 0.7rem;
  display: flex; flex-direction: column; gap: 0.45rem;
  min-height: 80px;
}
.ui-tw-asst-empty { color: var(--text-mute); font-size: 0.82rem; padding: 0.4rem 0.2rem; }
.ui-tw-asst-input-row {
  display: flex; gap: 0.5rem; padding: 0.5rem 0.7rem;
  border-top: 1px solid var(--border);
}
.ui-tw-image-row {
  display: flex; gap: 0.6rem; align-items: flex-start;
  padding: 0.6rem 0.8rem; border-bottom: 1px solid var(--border);
  background: var(--bg-2);
}
.ui-tw-image { max-width: 360px; max-height: 200px; border-radius: 6px; border: 1px solid var(--border); }
.ui-tw-revs {
  position: absolute; top: 60px; right: 0.6rem; z-index: 5;
  width: 320px; max-height: 60vh; overflow-y: auto;
  background: var(--bg-1); border: 1px solid var(--border); border-radius: 8px;
  padding: 0.5rem; box-shadow: 0 6px 20px rgba(0,0,0,0.5);
}
.ui-tw-revs-h {
  display: flex; align-items: center; justify-content: space-between;
  padding: 0.3rem 0.4rem 0.5rem; border-bottom: 1px solid var(--border);
  margin-bottom: 0.4rem;
  font-size: 0.78rem; color: var(--text-mute); text-transform: uppercase; letter-spacing: 0.04em;
}
.ui-tw-rev-row {
  display: flex; align-items: center; justify-content: space-between;
  padding: 0.4rem 0.5rem; border-radius: 6px;
}
.ui-tw-rev-row:hover { background: var(--bg-2); }
.ui-tw-rev-date { font-size: 0.85rem; color: var(--text); }
.ui-tw-rev-indicator {
  font-size: 0.75rem; color: var(--text-mute);
  font-family: ui-monospace, monospace;
  padding: 0 0.3rem;
  white-space: nowrap;
}
/* Line-level diff for assistant article proposals. Red/green tinted
 * backgrounds with monospace formatting so additions and removals
 * read like a github diff. */
.ui-tw-diff-h {
  font-size: 0.78rem; color: var(--text-mute);
  margin-bottom: 0.4rem;
}
.ui-tw-diff-h .add { color: #56d364; font-weight: 600; }
.ui-tw-diff-h .rem { color: var(--danger); font-weight: 600; }
.ui-tw-diff {
  background: var(--bg-0); border: 1px solid var(--border); border-radius: 6px;
  padding: 0.4rem 0.5rem;
  max-height: 320px; overflow: auto;
  font-family: ui-monospace, "SF Mono", Menlo, monospace;
  font-size: 0.78rem; line-height: 1.45;
}
.ui-tw-diff-row { padding: 0.05rem 0.3rem; white-space: pre-wrap; word-break: break-word; }
.ui-tw-diff-row.add { background: rgba(86, 211, 100, 0.12); color: #56d364; border-left: 2px solid #56d364; padding-left: 0.55rem; }
.ui-tw-diff-row.rem { background: rgba(248, 81, 73, 0.12); color: var(--danger);  border-left: 2px solid var(--danger); padding-left: 0.55rem; text-decoration: none; }
.ui-tw-diff-row.same { color: var(--text-mute); }
.ui-tw-diff-row .prefix { display: inline-block; width: 1.2rem; opacity: 0.7; }
.ui-tw-image-actions { display: flex; flex-direction: column; gap: 0.4rem; }
.ui-chat-act-status { font-size: 0.78rem; color: var(--text-mute); padding: 0.3rem 0.5rem; }
.ui-tw-rules-ta {
  width: 100%; box-sizing: border-box;
  min-height: 220px; resize: vertical;
  padding: 0.55rem 0.7rem;
  background: var(--bg-0); color: var(--text);
  border: 1px solid var(--border); border-radius: 6px;
  font-family: ui-monospace, "SF Mono", Menlo, monospace;
  font-size: 0.85rem; line-height: 1.5; outline: none;
}
.ui-tw-rules-ta:focus { border-color: var(--accent); }
.ui-tw-rules-hint { font-size: 0.78rem; color: var(--text-mute); margin-bottom: 0.4rem; }
.ui-tw-rules-actions {
  display: flex; align-items: center; justify-content: space-between;
  margin-top: 0.5rem; gap: 0.5rem;
}
.ui-tw-rules-saved { font-size: 0.78rem; color: var(--text-mute); }
/* Merge panel gets a touch wider than the rules panel so the dropdown
 * + paste area + guidance fit without horizontal squeeze. */
.ui-tw-merge-panel { width: 420px; }

@media (max-width: 700px) {
  .ui-tw {
    grid-template-columns: 1fr;
    height: calc(100vh - 120px);
    height: calc(100dvh - 120px);
  }
  .ui-tw-side {
    position: absolute; top: 0; left: 0; bottom: 0; right: 0; z-index: 30;
    width: 100%; max-width: none;
    transform: translateX(-105%); transition: transform 0.22s ease;
    border-radius: 0;
    box-shadow: none;
  }
  .ui-tw-side.open { transform: translateX(0); }
  .ui-tw-asst { max-height: 45%; }
}

/* --- Card escape hatch --- */
.ui-card { padding: 0.4rem 0.2rem; }

@media (min-width: 700px) {
  #ui-root { padding: 1rem; }
  .ui-panic-bar { margin: -1rem -1rem 0.8rem; padding: 1rem; }
}
`

// runtimeJS — single shared client runtime. Reads window.__UI_CONFIG
// (injected by the page) and hydrates each component. Pure vanilla JS,
// no dependencies. Components are dispatched on the "type" tag.
const runtimeJS = `
(function() {
  'use strict';

  // --- helpers ----------------------------------------------------------
  // renderBulkBar adds a select-mode toggle pill above a side list.
  // When the pill is active:
  //   - Clicking a list item toggles its selection instead of opening it.
  //   - Selected items get the .selected highlight.
  //   - A bottom action bar shows Delete / Select all / Done buttons.
  // When inactive (default), items behave normally and the bar
  // collapses to just the "Select" pill.
  //
  // The caller drives it via:
  //   - state.mode (bool) — owned by the component, initially false
  //   - selectedMap (object) — mutated by item clicks
  //   - reload() — re-renders the list after any state change
  //   - onDelete() — runs the bulk action
  function renderBulkBar(items, listEl, state, selectedMap, idOf, reload, onDelete) {
    // The "Select" toggle now lives in the sidebar header (next to
    // "+ New") so users see it before scrolling. This bar only
    // surfaces while select mode is active and only renders the
    // contextual controls — Select all + Delete with a live count.
    if (!state.mode) return;
    var idKeys = Object.keys(selectedMap);
    var count = idKeys.length;

    var allActive = items.length > 0 && idKeys.length === items.length;
    var allBtn = el('button', {class: 'ui-row-btn', onclick: function() {
      if (allActive) {
        Object.keys(selectedMap).forEach(function(k){ delete selectedMap[k]; });
      } else {
        items.forEach(function(s){ var k = idOf(s); if (k) selectedMap[k] = true; });
      }
      reload();
    }}, [allActive ? 'Deselect all' : 'Select all']);
    listEl.appendChild(el('div', {class: 'ui-bulk-bar'}, [allBtn]));

    if (count > 0) {
      var delBtn = el('button', {class: 'ui-row-btn danger', onclick: onDelete}, ['Delete (' + count + ')']);
      listEl.appendChild(el('div', {class: 'ui-bulk-bar bottom'}, [delBtn]));
    }
  }

  function el(tag, opts, children) {
    var n = document.createElement(tag);
    if (opts) {
      for (var k in opts) {
        if (k === 'class') n.className = opts[k];
        else if (k === 'text') n.textContent = opts[k];
        else if (k === 'html') n.innerHTML = opts[k];
        else if (k.indexOf('on') === 0) n.addEventListener(k.slice(2), opts[k]);
        else n.setAttribute(k, opts[k]);
      }
    }
    if (children) {
      for (var i = 0; i < children.length; i++) {
        var c = children[i];
        if (c == null) continue;
        n.appendChild(typeof c === 'string' ? document.createTextNode(c) : c);
      }
    }
    return n;
  }
  function fetchJSON(url, opts) {
    return fetch(url, opts).then(function(r) {
      if (!r.ok) return r.text().then(function(t){ throw new Error(t || ('HTTP ' + r.status)); });
      var ct = r.headers.get('Content-Type') || '';
      return ct.indexOf('application/json') >= 0 ? r.json() : r.text();
    });
  }
  function relTime(iso) {
    if (!iso) return '';
    var t = new Date(iso).getTime();
    if (!t) return '';
    var s = Math.round((Date.now() - t) / 1000);
    if (s < 60) return s + 's ago';
    if (s < 3600) return Math.round(s/60) + 'm ago';
    if (s < 86400) return Math.round(s/3600) + 'h ago';
    return Math.round(s/86400) + 'd ago';
  }
  // Shared minimal markdown renderer. Used by chat_panel for message
  // bubbles and pipeline_panel for transcript blocks. Top-level so
  // any component can call it without scope juggling.
  function mdToHTML(s) {
    s = String(s).replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;');
    var BT = String.fromCharCode(96);
    var fenceRe  = new RegExp(BT + BT + BT + '([\\s\\S]*?)' + BT + BT + BT, 'g');
    var inlineRe = new RegExp(BT + '([^' + BT + '\\n]+)' + BT, 'g');
    s = s.replace(fenceRe, function(_, body){ return '<pre><code>' + body.replace(/^\n/, '') + '</code></pre>'; });
    s = s.replace(inlineRe, '<code>$1</code>');
    s = s.replace(/\*\*([^*\n]+)\*\*/g, '<strong>$1</strong>');
    s = s.replace(/(^|[^*])\*([^*\n]+)\*/g, '$1<em>$2</em>');
    // Side-coded headings — h3 lines starting with FOR or AGAINST
    // get rendered with side classes so side-coded sections
    // surface as visually distinct color-coded blocks. Verdict h2
    // gets an amber accent class for legacy parity. Generic
    // headings fall through to plain h1/h2/h3.
    //
    // Each replacement appends an extra \n so when the source has
    // a heading immediately followed by text on the next line
    // (single newline, no blank — common LLM output), the
    // paragraph splitter below sees a double newline between the
    // heading and the loose text and wraps that text in a p tag.
    // Without this, raw text after a heading bypasses the p
    // wrapper and any rule keyed off h2-plus-p never matches.
    s = s.replace(/^### (FOR)\b([^\n]*)$/gm,
      '<h3 class="ui-pl-side-h ui-pl-side-for">$1$2</h3>\n');
    s = s.replace(/^### (AGAINST)\b([^\n]*)$/gm,
      '<h3 class="ui-pl-side-h ui-pl-side-against">$1$2</h3>\n');
    s = s.replace(/^### (.+)$/gm, '<h3>$1</h3>\n');
    s = s.replace(/^## (.+)$/gm, '<h2>$1</h2>\n');
    s = s.replace(/^# (.+)$/gm, '<h1>$1</h1>\n');
    // Markdown links — accept absolute http(s) URLs AND relative
    // paths starting with ?, #, or / so internal cross-links
    // (e.g. ?id=<id> for cross-links) render as anchors.
    // Absolute URLs open in a new tab; relative ones navigate the
    // same window so the page deep-link logic can pick them up.
    s = s.replace(/\[([^\]]+)\]\((https?:[^)]+)\)/g,
      '<a href="$2" target="_blank" rel="noopener">$1</a>');
    s = s.replace(/\[([^\]]+)\]\(([?#/][^)]+)\)/g, '<a href="$2">$1</a>');
    // Auto-link bare http/https URLs that didn't go through the
    // [text](url) replacement above. The (^|[^"'>=]) prefix avoids
    // matching URLs already inside href="..." attributes or as the
    // text content of a freshly-built <a> tag (>url</a>). Trailing
    // punctuation like ".," common at end of sentences gets pulled
    // out of the match.
    s = s.replace(/(^|[^"'>=])(https?:\/\/[^\s<)]+?)([.,;:!?]?(?=\s|$|<|\)))/g,
      '$1<a href="$2" target="_blank" rel="noopener">$2</a>$3');
    s = s.replace(/(^|\n)((?:[-*] .+(?:\n|$))+)/g, function(_, p, block) {
      var items = block.trim().split(/\n/).map(function(line){
        return '<li>' + line.replace(/^[-*]\s+/, '') + '</li>';
      }).join('');
      return p + '<ul>' + items + '</ul>';
    });
    s = s.split(/\n\n+/).map(function(block) {
      var t = block.trim();
      if (!t) return '';
      if (/^<(h[1-3]|pre|ul|ol|p|blockquote)/i.test(t)) return t;
      // Side-coded paragraphs — content starting with "For:" or
      // "Against:" (bolded by an earlier pass) gets a side class
      // so the whole paragraph renders green / red. Matches
      // legacy display where each round emitted colored
      // For:/Against: lines.
      var sideMatch = t.match(/^<strong>(For|Against):<\/strong>/i);
      if (sideMatch) {
        var cls = sideMatch[1].toLowerCase() === 'for'
          ? 'ui-pl-side-for-p' : 'ui-pl-side-against-p';
        return '<p class="' + cls + '">' + t.replace(/\n/g, '<br>') + '</p>';
      }
      return '<p>' + t.replace(/\n/g, '<br>') + '</p>';
    }).join('');
    // App-registered post-processors get a final crack at the
    // rendered HTML. Used for domain-specific markdown extensions
    // (a heading pattern unique to one app, etc.) that shouldn't
    // leak into the shared runtime.
    var exts = window.UIMarkdownExtensions || [];
    for (var ei = 0; ei < exts.length; ei++) {
      try { s = exts[ei](s) || s; } catch (_) {}
    }
    return s;
  }
  // Expose the DOM-constructor + markdown-renderer helpers so app
  // JS payloads (loaded via Page.ExtraHeadHTML) can build block
  // renderers without duplicating these. Per-renderer context (cfg
  // flags, etc.) is threaded through the renderer's second arg, so
  // these globals stay panel-agnostic.
  window.uiEl = el;
  window.uiMdToHTML = mdToHTML;
  // Markdown extension registry — apps add post-processors that
  // run after base mdToHTML passes complete.
  if (!window.UIMarkdownExtensions) window.UIMarkdownExtensions = [];
  window.uiRegisterMarkdownExtension = function(fn) {
    if (typeof fn === 'function') window.UIMarkdownExtensions.push(fn);
  };
  // Block renderer registry — apps register types via JS shipped
  // through Page.ExtraHeadHTML. Hoisted to module scope (not buried
  // inside pipeline_panel's render function) so it exists at
  // DOMContentLoaded time, BEFORE the panel mounts. Without this,
  // an app's deferred DOMContentLoaded handler would find
  // uiRegisterBlockRenderer undefined and fail to register
  // anything.
  if (!window.UIBlockRenderers) window.UIBlockRenderers = {};
  window.uiRegisterBlockRenderer = function(name, fn) {
    if (typeof fn === 'function') window.UIBlockRenderers[name] = fn;
  };
  // Client-action registry — toolbar buttons with Method="client"
  // call into one of these handlers by name. Lets apps wire
  // browser-side actions (window.print, copy-to-clipboard with
  // custom shape, etc.) without needing a server round-trip.
  if (!window.UIClientActions) window.UIClientActions = {};
  window.uiRegisterClientAction = function(name, fn) {
    if (typeof fn === 'function') window.UIClientActions[name] = fn;
  };
  // Data-source invalidation. Apps and components fire this when a
  // write completes so any list/table fetched from the same source
  // can refetch. Pattern:
  //   window.uiInvalidate('api/queue')          // exact URL match
  //   window.uiInvalidate(['api/queue', ...])    // multiple sources
  // Listeners (Tables, etc.) compare their cfg.source to detail and
  // call their own reload() on match. Avoids polling and avoids
  // wiring every action handler to a specific list refresh.
  window.uiInvalidate = function(source) {
    var sources = Array.isArray(source) ? source : [source];
    try {
      window.dispatchEvent(new CustomEvent('ui-data-changed',
        {detail: {sources: sources}}));
    } catch (_) {}
  };
  // Message decorator registry — fires after a message is finalized
  // (markdown pass complete). Apps register a function that gets
  // {role, id, wrap, body, rawText} and can append affordances to
  // the wrap (e.g. "Save to TechWriter" / "Save to Workspace"
  // buttons under each assistant reply). One registry is shared
  // across panels so a single registration covers chat / agent
  // loop / pipeline reply rendering uniformly.
  if (!window.UIMessageDecorators) window.UIMessageDecorators = [];
  window.uiRegisterMessageDecorator = function(fn) {
    if (typeof fn === 'function') window.UIMessageDecorators.push(fn);
  };
  function fmt(value, format) {
    if (value == null) return '';
    if (format === 'reltime') return relTime(value);
    if (format === 'fromnow') return fromNow(value);
    if (format === 'bytes')   return fmtBytes(value);
    if (format === 'duration') return fmtDuration(value);
    if (format === 'thousands') return fmtThousands(value);
    return String(value);
  }
  // fromNow renders an ISO timestamp as a signed relative time —
  // future ("in 5m") or past ("5m ago"). Reltime only handles past.
  // Use for fields like "run_at" on scheduled tasks where the value
  // is almost always in the future.
  function fromNow(iso) {
    if (!iso) return '';
    var t = new Date(iso).getTime();
    if (!t) return '';
    var diff = t - Date.now(); // positive = future
    var future = diff > 0;
    var s = Math.abs(Math.round(diff / 1000));
    var label;
    if (s < 5)        label = 'now';
    else if (s < 60)  label = s + 's';
    else if (s < 3600) label = Math.round(s/60) + 'm';
    else if (s < 86400) label = Math.round(s/3600) + 'h';
    else label = Math.round(s/86400) + 'd';
    if (label === 'now') return 'now';
    return future ? ('in ' + label) : (label + ' ago');
  }
  function fmtThousands(n) {
    var v = Number(n);
    if (isNaN(v)) return String(n);
    return v.toLocaleString();
  }
  function fmtBytes(b) {
    var n = Number(b);
    if (isNaN(n)) return String(b);
    if (n < 1024) return n + ' B';
    if (n < 1024*1024) return (n/1024).toFixed(1) + ' KB';
    if (n < 1024*1024*1024) return (n/(1024*1024)).toFixed(1) + ' MB';
    return (n/(1024*1024*1024)).toFixed(2) + ' GB';
  }
  function fmtDuration(s) {
    var n = Number(s);
    if (isNaN(n)) return String(s);
    if (n < 60) return n.toFixed(0) + 's';
    if (n < 3600) return (n/60).toFixed(0) + 'm';
    if (n < 86400) return (n/3600).toFixed(1) + 'h';
    return (n/86400).toFixed(1) + 'd';
  }
  function substitute(template, record) {
    // Allow lowercase, uppercase, digits, underscore, and dots so dotted
    // paths like "tool.name" work for endpoints with nested records.
    return template.replace(/\{([A-Za-z0-9_.]+)\}/g, function(_, key) {
      var v = lookup(record, key);
      return encodeURIComponent(v != null ? v : '');
    });
  }
  // lookup walks a dotted path through an object: "tool.name" against
  // {tool:{name:"x"}} returns "x". Also handles plain field lookups
  // (no dot) — just returns obj[path]. Returns undefined for any
  // intermediate null/undefined, so callers can treat the result as
  // a single optional value without nested null checks.
  function lookup(obj, path) {
    if (obj == null) return undefined;
    if (path.indexOf('.') < 0) return obj[path];
    var parts = path.split('.');
    var cur = obj;
    for (var i = 0; i < parts.length; i++) {
      if (cur == null) return undefined;
      cur = cur[parts[i]];
    }
    return cur;
  }
  function showToast(msg) {
    var t = el('div', {class: 'ui-toast'}, [msg]);
    t.style.cssText = 'position:fixed;bottom:1.5rem;left:50%;transform:translateX(-50%);background:var(--bg-2);border:1px solid var(--border);color:var(--text);padding:0.6rem 1rem;border-radius:8px;z-index:50;box-shadow:0 4px 12px rgba(0,0,0,0.4);font-size:0.85rem;';
    document.body.appendChild(t);
    setTimeout(function(){ t.remove(); }, 2500);
  }

  // parseRules splits a free-form rules string into an array of
  // individual rule strings. Splits on newlines, strips common bullet
  // and number prefixes ("1. ", "2)", "- ", "* ") so existing rules
  // round-trip through the rules-list editor without doubling-up.
  function parseRules(s) {
    if (!s) return [];
    return s.split(/\r?\n/).map(function(line) {
      var t = line.trim();
      t = t.replace(/^\d+[.)]\s*/, '');
      t = t.replace(/^[-*]\s+/, '');
      return t;
    }).filter(function(t){ return t !== ''; });
  }

  // renderSideHeader builds the standard sidebar header used by chat,
  // pipeline, and article-editor panels. Layout is:
  //   [Label (flex:1), ...extras, + New, × close (mobile only)]
  // so + New always sits at the right edge on desktop and × close
  // takes that spot on mobile when the drawer is open. Returns the
  // built header element plus references to the close + new buttons
  // so callers can wire additional behavior (e.g. tw's collapse, or
  // Select toggle that lives between extras and + New).
  //
  // opts:
  //   label       — the header label text ("Sessions", "Articles")
  //   className   — header class ('ui-chat-side-h' or 'ui-tw-side-h')
  //   newLabel    — text for the + New button (default: '+ New')
  //   newTitle    — tooltip for the + New button
  //   onNew       — click handler for + New
  //   onClose     — click handler for × close (mobile)
  //   leftExtras  — components inserted between label and Select/New
  //   rightExtras — components inserted between Select/New and close
  function renderSideHeader(opts) {
    var sideClose = el('button', {
      class: 'ui-chat-side-close', title: 'Close',
      onclick: opts.onClose,
    }, ['×']);
    var sideNew = el('button', {
      class: 'ui-chat-new', title: opts.newTitle || '',
      onclick: opts.onNew,
    }, [opts.newLabel || '+ New']);
    var children = [el('span', {text: opts.label})];
    (opts.leftExtras || []).forEach(function(c) { children.push(c); });
    children.push(sideNew);
    (opts.rightExtras || []).forEach(function(c) { children.push(c); });
    children.push(sideClose);
    return {
      elt: el('div', {class: opts.className || 'ui-chat-side-h'}, children),
      closeBtn: sideClose,
      newBtn: sideNew,
    };
  }

  // makeDrawer wires the mobile sidebar drawer used by chat, pipeline,
  // and article-editor panels. Builds the mobile-only header (hamburger
  // + title + optional + button) and the backdrop, plus open/close
  // functions that toggle the side drawer's "open" class. Each panel
  // appends mobileHdr to its main column and backdrop to its wrap.
  //
  // opts:
  //   title           — initial mobile title text
  //   hamburgerTitle  — tooltip on the ☰ button
  //   newTitle, onNew — optional "+ N" button on the mobile header
  //   newLabel        — label for the new button (default '+')
  function makeDrawer(side, opts) {
    var backdrop    = el('div', {class: 'ui-chat-backdrop'});
    var mobileHdr   = el('div', {class: 'ui-chat-mobile-hdr'});
    var mobileTitle = el('div', {class: 'ui-chat-mobile-title'}, [opts.title || '']);
    function openDrawer()  { side.classList.add('open');    backdrop.classList.add('show'); }
    function closeDrawer() { side.classList.remove('open'); backdrop.classList.remove('show'); }
    backdrop.onclick = closeDrawer;
    mobileHdr.appendChild(el('button', {
      class: 'ui-chat-hamburger', title: opts.hamburgerTitle || 'Menu',
      onclick: openDrawer,
    }, ['☰']));
    mobileHdr.appendChild(mobileTitle);
    if (opts.onNew) {
      mobileHdr.appendChild(el('button', {
        class: 'ui-chat-hamburger', title: opts.newTitle || 'New',
        onclick: function() { opts.onNew(); closeDrawer(); },
      }, [opts.newLabel || '+']));
    }
    return {
      mobileHdr:   mobileHdr,
      backdrop:    backdrop,
      mobileTitle: mobileTitle,
      openDrawer:  openDrawer,
      closeDrawer: closeDrawer,
    };
  }

  // makeSideSearch builds the small search input rendered below the
  // sidebar header. Returns the input element. Filters elements
  // matching itemSelector (default '.ui-chat-side-item') under the
  // given list element by matching their textContent.
  function makeSideSearch(sideList, itemSelector) {
    var input = el('input', {
      type: 'search', class: 'ui-chat-side-search',
      placeholder: 'Search…',
      autocomplete: 'off', autocorrect: 'off', spellcheck: 'false',
    });
    input.addEventListener('input', function() {
      var q = (input.value || '').trim().toLowerCase();
      var sel = itemSelector || '.ui-chat-side-item';
      sideList.querySelectorAll(sel).forEach(function(it) {
        if (!q) { it.style.display = ''; return; }
        var hay = (it.textContent || '').toLowerCase();
        it.style.display = hay.indexOf(q) >= 0 ? '' : 'none';
      });
    });
    return input;
  }

  // --- component renderers ---------------------------------------------
  var components = {};

  components.panic_bar = function(cfg) {
    var status = el('span', {class: 'ui-panic-status'});
    var btn = el('button', {
      class: 'ui-panic-btn',
      onclick: function() {
        if (cfg.confirm && !confirm(cfg.confirm)) return;
        status.textContent = 'Engaging…';
        fetchJSON(cfg.on_click, {method: 'POST'}).then(function(d) {
          status.textContent = (d && d.message) ? d.message : 'Done.';
        }).catch(function(err) {
          status.textContent = 'Failed: ' + err.message;
        });
      }
    }, [cfg.label]);
    return el('div', {class: 'ui-panic-bar'}, [btn, status]);
  };

  components.toggle_group = function(cfg) {
    var wrap = el('div', {class: 'ui-toggle-group'});
    var current = {};
    function render() {
      wrap.innerHTML = '';
      cfg.toggles.forEach(function(t) {
        var input = el('input', {type: 'checkbox', class: 'ui-switch'});
        input.checked = !!current[t.field];
        input.addEventListener('change', function() {
          current[t.field] = input.checked;
          fetchJSON(cfg.source, {
            method: 'POST', headers: {'Content-Type': 'application/json'},
            body: JSON.stringify(current)
          }).catch(function(err){ showToast('Save failed: ' + err.message); input.checked = !input.checked; });
        });
        var labelText = el('span', {class: 'ui-toggle-label'}, [t.label,
          t.help ? el('span', {class: 'ui-toggle-help'}, [t.help]) : null]);
        var row = el('label', {class: 'ui-toggle-row'}, [labelText, input]);
        wrap.appendChild(row);
      });
    }
    fetchJSON(cfg.source).then(function(d) { current = d || {}; render(); })
      .catch(function(err){ wrap.textContent = 'Failed to load: ' + err.message; });
    return wrap;
  };

  components.table = function(cfg) {
    var listEl = el('div', {class: 'ui-table-list'}, ['Loading…']);
    var refreshIndicator = null;
    var records = [];
    var openExpansions = {}; // rowKey -> {actionIndex: panel}

    // Refetch when an external action invalidates our source. See
    // window.uiInvalidate above for the helper. Compare exact URL —
    // sources with placeholders ({id} etc.) won't ever match because
    // they get resolved separately per row, so this is safe.
    window.addEventListener('ui-data-changed', function(ev) {
      var sources = ev.detail && ev.detail.sources;
      if (!sources) return;
      if (sources.indexOf(cfg.source) >= 0) reload(true);
    });

    function reload(quiet) {
      if (!quiet && refreshIndicator) refreshIndicator.textContent = '↻';
      // Skip while any expansion is open (don't slam them shut).
      if (Object.keys(openExpansions).some(function(k){ return Object.keys(openExpansions[k]).length > 0; })) return;
      fetchJSON(cfg.source).then(function(d) {
        if (cfg.records_field) {
          // Strict: when records_field is set, use it exactly. A
          // missing/null field means "empty list" — don't fall back
          // to a different key, which would silently render the
          // wrong section (an empty pending list falling through to
          // active because alphabetical key ordering picks it first).
          var v = d ? d[cfg.records_field] : null;
          records = Array.isArray(v) ? v : [];
        } else {
          records = (d && d.conversations) || (Array.isArray(d) ? d : (d && d[Object.keys(d)[0]]) || []);
        }
        if (cfg.sort_by) {
          records.sort(function(a, b) {
            var av = a[cfg.sort_by] || '', bv = b[cfg.sort_by] || '';
            return cfg.sort_desc ? String(bv).localeCompare(String(av)) : String(av).localeCompare(String(bv));
          });
        }
        renderRows();
      }).catch(function(err){ listEl.textContent = 'Failed: ' + err.message; })
        .then(function(){ if (refreshIndicator) setTimeout(function(){ refreshIndicator.textContent = ''; }, 600); });
    }

    function renderRows() {
      listEl.innerHTML = '';
      if (!records.length) {
        listEl.appendChild(el('div', {class: 'ui-table-empty'}, [cfg.empty_text || 'Nothing here yet.']));
        return;
      }
      records.forEach(function(rec) {
        var rowKey = rec[cfg.row_key];
        var row = el('div', {class: 'ui-table-row'});

        // Leading actions go directly on the row (they stay pinned to
        // the left even when cells stack on narrow viewports).
        (cfg.row_actions || []).forEach(function(act, ai) {
          if (!act.leading) return;
          appendAction(row, act, ai, rec, rowKey, row);
        });

        var cellsWrap = el('div', {class: 'ui-row-cells'});
        cfg.columns.forEach(function(col) {
          var v = lookup(rec, col.field);
          if (col.type === 'badge') {
            cellsWrap.appendChild(renderBadgeCell(col, v));
            return;
          }
          var cell = el('div', {class: 'ui-table-cell' + (col.mute ? ' mute' : '')});
          if (col.flex) cell.style.flex = col.flex;
          cell.textContent = fmt(v, col.format);
          cellsWrap.appendChild(cell);
        });
        row.appendChild(cellsWrap);

        var actionsWrap = el('div', {class: 'ui-row-actions'});
        (cfg.row_actions || []).forEach(function(act, ai) {
          if (act.leading) return;
          // parent = where to append the control (actionsWrap so all
          // buttons live in one flex group). rowEl = the actual table
          // row, so expand handlers can insert their panel as a true
          // sibling of the row in the list rather than as a child of
          // actionsWrap (which would render the panel beside or
          // beneath the buttons inside the row).
          appendAction(actionsWrap, act, ai, rec, rowKey, row);
        });
        if (actionsWrap.childNodes.length > 0) row.appendChild(actionsWrap);

        listEl.appendChild(row);
      });
    }

    function appendAction(parent, act, ai, rec, rowKey, rowEl) {
      // Conditional rendering: skip when only_if field is falsy or
      // when hide_if field is truthy. Either gate alone is enough.
      if (act.only_if && !lookup(rec, act.only_if)) return;
      if (act.hide_if && lookup(rec, act.hide_if)) return;
      if (act.type === 'toggle') {
        var input = el('input', {type: 'checkbox', class: 'ui-switch'});
        input.checked = !!rec[act.field];
        input.addEventListener('change', function() {
          var url = substitute(act.post_to, rec);
          var body = {}; body[act.field] = input.checked;
          fetchJSON(url, {
            method: act.method || 'POST', headers: {'Content-Type': 'application/json'},
            body: JSON.stringify(body)
          }).catch(function(err){ showToast('Save failed: ' + err.message); input.checked = !input.checked; });
          rec[act.field] = input.checked;
        });
        // Optional label rendered to the LEFT of the switch so the
        // operator knows what the toggle controls. Used in tables
        // without column headers (e.g. Users → "Admin").
        if (act.label) {
          var pair = el('span', {class: 'ui-row-toggle-pair'}, [
            el('span', {class: 'ui-row-toggle-label'}, [act.label]),
            input,
          ]);
          parent.appendChild(pair);
        } else {
          parent.appendChild(input);
        }
      } else if (act.type === 'select') {
        var sel = el('select', {class: 'ui-row-select'});
        if (act.width) sel.style.width = act.width;
        var disabled = act.disable_if && rec[act.disable_if];
        var filtered = act.filter_options_if && rec[act.filter_options_if]
          ? (act.filter_options || '').split(',').map(function(s){ return s.trim(); }).filter(Boolean)
          : [];
        // act.default_field (when set) names a per-row field that
        // carries the out-of-the-box default for this row's select.
        // Append "*" to the matching option's label so a user who
        // has overridden the value can still see what the default
        // was without consulting the docs.
        var defaultValue = act.default_field ? String(rec[act.default_field] || '') : '';
        (act.options || []).forEach(function(o) {
          if (filtered.indexOf(o.value) >= 0) return;
          var label = o.label || o.value;
          if (defaultValue && String(o.value) === defaultValue) label = label + ' *';
          var opt = el('option', {value: o.value}, [label]);
          if (String(rec[act.field]) === String(o.value)) opt.selected = true;
          sel.appendChild(opt);
        });
        if (disabled) sel.disabled = true;
        sel.addEventListener('change', function() {
          var url = substitute(act.post_to, rec);
          var body = {}; body[act.field] = sel.value;
          // Routing endpoint also wants the row_key (e.g. {key: "...", value: "...", think_budget: N})
          // — when row_key is set on the table, include it so a single
          // POST endpoint can identify which stage to update. Other
          // fields the endpoint cares about ride along when present.
          if (cfg.row_key) body[cfg.row_key] = rec[cfg.row_key];
          fetchJSON(url, {
            method: act.method || 'POST', headers: {'Content-Type': 'application/json'},
            body: JSON.stringify(body)
          }).catch(function(err){ showToast('Save failed: ' + err.message); });
          rec[act.field] = sel.value;
        });
        parent.appendChild(sel);
      } else if (act.type === 'number') {
        var ninput = el('input', {type: 'number', class: 'ui-row-number'});
        if (act.width) ninput.style.width = act.width;
        if (act.min) ninput.min = String(act.min);
        if (act.max) ninput.max = String(act.max);
        var v = rec[act.field];
        ninput.value = (v == null || v === 0) ? '' : String(v);
        if (act.label) ninput.placeholder = act.label;
        ninput.addEventListener('change', function() {
          var n = parseInt(ninput.value, 10);
          if (isNaN(n)) n = 0;
          var url = substitute(act.post_to, rec);
          var body = {}; body[act.field] = n;
          if (cfg.row_key) body[cfg.row_key] = rec[cfg.row_key];
          fetchJSON(url, {
            method: act.method || 'POST', headers: {'Content-Type': 'application/json'},
            body: JSON.stringify(body)
          }).catch(function(err){ showToast('Save failed: ' + err.message); });
          rec[act.field] = n;
        });
        parent.appendChild(ninput);
      } else if (act.type === 'button') {
        var classes = 'ui-row-btn';
        if (act.compact) classes += ' compact';
        if (act.variant) classes += ' ' + act.variant;
        var btn = el('button', {class: classes, onclick: function() {
          if (act.confirm && !confirm(act.confirm)) return;
          var url = substitute(act.post_to, rec);
          fetchJSON(url, {method: act.method || 'POST'})
            .then(function(resp) {
              // RedirectURL — substitute response-JSON fields into
              // the destination URL and navigate. {id}, {session},
              // etc. resolve from the response body so a "Run"
              // button can hop straight to a watch page using the
              // freshly-allocated session ID. Falls back to a row
              // reload when no redirect is configured.
              if (act.redirect_url) {
                var dest = substitute(act.redirect_url, resp || {});
                var target = act.redirect_target || '_blank';
                if (target === '_self') window.location.href = dest;
                else window.open(dest, target);
                reload(true);
                return;
              }
              reload(true);
            })
            .catch(function(err){ showToast('Failed: ' + err.message); });
        }}, [act.label || '…']);
        parent.appendChild(btn);
      } else if (act.type === 'expand') {
        var btn2 = el('button', {class: 'ui-row-btn' + (act.compact ? ' compact' : ''), onclick: function() {
          // Pass the actual table row (rowEl), not parent — the expand
          // panel must insert as a sibling of the row in the list,
          // not as a child of the actions container.
          toggleExpand(rec, rowKey, ai, act, rowEl);
        }}, [act.label || 'More']);
        parent.appendChild(btn2);
      }
    }

    function toggleExpand(rec, rowKey, ai, act, row) {
      openExpansions[rowKey] = openExpansions[rowKey] || {};
      if (openExpansions[rowKey][ai]) {
        openExpansions[rowKey][ai].remove();
        delete openExpansions[rowKey][ai];
        return;
      }
      // Close any other open expansions on the same row OR other rows
      // (one-at-a-time keeps the page tidy).
      Object.keys(openExpansions).forEach(function(rk) {
        Object.keys(openExpansions[rk]).forEach(function(idx) {
          openExpansions[rk][idx].remove();
        });
        openExpansions[rk] = {};
      });
      var panel = el('div', {class: 'ui-expand'});
      // Substitute {row_key}-style placeholders into the nested
      // component's URLs before mounting. Then pass rec as ctx so
      // components like JSONView / RecordView can render row data
      // without re-fetching what the table already loaded.
      var renderCfg = JSON.parse(JSON.stringify(act.render));
      substituteRefs(renderCfg, rec);
      mountComponent(renderCfg, panel, rec);
      row.parentNode.insertBefore(panel, row.nextSibling);
      openExpansions[rowKey][ai] = panel;
    }

    function substituteRefs(obj, rec) {
      if (!obj || typeof obj !== 'object') return;
      for (var k in obj) {
        if (typeof obj[k] === 'string' && obj[k].indexOf('{') >= 0) {
          obj[k] = substitute(obj[k], rec);
        } else if (typeof obj[k] === 'object') {
          substituteRefs(obj[k], rec);
        }
      }
    }

    if (cfg.auto_refresh_ms && cfg.auto_refresh_ms > 0) {
      setInterval(function(){ reload(true); }, cfg.auto_refresh_ms);
    }
    if (cfg.pull_to_refresh) setupPTR(function(){ reload(false); });
    reload(true);

    // Surface refresh indicator into the parent section header (set
    // later by the section renderer, which finds .ui-section-h-r).
    setTimeout(function(){
      var section = listEl.closest('.ui-section');
      if (section) {
        var h = section.querySelector('.ui-section-h-r');
        if (h) refreshIndicator = h;
      }
    }, 0);
    return listEl;
  };

  components.history_panel = function(cfg) {
    var panel = el('div', {class: 'ui-history'}, ['Loading…']);
    var roleField = cfg.role_field || 'role';
    var textField = cfg.text_field || 'text';
    var whoField = cfg.who_field || 'display_name';
    var timeField = cfg.time_field || 'timestamp';
    var aiTag = cfg.assistant_tag || 'assistant';
    fetchJSON(cfg.source).then(function(msgs) {
      panel.innerHTML = '';
      panel.appendChild(el('div', {class: 'ui-history-h'}, [cfg.header || 'Recent messages']));
      if (!msgs || !msgs.length) {
        panel.appendChild(el('div', {class: 'ui-history-empty'}, [cfg.empty_text || 'No messages yet.']));
        return;
      }
      var slice = cfg.max_messages > 0 ? msgs.slice(-cfg.max_messages) : msgs;
      slice.forEach(function(m) {
        var isAI = m[roleField] === aiTag;
        var label = isAI ? 'AI' : (m[whoField] || m.handle || 'them');
        var ts = m[timeField] ? ' · ' + relTime(m[timeField]) : '';
        panel.appendChild(el('div', {class: 'ui-history-msg' + (isAI ? ' ai' : '')}, [
          el('div', {class: 'ui-history-who'}, [label + ts]),
          el('div', {class: 'ui-history-body'}, [m[textField] || '(no text)']),
        ]));
      });
    }).catch(function(err){ panel.textContent = 'Failed: ' + err.message; });
    return panel;
  };

  components.member_editor = function(cfg) {
    var field    = cfg.field         || 'members';
    var handleF  = cfg.handle_field  || 'handle';
    var nameF    = cfg.name_field    || 'name';
    var aliasF   = cfg.aliases_field || 'aliases';
    var ahField  = cfg.alias_handles_field || '';
    var method   = cfg.method        || 'POST';

    var wrap = el('div', {class: 'ui-mem'});
    var rowsHost = el('div', {class: 'ui-mem'});
    var addBtn = el('button', {class: 'ui-mem-add'}, ['+ Add member']);
    var aliasHandlesRow = null;
    var aliasHandlesInput = null;
    if (ahField) {
      aliasHandlesRow = el('div', {class: 'ui-mem-aliasrow'});
      aliasHandlesRow.appendChild(el('label', {}, ['Conversation alias handles (comma-separated phone/emails that map to this same chat):']));
      aliasHandlesInput = el('input', {type: 'text', placeholder: '+15551234567, alice@example.com'});
      aliasHandlesRow.appendChild(aliasHandlesInput);
    }

    wrap.appendChild(rowsHost);
    wrap.appendChild(addBtn);
    if (aliasHandlesRow) wrap.appendChild(aliasHandlesRow);

    // Local working copy of the members array. Saved (PATCHed) back to
    // the server on every blur — debouncing felt fragile here because
    // a row removal also has to round-trip immediately.
    var members = [];

    function save() {
      var body = {};
      body[field] = members;
      if (aliasHandlesInput) {
        body[ahField] = (aliasHandlesInput.value || '')
          .split(',')
          .map(function(s){ return s.trim(); })
          .filter(function(s){ return s; });
      }
      fetch(cfg.post_to, {
        method: method,
        headers: {'Content-Type': 'application/json'},
        body: JSON.stringify(body),
      }).catch(function(err){ showToast('Save failed: ' + err.message); });
    }
    function render() {
      rowsHost.innerHTML = '';
      if (!members.length) {
        rowsHost.appendChild(el('div', {class: 'ui-mem-empty'}, [cfg.empty_text || 'No members yet. Add one below.']));
        return;
      }
      members.forEach(function(m, idx) {
        var row = el('div', {class: 'ui-mem-row'});
        var hInput = el('input', {type: 'text', placeholder: '+15551234567 or alice@example.com', value: m[handleF] || ''});
        var nInput = el('input', {type: 'text', placeholder: 'Display name', value: m[nameF] || ''});
        var aInput = el('input', {class: 'ui-mem-aliases', type: 'text',
          placeholder: 'aliases (comma-sep)',
          value: (m[aliasF] || []).join(', ')});
        var del = el('button', {class: 'ui-mem-del', title: 'Remove'}, ['×']);
        hInput.addEventListener('blur', function() {
          members[idx][handleF] = hInput.value.trim();
          save();
        });
        nInput.addEventListener('blur', function() {
          members[idx][nameF] = nInput.value.trim();
          save();
        });
        aInput.addEventListener('blur', function() {
          members[idx][aliasF] = aInput.value
            .split(',').map(function(s){ return s.trim(); })
            .filter(function(s){ return s; });
          save();
        });
        del.addEventListener('click', function() {
          members.splice(idx, 1);
          render();
          save();
        });
        row.appendChild(hInput);
        row.appendChild(nInput);
        row.appendChild(aInput);
        row.appendChild(del);
        rowsHost.appendChild(row);
      });
    }
    function load() {
      fetchJSON(cfg.source).then(function(rec) {
        members = (rec && rec[field]) ? rec[field].slice() : [];
        // Normalize: ensure each member has a handle/name/aliases shape.
        members = members.map(function(m) {
          var x = {};
          x[handleF] = m[handleF] || '';
          x[nameF]   = m[nameF]   || '';
          x[aliasF]  = Array.isArray(m[aliasF]) ? m[aliasF].slice() : [];
          return x;
        });
        if (aliasHandlesInput) {
          var ah = (rec && rec[ahField]) || [];
          aliasHandlesInput.value = Array.isArray(ah) ? ah.join(', ') : '';
          aliasHandlesInput.addEventListener('blur', save);
        }
        render();
      }).catch(function(err) {
        rowsHost.innerHTML = '';
        rowsHost.appendChild(el('div', {class: 'ui-mem-empty'}, ['Failed to load: ' + err.message]));
      });
    }
    addBtn.addEventListener('click', function() {
      var x = {};
      x[handleF] = ''; x[nameF] = ''; x[aliasF] = [];
      members.push(x);
      render();
    });

    load();
    return wrap;
  };

  components.key_manager = function(cfg) {
    var nameF    = cfg.name_field      || 'name';
    var idF      = cfg.id_field        || 'id';
    var secretF  = cfg.secret_field    || 'key';
    var createdF = cfg.created_field   || 'created';
    var lastSeenF = cfg.last_seen_field || 'last_seen';
    var newLbl   = cfg.new_label       || '+ New API key';

    var wrap     = el('div', {class: 'ui-keys'});
    var actions  = el('div', {class: 'ui-keys-actions'});
    var newBtn   = el('button', {class: 'ui-keys-new'}, [newLbl]);
    actions.appendChild(newBtn);

    var formWrap     = el('div', {class: 'ui-keys-form', style: 'display:none'});
    var formInput    = el('input', {type: 'text', placeholder: 'Friendly name, e.g. MacBook bridge'});
    var formCreate   = el('button', {}, ['Create']);
    var formCancel   = el('button', {class: 'secondary'}, ['Cancel']);
    formWrap.appendChild(formInput);
    formWrap.appendChild(formCreate);
    formWrap.appendChild(formCancel);

    var revealed = el('div', {class: 'ui-keys-revealed', style: 'display:none'});

    var listEl = el('div', {class: 'ui-keys-list'}, ['Loading…']);

    wrap.appendChild(actions);
    wrap.appendChild(formWrap);
    wrap.appendChild(revealed);
    wrap.appendChild(listEl);

    function showForm() {
      newBtn.style.display = 'none';
      revealed.style.display = 'none';
      formWrap.style.display = '';
      formInput.value = '';
      formInput.focus();
    }
    function hideForm() {
      formWrap.style.display = 'none';
      newBtn.style.display = '';
    }
    function showRevealed(rec) {
      revealed.innerHTML = '';
      revealed.appendChild(el('div', {class: 'ui-keys-revealed-h'}, ['Key created — copy now, it will not be shown again']));
      if (cfg.secret_hint) revealed.appendChild(el('div', {class: 'ui-keys-revealed-hint'}, [cfg.secret_hint]));
      var secret = rec[secretF] || '';
      revealed.appendChild(el('div', {class: 'ui-keys-revealed-secret'}, [secret]));
      var copyBtn = el('button', {}, ['Copy']);
      copyBtn.addEventListener('click', function() {
        navigator.clipboard.writeText(secret).then(function() {
          var orig = copyBtn.textContent;
          copyBtn.textContent = 'Copied!';
          copyBtn.classList.add('copied');
          setTimeout(function() {
            copyBtn.textContent = orig;
            copyBtn.classList.remove('copied');
          }, 1500);
        });
      });
      var dismissBtn = el('button', {}, ['Dismiss']);
      dismissBtn.addEventListener('click', function() {
        revealed.style.display = 'none';
        revealed.innerHTML = '';
        loadList();
      });
      revealed.appendChild(el('div', {class: 'ui-keys-revealed-row'}, [copyBtn, dismissBtn]));
      revealed.style.display = '';
    }
    function loadList() {
      listEl.innerHTML = 'Loading…';
      fetchJSON(cfg.list_url).then(function(items) {
        listEl.innerHTML = '';
        items = items || [];
        if (!items.length) {
          listEl.appendChild(el('div', {class: 'ui-keys-empty'}, [cfg.empty_text || 'No keys yet.']));
          return;
        }
        items.forEach(function(rec) {
          var row     = el('div', {class: 'ui-keys-row'});
          var name    = el('div', {class: 'ui-keys-row-name'}, [rec[nameF] || '(unnamed)']);
          var metaBits = [];
          if (rec[createdF])  metaBits.push('created ' + relTime(rec[createdF]));
          if (rec[lastSeenF]) metaBits.push('last seen ' + relTime(rec[lastSeenF]));
          var meta    = el('div', {class: 'ui-keys-row-meta'}, [metaBits.join(' · ') || '—']);
          var del     = el('button', {class: 'ui-keys-row-del', title: 'Delete this key'}, ['×']);
          del.addEventListener('click', function() {
            if (!confirm('Delete this API key? Any client using it will stop working.')) return;
            del.disabled = true;
            var url = cfg.delete_url.replace(/\/+$/, '') + '/' + encodeURIComponent(rec[idF]);
            fetch(url, {method: 'DELETE'}).then(function(r) {
              if (!r.ok && r.status !== 204) {
                return r.text().then(function(t) { throw new Error(t || ('HTTP ' + r.status)); });
              }
              loadList();
            }).catch(function(err) {
              alert('Delete failed: ' + err.message);
              del.disabled = false;
            });
          });
          row.appendChild(name);
          row.appendChild(meta);
          row.appendChild(del);
          listEl.appendChild(row);
        });
      }).catch(function(err) {
        listEl.innerHTML = '';
        listEl.appendChild(el('div', {class: 'ui-keys-empty'}, ['Failed to load: ' + err.message]));
      });
    }
    function doCreate() {
      var name = (formInput.value || '').trim();
      if (!name) { formInput.focus(); return; }
      formCreate.disabled = true; formCancel.disabled = true;
      fetch(cfg.create_url, {
        method: 'POST',
        headers: {'Content-Type': 'application/json'},
        body: JSON.stringify({name: name}),
      }).then(function(r) {
        if (!r.ok) return r.text().then(function(t) { throw new Error(t || ('HTTP ' + r.status)); });
        return r.json();
      }).then(function(rec) {
        hideForm();
        showRevealed(rec || {});
      }).catch(function(err) {
        alert('Create failed: ' + err.message);
      }).then(function() {
        formCreate.disabled = false; formCancel.disabled = false;
      });
    }

    newBtn.addEventListener('click', showForm);
    formCancel.addEventListener('click', hideForm);
    formCreate.addEventListener('click', doCreate);
    formInput.addEventListener('keydown', function(ev) {
      if (ev.key === 'Enter') { ev.preventDefault(); doCreate(); }
      else if (ev.key === 'Escape') { ev.preventDefault(); hideForm(); }
    });

    loadList();
    return wrap;
  };

  components.chip_picker = function(cfg) {
    var wrap = el('div', {class: 'ui-chips'}, ['Loading…']);
    var nameField  = cfg.name_field  || 'name';
    var labelField = cfg.label_field || nameField; // display key; defaults to value key
    var descField  = cfg.desc_field  || 'desc';
    Promise.all([
      fetchJSON(cfg.options_source),
      fetchJSON(cfg.record_source)
    ]).then(function(r) {
      var options = r[0] || [];
      var record = r[1] || {};
      var enabled = record[cfg.field] || [];
      wrap.innerHTML = '';
      options.forEach(function(opt) {
        var chip = el('span', {
          class: 'ui-chip' + (enabled.indexOf(opt[nameField]) >= 0 ? ' on' : ''),
          title: opt[descField] || ''
        }, [opt[labelField] || opt[nameField]]);
        chip.addEventListener('click', function() {
          var i = enabled.indexOf(opt[nameField]);
          if (i >= 0) { enabled.splice(i, 1); chip.classList.remove('on'); }
          else { enabled.push(opt[nameField]); chip.classList.add('on'); }
          record[cfg.field] = enabled;
          var body = (cfg.method && cfg.method.toUpperCase() === 'PATCH') ? (function(){ var o = {}; o[cfg.field] = enabled; return o; })() : record;
          fetchJSON(cfg.post_to, {
            method: cfg.method || 'POST', headers: {'Content-Type': 'application/json'},
            body: JSON.stringify(body)
          }).catch(function(err){ showToast('Save failed: ' + err.message); });
        });
        wrap.appendChild(chip);
      });
    }).catch(function(err){ wrap.textContent = 'Failed: ' + err.message; });
    return wrap;
  };

  components.form_panel = function(cfg) {
    var wrap = el('div', {class: 'ui-form'});
    var current = {};
    var debounceTimers = {}; // field -> setTimeout id
    var savingIndicator = el('span', {class: 'ui-form-saving'}, ['Saving…']);
    var fieldEls = {}; // field name -> rendered field wrap (for show_when)

    function save(field, value) {
      current[field] = value;
      // PATCH endpoints take just the changed field; POST gets full record.
      var isPatch = cfg.method && cfg.method.toUpperCase() === 'PATCH';
      var body = isPatch ? (function(){ var o = {}; o[field] = value; return o; })() : current;
      savingIndicator.classList.add('show');
      // Optional separate write target — used when the GET endpoint
      // that returned the current record shape isn't the right place
      // to POST updates (e.g. edit form GETs /api/record/{id} but
      // posts to /api/records list-endpoint that handles both create
      // and update by ID).
      var postURL = cfg.post_url || cfg.source;
      fetchJSON(postURL, {
        method: cfg.method || 'POST', headers: {'Content-Type': 'application/json'},
        body: JSON.stringify(body)
      }).then(function(){
        setTimeout(function(){ savingIndicator.classList.remove('show'); }, 300);
      }).catch(function(err){
        savingIndicator.classList.remove('show');
        showToast('Save failed: ' + err.message);
      });
      applyVisibility();
    }

    function applyVisibility() {
      cfg.fields.forEach(function(f) {
        var node = fieldEls[f.field];
        if (!node) return;
        if (f.show_when) {
          node.style.display = current[f.show_when] ? '' : 'none';
        }
      });
    }

    function debounced(field, value) {
      clearTimeout(debounceTimers[field]);
      debounceTimers[field] = setTimeout(function(){ save(field, value); }, 600);
    }

    function renderField(f) {
      var fieldWrap = el('div', {class: 'ui-form-field'});
      if (f.label) fieldWrap.appendChild(el('label', {class: 'ui-form-label'}, [f.label]));

      var input;
      var t = f.type || 'text';
      var initial = current[f.field];
      if (initial == null) initial = '';

      if (t === 'textarea') {
        input = el('textarea', {class: 'ui-form-textarea', rows: String(f.rows || 3), placeholder: f.placeholder || ''});
        input.value = String(initial);
        input.addEventListener('input', function(){ debounced(f.field, input.value); });
        input.addEventListener('blur', function(){
          clearTimeout(debounceTimers[f.field]);
          if (current[f.field] !== input.value) save(f.field, input.value);
        });
      } else if (t === 'select') {
        input = el('select', {class: 'ui-form-select'});
        (f.options || []).forEach(function(o) {
          var opt = el('option', {value: o.value}, [o.label || o.value]);
          if (String(initial) === String(o.value)) opt.selected = true;
          input.appendChild(opt);
        });
        input.addEventListener('change', function(){ save(f.field, input.value); });
      } else if (t === 'toggle') {
        // iOS-style switch as a form field. Saves immediately on
        // change (no debounce) since toggles are discrete decisions.
        input = el('input', {type: 'checkbox', class: 'ui-switch'});
        input.checked = !!initial;
        input.addEventListener('change', function(){ save(f.field, input.checked); });
        // Field layout for toggles: label and switch on one row, no
        // big input below. Override the default vertical stacking by
        // reflowing the field wrap.
        fieldWrap.style.display = 'flex';
        fieldWrap.style.alignItems = 'center';
        fieldWrap.style.justifyContent = 'space-between';
        fieldWrap.style.gap = '0.7rem';
        if (fieldWrap.firstChild && fieldWrap.firstChild.classList && fieldWrap.firstChild.classList.contains('ui-form-label')) {
          fieldWrap.firstChild.style.marginBottom = '0';
          fieldWrap.firstChild.style.flex = '1';
        }
      } else if (t === 'tags') {
        // Tag-style array editor — values render as compact chips
        // with × removers, plus an inline input that accepts new
        // entries (Enter or blur commits). Saves the field as a
        // pure JSON array, so endpoints can keep their natural
        // shape ({keywords:[...]}) instead of round-tripping
        // through a newline-joined string. Compact horizontal flow
        // matches the legacy autoblog keyword UI.
        input = el('div', {class: 'ui-tags'});
        var values = Array.isArray(initial) ? initial.slice() : [];
        function persistTags() {
          // Drop empties + dedup (case-insensitive). Avoid noisy
          // duplicate saves — only POST when the array actually
          // changed from the loaded record.
          var clean = [];
          var seen = {};
          values.forEach(function(v) {
            var t = String(v || '').trim();
            if (!t) return;
            var k = t.toLowerCase();
            if (seen[k]) return;
            seen[k] = true;
            clean.push(t);
          });
          var prev = Array.isArray(current[f.field]) ? current[f.field] : [];
          var changed = prev.length !== clean.length;
          for (var i = 0; !changed && i < clean.length; i++) {
            if (prev[i] !== clean[i]) changed = true;
          }
          if (changed) save(f.field, clean);
        }
        function renderTags() {
          input.innerHTML = '';
          if (!values.length) {
            input.appendChild(el('div', {class: 'ui-tags-empty'}, [f.placeholder || 'No tags yet — add one below.']));
          }
          values.forEach(function(v, idx) {
            var chip = el('span', {class: 'ui-tag'});
            chip.appendChild(document.createTextNode(v));
            var del = el('button', {class: 'ui-tag-del', type: 'button', title: 'Remove'}, ['×']);
            del.addEventListener('click', function() {
              values.splice(idx, 1);
              renderTags();
              persistTags();
            });
            chip.appendChild(del);
            input.appendChild(chip);
          });
          var addInput = el('input', {type: 'text', class: 'ui-tag-input',
            placeholder: f.add_placeholder || 'Add tag…'});
          function commit() {
            var v = addInput.value.trim();
            if (!v) return;
            values.push(v);
            renderTags();
            persistTags();
            // Re-find the input — renderTags rebuilt the DOM.
            var fresh = input.querySelector('.ui-tag-input');
            if (fresh) fresh.focus();
          }
          addInput.addEventListener('keydown', function(ev) {
            if (ev.key === 'Enter') {
              ev.preventDefault();
              commit();
            }
          });
          addInput.addEventListener('blur', commit);
          input.appendChild(addInput);
        }
        renderTags();
      } else if (t === 'rules') {
        // Rules-list editor — each line of the underlying string field
        // becomes a removable list item, with "+ Add rule" appending a
        // new empty input. Saves on blur as the joined text. Strips
        // common bullet/number prefixes ("1. ", "2.", "- ", "* ") on
        // load so existing free-form rules don't double-prefix.
        input = el('div', {class: 'ui-rules'});
        var rules = parseRules(String(initial));
        function persist() {
          var joined = rules.filter(function(r){ return r.trim() !== ''; }).join('\n');
          if (current[f.field] !== joined) save(f.field, joined);
        }
        function renderRules() {
          input.innerHTML = '';
          if (!rules.length) {
            input.appendChild(el('div', {class: 'ui-rules-empty'}, [f.placeholder || 'No rules yet — add one below.']));
          }
          rules.forEach(function(r, idx) {
            var row = el('div', {class: 'ui-rules-row'});
            var num = el('span', {class: 'ui-rules-num'}, [String(idx + 1) + '.']);
            var ti = el('input', {type: 'text', class: 'ui-rules-input', value: r, placeholder: 'rule…'});
            ti.addEventListener('blur', function() {
              rules[idx] = ti.value;
              persist();
            });
            ti.addEventListener('keydown', function(ev) {
              if (ev.key === 'Enter') {
                ev.preventDefault();
                rules[idx] = ti.value;
                rules.splice(idx + 1, 0, '');
                renderRules();
                persist();
                var inputs = input.querySelectorAll('.ui-rules-input');
                if (inputs[idx + 1]) inputs[idx + 1].focus();
              }
            });
            var del = el('button', {class: 'ui-rules-del', title: 'Remove this rule', type: 'button'}, ['×']);
            del.addEventListener('click', function() {
              rules.splice(idx, 1);
              renderRules();
              persist();
            });
            row.appendChild(num);
            row.appendChild(ti);
            row.appendChild(del);
            input.appendChild(row);
          });
          var addBtn = el('button', {class: 'ui-rules-add', type: 'button'}, ['+ Add rule']);
          addBtn.addEventListener('click', function() {
            rules.push('');
            renderRules();
            var inputs = input.querySelectorAll('.ui-rules-input');
            var last = inputs[inputs.length - 1];
            if (last) last.focus();
          });
          input.appendChild(addBtn);
        }
        renderRules();
      } else if (t === 'number') {
        input = el('input', {type: 'number', class: 'ui-form-input', placeholder: f.placeholder || ''});
        if (f.min) input.min = String(f.min);
        if (f.max) input.max = String(f.max);
        if (f.decimals > 0) input.step = String(Math.pow(10, -f.decimals));
        input.value = (initial === '' || initial == null) ? '' : String(initial);
        input.addEventListener('change', function(){
          var n = (f.decimals > 0) ? parseFloat(input.value) : parseInt(input.value, 10);
          save(f.field, isNaN(n) ? 0 : n);
        });
      } else {
        // text, tel, anything else
        input = el('input', {type: t, class: 'ui-form-input', placeholder: f.placeholder || ''});
        input.value = String(initial);
        input.addEventListener('input', function(){ debounced(f.field, input.value); });
        input.addEventListener('blur', function(){
          clearTimeout(debounceTimers[f.field]);
          if (current[f.field] !== input.value) save(f.field, input.value);
        });
      }
      // Chips row above the input — declared via f.chips_source.
      // Each chip applies a preset value to the input and saves.
      // "+ New" optionally opens a create dialog with AI Assist.
      // Append chipsHost FIRST then input — DOM order matches visual
      // order ("chips on top of input"). Earlier code did
      // insertBefore(chipsHost, input) before input was attached,
      // which threw "node is not a child of this node" when the
      // chip-source field rendered (settings + persona).
      if (f.chips_source) {
        var chipsHost = el('div', {class: 'ui-form-chips'});
        fieldWrap.appendChild(chipsHost);
        renderFormChips(chipsHost, f, input);
      }
      fieldWrap.appendChild(input);
      if (f.help) fieldWrap.appendChild(el('span', {class: 'ui-form-help'}, [f.help]));
      fieldEls[f.field] = fieldWrap;
      return fieldWrap;
    }

    function renderFormChips(host, f, input) {
      var valueField = f.chips_value_field || 'value';
      function applyChip(chipObj) {
        var v = chipObj ? (chipObj[valueField] || '') : '';
        input.value = v;
        save(f.field, v);
        // Companion fields — chips_also_set maps {targetField: chipProp}
        // so a single click can fan a multi-property chip out into
        // multiple inputs (persona chip → personality + persona_name).
        if (f.chips_also_set && chipObj) {
          Object.keys(f.chips_also_set).forEach(function(targetField) {
            var src = f.chips_also_set[targetField];
            var newVal = chipObj[src];
            if (newVal == null) return;
            var fw = fieldEls[targetField];
            if (!fw) return;
            var targetInput = fw.querySelector('input, textarea, select');
            if (!targetInput) return;
            targetInput.value = newVal;
            save(targetField, newVal);
          });
        }
      }
      // Clear chip path — wipes both the primary input and any
      // companion fields declared via chips_also_set so the form
      // returns to a fully-empty state on a single click.
      function clearChip() {
        input.value = '';
        save(f.field, '');
        if (f.chips_also_set) {
          Object.keys(f.chips_also_set).forEach(function(targetField) {
            var fw = fieldEls[targetField];
            if (!fw) return;
            var targetInput = fw.querySelector('input, textarea, select');
            if (!targetInput) return;
            targetInput.value = '';
            save(targetField, '');
          });
        }
      }
      function refresh() {
        host.innerHTML = '';
        host.appendChild(el('span', {class: 'ui-form-chips-loading'}, ['Loading…']));
        fetchJSON(f.chips_source).then(function(items) {
          host.innerHTML = '';
          (items || []).forEach(function(p) {
            var chip = el('span', {class: 'ui-form-chip', title: p.builtin ? 'Built-in' : 'Click to apply, double-click to delete'},
              [p.name || '?']);
            chip.addEventListener('click', function() { applyChip(p); });
            if (!p.builtin && f.chips_delete_url) {
              chip.addEventListener('dblclick', function(ev) {
                ev.stopPropagation();
                if (!confirm('Delete "' + p.name + '"?')) return;
                var url = f.chips_delete_url.replace('{id}', encodeURIComponent(p.id || ''));
                fetch(url, {method: 'DELETE'}).then(refresh);
              });
            }
            host.appendChild(chip);
          });
          // Clear chip.
          var clr = el('span', {class: 'ui-form-chip'}, ['Clear']);
          clr.addEventListener('click', clearChip);
          host.appendChild(clr);
          // + New chip (only if create endpoint set).
          if (f.chips_create_url) {
            var add = el('span', {class: 'ui-form-chip ui-form-chip-add'}, [f.chips_add_label || '+ New']);
            add.addEventListener('click', function() {
              showFormChipCreate(f, input, refresh);
            });
            host.appendChild(add);
          }
        }).catch(function() {
          host.innerHTML = '';
          host.appendChild(el('span', {class: 'ui-form-chips-loading'}, ['(failed to load)']));
        });
      }
      refresh();
    }

    function showFormChipCreate(f, targetInput, onSaved) {
      var valueField = f.chips_value_field || 'value';
      // Modal overlay rendered in document.body so it isn't clipped
      // by parent containers.
      var overlay = el('div', {class: 'ui-form-modal-overlay'});
      var modal = el('div', {class: 'ui-form-modal'});
      modal.appendChild(el('div', {class: 'ui-form-modal-h'}, ['New ' + (f.label || 'preset')]));
      modal.appendChild(el('div', {class: 'ui-form-modal-hint'},
        ['Type a name (e.g. "Hank Hill"). Click AI Assist to expand into a full prompt, then save.']));

      modal.appendChild(el('label', {class: 'ui-form-label'}, ['Name']));
      var nameIn = el('input', {type: 'text', class: 'ui-form-input', placeholder: 'e.g. Hank Hill'});
      modal.appendChild(nameIn);

      modal.appendChild(el('label', {class: 'ui-form-label'}, ['Personality']));
      var textIn = el('textarea', {class: 'ui-form-textarea', rows: '6',
        placeholder: 'Type a seed name and click AI Assist below — or write your own.'});
      modal.appendChild(textIn);

      var actions = el('div', {class: 'ui-form-modal-actions'});
      var assistBtn = null;
      if (f.chips_assist_url) {
        assistBtn = el('button', {class: 'ui-pl-btn secondary'}, ['AI Assist']);
        assistBtn.addEventListener('click', function() {
          var seed = nameIn.value.trim() || textIn.value.trim();
          if (!seed) { showToast('Type a name or seed first.'); return; }
          var orig = assistBtn.textContent;
          assistBtn.textContent = 'Generating…';
          assistBtn.disabled = true;
          fetch(f.chips_assist_url, {
            method: 'POST', headers: {'Content-Type': 'application/json'},
            body: JSON.stringify({seed: seed}),
          }).then(function(r) {
            if (!r.ok) return r.text().then(function(t){ throw new Error(t || ('HTTP ' + r.status)); });
            return r.text();
          }).then(function(text) {
            textIn.value = text;
          }).catch(function(err) {
            showToast('AI Assist failed: ' + err.message);
          }).then(function() {
            assistBtn.textContent = orig;
            assistBtn.disabled = false;
          });
        });
        actions.appendChild(assistBtn);
      }
      var spacer = el('div', {style: 'flex:1'});
      actions.appendChild(spacer);
      var cancelBtn = el('button', {class: 'ui-pl-btn secondary'}, ['Cancel']);
      cancelBtn.addEventListener('click', function() { overlay.remove(); });
      actions.appendChild(cancelBtn);
      var saveBtn = el('button', {class: 'ui-pl-btn primary'}, ['Save']);
      saveBtn.addEventListener('click', function() {
        var nm = nameIn.value.trim();
        var val = textIn.value.trim();
        if (!nm) { showToast('Name required.'); return; }
        if (!val) { showToast('Value required (use AI Assist or type your own).'); return; }
        var body = {name: nm};
        body[valueField] = val;
        fetch(f.chips_create_url, {
          method: 'POST', headers: {'Content-Type': 'application/json'},
          body: JSON.stringify(body),
        }).then(function(r) {
          if (!r.ok) return r.text().then(function(t){ throw new Error(t || ('HTTP ' + r.status)); });
          return r.json().catch(function(){ return null; });
        }).then(function() {
          targetInput.value = val;
          save(f.field, val);
          overlay.remove();
          if (onSaved) onSaved();
        }).catch(function(err) {
          showToast('Save failed: ' + err.message);
        });
      });
      actions.appendChild(saveBtn);
      modal.appendChild(actions);

      overlay.appendChild(modal);
      overlay.addEventListener('click', function(ev) { if (ev.target === overlay) overlay.remove(); });
      document.body.appendChild(overlay);
      nameIn.focus();
    }

    function render() {
      wrap.innerHTML = '';
      fieldEls = {};
      cfg.fields.forEach(function(f){ wrap.appendChild(renderField(f)); });
      applyVisibility();
      // Saving indicator gets attached to the parent section header.
      var section = wrap.closest('.ui-section');
      if (section) {
        var h = section.querySelector('.ui-section-h-r');
        if (h && !h.contains(savingIndicator)) h.appendChild(savingIndicator);
      }
    }

    // Source empty / unset → render with an empty record. Lets a
    // FormPanel act as a create-form when there's nothing to load,
    // posting the typed fields to PostURL on save.
    if (cfg.source) {
      fetchJSON(cfg.source).then(function(d){ current = d || {}; render(); })
        .catch(function(err){ wrap.textContent = 'Failed to load: ' + err.message; });
    } else {
      render();
    }
    return wrap;
  };

  components.display_panel = function(cfg) {
    var wrap = el('div', {class: 'ui-display'}, ['Loading…']);
    function reload() {
      fetchJSON(cfg.source).then(function(d) {
        wrap.innerHTML = '';
        var data = d || {};
        cfg.pairs.forEach(function(p) {
          var row = el('div', {class: 'ui-display-row'}, [
            el('span', {class: 'ui-display-label'}, [p.label]),
            el('span', {class: 'ui-display-value' + (p.mono ? ' mono' : '')}, [fmt(data[p.field], p.format)]),
          ]);
          wrap.appendChild(row);
        });
      }).catch(function(err){ wrap.textContent = 'Failed: ' + err.message; });
    }
    reload();
    if (cfg.auto_refresh_ms && cfg.auto_refresh_ms > 0) setInterval(reload, cfg.auto_refresh_ms);
    return wrap;
  };

  components.pipeline_watch_panel = function(cfg) {
    // Live pipeline-follow view. Header + stage bar + status feed +
    // article body + completion actions. SSE-driven; reconnects on
    // dropped connections; shows a completion view when DoneStage
    // fires (or on graceful disconnect after that).
    var topicField = cfg.topic_field || 'topic';
    var doneField  = cfg.done_field  || 'done';
    var articleStage = cfg.article_stage || 'article';
    var draftStage   = cfg.draft_stage   || 'rough_draft';
    var doneStage    = cfg.done_stage    || 'done';
    var errorStage   = cfg.error_stage   || 'error';
    var stageList = (cfg.stages || []);
    var stageOrder = stageList.map(function(s) { return s.key; });
    var stageMeta = {};
    stageList.forEach(function(s) { stageMeta[s.key] = s; });

    var wrap = el('div', {class: 'ui-watch'});
    var header = el('div', {class: 'ui-watch-header'});
    if (cfg.app_name) header.appendChild(el('div', {class: 'ui-watch-app'}, [cfg.app_name]));
    var titleRow = el('div', {class: 'ui-watch-title-row'});
    var titleText = el('span', {class: 'ui-watch-title'},
      [el('span', {class: 'ui-watch-spinner'}), ' Pipeline']);
    titleRow.appendChild(titleText);
    var cancelBtn = null;
    if (cfg.cancel_url) {
      cancelBtn = el('button', {class: 'ui-watch-cancel'}, ['Cancel']);
      titleRow.appendChild(cancelBtn);
    }
    header.appendChild(titleRow);
    var topicEl = el('div', {class: 'ui-watch-topic'}, ['Connecting…']);
    header.appendChild(topicEl);
    wrap.appendChild(header);

    var stageBar = el('div', {class: 'ui-watch-stages'});
    wrap.appendChild(stageBar);
    var pills = {};
    var currentSubPillKey = null;

    function setStageState(key, state) {
      var pill = pills[key];
      if (!pill) {
        pill = el('span', {class: 'ui-watch-pill'});
        pill.dataset.key = key;
        var meta = stageMeta[key] || {};
        var label = meta.label || (key.charAt(0).toUpperCase() + key.slice(1));
        if (meta.icon) label = meta.icon + ' ' + label;
        pill._label = label;
        pill.textContent = label;
        stageBar.appendChild(pill);
        pills[key] = pill;
      }
      pill.classList.remove('active', 'done', 'error');
      if (state) pill.classList.add(state);
      if (state === 'active') {
        pill.innerHTML = '';
        pill.appendChild(el('span', {class: 'ui-watch-spinner'}));
        pill.appendChild(document.createTextNode(' ' + pill._label));
      } else {
        pill.textContent = pill._label;
      }
    }

    function ensureSubPill(parentKey, subKey, subLabel, state) {
      var key = parentKey + '::' + subKey;
      if (!pills[key]) {
        var meta = stageMeta[parentKey] || {};
        var icon = meta.icon ? (meta.icon + ' ') : '';
        var pill = el('span', {class: 'ui-watch-pill'});
        pill._label = icon + subLabel;
        pill.textContent = pill._label;
        stageBar.appendChild(pill);
        pills[key] = pill;
      }
      // Mark previous sub-pill of the same parent as done when a new
      // one becomes active — matches legacy "Gap 1 → Gap 2 → Main".
      if (currentSubPillKey && currentSubPillKey !== key) {
        var prev = pills[currentSubPillKey];
        if (prev && prev.classList.contains('active')) {
          prev.classList.remove('active');
          prev.classList.add('done');
          prev.textContent = prev._label;
        }
      }
      setStageState(key, state || 'active');
      currentSubPillKey = key;
    }

    function advancePriorStages(currentKey) {
      var idx = stageOrder.indexOf(currentKey);
      if (idx < 0) return;
      stageOrder.forEach(function(k, i) {
        var p = pills[k];
        if (p && i < idx && p.classList.contains('active')) {
          p.classList.remove('active');
          p.classList.add('done');
          p.textContent = p._label;
        }
      });
      // When leaving a stage that had sub-pills, mark them done too.
      Object.keys(pills).forEach(function(key) {
        var sep = key.indexOf('::');
        if (sep < 0) return;
        var parent = key.slice(0, sep);
        if (parent !== currentKey) {
          var p = pills[key];
          if (p && p.classList.contains('active')) {
            p.classList.remove('active');
            p.classList.add('done');
            p.textContent = p._label;
          }
        }
      });
    }

    var statusView = el('div', {class: 'ui-watch-status'});
    wrap.appendChild(statusView);
    var draftView = el('div', {class: 'ui-watch-draft', style: 'display:none'});
    wrap.appendChild(draftView);
    var articleView = el('div', {class: 'ui-watch-article', style: 'display:none'});
    var articleTitle = el('h1', {class: 'ui-watch-article-title'});
    var articleBody  = el('div', {class: 'ui-watch-article-body'});
    articleView.appendChild(articleTitle);
    articleView.appendChild(articleBody);
    wrap.appendChild(articleView);
    var doneActions = el('div', {class: 'ui-watch-done-actions', style: 'display:none'});
    wrap.appendChild(doneActions);

    function addStatus(message) {
      var line = el('div', {class: 'ui-watch-status-msg'}, [String(message || '')]);
      statusView.appendChild(line);
      statusView.scrollTop = statusView.scrollHeight;
    }

    function showArticle(title, content) {
      statusView.style.display = 'none';
      articleView.style.display = '';
      articleTitle.textContent = title || '';
      articleBody.innerHTML = mdToHTML(String(content || ''));
    }

    function renderDoneActions(lastEvent) {
      if (!cfg.on_done_actions || !cfg.on_done_actions.length) return;
      doneActions.innerHTML = '';
      doneActions.style.display = '';
      cfg.on_done_actions.forEach(function(act) {
        var url = substitute(act.url, lastEvent || {});
        var classes = 'ui-watch-action';
        if (act.variant) classes += ' ' + act.variant;
        if ((act.method || 'GET').toUpperCase() === 'POST') {
          var btn = el('button', {class: classes}, [act.label || 'Action']);
          btn.addEventListener('click', function() {
            fetch(url, {method: 'POST'}).then(function(r) {
              if (!r.ok) throw new Error('HTTP ' + r.status);
            }).catch(function(err) {
              showToast('Failed: ' + err.message);
            });
          });
          doneActions.appendChild(btn);
        } else {
          var attrs = {class: classes, href: url};
          if (act.new_tab) { attrs.target = '_blank'; attrs.rel = 'noopener'; }
          doneActions.appendChild(el('a', attrs, [act.label || 'Open']));
        }
      });
    }

    var pipelineDone = false;
    function markComplete(message, lastEvent) {
      pipelineDone = true;
      titleText.innerHTML = '';
      titleText.appendChild(document.createTextNode('✅ Complete'));
      if (cancelBtn) cancelBtn.style.display = 'none';
      Object.keys(pills).forEach(function(k) {
        var p = pills[k];
        if (p.classList.contains('active')) {
          p.classList.remove('active');
          p.classList.add('done');
          p.textContent = p._label;
        }
      });
      if (message) topicEl.textContent = message;
      renderDoneActions(lastEvent || {});
    }
    function markError(message) {
      pipelineDone = true;
      titleText.innerHTML = '';
      titleText.appendChild(document.createTextNode('❌ Error'));
      if (cancelBtn) cancelBtn.style.display = 'none';
      Object.keys(pills).forEach(function(k) {
        var p = pills[k];
        if (p.classList.contains('active')) {
          p.classList.remove('active');
          p.classList.add('error');
          p.textContent = p._label;
        }
      });
      if (message) addStatus('ERROR: ' + message);
    }

    if (cancelBtn) {
      cancelBtn.addEventListener('click', function() {
        if (!confirm('Cancel the pipeline?')) return;
        fetch(cfg.cancel_url, {method: 'POST'}).then(function() {
          titleText.innerHTML = '';
          titleText.appendChild(document.createTextNode('⛔ Cancelled'));
          topicEl.textContent = 'Pipeline cancelled by user.';
          cancelBtn.style.display = 'none';
          if (evtSource) evtSource.close();
        }).catch(function() {});
      });
    }

    // Seed the header from /api/info before SSE arrives.
    fetchJSON(cfg.info_url).then(function(info) {
      info = info || {};
      if (info[topicField]) topicEl.textContent = info[topicField];
      if (info[doneField]) markComplete(info[topicField] || 'Pipeline finished', info);
    }).catch(function() {
      topicEl.textContent = 'Session not found or expired.';
    });

    var evtSource = null;
    function connectEvents() {
      evtSource = new EventSource(cfg.events_url);
      evtSource.onmessage = function(ev) {
        var data;
        try { data = JSON.parse(ev.data); } catch (_) { return; }
        var stage = data.stage;
        if (!stage) return;

        if (stage === draftStage) {
          var details = el('details', {class: 'ui-watch-draft-details', open: true});
          var summary = el('summary', {}, ['Rough draft (pre-voice pass)']);
          details.appendChild(summary);
          details.appendChild(el('div', {class: 'ui-watch-draft-body', html: mdToHTML(String(data.message || ''))}));
          draftView.innerHTML = '';
          draftView.appendChild(details);
          draftView.style.display = '';
          return;
        }
        if (stage === articleStage) {
          showArticle(data.title || data.message || '', data.content || data.message || '');
          return;
        }
        if (stage === doneStage || stage === 'stream_end') {
          markComplete(data.title || data.message || data[topicField] || 'Pipeline finished', data);
          if (evtSource) evtSource.close();
          return;
        }
        if (stage === errorStage) {
          markError(data.message);
          if (evtSource) evtSource.close();
          return;
        }
        // Generic stage pill update.
        if (stageMeta[stage]) {
          var meta = stageMeta[stage];
          if (meta.sub_pattern) {
            try {
              var re = new RegExp(meta.sub_pattern);
              var m = String(data.message || '').match(re);
              if (m) {
                var subKey = m[0];
                var label = meta.sub_label_template || '$1';
                label = label.replace(/\$(\d)/g, function(_, n) { return m[Number(n)] || ''; });
                ensureSubPill(stage, subKey, label, 'active');
                if (data.message) addStatus(data.message);
                advancePriorStages(stage);
                if (data.message) topicEl.textContent = data.message;
                return;
              }
            } catch (_) {}
          }
          setStageState(stage, 'active');
          advancePriorStages(stage);
        }
        if (data.message) {
          addStatus(data.message);
          topicEl.textContent = data.message;
        }
      };
      evtSource.onerror = function() {
        if (!evtSource) return;
        evtSource.close();
        evtSource = null;
        if (pipelineDone) return;
        // Reconnect after a delay; if the server reports done, switch
        // to completion view instead of reconnecting.
        setTimeout(function() {
          fetchJSON(cfg.info_url).then(function(info) {
            info = info || {};
            if (info[doneField]) {
              markComplete(info[topicField] || 'Pipeline finished', info);
              return;
            }
            connectEvents();
          }).catch(function() {
            topicEl.textContent = 'Connection lost. Retrying…';
            setTimeout(connectEvents, 5000);
          });
        }, 3000);
      };
    }
    connectEvents();
    return wrap;
  };

  components.api_key_panel = function(cfg) {
    var keyField = cfg.key_field || 'key';
    var wrap = el('div', {class: 'ui-apikey'});
    var input = el('input', {type: 'text', class: 'ui-apikey-input', readonly: 'readonly',
      placeholder: cfg.placeholder || 'No key generated'});
    wrap.appendChild(input);
    function setKey(v) { input.value = String(v || ''); }
    function load() {
      fetchJSON(cfg.source).then(function(d) {
        setKey((d || {})[keyField]);
      }).catch(function(err) { showToast('Failed to load: ' + err.message); });
    }
    if (cfg.generate_url) {
      var gen = el('button', {class: 'ui-apikey-btn'}, ['Generate']);
      gen.addEventListener('click', function() {
        if (cfg.confirm_generate && !confirm(cfg.confirm_generate)) return;
        gen.disabled = true;
        fetchJSON(cfg.generate_url, {method: 'POST'}).then(function(d) {
          setKey((d || {})[keyField]);
          showToast('Key rotated.');
        }).catch(function(err) {
          showToast('Failed: ' + err.message);
        }).then(function() {
          gen.disabled = false;
        });
      });
      wrap.appendChild(gen);
    }
    if (cfg.allow_copy) {
      var cp = el('button', {class: 'ui-apikey-btn'}, ['Copy']);
      cp.addEventListener('click', function() {
        if (!input.value) return;
        // Async clipboard API where available; fall back to
        // selectAll+execCommand on older / non-secure contexts.
        var done = function() { showToast('Copied.'); };
        var fail = function() { showToast('Copy failed — select manually.'); };
        if (navigator.clipboard && navigator.clipboard.writeText) {
          navigator.clipboard.writeText(input.value).then(done).catch(function() {
            input.select();
            try {
              if (document.execCommand('copy')) { done(); return; }
            } catch (_) {}
            fail();
          });
        } else {
          input.select();
          try {
            if (document.execCommand('copy')) { done(); return; }
          } catch (_) {}
          fail();
        }
      });
      wrap.appendChild(cp);
    }
    load();
    return wrap;
  };

  components.suggest_panel = function(cfg) {
    // Suggestion list with optional direction input + per-row
    // primary (click-the-row) and secondary (per-row button)
    // actions. Mirrors the legacy autoblog "Topic Ideas" UX.
    var wrap = el('div', {class: 'ui-suggest'});
    var controls = el('div', {class: 'ui-suggest-controls'});
    var direction = el('input', {type: 'text', class: 'ui-form-input ui-suggest-direction',
      placeholder: cfg.placeholder || 'Focus area (optional)…'});
    var btn = el('button', {class: 'ui-pl-btn ui-suggest-btn'},
      [el('span', {class: 'ui-pl-prefill-icon'}, ['✨']), ' ', cfg.suggest_label || 'Suggest']);
    controls.appendChild(direction);
    controls.appendChild(btn);
    wrap.appendChild(controls);
    var list = el('div', {class: 'ui-suggest-list'});
    wrap.appendChild(list);

    var qField = cfg.question_field || 'question';
    var hField = cfg.hook_field || 'hook';
    function pickField(item, primary, fallbacks) {
      if (item[primary]) return item[primary];
      for (var i = 0; i < fallbacks.length; i++) {
        if (item[fallbacks[i]]) return item[fallbacks[i]];
      }
      return '';
    }

    function fireAction(action, item) {
      if (!action || !action.url) return Promise.resolve();
      if (action.confirm && !confirm(action.confirm)) return Promise.resolve();
      var body = null;
      if (action.body_map) {
        body = {};
        Object.keys(action.body_map).forEach(function(k) {
          var src = action.body_map[k];
          body[k] = src === '__direction__' ? direction.value : item[src];
        });
      }
      var fetchOpts = {method: action.method || 'POST'};
      if (body) {
        fetchOpts.headers = {'Content-Type': 'application/json'};
        fetchOpts.body = JSON.stringify(body);
      }
      return fetch(action.url, fetchOpts).then(function(r) {
        if (!r.ok) throw new Error('HTTP ' + r.status);
        if (action.toast) {
          var msg = String(action.toast).replace(/\{question\}/g, pickField(item, qField, ['topic', 'text']))
            .replace(/\{hook\}/g, pickField(item, hField, ['description', 'summary']));
          showToast(msg);
        }
        // Refresh sibling lists declared in action.invalidate (e.g.
        // the Blog Queue table re-fetches after this row is queued).
        if (action.invalidate && action.invalidate.length) {
          window.uiInvalidate(action.invalidate);
        }
      }).catch(function(err) {
        showToast('Failed: ' + err.message);
      });
    }

    function loadSuggestions() {
      var orig = btn.textContent;
      btn.disabled = true;
      list.innerHTML = '<div class="ui-suggest-loading">Generating suggestions…</div>';
      var body = {};
      body[cfg.direction_field || 'direction'] = direction.value || '';
      fetch(cfg.url, {
        method: cfg.method || 'POST',
        headers: {'Content-Type': 'application/json'},
        body: JSON.stringify(body),
      }).then(function(r) {
        if (!r.ok) throw new Error('HTTP ' + r.status);
        return r.json();
      }).then(function(items) {
        list.innerHTML = '';
        if (!Array.isArray(items) || !items.length) {
          list.appendChild(el('div', {class: 'ui-suggest-empty'},
            [cfg.empty_text || 'No suggestions returned.']));
          return;
        }
        items.forEach(function(item) {
          var row = el('div', {class: 'ui-suggest-item'});
          var bodyEl = el('div', {class: 'ui-suggest-item-body'});
          var topic = pickField(item, qField, ['topic', 'text']);
          var hook = pickField(item, hField, ['description', 'summary']);
          if (topic) bodyEl.appendChild(el('div', {class: 'ui-suggest-item-q'}, [topic]));
          if (hook) bodyEl.appendChild(el('div', {class: 'ui-suggest-item-hook'}, [hook]));
          row.appendChild(bodyEl);
          if (cfg.primary_action) {
            bodyEl.style.cursor = 'pointer';
            bodyEl.addEventListener('click', function() {
              fireAction(cfg.primary_action, item);
            });
          }
          if (cfg.secondary_action) {
            var secBtn = el('button', {class: 'ui-pl-btn ui-suggest-secondary'},
              [cfg.secondary_action.label || 'Action']);
            secBtn.addEventListener('click', function(ev) {
              ev.stopPropagation();
              fireAction(cfg.secondary_action, item).then(function() {
                row.style.opacity = '0.4';
                row.style.pointerEvents = 'none';
              });
            });
            row.appendChild(secBtn);
          }
          list.appendChild(row);
        });
      }).catch(function(err) {
        list.innerHTML = '';
        list.appendChild(el('div', {class: 'ui-suggest-empty'}, ['Failed: ' + err.message]));
      }).then(function() {
        btn.disabled = false;
        btn.textContent = orig;
      });
    }

    btn.addEventListener('click', loadSuggestions);
    direction.addEventListener('keydown', function(ev) {
      if (ev.key === 'Enter') {
        ev.preventDefault();
        loadSuggestions();
      }
    });
    return wrap;
  };

  components.bar_chart = function(cfg) {
    var wrap = el('div', {class: 'ui-chart'}, ['Loading…']);
    var height = cfg.height_px || 200;
    var decimals = cfg.y_decimals != null ? cfg.y_decimals : 2;
    var prefix = cfg.y_prefix || '';

    function fmtX(v) {
      if (cfg.x_format === 'date' && v) {
        // Accept "YYYY-MM-DD" → "MMM DD".
        var m = String(v).match(/^(\d{4})-(\d{2})-(\d{2})$/);
        if (m) {
          var d = new Date(Number(m[1]), Number(m[2])-1, Number(m[3]));
          return d.toLocaleDateString(undefined, {month: 'short', day: 'numeric'});
        }
      }
      return String(v);
    }
    function fmtY(n) { return prefix + Number(n).toFixed(decimals); }

    fetchJSON(cfg.source).then(function(d) {
      var data = Array.isArray(d) ? d : (d && d.data) || [];
      wrap.innerHTML = '';
      // Position the wrap relatively so the tooltip can absolute-pos against it.
      wrap.style.position = 'relative';
      if (!data.length) {
        wrap.appendChild(el('div', {class: 'ui-chart-empty'}, [cfg.empty_text || 'No data yet.']));
        return;
      }
      var max = 0;
      data.forEach(function(p){ if (Number(p[cfg.y_field]) > max) max = Number(p[cfg.y_field]); });
      if (max === 0) max = 1; // avoid divide-by-zero, all-zero series renders flat

      // Instant-feedback tooltip element. Updated on bar mousemove.
      var tip = el('div', {class: 'ui-chart-tip'});
      tip.style.display = 'none';
      wrap.appendChild(tip);

      // Build SVG. Use viewBox with explicit aspect so the chart
      // scales fluidly with the section width.
      var svgNS = 'http://www.w3.org/2000/svg';
      var W = 600, H = height, P = 20; // padding
      var bw = (W - P*2) / data.length;
      var svg = document.createElementNS(svgNS, 'svg');
      svg.setAttribute('viewBox', '0 0 ' + W + ' ' + H);
      svg.setAttribute('preserveAspectRatio', 'none');
      svg.setAttribute('width', '100%');
      svg.setAttribute('height', String(H));
      svg.classList.add('ui-chart-svg');

      // Y axis baseline.
      var axis = document.createElementNS(svgNS, 'line');
      axis.setAttribute('x1', String(P)); axis.setAttribute('y1', String(H - P));
      axis.setAttribute('x2', String(W - P)); axis.setAttribute('y2', String(H - P));
      axis.setAttribute('class', 'ui-chart-axis');
      svg.appendChild(axis);

      // Max-value gridline + label.
      var gridY = P;
      var grid = document.createElementNS(svgNS, 'line');
      grid.setAttribute('x1', String(P)); grid.setAttribute('y1', String(gridY));
      grid.setAttribute('x2', String(W - P)); grid.setAttribute('y2', String(gridY));
      grid.setAttribute('class', 'ui-chart-grid');
      svg.appendChild(grid);
      var maxLabel = document.createElementNS(svgNS, 'text');
      maxLabel.setAttribute('x', String(P - 4));
      maxLabel.setAttribute('y', String(gridY + 4));
      maxLabel.setAttribute('class', 'ui-chart-axis-label');
      maxLabel.setAttribute('text-anchor', 'end');
      maxLabel.textContent = fmtY(max);
      svg.appendChild(maxLabel);

      data.forEach(function(p, i) {
        var v = Number(p[cfg.y_field]) || 0;
        var x = P + i * bw + bw * 0.15;
        var w = bw * 0.7;
        var h = (v / max) * (H - P*2);
        var y = H - P - h;
        var bar = document.createElementNS(svgNS, 'rect');
        bar.setAttribute('x', String(x));
        bar.setAttribute('y', String(y));
        bar.setAttribute('width', String(w));
        bar.setAttribute('height', String(h));
        bar.setAttribute('class', 'ui-chart-bar');
        // Instant tooltip — show on enter, position on move, hide on leave.
        // Hover area extends slightly beyond the bar so thin bars are
        // still easy to grab without pixel-precise aim.
        var hover = document.createElementNS(svgNS, 'rect');
        hover.setAttribute('x', String(P + i * bw));
        hover.setAttribute('y', String(P));
        hover.setAttribute('width', String(bw));
        hover.setAttribute('height', String(H - P*2));
        hover.setAttribute('fill', 'transparent');
        hover.style.cursor = 'crosshair';
        // Build the tooltip body — headline (date + cost), then the
        // optional breakdown rows in a compact two-column layout.
        var buildTip = function() {
          var parts = [];
          parts.push('<div class="ui-chart-tip-h">' + fmtX(p[cfg.x_field]) + '</div>');
          parts.push('<div class="ui-chart-tip-y">' + fmtY(v) + '</div>');
          if (cfg.breakdown && cfg.breakdown.length) {
            parts.push('<div class="ui-chart-tip-rows">');
            cfg.breakdown.forEach(function(pair) {
              var rawV = p[pair.field];
              if (rawV == null) return;
              var fv = fmt(rawV, pair.format);
              parts.push(
                '<div class="ui-chart-tip-row">' +
                  '<span class="ui-chart-tip-label">' + pair.label + '</span>' +
                  '<span class="ui-chart-tip-val' + (pair.mono ? ' mono' : '') + '">' + fv + '</span>' +
                '</div>'
              );
            });
            parts.push('</div>');
          }
          return parts.join('');
        };
        var tipHTML = buildTip();
        var showTip = function(ev) {
          tip.innerHTML = tipHTML;
          tip.style.display = 'block';
          var rect = wrap.getBoundingClientRect();
          var x = ev.clientX - rect.left;
          var y = ev.clientY - rect.top;
          // Flip horizontally if the tooltip would clip the right edge.
          var tw = tip.offsetWidth, th = tip.offsetHeight;
          var tx = x + 12; if (tx + tw + 8 > rect.width) tx = x - tw - 12;
          var ty = y - th - 8; if (ty < 4) ty = y + 16;
          tip.style.left = Math.max(4, tx) + 'px';
          tip.style.top  = ty + 'px';
        };
        hover.addEventListener('mouseenter', showTip);
        hover.addEventListener('mousemove', showTip);
        hover.addEventListener('mouseleave', function(){ tip.style.display = 'none'; });
        svg.appendChild(bar);
        svg.appendChild(hover);

        // X-axis label every Nth bar so they don't overlap.
        var step = Math.ceil(data.length / 10);
        if (i % step === 0 || i === data.length - 1) {
          var tx = document.createElementNS(svgNS, 'text');
          tx.setAttribute('x', String(x + w/2));
          tx.setAttribute('y', String(H - 4));
          tx.setAttribute('text-anchor', 'middle');
          tx.setAttribute('class', 'ui-chart-axis-label');
          tx.textContent = fmtX(p[cfg.x_field]);
          svg.appendChild(tx);
        }
      });
      wrap.appendChild(svg);

      // Total summary below chart.
      var total = data.reduce(function(s, p){ return s + (Number(p[cfg.y_field]) || 0); }, 0);
      wrap.appendChild(el('div', {class: 'ui-chart-summary'}, [
        'Total over ' + data.length + ' periods: ' + fmtY(total),
      ]));
    }).catch(function(err){ wrap.textContent = 'Failed: ' + err.message; });
    return wrap;
  };

  components.action_list = function(cfg) {
    var wrap = el('div', {class: 'ui-actionlist'}, ['Loading…']);
    var labelField = cfg.label_field || 'Label';
    var descField  = cfg.desc_field  || 'Desc';
    var btnText    = cfg.button_text || 'Run';
    fetchJSON(cfg.source).then(function(items) {
      wrap.innerHTML = '';
      if (!items || !items.length) {
        wrap.appendChild(el('div', {class: 'ui-actionlist-empty'}, [cfg.empty_text || 'Nothing here.']));
        return;
      }
      items.forEach(function(item) {
        var status = el('span', {class: 'ui-actionlist-status'});
        var btn = el('button', {class: 'ui-row-btn', onclick: function() {
          if (cfg.confirm && !confirm(cfg.confirm)) return;
          var url = substitute(cfg.post_to, item);
          btn.disabled = true;
          status.textContent = '…';
          fetchJSON(url, {method: cfg.method || 'POST'}).then(function(r) {
            btn.disabled = false;
            // Most maintenance endpoints return {fixed: N} or similar;
            // surface a digit if present.
            if (r && typeof r === 'object') {
              var n = r.fixed != null ? r.fixed : (r.removed != null ? r.removed : null);
              status.textContent = n != null ? ('done — ' + n) : 'done';
            } else {
              status.textContent = 'done';
            }
            setTimeout(function(){ status.textContent = ''; }, 4000);
          }).catch(function(err) {
            btn.disabled = false;
            status.textContent = '';
            showToast('Failed: ' + err.message);
          });
        }}, [btnText]);
        var row = el('div', {class: 'ui-actionlist-row'}, [
          el('div', {class: 'ui-actionlist-text'}, [
            el('div', {class: 'ui-actionlist-label'}, [item[labelField] || '?']),
            item[descField] ? el('div', {class: 'ui-actionlist-desc'}, [item[descField]]) : null,
          ]),
          status, btn,
        ]);
        wrap.appendChild(row);
      });
    }).catch(function(err){ wrap.textContent = 'Failed: ' + err.message; });
    return wrap;
  };

  function renderBadgeCell(col, value) {
    var match = null;
    if (col.badges) {
      for (var i = 0; i < col.badges.length; i++) {
        var b = col.badges[i];
        // Loose equality so JSON false/0/"" all match a Value: false rule.
        if (b.value === value || (b.value === false && !value) || (b.value === true && !!value)) {
          match = b;
          break;
        }
      }
    }
    var label = match ? match.label : (value == null ? '' : String(value));
    var color = match ? (match.color || 'mute') : 'mute';
    var cell = el('div', {class: 'ui-table-cell'});
    if (col.flex) cell.style.flex = col.flex;
    cell.appendChild(el('span', {class: 'ui-badge ' + color}, [label]));
    return cell;
  }

  components.stack = function(cfg, ctx) {
    var wrap = el('div', {class: 'ui-stack'});
    (cfg.items || []).forEach(function(item) { mountComponent(item, wrap, ctx); });
    return wrap;
  };

  components.json_view = function(cfg, ctx) {
    var wrap = el('div', {class: 'ui-jsonview'});
    if (cfg.title) wrap.appendChild(el('div', {class: 'ui-jsonview-h'}, [cfg.title]));
    var pre = el('pre', {class: 'ui-jsonview-body'});
    var raw = ctx ? ctx[cfg.field] : null;
    if (raw == null) {
      pre.textContent = '(no data)';
    } else if (typeof raw === 'string') {
      // Try to parse strings that look like JSON for pretty-printing;
      // fall back to the raw string when that fails.
      try { pre.textContent = JSON.stringify(JSON.parse(raw), null, 2); }
      catch (e) { pre.textContent = raw; }
    } else {
      pre.textContent = JSON.stringify(raw, null, 2);
    }
    wrap.appendChild(pre);
    return wrap;
  };

  components.record_view = function(cfg, ctx) {
    var wrap = el('div', {class: 'ui-display'});
    function render(rec) {
      wrap.innerHTML = '';
      cfg.pairs.forEach(function(p) {
        wrap.appendChild(el('div', {class: 'ui-display-row'}, [
          el('span', {class: 'ui-display-label'}, [p.label]),
          el('span', {class: 'ui-display-value' + (p.mono ? ' mono' : '')}, [fmt(lookup(rec, p.field), p.format)]),
        ]));
      });
    }
    if (cfg.source) {
      fetchJSON(cfg.source).then(render).catch(function(err){ wrap.textContent = 'Failed: ' + err.message; });
    } else {
      render(ctx || {});
    }
    return wrap;
  };

  components.chat_panel = function(cfg) {
    var idF    = cfg.session_id_field     || 'ID';
    var titleF = cfg.session_title_field  || 'Title';
    var lastF  = cfg.session_last_at_field || 'LastAt';
    var msgF   = cfg.session_messages_field || 'Messages';

    var wrap     = el('div', {class: 'ui-chat'});
    var side     = el('div', {class: 'ui-chat-side'});

    // Sidebar collapse — desktop only. Mobile uses the slide-in
    // drawer mechanism and ignores this state. Persisted in
    // localStorage so the operator's preference survives reloads.
    var sideCollapsed = false;
    try { sideCollapsed = localStorage.getItem('chat.sideCollapsed') === '1'; } catch (_) {}
    var collapseBtn = el('button', {
      class: 'ui-tw-collapse', title: 'Hide sessions list',
      onclick: function(){ toggleCollapse(); },
    }, ['‹']);
    function toggleCollapse() {
      sideCollapsed = !sideCollapsed;
      wrap.classList.toggle('side-collapsed', sideCollapsed);
      collapseBtn.title = sideCollapsed ? 'Show sessions list' : 'Hide sessions list';
      collapseBtn.textContent = sideCollapsed ? '›' : '‹';
      try { localStorage.setItem('chat.sideCollapsed', sideCollapsed ? '1' : '0'); } catch (_) {}
    }

    var sideSelectBtn = null;
    if (cfg.bulk_select) {
      sideSelectBtn = el('button', {
        class: 'ui-chat-side-btn',
        title: 'Tap items to select multiple',
        onclick: function() {
          bulkState.mode = !bulkState.mode;
          if (!bulkState.mode) {
            Object.keys(bulkSelected).forEach(function(k){ delete bulkSelected[k]; });
          }
          sideSelectBtn.classList.toggle('active', bulkState.mode);
          sideSelectBtn.textContent = bulkState.mode ? '✓ Selecting' : 'Select';
          loadSessions();
        },
      }, ['Select']);
    }
    var leftExtras = [collapseBtn];
    if (sideSelectBtn) leftExtras.push(sideSelectBtn);
    var sideHdrBuilt = renderSideHeader({
      label: 'Sessions',
      newTitle: 'Start a new session',
      onNew:    function(){ openSession(null); },
      onClose:  function(){ closeDrawer(); },
      leftExtras: leftExtras,
    });
    var sideHdr  = sideHdrBuilt.elt;
    var sideList = el('div', {class: 'ui-chat-side-list'}, ['Loading…']);
    var sideSearch = makeSideSearch(sideList);
    side.appendChild(sideHdr);
    side.appendChild(sideSearch);
    side.appendChild(sideList);

    var main = el('div', {class: 'ui-chat-main'});

    var drawer = makeDrawer(side, {
      title:          'New chat',
      hamburgerTitle: 'Sessions',
      newTitle:       'New session',
      onNew:          function(){ openSession(null); },
    });
    var mobileTitle    = drawer.mobileTitle;
    var drawerBackdrop = drawer.backdrop;
    main.appendChild(drawer.mobileHdr);

    // Mode-toggles row above the thread (Private, Explorer, voice
    // toggles when available). Each pill's state is server-persisted
    // and also rides along on outgoing send bodies so the server
    // honors the active modes.
    // Top bar holds the mode toggles (modesRow) on the left and a
    // helpful "pick a session" hint (emptyHint) pinned to the right.
    // Two children inside one flex container — modesRow's
    // innerHTML='' rebuilds don't touch emptyHint because it's a
    // sibling, not a descendant.
    var modesBar = el('div', {class: 'ui-chat-modesbar'});
    var modesRow = el('div', {class: 'ui-chat-modes'});
    modesBar.appendChild(modesRow);
    main.appendChild(modesBar);

    // Empty-state hint pinned to the right side of the modes bar.
    // Hidden once a session is active or any message lands so it
    // doesn't compete with the toggles for attention mid-conversation.
    var emptyHint = el('div', {class: 'ui-chat-empty-hint'},
      [cfg.empty_text || 'Pick a session from the sidebar or start a new one.']);
    modesBar.appendChild(emptyHint);

    // Tools badge — expandable "N tools" pill that lists every tool
    // the LLM can call. Lazy-loaded on first click; cached for the
    // session so toggling open/closed doesn't re-fetch.
    var toolsBadge = null;
    var toolsPopover = null;
    var toolsLoaded = false;
    var toolsItems = [];
    if (cfg.tools_url) {
      toolsBadge = el('button', {
        class: 'ui-chat-tools-badge',
        title: 'Tools the LLM can use',
        onclick: function() {
          if (toolsPopover.style.display !== 'none') {
            toolsPopover.style.display = 'none';
            return;
          }
          toolsPopover.style.display = '';
          // If we already prefetched the list, just render it; the
          // fetch on init handles the network round-trip so the
          // popover opens instantly.
          if (toolsLoaded) {
            renderToolsPopover();
            return;
          }
          toolsPopover.innerHTML = '<div class="ui-chat-tools-loading">Loading…</div>';
          fetchTools();
        },
      }, ['🔧 Tools']);
      toolsPopover = el('div', {class: 'ui-chat-tools-popover', style: 'display:none'});
      function renderToolsPopover() {
        toolsPopover.innerHTML = '';
        if (!toolsItems.length) {
          toolsPopover.appendChild(el('div', {class: 'ui-chat-tools-loading'}, ['No tools available.']));
          return;
        }
        toolsItems.forEach(function(t) {
          var row = el('div', {class: 'ui-chat-tools-row'});
          row.appendChild(el('div', {class: 'ui-chat-tools-name'}, [t.name || t.Name || '?']));
          var desc = t.desc || t.Desc || t.description || '';
          if (desc) row.appendChild(el('div', {class: 'ui-chat-tools-desc'}, [desc]));
          toolsPopover.appendChild(row);
        });
      }
      function setBadgeLabel(n) {
        toolsBadge.textContent = '🔧 ' + n + ' tool' + (n === 1 ? '' : 's');
      }
      function fetchTools() {
        // Mix the active mode toggles into the URL so server-side
        // filtering reflects them (e.g. ?private=true hides
        // internet-touching tools). Without this the badge and
        // popover would show all tools regardless of the private
        // toggle's state.
        var url = cfg.tools_url;
        var qs = [];
        Object.keys(modeState || {}).forEach(function(k) {
          if (modeState[k]) qs.push(encodeURIComponent(k) + '=true');
        });
        if (qs.length) {
          url += (url.indexOf('?') >= 0 ? '&' : '?') + qs.join('&');
        }
        return fetchJSON(url).then(function(list) {
          toolsLoaded = true;
          toolsItems = Array.isArray(list) ? list : [];
          setBadgeLabel(toolsItems.length);
          if (toolsPopover.style.display !== 'none') renderToolsPopover();
        }).catch(function(err) {
          if (toolsPopover.style.display !== 'none') {
            toolsPopover.innerHTML = '<div class="ui-chat-tools-loading">Load failed: ' + err.message + '</div>';
          }
        });
      }
      // Prefetch so the badge shows the count immediately, no click
      // required. The badge starts as "🔧 Tools" and flips to
      // "🔧 N tools" once the fetch resolves.
      fetchTools();
      var toolsWrap = el('div', {class: 'ui-chat-tools-wrap'}, [toolsBadge, toolsPopover]);
      modesBar.appendChild(toolsWrap);
      // Outside-click dismiss.
      document.addEventListener('click', function(ev) {
        if (toolsPopover.style.display === 'none') return;
        if (!toolsWrap.contains(ev.target)) toolsPopover.style.display = 'none';
      });
    }

    var thread   = el('div', {class: 'ui-chat-thread'});

    // Cumulative session stats line — declared here, appended to the
    // main column between the thread and input area below.
    var statsBar = el('div', {class: 'ui-chat-stats-bar'});
    statsBar.style.display = 'none';

    var inputArea = el('div', {class: 'ui-chat-input-area'});
    var attachInput = null;
    var attachBtn = null;
    if (cfg.attachments) {
      attachInput = el('input', {type: 'file', accept: '.txt,.log,.json,.yaml,.yml,.md,.conf,text/*', style: 'display:none'});
      attachInput.addEventListener('change', handleAttach);
      attachBtn = el('button', {class: 'ui-chat-iconbtn', title: 'Attach a text file', onclick: function(){ attachInput.click(); }}, ['📎']);
      inputArea.appendChild(attachInput);
      inputArea.appendChild(attachBtn);
    }
    var micBtn = null;
    if (cfg.voice) {
      micBtn = el('button', {class: 'ui-chat-iconbtn', title: 'Hold to record', style: 'display:none',
        onmousedown: voiceStartRecord, onmouseup: voiceStopRecord, onmouseleave: voiceStopRecord,
        ontouchstart: voiceStartRecord, ontouchend: voiceStopRecord}, ['🎤']);
      inputArea.appendChild(micBtn);
    }
    var input  = el('textarea', {class: 'ui-chat-input', rows: '1', placeholder: 'Message…'});
    var prefillBtn = null;
    if (cfg.prefill_url) {
      prefillBtn = el('button', {
        class: 'ui-chat-iconbtn',
        title: cfg.prefill_label || 'Suggest',
        onclick: function() {
          var orig = prefillBtn.textContent;
          prefillBtn.textContent = '…';
          prefillBtn.disabled = true;
          // Default GET; apps that need a POST body declare
          // prefill_method + prefill_body so the runtime doesn't
          // need an app-specific wrapper endpoint.
          var fetchOpts = {};
          if ((cfg.prefill_method || 'GET').toUpperCase() === 'POST') {
            fetchOpts.method = 'POST';
            fetchOpts.headers = {'Content-Type': 'application/json'};
            fetchOpts.body = cfg.prefill_body || '{}';
          }
          fetch(cfg.prefill_url, fetchOpts).then(function(r) {
            if (!r.ok) throw new Error('HTTP ' + r.status);
            return r.text();
          }).then(function(text) {
            // Endpoint may return JSON {topic|text|suggestion} or plain text.
            var t = String(text || '').trim();
            try {
              var j = JSON.parse(t);
              if (j && typeof j === 'object') t = String(j.topic || j.text || j.suggestion || j.message || '').trim();
            } catch (_) {}
            if (t) input.value = t;
          }).catch(function(err) {
            showToast('Suggest failed: ' + err.message);
          }).then(function() {
            prefillBtn.textContent = orig;
            prefillBtn.disabled = false;
          });
        },
      }, [cfg.prefill_label || '✨']);
      inputArea.appendChild(prefillBtn);
    }
    var sendBtn = el('button', {class: 'ui-chat-send', onclick: function(){ doSend(); }}, ['Send']);
    var cancelBtn = el('button', {class: 'ui-chat-cancel', onclick: function(){ doCancel(); }}, ['Cancel']);
    cancelBtn.style.display = 'none';
    inputArea.appendChild(input);
    inputArea.appendChild(cancelBtn);
    inputArea.appendChild(sendBtn);

    main.appendChild(thread);
    main.appendChild(statsBar);
    main.appendChild(inputArea);

    // Floating expand-tab pinned to the left edge of main while the
    // sidebar is collapsed — same pattern techwriter / codewriter
    // use. Single tap re-opens the sessions list.
    var expandTab = el('button', {
      class: 'ui-tw-expand', title: 'Show sessions list',
      onclick: function(){ toggleCollapse(); },
    }, ['›']);

    wrap.appendChild(side);
    wrap.appendChild(main);
    wrap.appendChild(expandTab);
    wrap.appendChild(drawerBackdrop);

    // Apply persisted collapse state after the wrap is built.
    if (sideCollapsed) {
      wrap.classList.add('side-collapsed');
      collapseBtn.title = 'Show sessions list';
      collapseBtn.textContent = '›';
    }

    var openDrawer  = drawer.openDrawer;
    var closeDrawer = drawer.closeDrawer;

    // State.
    var currentSessionId = null;
    var history          = [];   // [{role, content}, ...] for /api/send
    var sending          = false;
    var sendController   = null; // AbortController for in-flight SSE
    var modeState        = {};   // bool flags per mode field
    var pendingAttachment = null; // {filename, text}
    // Cumulative session stats — totals across every round in the
    // currently-open session. Reset on session switch / new session.
    var sessionStats = {rounds: 0, in: 0, out: 0, think: 0, ms: 0, cost: 0};
    var voiceTranscribeAvailable = false;
    var voiceSpeakAvailable      = false;
    var autoSpeakEnabled         = false;
    var convoMode                = false;
    var voiceRecorder = null;
    var voiceStream = null;
    var voiceChunks = [];
    var voiceCurrentAudio = null;

    // --- Mode toggles ----------------------------------------------------
    function buildModes() {
      modesRow.innerHTML = '';
      (cfg.modes || []).forEach(function(m) {
        var btn = el('button', {
          class: 'ui-chat-mode',
          title: m.title || m.label,
          onclick: function() { toggleMode(m, btn); },
        }, [m.label]);
        modesRow.appendChild(btn);
        // Initial state from GET URL.
        fetchJSON(m.get_url).then(function(d) {
          modeState[m.send_field || m.field] = !!(d && d[m.field]);
          btn.classList.toggle('active', !!(d && d[m.field]));
        }).catch(function(){});
      });
      // ExtraFields are arbitrary form fields (number, select, text)
      // rendered next to the toggles. Their values ride along on
      // every send body so the server sees them as session params.
      (cfg.extra_fields || []).forEach(function(f) {
        var wrap = el('label', {class: 'ui-chat-field'},
          [el('span', {class: 'ui-chat-field-label'}, [f.label || f.name])]);
        var input;
        if (f.type === 'select') {
          input = el('select', {class: 'ui-chat-field-input'});
          (f.options || []).forEach(function(opt) {
            input.appendChild(el('option', {value: opt}, [opt]));
          });
          if (f.default) input.value = f.default;
        } else if (f.type === 'number') {
          input = el('input', {
            type: 'number', class: 'ui-chat-field-input',
            value: f.default || '',
            min: typeof f.min !== 'undefined' ? String(f.min) : '',
            max: typeof f.max !== 'undefined' ? String(f.max) : '',
          });
        } else {
          input = el('input', {type: 'text', class: 'ui-chat-field-input', value: f.default || ''});
        }
        input.dataset.field = f.name;
        wrap.appendChild(input);
        modesRow.appendChild(wrap);
      });
      // Voice + auto-speak + convo controls (rendered when /voice/status
      // says transcribe/speak are available).
      if (cfg.voice) buildVoiceControls();
    }

    function toggleMode(m, btn) {
      var key = m.send_field || m.field;
      var next = !modeState[key];
      modeState[key] = next;
      btn.classList.toggle('active', next);
      var body = {}; body[m.field] = next;
      fetchJSON(m.post_url, {
        method: 'POST', headers: {'Content-Type': 'application/json'},
        body: JSON.stringify(body),
      }).then(function() {
        // Refresh the tools list — server-side filtering (e.g.
        // private mode hiding internet-touching tools) depends on
        // the active mode flags, so the displayed list needs to
        // re-sync after every toggle.
        if (typeof fetchTools === 'function') fetchTools();
      }).catch(function(err) {
        // Roll back on failure.
        modeState[key] = !next;
        btn.classList.toggle('active', !next);
        showToast('Save failed: ' + err.message);
      });
    }

    // --- Voice -----------------------------------------------------------
    function buildVoiceControls() {
      try { autoSpeakEnabled = localStorage.getItem('chat.autospeak') === '1'; } catch(e) {}
      try { convoMode        = localStorage.getItem('chat.convo')     === '1'; } catch(e) {}
      var autoBtn = el('button', {class: 'ui-chat-mode' + (autoSpeakEnabled ? ' active' : ''),
        style: 'display:none', title: 'Auto-speak assistant replies',
        onclick: function() {
          autoSpeakEnabled = !autoSpeakEnabled;
          try { localStorage.setItem('chat.autospeak', autoSpeakEnabled ? '1' : '0'); } catch(e) {}
          autoBtn.classList.toggle('active', autoSpeakEnabled);
        }}, ['🔊 Auto']);
      var convoBtn = el('button', {class: 'ui-chat-mode' + (convoMode ? ' active' : ''),
        style: 'display:none', title: 'Conversation mode (auto-mic after assistant speaks)',
        onclick: function() {
          convoMode = !convoMode;
          try { localStorage.setItem('chat.convo', convoMode ? '1' : '0'); } catch(e) {}
          convoBtn.classList.toggle('active', convoMode);
          if (convoMode && !autoSpeakEnabled) {
            autoSpeakEnabled = true;
            try { localStorage.setItem('chat.autospeak', '1'); } catch(e) {}
            autoBtn.classList.add('active');
          }
        }}, ['💬 Convo']);
      modesRow.appendChild(autoBtn);
      modesRow.appendChild(convoBtn);
      fetch('/voice/status').then(function(r){ return r.ok ? r.json() : null; }).then(function(d) {
        if (!d) return;
        voiceTranscribeAvailable = d.transcribe_transport && d.transcribe_transport !== 'none';
        voiceSpeakAvailable      = d.speak_transport && d.speak_transport !== 'none';
        if (voiceTranscribeAvailable && micBtn) micBtn.style.display = '';
        if (voiceSpeakAvailable) {
          autoBtn.style.display = '';
          if (voiceTranscribeAvailable) convoBtn.style.display = '';
        }
      }).catch(function(){});
    }

    function voiceStartRecord(ev) {
      if (ev && ev.preventDefault) ev.preventDefault();
      if (!voiceTranscribeAvailable || voiceRecorder) return;
      if (!navigator.mediaDevices || !navigator.mediaDevices.getUserMedia) return;
      micBtn.classList.add('recording');
      navigator.mediaDevices.getUserMedia({audio: true}).then(function(stream) {
        voiceStream = stream;
        voiceChunks = [];
        var rec = new MediaRecorder(stream);
        voiceRecorder = rec;
        rec.ondataavailable = function(e){ if (e.data && e.data.size > 0) voiceChunks.push(e.data); };
        rec.onstop = function() {
          var blob = new Blob(voiceChunks, {type: rec.mimeType || 'audio/webm'});
          if (voiceStream) { voiceStream.getTracks().forEach(function(t){ t.stop(); }); voiceStream = null; }
          voiceTranscribeBlob(blob);
        };
        rec.start();
      }).catch(function(err) {
        micBtn.classList.remove('recording');
        showToast('Mic error: ' + err.message);
      });
    }
    function voiceStopRecord(ev) {
      if (ev && ev.preventDefault) ev.preventDefault();
      if (micBtn) micBtn.classList.remove('recording');
      if (voiceRecorder && voiceRecorder.state === 'recording') voiceRecorder.stop();
      voiceRecorder = null;
    }
    function voiceTranscribeBlob(blob) {
      var fd = new FormData();
      fd.append('audio', blob, 'recording.webm');
      var prev = input.placeholder;
      input.placeholder = 'Transcribing…';
      fetch('/voice/transcribe', {method: 'POST', body: fd}).then(function(r) {
        if (!r.ok) return r.text().then(function(t){ throw new Error(t); });
        return r.json();
      }).then(function(d) {
        input.placeholder = prev;
        if (d && d.text) {
          input.value = (input.value ? input.value + ' ' : '') + d.text;
          input.dispatchEvent(new Event('input'));
          input.focus();
        }
      }).catch(function(err) {
        input.placeholder = prev;
        showToast('Transcribe failed: ' + err.message);
      });
    }

    function speakText(text, btn) {
      if (!voiceSpeakAvailable) return;
      // Build regex strings via concatenation so the Go raw-string
      // literal that wraps this whole runtime doesn't terminate on
      // the embedded markdown backticks.
      var BT = String.fromCharCode(96); // backtick
      var fenceRe = new RegExp(BT + BT + BT + '[\\s\\S]*?' + BT + BT + BT, 'g');
      var inlineRe = new RegExp(BT + '([^' + BT + ']+)' + BT, 'g');
      var spoken = (text || '')
        .replace(fenceRe, ' (code block) ')
        .replace(inlineRe, '$1')
        .replace(/!\[[^\]]*\]\([^\)]*\)/g, '')
        .replace(/\[([^\]]+)\]\([^\)]*\)/g, '$1')
        .replace(/[*_#>]/g, '').trim();
      if (!spoken) return;
      if (voiceCurrentAudio && !voiceCurrentAudio.paused) voiceCurrentAudio.pause();
      if (btn) btn.textContent = '⏳';
      fetch('/voice/speak?text=' + encodeURIComponent(spoken)).then(function(r) {
        if (!r.ok) return r.text().then(function(t){ throw new Error(t); });
        return r.blob();
      }).then(function(blob) {
        var url = URL.createObjectURL(blob);
        var audio = new Audio(url);
        voiceCurrentAudio = audio;
        audio.onended = function() {
          if (btn) btn.textContent = '🔊';
          URL.revokeObjectURL(url);
          voiceCurrentAudio = null;
          if (convoMode && voiceTranscribeAvailable && !sending) {
            setTimeout(function(){ if (convoMode && !sending) voiceStartRecord(null); }, 250);
          }
        };
        audio.onerror = function(){ if (btn) btn.textContent = '🔊'; voiceCurrentAudio = null; };
        return audio.play();
      }).catch(function(err) {
        if (btn) btn.textContent = '🔊';
        showToast('TTS failed: ' + err.message);
      });
    }

    // --- Attachments -----------------------------------------------------
    function handleAttach(ev) {
      var f = ev.target.files && ev.target.files[0];
      if (!f) return;
      if (f.size > 1024 * 1024) { showToast('File too large (1MB max)'); return; }
      var reader = new FileReader();
      reader.onload = function() {
        pendingAttachment = {filename: f.name, text: String(reader.result)};
        if (attachBtn) attachBtn.classList.add('active');
        attachBtn.title = 'Attached: ' + f.name + ' (click to remove)';
        attachBtn.onclick = function() {
          pendingAttachment = null;
          attachBtn.classList.remove('active');
          attachBtn.title = 'Attach a text file';
          attachBtn.onclick = function(){ attachInput.click(); };
        };
      };
      reader.readAsText(f);
      ev.target.value = '';
    }

    // --- Sessions sidebar -------------------------------------------------
    var bulkSelected = {}; // id -> true
    var bulkState    = {mode: false};
    // sessionsByID caches the most recent sidebar fetch keyed by id
    // so renderActions can read per-session fields (HasDescendants
    // etc.) without a second round-trip. Populated by every
    // loadSessions() call; renderActions reads it on demand.
    var sessionsByID = {};
    function loadSessions() {
      fetchJSON(cfg.sessions_list_url).then(function(list) {
        sideList.innerHTML = '';
        sessionsByID = {};
        (list || []).forEach(function(s){ if (s && s[idF]) sessionsByID[s[idF]] = s; });
        if (!list || !list.length) {
          if (cfg.bulk_select && bulkState.mode) {
            renderBulkBar([], sideList, bulkState, bulkSelected,
              function(s){ return s[idF]; }, loadSessions, function(){});
          }
          sideList.appendChild(el('div', {class: 'ui-chat-empty', style: 'padding:0.5rem'}, ['No sessions yet.']));
          return;
        }
        list.sort(function(a, b){ return String(b[lastF] || '').localeCompare(String(a[lastF] || '')); });
        var ids = {}; list.forEach(function(s){ ids[s[idF]] = true; });
        Object.keys(bulkSelected).forEach(function(k){ if (!ids[k]) delete bulkSelected[k]; });

        if (cfg.bulk_select) {
          renderBulkBar(list, sideList, bulkState, bulkSelected,
            function(s){ return s[idF]; },
            loadSessions,
            function() {
              var ids = Object.keys(bulkSelected);
              if (!ids.length) return;
              if (!confirm('Delete ' + ids.length + ' session(s) permanently?')) return;
              Promise.all(ids.map(function(id) {
                var url = cfg.session_delete_url.replace('{id}', encodeURIComponent(id));
                return fetchJSON(url, {method: 'DELETE'}).catch(function(){});
              })).then(function() {
                if (bulkSelected[currentSessionId]) openSession(null);
                bulkSelected = {};
                bulkState.mode = false;
                loadSessions();
              });
            });
        }
        list.forEach(function(s) {
          var inMode = cfg.bulk_select && bulkState.mode;
          var selected = !!bulkSelected[s[idF]];
          var item = el('div', {class:
            'ui-chat-side-item' +
            (s[idF] === currentSessionId ? ' active' : '') +
            (inMode ? ' selectable' : '') +
            (selected ? ' selected' : '')
          }, [
            el('div', {class: 'ui-chat-side-text'}, [
              el('div', {class: 'ui-chat-side-title'}, [s[titleF] || '(untitled)']),
              el('div', {class: 'ui-chat-side-meta'}, [relTime(s[lastF])]),
            ]),
            inMode ? null : el('button', {
              class: 'ui-chat-side-del', title: 'Delete session',
              onclick: function(ev){
                ev.stopPropagation();
                if (!confirm('Delete this session permanently?')) return;
                var url = cfg.session_delete_url.replace('{id}', encodeURIComponent(s[idF]));
                fetchJSON(url, {method: 'DELETE'}).then(function() {
                  if (currentSessionId === s[idF]) openSession(null);
                  loadSessions();
                }).catch(function(err){ showToast('Delete failed: ' + err.message); });
              },
            }, ['×']),
          ]);
          item.addEventListener('click', function() {
            if (inMode) {
              if (bulkSelected[s[idF]]) delete bulkSelected[s[idF]];
              else bulkSelected[s[idF]] = true;
              loadSessions();
            } else {
              openSession(s[idF]);
              closeDrawer();
            }
          });
          sideList.appendChild(item);
        });
      }).catch(function(err){
        sideList.textContent = 'Failed to load: ' + err.message;
      });
    }

    function openSession(id) {
      currentSessionId = id;
      thread.innerHTML = '';
      history = [];
      // Reset cumulative stats — historical rounds aren't summed
      // (the server doesn't replay token counts on session GET).
      sessionStats = {rounds: 0, in: 0, out: 0, think: 0, ms: 0, cost: 0};
      renderStatsBar();
      if (!id) {
        mobileTitle.textContent = 'New chat';
        emptyHint.style.display = '';
        loadSessions();
        return;
      }
      emptyHint.style.display = 'none';
      var url = cfg.session_load_url.replace('{id}', encodeURIComponent(id));
      fetchJSON(url).then(function(s) {
        var msgs = (s && s[msgF]) || [];
        msgs.forEach(function(m) {
          var role = m.role || m.Role;
          var content = m.content || m.Content;
          var msgEl = appendMessage(role, content);
          history.push({role: role, content: content});
          // Loaded messages are final — render markdown immediately
          // instead of leaving them as plain text. (Streaming chunks
          // can't do this because incomplete markdown corrupts the
          // render; session loads have the full text already.)
          if (role === 'assistant' && msgEl) {
            var body = msgEl.querySelector('.ui-chat-msg-body');
            if (body) renderMessageBody(body, content);
          }
        });
        // Update the mobile header's title to the session's title (or
        // "New chat" when blank). Desktop ignores it via CSS.
        if (s && s[titleF]) mobileTitle.textContent = s[titleF];
        else mobileTitle.textContent = 'Untitled';
        scrollToBottom();
        loadSessions();
      }).catch(function(err){
        thread.appendChild(el('div', {class: 'ui-chat-error'}, ['Failed to load: ' + err.message]));
      });
    }

    // --- Thread rendering -------------------------------------------------
    function appendMessage(role, content) {
      // Any message in the thread means the user is no longer at the
      // empty state — hide the "pick a session" hint.
      if (emptyHint) emptyHint.style.display = 'none';
      var msg = el('div', {class: 'ui-chat-msg ' + (role === 'assistant' ? 'assistant' : 'user')});
      var body = el('div', {class: 'ui-chat-msg-body'});
      body.textContent = content || '';
      msg.appendChild(body);
      msg.dataset.role = role;
      msg.dataset.raw  = content || '';
      thread.appendChild(msg);
      // User messages get Edit + Retry actions once they're committed.
      // Skip during streaming (the assistant placeholder uses
      // appendMessage too but for role="assistant" — it gets its
      // actions later in the done handler).
      if (role === 'user' && content) addUserActions(msg);
      scrollToBottom();
      return msg;
    }

    // Edit + Retry on the LAST user message in the thread. Older user
    // messages are read-only — editing mid-history would require
    // re-running every assistant turn after it, which is closer to
    // "fork session" than "edit". Retry on a non-final user message
    // would also need to drop everything after it, which the Retry
    // helper already does.
    function addUserActions(msgEl) {
      var bar = el('div', {class: 'ui-chat-actions'});
      bar.appendChild(el('button', {class: 'ui-chat-act', onclick: function() {
        editUserMessage(msgEl);
      }}, ['Edit']));
      bar.appendChild(el('button', {class: 'ui-chat-act', title: 'Re-send this message and drop the response below', onclick: function() {
        retryUserMessage(msgEl);
      }}, ['Retry']));
      msgEl.appendChild(bar);
      // After appending a user msg with actions, prune actions from
      // any earlier user messages — only the most recent one should
      // be editable. Older ones become read-only.
      var msgs = thread.querySelectorAll('.ui-chat-msg.user');
      for (var i = 0; i < msgs.length - 1; i++) {
        var older = msgs[i].querySelector('.ui-chat-actions');
        if (older) older.remove();
      }
    }

    function editUserMessage(msgEl) {
      var raw = msgEl.dataset.raw || msgEl.querySelector('.ui-chat-msg-body').textContent;
      var body = msgEl.querySelector('.ui-chat-msg-body');
      var actions = msgEl.querySelector('.ui-chat-actions');
      // Replace body with textarea + Save/Cancel.
      body.style.display = 'none';
      if (actions) actions.style.display = 'none';
      var ta = el('textarea', {class: 'ui-chat-edit-ta', rows: '3'});
      ta.value = raw;
      var save = el('button', {class: 'ui-chat-act', onclick: function() {
        var newText = ta.value.trim();
        if (!newText) return;
        // Replace the message text in DOM + history, then drop every
        // turn after this one and re-send. The send will append a
        // fresh assistant placeholder.
        msgEl.dataset.raw = newText;
        body.textContent = newText;
        truncateAfter(msgEl, /*inclusive*/ true);
        // history: drop entries from this user message onward.
        for (var i = history.length - 1; i >= 0; i--) {
          if (history[i].role === 'user' && history[i].content === raw) {
            history.length = i;
            break;
          }
        }
        input.value = newText;
        doSend();
      }}, ['Save & resend']);
      var cancel = el('button', {class: 'ui-chat-act', onclick: function() {
        editBar.remove();
        body.style.display = '';
        if (actions) actions.style.display = '';
      }}, ['Cancel']);
      var editBar = el('div', {class: 'ui-chat-edit-bar'}, [ta, el('div', {class: 'ui-chat-edit-actions'}, [save, cancel])]);
      msgEl.appendChild(editBar);
      ta.focus();
      ta.setSelectionRange(ta.value.length, ta.value.length);
    }

    function retryUserMessage(msgEl) {
      var raw = msgEl.dataset.raw || msgEl.querySelector('.ui-chat-msg-body').textContent;
      truncateAfter(msgEl, /*inclusive*/ true);
      for (var i = history.length - 1; i >= 0; i--) {
        if (history[i].role === 'user' && history[i].content === raw) {
          history.length = i;
          break;
        }
      }
      input.value = raw;
      doSend();
    }

    // Remove every DOM sibling after msgEl. Inclusive removes msgEl
    // itself too. Used by edit/retry to wipe the current turn before
    // re-running.
    function truncateAfter(msgEl, inclusive) {
      var n = inclusive ? msgEl : msgEl.nextElementSibling;
      while (n) {
        var next = n.nextElementSibling;
        if (n.parentNode === thread) n.remove();
        n = next;
      }
    }
    // Tool calls don't render inline anymore — instead the runtime
    // attaches an expandable "🔧 N tools" button to the assistant
    // bubble that fired them. Click to reveal a panel with every
    // tool call + result for that round. The bubble-attaching work
    // happens inline in the SSE switch (case 'tool_call'/'tool_result')
    // because it needs assistantMsg, which is var-declared inside
    // doSend's closure. The helpers below only manipulate detached
    // pending arrays + render the panel from a tools[] reference
    // already attached to a message element.
    function renderToolPanel(panel) {
      var tools = panel.parentNode && panel.parentNode.tools || [];
      panel.innerHTML = '';
      tools.forEach(function(t) {
        var summaryChildren = [el('span', {class: 'ui-chat-tool-name'}, ['→ ' + t.name])];
        if (t.args) summaryChildren.push(el('span', {class: 'ui-chat-tool-args'}, [t.args]));
        var summary = el('summary', {class: 'ui-chat-tool-summary'}, summaryChildren);
        var det = el('details', {class: 'ui-chat-tool'});
        det.appendChild(summary);
        var body = el('div', {class: 'ui-chat-tool-body'});
        var trimmed = String(t.output || '').trim();
        if (t.output === null) {
          body.appendChild(el('div', {class: 'ui-chat-tool-empty'}, ['(running…)']));
        } else if (!trimmed) {
          body.appendChild(el('div', {class: 'ui-chat-tool-empty'}, ['(no output)']));
        } else {
          var pre = el('pre', {class: 'ui-chat-tool-result'});
          pre.textContent = t.output;
          body.appendChild(pre);
        }
        det.appendChild(body);
        panel.appendChild(det);
      });
    }
    // attachToolToggle creates the toggle+panel button on a given
    // assistant message element and points it at that bubble's
    // tools[] array. Called from inside the SSE switch where the
    // bubble is in scope. Idempotent — reuses the existing toggle
    // when called again to refresh the count.
    function attachToolToggle(msgEl) {
      if (!msgEl || !msgEl.tools) return;
      var count = msgEl.tools.length;
      var label = '🔧 ' + count + ' tool' + (count === 1 ? '' : 's');
      var toggle = msgEl.querySelector(':scope > .ui-chat-tools-toggle');
      var panel  = msgEl.querySelector(':scope > .ui-chat-tools-panel');
      if (toggle) {
        toggle.textContent = label;
        // Re-render an open panel so newly-arrived tool_result data
        // replaces the "(running…)" placeholders for finished tools.
        if (panel && panel.style.display !== 'none') renderToolPanel(panel);
        return;
      }
      panel = el('div', {class: 'ui-chat-tools-panel', style: 'display:none'});
      toggle = el('button', {
        class: 'ui-chat-tools-toggle',
        onclick: function() {
          var open = panel.style.display !== 'none';
          panel.style.display = open ? 'none' : '';
          toggle.classList.toggle('open', !open);
          if (!open) renderToolPanel(panel);
        },
      }, [label]);
      msgEl.appendChild(toggle);
      msgEl.appendChild(panel);
    }
    function appendError(msg) {
      thread.appendChild(el('div', {class: 'ui-chat-error'}, [msg]));
      scrollToBottom();
    }

    // Inject / remove a three-dot typing indicator in an assistant
    // bubble's body. Used while the LLM is thinking + prefilling
    // before the first chunk arrives, so the empty bubble doesn't
    // sit there looking dead. Cleared by clearTyping(body) on the
    // first chunk OR by direct replacement when content lands.
    function showTyping(body) {
      if (!body || body.querySelector('.ui-chat-typing')) return;
      body.innerHTML = '<span class="ui-chat-typing" aria-label="Thinking"><span></span><span></span><span></span></span>';
    }
    function clearTyping(body) {
      if (!body) return;
      var t = body.querySelector('.ui-chat-typing');
      if (t) body.innerHTML = '';
    }
    function scrollToBottom() {
      // Defer to the next frame so layout has settled — scrollTop set
      // synchronously right after appendChild lands at the OLD
      // scrollHeight, before the browser has measured the new node.
      // Run twice (rAF + 100ms timeout) to also catch the case where a
      // markdown re-render or <details> expansion changes height after
      // the first frame.
      var pin = function() { thread.scrollTop = thread.scrollHeight; };
      pin();
      requestAnimationFrame(pin);
      setTimeout(pin, 100);
    }

    // Per-round stats footer, attached to the assistant bubble that
    // just finished. Mirrors legacy chat's appendStatsFooter.
    function renderRoundStats(msgEl, stats) {
      if (!msgEl || !stats) return;
      if (!stats.output_tokens && !stats.input_tokens && !stats.elapsed_ms) return;
      var parts = [];
      if (stats.tokens_per_sec) parts.push(stats.tokens_per_sec.toFixed(1) + ' tk/s');
      if (stats.prompt_per_sec) parts.push(Math.round(stats.prompt_per_sec) + ' prefill');
      if (stats.elapsed_ms)     parts.push((stats.elapsed_ms / 1000).toFixed(1) + 's');
      if (stats.input_tokens)   parts.push(stats.input_tokens.toLocaleString() + ' in');
      if (stats.output_tokens)  parts.push(stats.output_tokens.toLocaleString() + ' out');
      if (stats.reasoning_tokens) parts.push(stats.reasoning_tokens.toLocaleString() + ' think');
      if (stats.est_cost && stats.est_cost > 0) parts.push('$' + Number(stats.est_cost).toFixed(4));
      if (!parts.length) return;
      var bar = el('div', {class: 'ui-chat-round-stats'}, [parts.join(' · ')]);
      msgEl.appendChild(bar);
    }

    // Cumulative session totals, redrawn after every round so the
    // operator sees running spend without visiting the admin cost page.
    function accumulateStats(stats) {
      if (!stats) return;
      sessionStats.rounds += 1;
      sessionStats.in    += Number(stats.input_tokens || 0);
      sessionStats.out   += Number(stats.output_tokens || 0);
      sessionStats.think += Number(stats.reasoning_tokens || 0);
      sessionStats.ms    += Number(stats.elapsed_ms || 0);
      sessionStats.cost  += Number(stats.est_cost || 0);
      renderStatsBar();
    }
    function renderStatsBar() {
      if (sessionStats.rounds === 0) {
        statsBar.style.display = 'none';
        return;
      }
      var parts = [];
      parts.push(sessionStats.rounds + (sessionStats.rounds === 1 ? ' round' : ' rounds'));
      if (sessionStats.in)    parts.push(sessionStats.in.toLocaleString()    + ' in');
      if (sessionStats.out)   parts.push(sessionStats.out.toLocaleString()   + ' out');
      if (sessionStats.think) parts.push(sessionStats.think.toLocaleString() + ' think');
      if (sessionStats.ms)    parts.push((sessionStats.ms / 1000).toFixed(1) + 's');
      if (sessionStats.cost > 0) parts.push('$' + sessionStats.cost.toFixed(4));
      statsBar.textContent = 'session: ' + parts.join(' · ');
      statsBar.style.display = '';
    }

    // --- Send + SSE handling ---------------------------------------------
    function doSend() {
      if (sending) return;
      var text = input.value.trim();
      if (!text) return;
      input.value = '';
      autoresize();
      sending = true;
      sendBtn.disabled = true;
      cancelBtn.style.display = '';

      // Append user msg + assistant placeholder immediately.
      appendMessage('user', text);
      history.push({role: 'user', content: text});
      var assistantMsg = appendMessage('assistant', '');
      var assistantBody = assistantMsg.querySelector('.ui-chat-msg-body');
      // Show a typing indicator until the first chunk arrives. This is
      // the thinking-then-prefill window where the user is otherwise
      // staring at an empty bubble. The indicator gets replaced in
      // place when the first chunk (or tool_call event) lands.
      showTyping(assistantBody);
      var fullReply = '';

      sendController = new AbortController();
      // Build the send body — base fields plus active mode flags
      // (private_mode etc) plus an attached-file fence if pending.
      var bodyMessage = text;
      if (pendingAttachment) {
        var BT = String.fromCharCode(96);
        var fence = BT + BT + BT;
        bodyMessage = text + '\n\n' +
          fence + '\n# attached: ' + pendingAttachment.filename + '\n' +
          pendingAttachment.text + '\n' + fence;
        pendingAttachment = null;
        if (attachBtn) {
          attachBtn.classList.remove('active');
          attachBtn.title = 'Attach a text file';
          attachBtn.onclick = function(){ attachInput.click(); };
        }
      }
      var sendBody = {session_id: currentSessionId, history: history.slice(0, -1), message: bodyMessage};
      Object.keys(modeState).forEach(function(k){ sendBody[k] = modeState[k]; });
      // Snapshot ExtraFields values into the send body. Numbers
      // get coerced; selects/text stay strings. Empty values are
      // dropped so the server sees absent rather than "".
      modesRow.querySelectorAll('[data-field]').forEach(function(inp) {
        var name = inp.dataset.field;
        if (!name) return;
        var v = inp.value;
        if (v === '' || v == null) return;
        if (inp.type === 'number') {
          var n = Number(v);
          if (!isNaN(n)) v = n;
        }
        sendBody[name] = v;
      });

      fetch(cfg.send_url, {
        method: 'POST',
        headers: {'Content-Type': 'application/json'},
        signal: sendController.signal,
        body: JSON.stringify(sendBody),
      }).then(function(r) {
        if (!r.ok) return r.text().then(function(t){ throw new Error(t || ('HTTP ' + r.status)); });
        var reader = r.body.getReader();
        var decoder = new TextDecoder();
        var buffer = '';
        function pump() {
          return reader.read().then(function(out) {
            if (out.done) { finish(); return; }
            buffer += decoder.decode(out.value, {stream: true});
            // Parse SSE: each event terminated by a blank line.
            var i;
            while ((i = buffer.indexOf('\n\n')) >= 0) {
              var raw = buffer.slice(0, i);
              buffer = buffer.slice(i + 2);
              processEvent(raw);
            }
            return pump();
          });
        }
        return pump();
      }).catch(function(err) {
        if (err.name === 'AbortError') {
          appendError('Cancelled.');
        } else {
          appendError('Error: ' + err.message);
        }
        finish();
      });

      function processEvent(raw) {
        var lines = raw.split('\n');
        var ev = '', dataStr = '';
        for (var li = 0; li < lines.length; li++) {
          var l = lines[li];
          if (l.indexOf('event:') === 0) ev = l.slice(6).trim();
          else if (l.indexOf('data:') === 0) dataStr += l.slice(5).trim();
        }
        if (!ev) return;
        var data = {};
        if (dataStr) { try { data = JSON.parse(dataStr); } catch(e) {} }
        switch (ev) {
          case 'chunk':
            // If the previous round was tool-only, its placeholder
            // got dropped on the prior done event. Recreate one at
            // the bottom of the thread so this round's text lands
            // BELOW any tool pills/results — not above them where
            // the original placeholder used to sit.
            if (!assistantMsg || !assistantMsg.parentNode) {
              assistantMsg = appendMessage('assistant', '');
              assistantBody = assistantMsg.querySelector('.ui-chat-msg-body');
              showTyping(assistantBody);
            }
            // First chunk replaces the typing indicator. Subsequent
            // chunks just append to the running reply text.
            if (fullReply === '') clearTyping(assistantBody);
            fullReply += data.text || '';
            assistantBody.textContent = fullReply;
            scrollToBottom();
            break;
          case 'thinking_chunk':
            // Stage 1: ignore. Stage 2 will surface in a collapsible block.
            break;
          case 'tool_call':
            // Tool calls can fire BEFORE the first chunk. Mint a host
            // assistant bubble on demand so the toggle has a parent.
            if (!assistantMsg || !assistantMsg.parentNode) {
              assistantMsg = appendMessage('assistant', '');
              assistantBody = assistantMsg.querySelector('.ui-chat-msg-body');
              showTyping(assistantBody);
            }
            if (!assistantMsg.tools) assistantMsg.tools = [];
            assistantMsg.tools.push({
              name: data.name || 'tool',
              args: data.args || '',
              output: null,
            });
            attachToolToggle(assistantMsg);
            // Tool toggle changes layout — keep the bottom visible
            // so the user can see the latest activity in flight.
            scrollToBottom();
            break;
          case 'tool_result':
            // Match against the last entry without an output — tools
            // run sequentially within a round, so last-unmatched is
            // the right pair. Chat emits the body under "result";
            // other apps may use "output". Accept either.
            if (assistantMsg && assistantMsg.tools) {
              var resultText = data.result;
              if (resultText == null) resultText = data.output;
              for (var ti = assistantMsg.tools.length - 1; ti >= 0; ti--) {
                if (assistantMsg.tools[ti].output === null) {
                  assistantMsg.tools[ti].output = String(resultText || '');
                  break;
                }
              }
              attachToolToggle(assistantMsg); // refresh count + label
              scrollToBottom();
            }
            break;
          case 'session':
            if (data.id) currentSessionId = data.id;
            break;
          case 'status':
            // Light status line (e.g. "Investigating…"). Append as a
            // mute system-style line.
            thread.appendChild(el('div', {class: 'ui-chat-status'}, [data.text || '']));
            scrollToBottom();
            break;
          case 'error':
            appendError(data.message || 'Error');
            break;
          case 'done':
            // Stats arrive even on tool-only rounds where the LLM
            // produced no visible content. Always update cumulative
            // totals; only attach the per-round footer when there's
            // actually a finalized message.
            accumulateStats(data);
            if (fullReply.trim()) {
              history.push({role: 'assistant', content: fullReply});
              assistantMsg.dataset.raw = fullReply;
              renderMessageBody(assistantBody, fullReply);
              renderRoundStats(assistantMsg, data);
              addAssistantActions(assistantMsg);
              // Jump to the TOP of the just-finalized assistant
              // message so the user can read from the start instead
              // of landing at the bottom (where the streaming had
              // pinned the scroll). Use rect deltas because the
              // thread is the scroll container, not the document.
              // Defer past finalize's own scrollTranscript pins.
              var msgEl = assistantMsg;
              var jumpToTop = function() {
                if (!msgEl || !msgEl.parentNode) return;
                var mRect = msgEl.getBoundingClientRect();
                var tRect = thread.getBoundingClientRect();
                thread.scrollTop += (mRect.top - tRect.top);
              };
              setTimeout(jumpToTop, 50);
              setTimeout(jumpToTop, 200);
              fullReply = '';
              assistantMsg = null;
              assistantBody = null;
            } else if (assistantMsg && assistantBody && assistantBody.textContent === '' && (!assistantMsg.tools || !assistantMsg.tools.length)) {
              // Tool-only round with no tool toggle either — drop the
              // empty placeholder so the user doesn't see a blank
              // assistant bubble between rounds. Keep it if tools fired
              // (rare: tool_call without a paired result yet) so the
              // toggle stays attached to its bubble.
              assistantMsg.remove();
              assistantMsg = null;
              assistantBody = null;
            } else if (assistantMsg && assistantBody) {
              // Tool-only round but the bubble HAS a tools toggle —
              // clear the typing indicator so the bubble shows just
              // the toggle and any later text from the next round
              // continues into a fresh placeholder.
              clearTyping(assistantBody);
              assistantMsg = null;
              assistantBody = null;
            }
            break;
        }
      }

      function finish() {
        // Final cleanup. The done-handler now drops empty placeholders
        // proactively, so by the time we get here assistantMsg should
        // either be null or have real content. Belt-and-suspenders:
        // remove anything still empty, finalize anything with text.
        if (assistantMsg && assistantBody) {
          if (assistantBody.textContent === '' && fullReply === '') {
            assistantMsg.remove();
          } else if (fullReply.trim() && !assistantMsg.dataset.raw) {
            history.push({role: 'assistant', content: fullReply});
            assistantMsg.dataset.raw = fullReply;
            renderMessageBody(assistantBody, fullReply);
            addAssistantActions(assistantMsg);
          }
        }
        sending = false;
        sendBtn.disabled = false;
        cancelBtn.style.display = 'none';
        sendController = null;
        loadSessions();

        // Auto-speak on completion if enabled.
        if (autoSpeakEnabled && voiceSpeakAvailable) {
          var lastReply = history.length > 0 && history[history.length-1].role === 'assistant'
            ? history[history.length-1].content : '';
          if (lastReply) speakText(lastReply, null);
        }
      }
    }

    function doCancel() {
      if (sendController) sendController.abort();
    }

    function addAssistantActions(msgEl) {
      var bar = el('div', {class: 'ui-chat-actions'});
      bar.appendChild(el('button', {class: 'ui-chat-act', onclick: function() {
        var t = msgEl.dataset.raw || '';
        if (navigator.clipboard) navigator.clipboard.writeText(t);
        showToast('Copied');
      }}, ['Copy']));
      bar.appendChild(el('button', {class: 'ui-chat-act', title: 'Drop this reply and re-run from the prior user message', onclick: function() {
        retryFromMessage(msgEl);
      }}, ['Retry']));
      if (cfg.voice && voiceSpeakAvailable) {
        var speakBtn = el('button', {class: 'ui-chat-act', onclick: function() {
          speakText(msgEl.dataset.raw || '', speakBtn);
        }}, ['🔊']);
        bar.appendChild(speakBtn);
      }
      msgEl.appendChild(bar);
    }

    // Walk back from msgEl to the most recent user message, drop everything
    // after it, then re-send that user text. Useful when an assistant reply
    // missed the mark and you want a fresh try.
    function retryFromMessage(msgEl) {
      // Find the user message immediately before this assistant turn.
      var prev = msgEl.previousElementSibling;
      while (prev && !(prev.classList && prev.classList.contains('ui-chat-msg') && prev.dataset.role === 'user')) {
        prev = prev.previousElementSibling;
      }
      if (!prev) { showToast('No prior user message to retry'); return; }
      var userText = prev.dataset.raw || prev.querySelector('.ui-chat-msg-body').textContent;
      // Remove everything from prev (inclusive) onward in the DOM.
      var n = prev;
      while (n) { var next = n.nextElementSibling; n.remove(); n = next; }
      // Trim history back to before that user turn.
      while (history.length > 0 && !(history[history.length-1].role === 'user' && history[history.length-1].content === userText)) {
        history.pop();
      }
      if (history.length > 0) history.pop(); // drop the matching user entry — doSend will re-add
      input.value = userText;
      doSend();
    }

    // Minimal markdown renderer for completed assistant messages.
    // Streaming chunks render as plain text (textContent) until done,
    // then this re-renders the bubble with formatting. Handles
    // headings, code fences, inline code, bold, italic, links, lists.
    // mdToHTML is a top-level helper shared with pipeline_panel.
    function renderMessageBody(target, raw) {
      if (!cfg.markdown) { target.textContent = raw; return; }
      target.innerHTML = mdToHTML(raw);
    }

    // --- Input ergonomics -------------------------------------------------
    function autoresize() {
      input.style.height = 'auto';
      input.style.height = Math.min(input.scrollHeight, 200) + 'px';
    }
    input.addEventListener('input', autoresize);
    input.addEventListener('keydown', function(ev) {
      if (ev.key === 'Enter' && !ev.shiftKey) {
        ev.preventDefault();
        doSend();
      }
    });

    buildModes();
    loadSessions();
    return wrap;
  };

  // Agent-loop panel — sessions sidebar (left), conversation
  // pane (center), live activity pane (right). The conversation
  // and activity panes are resizable. Built for any app that
  // drives an LLM agent through multi-turn tool use with
  // operator-in-the-loop confirmation. See AgentLoopPanel doc
  // comment in components.go for the SSE protocol.
  components.agent_loop_panel = function(cfg) {
    var idF   = cfg.id_field       || 'ID';
    var ttlF  = cfg.title_field    || 'Title';
    var atF   = cfg.date_field     || 'LastAt';
    var msgsF = cfg.messages_field || 'Messages';

    // The left rail is opt-in. Apps that don't supply list/load/
    // delete URLs get a single-column panel (no sidebar).
    var hasList = !!(cfg.list_url && cfg.load_url && cfg.delete_url);

    var activeSessionId = '';
    // activeContextId — used in CONTEXT mode for the left-rail's
    // active record (workspace, project, etc.). Distinct from
    // activeSessionId, which still tracks the server-issued chat
    // session for cancel/confirm routing.
    var activeContextId = '';
    var msgEls = {};      // message id -> {bubble, body, role, rawText}
    var activityEls = {}; // activity id -> element
    var blockEls = {};    // app-block id -> {wrap, body}
    var pendingAttachments = []; // {name, dataURL} for next send
    var activeStream = null;     // AbortController for in-flight send

    var wrap = el('div', {class: 'ui-agent' + (hasList ? '' : ' ui-agent-no-list')});

    // --- Optional list sidebar -------------------------------------------
    var side = null, sideList = null, sideSearch = null, drawer = null;
    function closeDrawer() {
      if (!side) return;
      side.classList.remove('open');
      if (drawer) drawer.backdrop.classList.remove('show');
    }
    if (hasList) {
      side = el('div', {class: 'ui-chat-side'});
      // Collapse button — desktop. Hamburger icon sits next to
      // the New button. Mobile uses the drawer mechanism (×).
      var collapseBtn = el('button', {
        class: 'ui-agent-collapse',
        title: 'Hide ' + (cfg.list_title || 'list'),
        onclick: function(){ toggleSideCollapse(); },
      }, ['☰']);
      var sideHdrBuilt = renderSideHeader({
        label:    cfg.list_title || 'Sessions',
        className: 'ui-chat-side-h',
        newTitle: cfg.new_label || 'New',
        onNew:    function(){ openSession(null); },
        onClose:  function(){ closeDrawer(); },
        // Hamburger inserted BEFORE the New button (left of it)
        // via leftExtras — matches the user's "next to new" ask.
        leftExtras: [collapseBtn],
      });
      sideList = el('div', {class: 'ui-chat-side-list'}, ['Loading…']);
      sideSearch = makeSideSearch(sideList);
      side.appendChild(sideHdrBuilt.elt);
      side.appendChild(sideSearch);
      side.appendChild(sideList);

      drawer = makeDrawer(side, {
        title:          cfg.new_label || 'New',
        hamburgerTitle: cfg.list_title || 'Sessions',
        newTitle:       cfg.new_label || 'New',
        onNew:          function(){ openSession(null); },
      });
    }

    // --- Main: conversation + activity panes ------------------------------
    // .ui-agent is a flex column: topbar (status + actions) above a
    // grid row that holds the side rail and the main column.
    var topbar = el('div', {class: 'ui-agent-topbar'});
    var gridRow = el('div', {class: 'ui-agent-grid'});

    var main = el('div', {class: 'ui-agent-main'});
    if (drawer) main.appendChild(drawer.mobileHdr);

    // Floating expand-tab shown when the left rail is collapsed on
    // desktop. Sits pinned against the conversation pane's left edge
    // so the user can always pull the list back. Hamburger icon for
    // symmetry with the in-rail collapse button.
    var expandTab = null;
    if (hasList) {
      expandTab = el('button', {
        class: 'ui-agent-expand', title: 'Show ' + (cfg.list_title || 'list'),
        onclick: function(){ toggleSideCollapse(); },
      }, ['☰']);
    }
    // Side collapse state — desktop only. Persisted in localStorage
    // so the user's preference sticks across reloads. Default is
    // collapsed (rail hidden) because the conversation is the
    // primary surface in most flows.
    var sideCollapsed = true;
    try {
      var stored = localStorage.getItem('agent.sideCollapsed');
      if (stored === '0') sideCollapsed = false;
    } catch (_) {}
    function applySideCollapse() {
      if (!hasList) return;
      wrap.classList.toggle('side-collapsed', sideCollapsed);
      try { localStorage.setItem('agent.sideCollapsed', sideCollapsed ? '1' : '0'); } catch (_) {}
    }
    function toggleSideCollapse() {
      sideCollapsed = !sideCollapsed;
      applySideCollapse();
    }
    applySideCollapse();

    var statusBar = el('div', {class: 'ui-agent-status', style: 'display:none'});
    topbar.appendChild(statusBar);

    var actionsBar = el('div', {class: 'ui-agent-actions'});
    (cfg.actions || []).forEach(function(action) {
      var classes = 'ui-row-btn';
      if (action.variant) classes += ' ' + action.variant;
      var btn = el('button', {class: classes, title: action.title || ''},
        [action.label || '(action)']);
      btn.addEventListener('click', function() {
        if (action.confirm && !confirm(action.confirm)) return;
        var method = action.method || 'post';
        if (method === 'client') {
          var name = action.url || '';
          var fn = window.UIClientActions && window.UIClientActions[name];
          if (typeof fn === 'function') {
            fn({
              sessionId: activeSessionId,
              button:    btn,
              action:    action,
              // clearConvo() / clearActivity() — wipe a pane. Used
              // by app-defined Clear actions that mirror the
              // legacy chat-header Clear button.
              clearConvo: function() {
                msgEls = {}; blockEls = {};
                convoLog.innerHTML = '';
                emptyMsg = el('div', {class: 'ui-agent-empty'},
                  [cfg.empty_text || 'Start typing below.']);
                convoLog.appendChild(emptyMsg);
              },
              clearActivity: function() {
                activityEls = {};
                activityLog.innerHTML = '';
              },
              // subscribe(url) — wire an EventSource to url and
              // pipe its events through the panel's handleEvent.
              // Lets client actions tap a server-side job (map,
              // synthesize, …) that emits events into the same
              // queue the chat uses, so progress shows up in the
              // activity pane.
              //
              // CRITICAL: register the EventSource as
              // activeEventSource so cancelMessage() and the
              // 'done' event handler close it cleanly. Without
              // this the browser auto-reconnects on stream end,
              // and the server's snapshot-then-stream translator
              // replays every buffered event each time — the
              // user sees the last output repeated forever after
              // cancel.
              subscribe: function(url) {
                if (activeEventSource) {
                  activeEventSource.close();
                  activeEventSource = null;
                }
                var es = new EventSource(url);
                activeEventSource = es;
                es.onmessage = function(ev) {
                  try { handleEvent(JSON.parse(ev.data)); } catch (_) {}
                };
                es.onerror = function() {
                  if (es.readyState === EventSource.CLOSED) {
                    if (activeEventSource === es) activeEventSource = null;
                    enableInput();
                  }
                };
                disableInput();
                return es;
              },
            });
          } else {
            showToast('No handler for client action: ' + name);
          }
          return;
        }
        var url = (action.url || '').replace('{id}',
          encodeURIComponent(activeSessionId || ''));
        if (method === 'open')          { window.open(url, '_blank', 'noopener'); }
        else if (method === 'redirect') { window.location.href = url; }
        else {
          fetchJSON(url, {method: 'POST'}).catch(function(err) {
            showToast('Failed: ' + (err && err.message || err));
          });
        }
      });
      actionsBar.appendChild(btn);
    });
    if ((cfg.actions || []).length === 0) actionsBar.style.display = 'none';
    topbar.appendChild(actionsBar);
    // ExtraFields strip lives in the topbar so context selectors
    // (active appliance, project, …) sit alongside the toolbar
    // buttons. The strip itself is built further below; we just
    // reserve its DOM slot here so it lands ABOVE the side rail
    // and main column. The actual <input>s get attached later
    // when we walk cfg.extra_fields.
    var extrasSlot = el('div', {class: 'ui-agent-extras-slot'});
    topbar.appendChild(extrasSlot);

    var split = el('div', {class: 'ui-agent-split'});
    var convoPane = el('div', {class: 'ui-agent-convo'});
    var convoLog  = el('div', {class: 'ui-agent-convo-log'});
    convoPane.appendChild(convoLog);
    var emptyMsg = el('div', {class: 'ui-agent-empty'},
      [cfg.empty_text || 'Start typing below.']);
    convoLog.appendChild(emptyMsg);
    var divider  = el('div', {class: 'ui-agent-divider', title: 'Drag to resize'});

    // Right pane — when cfg.terminal is set, this column splits
    // vertically into activity (top) + terminal (bottom). Otherwise
    // activity fills the column on its own.
    var rightPane = el('div', {class: 'ui-agent-right'});
    var activityPane = el('div', {class: 'ui-agent-activity'});
    if (cfg.hide_activity) activityPane.classList.add('collapsed');
    var activityHdr = el('div', {class: 'ui-agent-activity-h'},
      [el('span', {text: 'Activity'})]);
    var activityLog = el('div', {class: 'ui-agent-activity-log'},
      [el('div', {class: 'ui-agent-act ui-agent-act-status'},
        ['Tool calls and outputs appear here.'])]);
    activityPane.appendChild(activityHdr);
    activityPane.appendChild(activityLog);
    rightPane.appendChild(activityPane);

    var terminalPane = null;
    if (cfg.terminal && cfg.terminal.url) {
      var hDivider = el('div', {class: 'ui-agent-hdivider', title: 'Drag to resize'});
      terminalPane = el('div', {class: 'ui-agent-terminal'});
      var termHdr = el('div', {class: 'ui-agent-terminal-h'},
        [el('span', {text: cfg.terminal.title || 'Terminal'})]);
      var termBody = el('div', {class: 'ui-agent-terminal-body'},
        [el('div', {class: 'ui-agent-terminal-placeholder'},
          ['(terminal pane — xterm.js wiring deferred)'])]);
      terminalPane.appendChild(termHdr);
      terminalPane.appendChild(termBody);
      rightPane.appendChild(hDivider);
      rightPane.appendChild(terminalPane);

      var hResizing = false, hStartY = 0, hStartH = 0;
      hDivider.addEventListener('mousedown', function(ev) {
        hResizing = true; hStartY = ev.clientY;
        hStartH = activityPane.getBoundingClientRect().height;
        document.body.style.cursor = 'row-resize';
        ev.preventDefault();
      });
      document.addEventListener('mousemove', function(ev) {
        if (!hResizing) return;
        var dy = ev.clientY - hStartY;
        var newH = Math.max(80, hStartH + dy);
        activityPane.style.flex = '0 0 ' + newH + 'px';
      });
      document.addEventListener('mouseup', function() {
        if (hResizing) { hResizing = false; document.body.style.cursor = ''; }
      });
    }

    split.appendChild(convoPane);
    split.appendChild(divider);
    split.appendChild(rightPane);
    main.appendChild(split);

    // Resize handling — drag the divider to flex convo/activity widths.
    var resizing = false, startX = 0, startConvo = 0;
    divider.addEventListener('mousedown', function(ev) {
      resizing = true; startX = ev.clientX;
      startConvo = convoPane.getBoundingClientRect().width;
      document.body.style.cursor = 'col-resize';
      ev.preventDefault();
    });
    document.addEventListener('mousemove', function(ev) {
      if (!resizing) return;
      var dx = ev.clientX - startX;
      var newW = Math.max(280, startConvo + dx);
      convoPane.style.flex = '0 0 ' + newW + 'px';
    });
    document.addEventListener('mouseup', function() {
      if (resizing) { resizing = false; document.body.style.cursor = ''; }
    });

    // --- Input row --------------------------------------------------------
    var inputRow = el('div', {class: 'ui-agent-input-row'});
    var inputArea = el('textarea', {
      class: 'ui-agent-input',
      placeholder: cfg.placeholder || 'Ask something…',
      rows: 2,
    });
    inputArea.addEventListener('keydown', function(ev) {
      if (ev.key === 'Enter' && !ev.shiftKey) { ev.preventDefault(); sendMessage(); }
    });

    var attachInput = null, attachBtn = null;
    if (cfg.attachments) {
      attachInput = el('input', {type: 'file', accept: 'image/*', style: 'display:none'});
      attachInput.addEventListener('change', function(ev) {
        var files = Array.prototype.slice.call(ev.target.files || []);
        files.forEach(function(file) {
          var reader = new FileReader();
          reader.onload = function() {
            pendingAttachments.push({name: file.name, dataURL: reader.result});
            renderAttachments();
          };
          reader.readAsDataURL(file);
        });
        attachInput.value = '';
      });
      attachBtn = el('button', {
        class: 'ui-row-btn ui-agent-attach', title: 'Attach image',
        onclick: function(){ attachInput.click(); },
      }, ['📎']);
    }

    // Extra fields strip — same shape as ChatPanel: each ChatField
    // becomes one input that rides on every send body. Values also
    // get substituted into ListURL / LoadURL / DeleteURL templates
    // via {field_name} placeholders, so the list rail can be scoped
    // to the active value (e.g. workspaces for the active appliance).
    var extraInputs = {};
    var extrasRow = el('div', {class: 'ui-agent-extras', style: 'display:none'});
    (cfg.extra_fields || []).forEach(function(f) {
      var label = el('label', {class: 'ui-agent-extras-label', text: f.label || f.name});
      var input;
      if (f.type === 'select') {
        input = el('select', {class: 'ui-form-select'});
        // option_pairs (value/label) wins over options (value==label)
        // when both are supplied — apps usually want OptionPairs when
        // the form value is opaque (UUIDs).
        var pairs = f.option_pairs || [];
        if (pairs.length) {
          pairs.forEach(function(p) {
            input.appendChild(el('option', {value: p.value}, [p.label || p.value]));
          });
        } else {
          (f.options || []).forEach(function(opt) {
            input.appendChild(el('option', {value: opt}, [opt]));
          });
        }
        if (f.default) input.value = f.default;
      } else if (f.type === 'number') {
        input = el('input', {type: 'number', class: 'ui-form-input',
          min: f.min || undefined, max: f.max || undefined, value: f.default || ''});
      } else {
        input = el('input', {type: 'text', class: 'ui-form-input', value: f.default || ''});
      }
      extraInputs[f.name] = input;
      // On change, refresh the list rail. The list URL gets the new
      // extra value substituted into its {field_name} placeholder,
      // so changing the appliance picker reloads the workspace list
      // for that appliance.
      input.addEventListener('change', function() {
        if (hasList) loadSessions();
      });
      label.appendChild(input);
      extrasRow.appendChild(label);
    });
    if ((cfg.extra_fields || []).length > 0) extrasRow.style.display = '';

    function substituteExtras(url) {
      if (!url) return url;
      Object.keys(extraInputs).forEach(function(k) {
        var v = extraInputs[k].value || '';
        url = url.replace('{' + k + '}', encodeURIComponent(v));
      });
      return url;
    }

    var sendBtn = el('button', {class: 'ui-row-btn primary',
      onclick: function(){ sendMessage(); }}, [cfg.submit_label || 'Send']);
    var cancelBtn = el('button', {class: 'ui-row-btn ui-agent-cancel',
      style: 'display:none',
      onclick: function(){ cancelMessage(); }}, ['Cancel']);
    var spinner = el('span', {class: 'ui-agent-spinner', style: 'display:none'},
      [el('span', {class: 'ui-spinner'})]);

    inputRow.appendChild(inputArea);
    if (attachBtn) inputRow.appendChild(attachBtn);
    inputRow.appendChild(sendBtn);
    inputRow.appendChild(cancelBtn);
    inputRow.appendChild(spinner);

    var attachStrip = el('div', {class: 'ui-agent-attach-strip', style: 'display:none'});

    extrasSlot.appendChild(extrasRow);
    convoPane.appendChild(inputRow);
    convoPane.appendChild(attachStrip);

    function renderAttachments() {
      attachStrip.innerHTML = '';
      if (!pendingAttachments.length) { attachStrip.style.display = 'none'; return; }
      attachStrip.style.display = '';
      pendingAttachments.forEach(function(att, idx) {
        var chip = el('span', {class: 'ui-agent-attach-chip'}, [att.name]);
        chip.appendChild(el('button', {
          class: 'ui-agent-attach-x', title: 'Remove',
          onclick: function() {
            pendingAttachments.splice(idx, 1);
            renderAttachments();
          },
        }, ['×']));
        attachStrip.appendChild(chip);
      });
    }

    // --- Conversation / activity rendering --------------------------------

    function clearEmpty() {
      if (emptyMsg && emptyMsg.parentNode) emptyMsg.remove();
    }

    function addMessage(role, id, text) {
      clearEmpty();
      var bubble = el('div', {class: 'ui-agent-msg ui-agent-msg-' + (role || 'system')});
      var body = el('div', {class: 'ui-agent-msg-body'});
      if (cfg.markdown && role === 'assistant' && text) {
        body.innerHTML = mdToHTML(text);
      } else {
        body.textContent = text || '';
      }
      bubble.appendChild(body);
      convoLog.appendChild(bubble);
      // Insert ABOVE the input row — input lives in convoPane after
      // convoLog, so just appending to convoLog keeps order correct.
      convoLog.scrollTop = convoLog.scrollHeight;
      msgEls[id] = {bubble: bubble, body: body, role: role, rawText: text || ''};
      return msgEls[id];
    }

    function appendChunk(id, text) {
      var m = msgEls[id];
      if (!m) { m = addMessage('assistant', id, ''); }
      m.rawText = (m.rawText || '') + text;
      // Streaming text stays plain — markdown pass on message_done.
      m.body.textContent = m.rawText;
      convoLog.scrollTop = convoLog.scrollHeight;
    }

    function replaceChunk(id, text) {
      var m = msgEls[id];
      if (!m) { m = addMessage('assistant', id, ''); }
      m.rawText = text || '';
      m.body.textContent = m.rawText;
      convoLog.scrollTop = convoLog.scrollHeight;
    }

    function finalizeMessage(id) {
      var m = msgEls[id];
      if (!m) return;
      if (cfg.markdown && m.role === 'assistant') {
        m.body.innerHTML = mdToHTML(m.rawText || '');
      }
      // Fire registered decorators so apps can append per-message
      // affordances (save buttons, copy actions, …).
      var decorators = window.UIMessageDecorators || [];
      for (var i = 0; i < decorators.length; i++) {
        try {
          decorators[i]({
            role:    m.role,
            id:      id,
            wrap:    m.bubble,
            body:    m.body,
            rawText: m.rawText || '',
          });
        } catch (_) {}
      }
    }

    function addActivity(type, id, text) {
      var line = el('div', {class: 'ui-agent-act ui-agent-act-' + (type || 'status')});
      if (type === 'cmd') {
        line.textContent = '$ ' + (text || '');
      } else if (type === 'output') {
        line.classList.add('collapsed');
        line.textContent = text || '';
        line.addEventListener('click', function() {
          if (line.classList.contains('no-truncate')) return;
          line.classList.toggle('collapsed');
        });
        // After the line lands in the DOM we measure: if the
        // content fits within the collapsed max-height, drop the
        // show-more affordance entirely. Output rows that are
        // genuinely short shouldn't pretend to be truncated.
        setTimeout(function() {
          if (line.scrollHeight <= line.clientHeight + 2) {
            line.classList.add('no-truncate');
            line.classList.remove('collapsed');
          }
        }, 0);
      } else if (type === 'watch') {
        line.appendChild(el('span', {class: 'ui-spinner'}));
        line.appendChild(document.createTextNode(' ' + (text || '')));
      } else if (type === 'error') {
        line.textContent = 'Error: ' + (text || '');
      } else {
        line.textContent = text || '';
      }
      if (id) activityEls[id] = line;
      activityLog.appendChild(line);
      activityLog.scrollTop = activityLog.scrollHeight;
    }

    function updateActivity(id, text) {
      var line = activityEls[id];
      if (!line) return;
      // Preserve type-specific structure when updating (watch
      // keeps its spinner). Fallback to textContent.
      var spin = line.querySelector('.ui-spinner');
      if (spin) {
        line.innerHTML = '';
        line.appendChild(spin);
        line.appendChild(document.createTextNode(' ' + (text || '')));
      } else if (line.classList.contains('ui-agent-act-cmd')) {
        line.textContent = '$ ' + (text || '');
      } else {
        line.textContent = text || '';
      }
    }

    function addConfirm(d) {
      var id = d.id || '';
      var card = el('div', {class: 'ui-agent-confirm', id: 'confirm-' + id});
      card.appendChild(el('div', {class: 'ui-agent-confirm-prompt'}, [d.prompt || 'Confirm?']));
      if (d.detail) {
        card.appendChild(el('div', {class: 'ui-agent-confirm-detail'}, [d.detail]));
      }
      var btns = el('div', {class: 'ui-agent-confirm-btns'});
      (d.actions || []).forEach(function(a) {
        var cls = 'ui-row-btn';
        if (a.variant) cls += ' ' + a.variant;
        var b = el('button', {class: cls,
          onclick: function() { submitConfirm(id, a.value, card); }},
          [a.label || a.value || 'OK']);
        btns.appendChild(b);
      });
      card.appendChild(btns);
      activityLog.appendChild(card);
      activityLog.scrollTop = activityLog.scrollHeight;
    }

    function submitConfirm(id, value, card) {
      if (!cfg.confirm_url) return;
      // Disable all buttons immediately so a double-click doesn't
      // submit the same answer twice.
      card.querySelectorAll('button').forEach(function(b) { b.disabled = true; });
      fetchJSON(cfg.confirm_url, {
        method: 'POST',
        headers: {'Content-Type': 'application/json'},
        body: JSON.stringify({id: id, value: value}),
      }).catch(function(err) {
        showToast('Confirm failed: ' + (err && err.message || err));
      });
    }

    // App-registered block renderer dispatcher — same shape as
    // PipelinePanel uses. Apps register via window.uiRegisterBlockRenderer.
    function addBlock(d) {
      var id = d.id || '';
      var fn = window.UIBlockRenderers && window.UIBlockRenderers[d.type];
      if (typeof fn !== 'function') {
        // Fallback: render as a plain activity row so the event isn't
        // lost. App can register a proper renderer later.
        if (window.console && console.warn) {
          console.warn('[ui] no block renderer for type:', d.type,
            '— registered:', Object.keys(window.UIBlockRenderers || {}));
        }
        addActivity('status', id, '[' + d.type + '] ' + (d.text || d.title || ''));
        return;
      }
      // Update-in-place when the same id arrives again. The renderer
      // can opt in by returning {wrap, body, onUpdate}; onUpdate gets
      // the new event data and is expected to refresh the existing
      // DOM. Use case: plan checklists that stream status changes.
      var existing = blockEls[id];
      if (existing && typeof existing.onUpdate === 'function') {
        try { existing.onUpdate(d); } catch (_) {}
        return;
      }
      var built = fn(d, {sessionId: activeSessionId});
      if (!built || !built.wrap) return;
      blockEls[id] = built;
      // App blocks default to the conversation pane (the more
      // visible area). If the block specifies pane:"activity",
      // route there instead.
      var target = d.pane === 'activity' ? activityLog : convoLog;
      target.appendChild(built.wrap);
      target.scrollTop = target.scrollHeight;
    }

    function setStatus(text) {
      if (!text) { statusBar.style.display = 'none'; statusBar.textContent = ''; return; }
      statusBar.style.display = '';
      statusBar.textContent = text;
    }

    // --- SSE handling -----------------------------------------------------

    // Heartbeat watch — fires "Still processing… (Ns)" into the
    // activity pane after a configurable quiet period (28s). Lets
    // the user see that a long LLM round is still in flight, not
    // a hung session. Cleared on every incoming event and torn
    // down on enableInput.
    var lastEventTime = 0;
    var heartbeatTimer = null;
    var heartbeatEl = null;
    function startHeartbeat() {
      stopHeartbeat();
      lastEventTime = Date.now();
      heartbeatTimer = setInterval(function() {
        var elapsed = Date.now() - lastEventTime;
        if (elapsed <= 28000) {
          if (heartbeatEl) { heartbeatEl.remove(); heartbeatEl = null; }
          return;
        }
        var secs = Math.round(elapsed / 1000);
        if (!heartbeatEl) {
          heartbeatEl = el('div', {class: 'ui-agent-act ui-agent-act-watch'});
          heartbeatEl.appendChild(el('span', {class: 'ui-spinner'}));
          heartbeatEl.appendChild(document.createTextNode(
            ' Still processing… (' + secs + 's)'));
          activityLog.appendChild(heartbeatEl);
          activityLog.scrollTop = activityLog.scrollHeight;
        } else {
          // Re-use the spinner span; just refresh the trailing text.
          var spin = heartbeatEl.querySelector('.ui-spinner');
          heartbeatEl.innerHTML = '';
          if (spin) heartbeatEl.appendChild(spin);
          heartbeatEl.appendChild(document.createTextNode(
            ' Still processing… (' + secs + 's)'));
        }
      }, 5000);
    }
    function stopHeartbeat() {
      if (heartbeatTimer) { clearInterval(heartbeatTimer); heartbeatTimer = null; }
      if (heartbeatEl) { heartbeatEl.remove(); heartbeatEl = null; }
    }

    function handleEvent(ev) {
      // Bump the heartbeat clock on every event — clears the
      // "still processing" indicator if it was showing.
      lastEventTime = Date.now();
      if (heartbeatEl) { heartbeatEl.remove(); heartbeatEl = null; }
      if (!ev || !ev.kind) return;
      switch (ev.kind) {
        case 'session':
          activeSessionId = ev.id || '';
          if (cfg.deep_link_param) updateURLParam(cfg.deep_link_param, activeSessionId);
          // App-side hook — fires the full session event so app
          // handlers can react (e.g. set the appliance picker on
          // reconnect from the appliance_id field servitor adds).
          try {
            window.dispatchEvent(new CustomEvent('ui-agent-session',
              {detail: ev}));
          } catch (_) {}
          break;
        case 'message':
          addMessage(ev.role || 'assistant', ev.id || ('m-' + Date.now()), ev.text || '');
          break;
        case 'chunk':
          appendChunk(ev.id, ev.text || '');
          break;
        case 'chunk_replace':
          replaceChunk(ev.id, ev.text || '');
          break;
        case 'message_done':
          finalizeMessage(ev.id);
          break;
        case 'activity':
          addActivity(ev.type || 'status', ev.id || '', ev.text || '');
          break;
        case 'activity_update':
          updateActivity(ev.id, ev.text || '');
          break;
        case 'confirm':
          addConfirm(ev);
          break;
        case 'block':
          addBlock(ev);
          break;
        case 'block_done': {
          var be = blockEls[ev.id];
          if (be && be.onDone) be.onDone();
          break;
        }
        case 'block_remove': {
          var be2 = blockEls[ev.id];
          if (be2 && be2.wrap && be2.wrap.parentNode) be2.wrap.remove();
          delete blockEls[ev.id];
          break;
        }
        case 'status':
          setStatus(ev.text || '');
          break;
        case 'done':
          enableInput();
          setStatus('');
          break;
        case 'error':
          addActivity('error', '', ev.text || 'unknown error');
          setStatus('');
          enableInput();
          break;
      }
    }

    function updateURLParam(key, value) {
      try {
        var u = new URL(window.location.href);
        if (value) u.searchParams.set(key, value);
        else u.searchParams.delete(key);
        window.history.replaceState({}, '', u.toString());
      } catch (_) {}
    }

    function disableInput() {
      sendBtn.disabled = true;
      sendBtn.style.display = 'none';
      cancelBtn.style.display = '';
      spinner.style.display = '';
      // Drop the empty-state placeholder as soon as any work
      // starts (chat send, Map subscribe, reconnect) so the user
      // sees a clean canvas the events fill into. Without this the
      // "Pick an appliance below…" hint sits above the first
      // activity / intent / plan event.
      clearEmpty();
      startHeartbeat();
    }
    function enableInput() {
      sendBtn.disabled = false;
      sendBtn.style.display = '';
      cancelBtn.style.display = 'none';
      spinner.style.display = 'none';
      if (activeStream) { try { activeStream.abort(); } catch(_) {} activeStream = null; }
      if (activeEventSource) { activeEventSource.close(); activeEventSource = null; }
      stopHeartbeat();
    }

    function sendMessage() {
      var text = inputArea.value.trim();
      if (!text && !pendingAttachments.length) return;
      // Interjection path — when a session is already running and
      // the app configured an InjectURL, route this send into the
      // running session's note queue instead of starting a new
      // session. The agent picks queued notes up between rounds.
      var inFlight = !!(activeStream || activeEventSource);
      if (inFlight && cfg.inject_url && activeSessionId) {
        var noteId = 'u-' + Date.now();
        addMessage('user', noteId, text);
        // Tag the bubble so the notes_consumed handler can find it
        // when the agent drains the queue (server-issued note_id
        // overwrites this once the POST returns). Session id is
        // also tagged so an app-side decorator can wire edit /
        // delete against /api/inject without needing access to
        // the panel's internal state.
        var noteBubble = msgEls[noteId];
        if (noteBubble && noteBubble.bubble) {
          noteBubble.bubble.classList.add('ui-agent-interjection');
          noteBubble.bubble.dataset.sessionId = activeSessionId;
          noteBubble.bubble.dataset.injectUrl = cfg.inject_url;
        }
        inputArea.value = '';
        fetch(cfg.inject_url, {
          method: 'POST', headers: {'Content-Type': 'application/json'},
          body: JSON.stringify({id: activeSessionId, text: text}),
        }).then(function(r) {
          if (!r.ok) { return r.text().then(function(t){ throw new Error(t); }); }
          return r.json();
        }).then(function(d) {
          if (noteBubble && noteBubble.bubble && d && d.note_id) {
            noteBubble.bubble.dataset.noteId = d.note_id;
          }
        }).catch(function(err) {
          if (noteBubble && noteBubble.bubble) {
            noteBubble.bubble.classList.add('ui-agent-interjection-failed');
          }
          showToast('Note failed: ' + (err && err.message || err));
        });
        return;
      }
      var localMsgId = 'u-' + Date.now();
      addMessage('user', localMsgId, text);
      inputArea.value = '';
      var images = pendingAttachments.map(function(a) {
        // Strip the data URL prefix; server expects raw base64.
        var s = a.dataURL || '';
        var i = s.indexOf(',');
        return i >= 0 ? s.substring(i + 1) : s;
      });
      pendingAttachments = [];
      renderAttachments();

      disableInput();

      var body = {
        session_id: activeSessionId || '',
        message:    text,
        images:     images,
      };
      if (cfg.list_is_context) {
        // In CONTEXT mode, the rail's active id ships under a
        // user-configured key (default "context_id"). session_id
        // is reserved for the server-issued chat session and is
        // empty on every new send unless the server provides one.
        var contextKey = cfg.list_body_field || 'context_id';
        body[contextKey] = activeContextId || '';
        body.session_id = '';
      }
      Object.keys(extraInputs).forEach(function(k) {
        body[k] = extraInputs[k].value;
      });

      activeStream = new AbortController();
      var resp = fetch(cfg.send_url, {
        method: 'POST',
        headers: {'Content-Type': 'application/json'},
        body: JSON.stringify(body),
        signal: activeStream.signal,
      });
      dispatchResponse(resp);
    }

    // dispatchResponse branches based on the SendURL response shape:
    //   - text/event-stream  → stream SSE directly off this response
    //   - application/json   → expect {session_id}; subscribe to EventsURL
    // The JSON pattern fits apps with a queue-backed session store —
    // it lets the client reconnect to the same event stream after a
    // page reload.
    function dispatchResponse(respPromise) {
      respPromise.then(function(resp) {
        if (!resp.ok) {
          return resp.text().then(function(t) { throw new Error(t || resp.statusText); });
        }
        var ct = (resp.headers.get('Content-Type') || '').toLowerCase();
        if (ct.indexOf('application/json') >= 0 && cfg.events_url) {
          return resp.json().then(function(d) {
            var sid = (d && (d.session_id || d.id)) || '';
            if (!sid) throw new Error('server did not return a session_id');
            activeSessionId = sid;
            if (cfg.deep_link_param) updateURLParam(cfg.deep_link_param, sid);
            subscribeEvents(sid);
          });
        }
        // Fallback: parse this response as the SSE stream directly.
        return streamSSE(resp);
      }).catch(function(err) {
        if (err.name === 'AbortError') return;
        addActivity('error', '', err.message || String(err));
        enableInput();
      });
    }

    function streamSSE(resp) {
      var reader = resp.body.getReader();
      var decoder = new TextDecoder('utf-8');
      var buffer = '';
      function pump() {
        return reader.read().then(function(r) {
          if (r.done) { enableInput(); return; }
          buffer += decoder.decode(r.value, {stream: true});
          var lines = buffer.split('\n');
          buffer = lines.pop();
          lines.forEach(function(line) {
            if (line.startsWith('data: ')) {
              try { handleEvent(JSON.parse(line.slice(6))); }
              catch(e) {}
            }
          });
          return pump();
        });
      }
      return pump();
    }

    // subscribeEvents — used for the POST-ack + subscribe flow. Opens
    // an EventSource to EventsURL?id=<sid>. The server stream is
    // expected to replay buffered events on connect (so reconnects
    // are safe) and emit new events as the in-flight session
    // produces them.
    var activeEventSource = null;
    function subscribeEvents(sid) {
      if (activeEventSource) { activeEventSource.close(); activeEventSource = null; }
      var url = cfg.events_url + (cfg.events_url.indexOf('?') >= 0 ? '&' : '?') +
        'id=' + encodeURIComponent(sid);
      activeEventSource = new EventSource(url);
      activeEventSource.onmessage = function(ev) {
        try { handleEvent(JSON.parse(ev.data)); } catch (_) {}
      };
      activeEventSource.onerror = function() {
        // EventSource auto-reconnects on transient errors. The server
        // closes the stream once the session ends; we get a final
        // onerror in that case and tear down here.
        if (activeEventSource && activeEventSource.readyState === EventSource.CLOSED) {
          activeEventSource = null;
          enableInput();
        }
      };
    }

    function cancelMessage() {
      if (activeStream) {
        try { activeStream.abort(); } catch(_) {}
        activeStream = null;
      }
      if (activeEventSource) {
        activeEventSource.close();
        activeEventSource = null;
      }
      if (activeSessionId && cfg.cancel_url) {
        fetchJSON(cfg.cancel_url + '?id=' + encodeURIComponent(activeSessionId),
          {method: 'POST'}).catch(function(){});
      }
      enableInput();
    }

    // --- Session list / load / delete -------------------------------------

    // Refresh the list rail when any uiInvalidate fires for our
    // list source. Compares the base URL (strip query string) so
    // an invalidate for "api/workspace/list" matches a listener
    // configured with "api/workspace/list?appliance_id={appliance_id}".
    window.addEventListener('ui-data-changed', function(ev) {
      if (!hasList) return;
      var sources = ev.detail && ev.detail.sources;
      if (!sources) return;
      var baseURL = (cfg.list_url || '').split('?')[0];
      for (var i = 0; i < sources.length; i++) {
        if ((sources[i] || '').split('?')[0] === baseURL) {
          loadSessions();
          return;
        }
      }
    });

    function loadSessions() {
      if (!hasList) return;
      var activeID = cfg.list_is_context ? activeContextId : activeSessionId;
      fetchJSON(substituteExtras(cfg.list_url)).then(function(items) {
        sideList.innerHTML = '';
        if (!Array.isArray(items) || !items.length) {
          sideList.appendChild(el('div', {class: 'ui-chat-side-empty'}, ['(none)']));
          return;
        }
        items.forEach(function(rec) {
          var sid = rec[idF];
          var ttl = rec[ttlF] || sid;
          // Use the shared .ui-chat-side-item class so the row gets
          // the framework's hover / active styling AND the
          // position:relative context that the absolutely-positioned
          // ✎/× buttons need to land on the right edge.
          var row = el('div', {class: 'ui-chat-side-item'}, [
            el('span', {class: 'ui-chat-side-title', text: ttl}),
          ]);
          row.addEventListener('click', function() { openSession(sid); });
          // Optional rename button (✎). When the app provides
          // a RenameURL, each row gets an inline edit affordance
          // that prompts for a new name and POSTs {id, name}.
          if (cfg.rename_url) {
            // Bump right padding so the title doesn't run under
            // both action buttons.
            row.classList.add('ui-chat-side-item-renable');
            var renBtn = el('button', {
              class: 'ui-chat-side-ren', title: 'Rename',
              onclick: function(ev) {
                ev.stopPropagation();
                var next = prompt('Rename to:', ttl);
                if (next == null) return;
                next = next.trim();
                if (!next || next === ttl) return;
                fetchJSON(cfg.rename_url, {
                  method: 'POST',
                  headers: {'Content-Type': 'application/json'},
                  body: JSON.stringify({id: sid, name: next}),
                }).then(function() { loadSessions(); })
                  .catch(function(err) {
                    showToast('Rename failed: ' + (err && err.message || err));
                  });
              },
            }, ['✎']);
            row.appendChild(renBtn);
          }
          var delBtn = el('button', {class: 'ui-chat-side-del', title: 'Delete',
            onclick: function(ev) {
              ev.stopPropagation();
              if (!confirm('Delete this item?')) return;
              var url = cfg.delete_url.replace('{id}', encodeURIComponent(sid));
              fetchJSON(url, {method: 'DELETE'}).then(function() {
                if (cfg.list_is_context) {
                  if (activeContextId === sid) activeContextId = '';
                } else {
                  if (activeSessionId === sid) openSession(null);
                }
                loadSessions();
              });
            }}, ['×']);
          row.appendChild(delBtn);
          if (sid === activeID) row.classList.add('active');
          sideList.appendChild(row);
        });
      }).catch(function() {
        sideList.innerHTML = '';
        sideList.appendChild(el('div', {class: 'ui-chat-side-empty'}, ['(failed to load)']));
      });
    }

    function openSession(sid) {
      // CONTEXT mode — list rows are reference contexts (workspaces,
      // projects, …). Selecting one binds future sends to that id
      // via cfg.list_body_field. Server-side LoadURL still gets
      // fetched if set, and any messages it returns replay into the
      // conversation pane so the user sees the context's history
      // when picking it. The activity pane is NOT cleared — that's
      // live per-send state, distinct from saved context history.
      if (cfg.list_is_context) {
        activeContextId = sid || '';
        if (cfg.deep_link_param) updateURLParam(cfg.deep_link_param, sid || '');
        msgEls = {};
        convoLog.innerHTML = '';
        if (!sid) {
          emptyMsg = el('div', {class: 'ui-agent-empty'},
            [cfg.empty_text || 'Start typing below.']);
          convoLog.appendChild(emptyMsg);
          if (hasList) loadSessions();
          return;
        }
        if (cfg.load_url) {
          var url = cfg.load_url.replace('{id}', encodeURIComponent(sid));
          fetchJSON(url).then(function(rec) {
            var msgs = rec && rec[msgsF];
            if (Array.isArray(msgs)) {
              msgs.forEach(function(m) {
                var mid = m.id || ('m-' + Math.random().toString(36).slice(2));
                addMessage(m.role || 'assistant', mid, m.content || m.text || '');
                if (cfg.markdown && (m.role === 'assistant')) finalizeMessage(mid);
              });
            }
            if (hasList) loadSessions();
          }).catch(function(err) {
            addActivity('error', '', err.message || String(err));
          });
        } else if (hasList) {
          loadSessions();
        }
        return;
      }
      // SESSION mode — replay messages from the saved conversation.
      activeSessionId = sid || '';
      msgEls = {}; activityEls = {}; blockEls = {};
      convoLog.innerHTML = '';
      activityLog.innerHTML = '';
      if (!sid) {
        emptyMsg = el('div', {class: 'ui-agent-empty'},
          [cfg.empty_text || 'Start typing below.']);
        convoLog.appendChild(emptyMsg);
        if (cfg.deep_link_param) updateURLParam(cfg.deep_link_param, '');
        if (hasList) loadSessions();
        return;
      }
      if (!hasList) {
        // Without a list URL, there's nothing to load from. The
        // app is treating sessions as ephemeral; just clear the
        // pane and let the next send carry the session forward.
        if (cfg.deep_link_param) updateURLParam(cfg.deep_link_param, sid);
        return;
      }
      var url = cfg.load_url.replace('{id}', encodeURIComponent(sid));
      fetchJSON(url).then(function(rec) {
        var msgs = rec && rec[msgsF];
        if (Array.isArray(msgs)) {
          msgs.forEach(function(m) {
            var mid = m.id || ('m-' + Math.random().toString(36).slice(2));
            addMessage(m.role || 'assistant', mid, m.content || m.text || '');
            if (cfg.markdown && (m.role === 'assistant')) finalizeMessage(mid);
          });
        }
        if (cfg.deep_link_param) updateURLParam(cfg.deep_link_param, sid);
        loadSessions();
      }).catch(function(err) {
        addActivity('error', '', err.message || String(err));
      });
    }

    // Deep-link bootstrapping: if the URL carries the configured
    // session param, open it on mount. In CONTEXT mode this
    // restores the active context; in SESSION mode it replays the
    // saved conversation.
    if (cfg.deep_link_param) {
      try {
        var qs = new URL(window.location.href).searchParams;
        var sid = qs.get(cfg.deep_link_param);
        if (sid) {
          if (cfg.list_is_context) activeContextId = sid;
          else activeSessionId = sid;
          openSession(sid);
        }
      } catch (_) {}
    }
    // Live-reconnect bootstrapping: if the URL carries
    // ?reconnect=<id> and EventsURL is configured, hop straight
    // into a running session's stream. Used by the global "live
    // sessions" pill to attach to an in-flight job (map run,
    // long-running chat) after a page navigation.
    if (cfg.events_url) {
      try {
        var rid = new URL(window.location.href).searchParams.get('reconnect');
        if (rid) {
          activeSessionId = rid;
          disableInput();
          subscribeEvents(rid);
        }
      } catch (_) {}
    }

    // Assemble: topbar across the top, then the grid row below
    // (side rail + main column). The floating expand tab lives
    // INSIDE the grid row so it sits below the topbar — otherwise
    // it overlaps full-width toolbar buttons at the top.
    wrap.appendChild(topbar);
    if (hasList) {
      gridRow.appendChild(side);
      wrap.appendChild(drawer.backdrop);
    }
    if (expandTab) gridRow.appendChild(expandTab);
    gridRow.appendChild(main);
    wrap.appendChild(gridRow);

    if (hasList) loadSessions();
    return wrap;
  };

  // Pipeline panel — submit form on top, structured streaming
  // transcript below, sessions sidebar on the left. Designed for
  // originally built for one app but reusable for any "kick off a multi-stage run, watch
  // it fill in, save the result" workflow (multi-stage runs, pipelines, ...).
  components.pipeline_panel = function(cfg) {
    var idF     = cfg.session_id_field    || 'ID';
    var titleF  = cfg.session_title_field || 'Title';
    var dateF   = cfg.session_date_field  || 'Date';
    var blocksF = cfg.session_blocks_field || 'Blocks';

    var currentSessionId = '';
    var blockEls = {};       // block id -> DOM element { wrap, body, raw }
    var liveVerdictBid = null; // most-recent verdict id (for auto-scroll on done)
    var activeStream = null; // AbortController for in-flight submit

    var wrap = el('div', {class: 'ui-pl ui-chat'});
    var side = el('div', {class: 'ui-chat-side'});
    var bulkSelected = {};
    var bulkState    = {mode: false};

    var sideSelectBtn = null;
    if (cfg.bulk_select) {
      sideSelectBtn = el('button', {
        class: 'ui-chat-side-btn', title: 'Tap items to select multiple',
        onclick: function() {
          bulkState.mode = !bulkState.mode;
          if (!bulkState.mode) Object.keys(bulkSelected).forEach(function(k){ delete bulkSelected[k]; });
          sideSelectBtn.classList.toggle('active', bulkState.mode);
          sideSelectBtn.textContent = bulkState.mode ? '✓ Selecting' : 'Select';
          loadSessions();
        },
      }, ['Select']);
    }
    var sideHdrBuilt = renderSideHeader({
      label: 'Sessions',
      newTitle: 'New run',
      onNew:    function(){ openSession(null); },
      onClose:  function(){ closeDrawer(); },
      leftExtras: sideSelectBtn ? [sideSelectBtn] : [],
    });
    var sideHdr  = sideHdrBuilt.elt;
    var sideList = el('div', {class: 'ui-chat-side-list'}, ['Loading…']);
    var sideSearch = makeSideSearch(sideList);
    side.appendChild(sideHdr);
    side.appendChild(sideSearch);
    side.appendChild(sideList);

    var main = el('div', {class: 'ui-chat-main'});
    var drawer = makeDrawer(side, {
      title:          'New run',
      hamburgerTitle: 'Sessions',
    });
    var mobileTitle    = drawer.mobileTitle;
    var drawerBackdrop = drawer.backdrop;
    var openDrawer     = drawer.openDrawer;
    var closeDrawer    = drawer.closeDrawer;
    main.appendChild(drawer.mobileHdr);

    // Submit form section. Each field becomes an input in field-order.
    var form = el('div', {class: 'ui-pl-form'});
    var formInputs = {}; // field name -> input element
    var toggleRows = []; // toggle fields stash — appended to formActions
    (cfg.fields || []).forEach(function(f) {
      var row = el('div', {class: 'ui-pl-formrow'});
      if (f.label) row.appendChild(el('label', {class: 'ui-pl-formlabel'}, [f.label]));
      var inp;
      if (f.type === 'textarea') {
        inp = el('textarea', {
          class: 'ui-pl-input ui-pl-textarea',
          rows: String(f.rows || 3),
          placeholder: f.placeholder || '',
        });
      } else if (f.type === 'select') {
        inp = el('select', {class: 'ui-pl-input'});
        (f.options || []).forEach(function(opt) { inp.appendChild(el('option', {value: opt}, [opt])); });
      } else if (f.type === 'number') {
        inp = el('input', {
          type: 'number', class: 'ui-pl-input',
          min: typeof f.min !== 'undefined' ? String(f.min) : '',
          max: typeof f.max !== 'undefined' ? String(f.max) : '',
          placeholder: f.placeholder || '',
        });
      } else if (f.type === 'toggle') {
        // Compact inline toggle. Renders as a pill-shaped row with
        // a small switch + tight label, sitting flush-left in the
        // form column rather than stretching across it. Keeps
        // optional flags (email-on-done, "draft mode", etc.) from
        // dominating a form whose primary input is a textarea.
        inp = el('input', {type: 'checkbox', class: 'ui-switch ui-switch-sm'});
        inp.checked = !!f.default;
        row.classList.add('ui-pl-formrow-toggle');
        var firstChild = row.firstChild;
        if (firstChild && firstChild.classList && firstChild.classList.contains('ui-pl-formlabel')) {
          firstChild.style.marginBottom = '0';
        }
      } else {
        // Honor HTML5 input types beyond plain text (email, tel,
        // url) — gives mobile users the right keyboard and lets
        // the browser do basic validation. Unknown values fall
        // back to "text".
        var htmlType = 'text';
        if (f.type === 'email' || f.type === 'tel' || f.type === 'url' || f.type === 'password') {
          htmlType = f.type;
        }
        inp = el('input', {type: htmlType, class: 'ui-pl-input', placeholder: f.placeholder || ''});
      }
      if (f.default) inp.value = f.default;
      formInputs[f.name] = inp;
      row.appendChild(inp);
      // Toggles land in the formActions bar (right-aligned) instead
      // of the form column so an "Email me when done" pill sits
      // beside the Start button rather than burning vertical space
      // above it. The row is held in a sidecar slot and appended
      // after formActions is built below.
      if (f.type === 'toggle') {
        toggleRows.push(row);
      } else {
        form.appendChild(row);
      }
    });
    var formActions = el('div', {class: 'ui-pl-formactions'});
    var prefillBtn = null;
    if (cfg.prefill_url) {
      var target = cfg.prefill_target || (function() {
        for (var i = 0; i < (cfg.fields || []).length; i++) {
          if (cfg.fields[i].type === 'textarea') return cfg.fields[i].name;
        }
        return cfg.fields && cfg.fields[0] && cfg.fields[0].name;
      })();
      // Popover that hosts an array of suggestions to pick from.
      // Hidden until a list response lands; click any item to fill
      // the target input. Click outside to dismiss.
      var prefillMenu = el('div', {class: 'ui-pl-prefill-menu', style: 'display:none'});
      var prefillWrap = el('div', {class: 'ui-pl-prefill-wrap'});
      prefillBtn = el('button', {
        class: 'ui-pl-btn ui-pl-prefill-btn',
        onclick: function() {
          var orig = prefillBtn.textContent;
          prefillBtn.textContent = '…';
          prefillBtn.disabled = true;
          // Default GET; apps that need a POST body declare
          // prefill_method + prefill_body so the runtime doesn't
          // need an app-specific wrapper endpoint.
          var fetchOpts = {};
          if ((cfg.prefill_method || 'GET').toUpperCase() === 'POST') {
            fetchOpts.method = 'POST';
            fetchOpts.headers = {'Content-Type': 'application/json'};
            fetchOpts.body = cfg.prefill_body || '{}';
          }
          fetch(cfg.prefill_url, fetchOpts).then(function(r) {
            if (!r.ok) throw new Error('HTTP ' + r.status);
            return r.text();
          }).then(function(text) {
            var t = String(text || '').trim();
            var arr = null;
            // Array response → show popover. Object → look for
            // common single-suggestion keys. String → direct fill.
            try {
              var j = JSON.parse(t);
              if (Array.isArray(j)) arr = j;
              else if (j && typeof j === 'object') {
                t = String(j.topic || j.text || j.suggestion || '').trim();
              }
            } catch (_) {}
            if (arr && arr.length) {
              prefillMenu.innerHTML = '';
              // Field overrides let apps point at their own response
              // shape (one app returns {question, hook}; another
              // returns {topic, text}; etc.). Without overrides,
              // {topic|text|question} fall through as the value and
              // {hook|description|summary} as the muted second line.
              var qField = cfg.prefill_list_question_field || '';
              var hField = cfg.prefill_list_hook_field || '';
              arr.forEach(function(s) {
                var label, hook;
                if (typeof s === 'string') {
                  label = s;
                } else {
                  label = qField ? s[qField] : (s.topic || s.text || s.question);
                  hook  = hField ? s[hField] : (s.hook || s.description || s.summary);
                }
                if (!label) return;
                var children = [el('div', {class: 'ui-pl-prefill-item-q'}, [label])];
                if (hook) children.push(el('div', {class: 'ui-pl-prefill-item-hook'}, [hook]));
                var fillValue = label;
                var item = el('div', {class: 'ui-pl-prefill-item', onclick: function() {
                  if (target && formInputs[target]) formInputs[target].value = fillValue;
                  prefillMenu.style.display = 'none';
                }}, children);
                prefillMenu.appendChild(item);
              });
              prefillMenu.style.display = '';
              return;
            }
            if (t && target && formInputs[target]) formInputs[target].value = t;
          }).catch(function(err) { showToast('Suggest failed: ' + err.message); }).then(function() {
            prefillBtn.textContent = orig;
            prefillBtn.disabled = false;
          });
        },
      }, [
        // AI sparkle icon — span keeps the icon styled distinctly
        // from the label and lets CSS target it without touching
        // the label text the app provides.
        el('span', {class: 'ui-pl-prefill-icon'}, ['✨']),
        ' ',
        cfg.prefill_label || 'Suggest',
      ]);
      prefillWrap.appendChild(prefillBtn);
      prefillWrap.appendChild(prefillMenu);
      formActions.appendChild(prefillWrap);
      // Dismiss on outside click.
      document.addEventListener('click', function(ev) {
        if (prefillMenu.style.display === 'none') return;
        if (!prefillWrap.contains(ev.target)) prefillMenu.style.display = 'none';
      });
    }
    var submitBtn = el('button', {class: 'ui-pl-btn primary', onclick: function(){ doSubmit(); }}, [cfg.submit_label || 'Start']);
    // Cancel button stays in the top action bar (defined below) so
    // it remains visible during a live run when the form is hidden.
    // ui-pl-btn-cancel uses margin-left:auto so it sits on the far
    // right of the actions toolbar regardless of how many action
    // buttons sit on the left.
    var cancelBtn = el('button', {class: 'ui-pl-btn danger ui-pl-btn-cancel', style: 'display:none', onclick: function(){ doCancel(); }}, ['Cancel']);
    // Toggle pills land on the far LEFT of the actions row,
    // opposite the Suggest + Start buttons. Achieved by prepending
    // toggle rows and giving the first one margin-right:auto so it
    // pushes the rest of the row content (Suggest, Start) to the
    // right edge that flex-end already targets.
    toggleRows.forEach(function(r, i) {
      if (i === 0) r.style.marginRight = 'auto';
      formActions.insertBefore(r, formActions.firstChild);
    });
    formActions.appendChild(submitBtn);
    form.appendChild(formActions);

    // Per-session actions toolbar — visible when a session is loaded
    // (live or saved) OR when a run is in flight (Cancel pinned to
    // the right of the toolbar so the user can abort even when the
    // form is hidden). renderActions repopulates it on each call;
    // cancelBtn is added here so it's reachable for an early submit
    // before the first 'session' SSE event triggers renderActions.
    var actionsBar = el('div', {class: 'ui-pl-actions', style: 'display:none'});
    actionsBar.appendChild(cancelBtn);
    // Session-title strip — shows the loaded topic immediately under
    // the actions toolbar so users always know which run they're
    // looking at without scrolling. Hidden until a session loads.
    var sessionTitle = el('div', {class: 'ui-pl-session-title', style: 'display:none'});

    var transcript = el('div', {class: 'ui-pl-transcript'});
    var emptyHint = el('div', {class: 'ui-chat-empty', style: 'padding:1rem'},
      [cfg.empty_text || 'Submit a topic to begin, or pick one from the sidebar.']);
    transcript.appendChild(emptyHint);

    main.appendChild(form);
    main.appendChild(actionsBar);
    main.appendChild(sessionTitle);
    main.appendChild(transcript);

    function setSessionTitle(text) {
      if (text && String(text).trim()) {
        sessionTitle.textContent = text;
        sessionTitle.style.display = '';
      } else {
        sessionTitle.style.display = 'none';
      }
    }

    // openStreamModal creates a centered modal overlay and streams
    // an SSE response into its body. Designed for "Generate Report"
    // style flows where the user wants the result on top of the
    // current page rather than replacing the transcript. Recognizes
    // the legacy report-stream event shape (report_header, _stream,
    // _replace, _status, _done) so handleReportSSE works unchanged.
    // openRelatedPopover wires a "Related" toolbar action — POSTs
    // [currentId] to the URL, expects a JSON response of either
    // {suggestions: [{question, reason?}, ...]} OR a bare array of
    // {question, reason?} objects. Shows a clickable popover; pick
    // an entry → fills the form's first textarea with the question
    // and submits it as a new run. This is the framework analog of
    // the legacy suggest panel.
    function openRelatedPopover(action, url, btn) {
      var origLabel = btn.textContent;
      btn.textContent = 'Loading…';
      btn.disabled = true;
      fetch(url, {
        method: 'POST',
        headers: {'Content-Type': 'application/json'},
        body: JSON.stringify([currentSessionId]),
      }).then(function(r) {
        if (!r.ok) return r.text().then(function(t){ throw new Error(t || ('HTTP ' + r.status)); });
        return r.json();
      }).then(function(data) {
        var suggestions = (data && data.suggestions) || data || [];
        var unanswered  = (data && data.unanswered)  || [];
        var answered    = (data && data.answered)    || [];
        if (!Array.isArray(suggestions)) suggestions = [];
        if (!Array.isArray(unanswered))  unanswered  = [];
        if (!Array.isArray(answered))    answered    = [];
        if (!suggestions.length && !unanswered.length && !answered.length) {
          showToast('No related topics found');
          return;
        }
        var overlay = el('div', {class: 'ui-pl-modal-overlay',
          onclick: function(ev){ if (ev.target === overlay) close(); }});
        var modal = el('div', {class: 'ui-pl-modal'});
        var header = el('div', {class: 'ui-pl-modal-h'});
        header.appendChild(el('div', {class: 'ui-pl-modal-title'}, [action.label || 'Related']));
        header.appendChild(el('button', {class: 'ui-pl-modal-close', title: 'Close',
          onclick: function() { close(); }}, ['×']));
        modal.appendChild(header);
        var body = el('div', {class: 'ui-pl-modal-body'});
        // mode:
        //   "submit" — fill the form + start a new run
        //   "open"   — load the linked existing session
        // variant:
        //   "accent"  — primary suggestions (theme accent rule)
        //   "warning" — unanswered (amber, needs follow-up)
        //   "success" — previously answered (green, click to open)
        //   "" / null — neutral border
        var addSection = function(label, items, variant, mode) {
          if (!items.length) return;
          var hClass = 'ui-pl-related-h' + (variant ? ' ' + variant : '');
          body.appendChild(el('div', {class: hClass}, [label]));
          items.forEach(function(s) {
            var q, reason, openID;
            if (typeof s === 'string') {
              q = s;
            } else {
              q       = s.question || s.topic || '';
              reason  = s.reason   || s.hook  || '';
              openID  = s.report_id || s.id;
            }
            if (!q) return;
            var iClass = 'ui-pl-related-item' + (variant ? ' ' + variant : '');
            var item = el('div', {class: iClass});
            item.appendChild(el('div', {class: 'ui-pl-related-q'}, [q]));
            if (reason) item.appendChild(el('div', {class: 'ui-pl-related-reason'}, [reason]));
            // Hint pill on answered entries so users know clicking
            // opens the existing report rather than starting a new run.
            if (mode === 'open' && openID) {
              item.appendChild(el('div', {class: 'ui-pl-related-hint'}, ['↗ open existing report']));
            }
            item.addEventListener('click', function() {
              close();
              if (mode === 'open' && openID) {
                openSession(openID);
                return;
              }
              // Default: fill the form's first textarea (or named
              // target) and submit a new run.
              var target = action.fill_target || (function() {
                for (var i = 0; i < (cfg.fields || []).length; i++) {
                  if (cfg.fields[i].type === 'textarea') return cfg.fields[i].name;
                }
                return cfg.fields && cfg.fields[0] && cfg.fields[0].name;
              })();
              openSession(null);
              if (target && formInputs[target]) {
                formInputs[target].value = q;
              }
              doSubmit();
            });
            body.appendChild(item);
          });
        };
        // Order matches the legacy Related panel: unanswered
        // first (most urgent — these gaps were never closed),
        // already-researched second (existing reports the user can
        // jump to), fresh suggestions last.
        addSection('Unanswered Questions',     unanswered,  'warning', 'submit');
        addSection('Previously Answered',      answered,    'success', 'open');
        addSection('Suggested Follow-ups',     suggestions, 'accent',  'submit');
        modal.appendChild(body);
        overlay.appendChild(modal);
        document.body.appendChild(overlay);
        function close() { overlay.remove(); }
      }).catch(function(err) {
        showToast('Related failed: ' + err.message);
      }).then(function() {
        btn.textContent = origLabel;
        btn.disabled = false;
      });
    }

    function openStreamModal(action, url) {
      var overlay = el('div', {class: 'ui-pl-modal-overlay'});
      var modal = el('div', {class: 'ui-pl-modal'});
      var header = el('div', {class: 'ui-pl-modal-h'});
      var titleEl = el('div', {class: 'ui-pl-modal-title'}, [action.label || 'Report']);
      // headerActions slot — modal-actions buttons live here on the
      // top right (Save as PDF, Regenerate, Copy, ×). Hidden until
      // the stream completes so it doesn't hint at not-yet-ready
      // actions while the body is still being generated.
      var headerActions = el('div', {class: 'ui-pl-modal-h-actions', style: 'display:none'});
      var closeBtn = el('button', {class: 'ui-pl-modal-close', title: 'Close',
        onclick: function() { close(); }}, ['×']);
      header.appendChild(titleEl);
      header.appendChild(headerActions);
      header.appendChild(closeBtn);
      var body = el('div', {class: 'ui-pl-modal-body'});
      body.appendChild(el('div', {class: 'ui-pl-modal-status'},
        ['Generating...', el('span', {class: 'ui-pl-spinner'})]));
      // Footer is no longer used — kept as a hidden stub so existing
      // references (footer.style.display = '') still resolve. All
      // actions render in headerActions instead.
      var footer = el('div', {style: 'display:none'});
      // Custom modal actions defined on the parent action — for
      // example: Save as PDF, Regenerate, Push to next stage.
      // Substitution: {id} uses the same id used for the parent
      // action's URL (extracted from the parent url).
      var idMatch = url.match(/\/([^\/?#]+)(?:\?|$)/);
      var modalSessionId = idMatch ? decodeURIComponent(idMatch[1]) : '';
      (action.modal_actions || []).forEach(function(ma) {
        var maURL = (ma.url || '').replace('{id}', encodeURIComponent(modalSessionId));
        var maMethod = ma.method || 'open';
        var maBtn;
        if (maMethod === 'open') {
          maBtn = el('a', {
            class: 'ui-pl-btn secondary',
            href: maURL, target: '_blank', rel: 'noopener',
            title: ma.title || ma.label,
            style: 'text-decoration:none',
          }, [ma.label]);
        } else {
          maBtn = el('button', {class: 'ui-pl-btn secondary', title: ma.title || ma.label, onclick: function() {
            if (ma.confirm && !confirm(ma.confirm)) return;
            if (maMethod === 'copy') {
              var fullURL = window.location.origin + window.location.pathname + maURL;
              if (ma.url && /^https?:/i.test(ma.url)) fullURL = maURL;
              navigator.clipboard.writeText(fullURL).then(function() { showToast('Copied'); })
                .catch(function() { showToast('Copy failed'); });
              return;
            }
            if (maMethod === 'regenerate') {
              // Re-run the parent stream with ?regenerate=1 appended.
              ctrl.abort();
              streamRaw = '';
              body.innerHTML = '';
              body.appendChild(el('div', {class: 'ui-pl-modal-status'},
                ['Regenerating...', el('span', {class: 'ui-pl-spinner'})]));
              headerActions.style.display = 'none';
              var sep = url.indexOf('?') >= 0 ? '&' : '?';
              var newURL = url + sep + 'regenerate=1';
              ctrl = new AbortController();
              fetch(newURL, {signal: ctrl.signal}).then(function(r) {
                if (!r.ok) throw new Error('HTTP ' + r.status);
                var reader = r.body.getReader();
                var dec = new TextDecoder();
                var buf = '';
                function pump() {
                  return reader.read().then(function(res) {
                    if (res.done) return;
                    buf += dec.decode(res.value, {stream: true});
                    var idx;
                    while ((idx = buf.indexOf('\n\n')) >= 0) {
                      processModalEvent(buf.slice(0, idx));
                      buf = buf.slice(idx + 2);
                    }
                    return pump();
                  });
                }
                return pump();
              }).catch(function(err) {
                if (err.name !== 'AbortError') {
                  body.innerHTML = '<div class="ui-pl-modal-status" style="color:var(--danger,#f85149)">Failed: ' + (err.message || err) + '</div>';
                }
              });
              return;
            }
            if (maMethod === 'post') {
              maBtn.disabled = true;
              fetch(maURL, {method: 'POST'}).then(function(r) {
                if (!r.ok) return r.text().then(function(t){ throw new Error(t || ('HTTP ' + r.status)); });
                showToast(ma.label + ' done');
              }).catch(function(err) { showToast(ma.label + ' failed: ' + err.message); })
                .then(function(){ maBtn.disabled = false; });
              return;
            }
          }}, [ma.label]);
        }
        headerActions.appendChild(maBtn);
      });
      var copyBtn = el('button', {class: 'ui-pl-btn secondary', onclick: function() {
        var raw = body.dataset.raw || body.textContent || '';
        navigator.clipboard.writeText(raw).then(function() { showToast('Copied'); })
          .catch(function() { showToast('Copy failed'); });
      }}, ['Copy']);
      headerActions.appendChild(copyBtn);
      modal.appendChild(header);
      modal.appendChild(body);
      modal.appendChild(footer);
      overlay.appendChild(modal);
      document.body.appendChild(overlay);
      overlay.addEventListener('click', function(ev) { if (ev.target === overlay) close(); });

      var ctrl = new AbortController();
      var streamRaw = '';
      function close() {
        ctrl.abort();
        overlay.remove();
      }

      fetch(url, {signal: ctrl.signal}).then(function(r) {
        if (!r.ok) throw new Error('HTTP ' + r.status);
        var reader = r.body.getReader();
        var dec = new TextDecoder();
        var buf = '';
        function pump() {
          return reader.read().then(function(res) {
            if (res.done) { return; }
            buf += dec.decode(res.value, {stream: true});
            var idx;
            while ((idx = buf.indexOf('\n\n')) >= 0) {
              processModalEvent(buf.slice(0, idx));
              buf = buf.slice(idx + 2);
            }
            return pump();
          });
        }
        return pump();
      }).catch(function(err) {
        if (err.name === 'AbortError') return;
        body.innerHTML = '<div class="ui-pl-modal-status" style="color:var(--danger,#f85149)">Failed: ' +
          (err.message || err) + '</div>';
      });

      function processModalEvent(raw) {
        var lines = raw.split('\n');
        var ev = 'message';
        var dataStr = '';
        for (var i = 0; i < lines.length; i++) {
          var l = lines[i];
          if (l.indexOf('event: ') === 0) ev = l.slice(7);
          else if (l.indexOf('data: ') === 0) dataStr += (dataStr ? '\n' : '') + l.slice(6);
        }
        if (!dataStr) return;
        var data = {};
        try { data = JSON.parse(dataStr); } catch (_) {}
        // Legacy report stream uses Type field on anonymous events.
        var type = ev !== 'message' ? ev : (data.Type || data.type || '');
        switch (type) {
          case 'report_header':
          case 'header':
            // Headline + topic + confidence on top of the modal.
            body.innerHTML = '';
            var headline = data.AgainstPosition || data.Topic || data.headline || '';
            if (headline) {
              body.appendChild(el('h2', {class: 'ui-pl-modal-headline'}, [headline]));
            }
            if (data.Topic && data.AgainstPosition) {
              var topicLine = data.Topic.length > 150 ? data.Topic.slice(0, 150) + '…' : data.Topic;
              body.appendChild(el('div', {class: 'ui-pl-modal-sub'}, ['Topic: ' + topicLine]));
            }
            if (data.Summary || data.Body) {
              body.appendChild(el('div', {class: 'ui-pl-modal-sub'},
                ['Generated: ' + (data.Summary || '') + (data.Body ? ' · From source: ' + data.Body : '')]));
            }
            if (data.ForPosition) {
              body.appendChild(el('div', {class: 'ui-pl-modal-sub'}, ['Confidence: ' + data.ForPosition]));
            }
            body.appendChild(el('div', {class: 'ui-pl-modal-content'}));
            break;
          case 'report_status':
          case 'status':
            var prior = body.querySelector('.ui-pl-modal-status');
            if (prior) prior.remove();
            body.appendChild(el('div', {class: 'ui-pl-modal-status'},
              [(data.Summary || data.text || ''), el('span', {class: 'ui-pl-spinner'})]));
            break;
          case 'report_stream':
          case 'chunk':
            var statusEl = body.querySelector('.ui-pl-modal-status');
            if (statusEl) statusEl.remove();
            streamRaw += (data.Body || data.text || '');
            body.dataset.raw = streamRaw;
            var content = body.querySelector('.ui-pl-modal-content');
            if (!content) {
              content = el('div', {class: 'ui-pl-modal-content'});
              body.appendChild(content);
            }
            content.innerHTML = mdToHTML(streamRaw);
            // Don't auto-scroll-to-bottom on each chunk — that
            // pushes the overlay past the headline and forces the
            // user to scroll back up. Scroll to top on done instead.
            break;
          case 'report_replace':
            streamRaw = data.Body || '';
            body.dataset.raw = streamRaw;
            var c2 = body.querySelector('.ui-pl-modal-content');
            if (c2) c2.innerHTML = mdToHTML(streamRaw);
            break;
          case 'report_done':
          case 'done':
            headerActions.style.display = '';
            var s = body.querySelector('.ui-pl-modal-status');
            if (s) s.remove();
            // Scroll the overlay back to the top of the report so
            // the user lands on the headline. Defer once so the
            // last chunk's layout has settled.
            requestAnimationFrame(function() {
              overlay.scrollTop = 0;
              modal.scrollTop = 0;
            });
            break;
          case 'error':
            body.innerHTML = '<div class="ui-pl-modal-status" style="color:var(--danger,#f85149)">' +
              (data.Body || data.message || 'Error') + '</div>';
            break;
        }
      }
    }

    function renderActions(sessionId) {
      actionsBar.innerHTML = '';
      if (!sessionId || !cfg.actions || !cfg.actions.length) {
        // Cancel button still belongs in the bar (when running) — re-
        // append it so a session-less reconnect or running-but-no-
        // actions config still has a Cancel.
        actionsBar.appendChild(cancelBtn);
        if (cancelBtn.style.display === 'none') {
          actionsBar.style.display = 'none';
        }
        return;
      }
      actionsBar.style.display = '';
      var sessionRec = sessionsByID[sessionId] || {};
      cfg.actions.forEach(function(a) {
        // ShowIfField — skip the action when the named summary field
        // on this session record is falsy. Lets apps hide buttons
        // that don't apply to every session (e.g. "Descendants"
        // only on parent records).
        if (a.show_if_field && !sessionRec[a.show_if_field]) return;
        if (a.hide_if_field && sessionRec[a.hide_if_field]) return;
        // URL placeholder substitution: {id} → current session id;
        // {<FieldName>} → that field on the session record. Lets
        // actions reference cross-session pointers like ParentID
        // (Back-to-parent button) without app-specific runtime code.
        var url = (a.url || '').replace(/\{([A-Za-z_][A-Za-z0-9_]*)\}/g, function(_, key) {
          if (key === 'id') return encodeURIComponent(sessionId);
          var v = sessionRec[key];
          return v != null ? encodeURIComponent(String(v)) : '';
        });
        var btnClass = 'ui-pl-btn ' + (a.variant || 'secondary');
        var btn;
        var method = a.method || 'open';
        if (method === 'open') {
          btn = el('a', {
            class: btnClass, href: url, target: '_blank', rel: 'noopener',
            title: a.title || a.label,
            style: 'text-decoration:none',
          }, [a.label]);
        } else {
          btn = el('button', {class: btnClass, title: a.title || a.label, onclick: function() {
            if (a.confirm && !confirm(a.confirm)) return;
            if (method === 'copy') {
              var fullURL = window.location.origin + window.location.pathname + url;
              if (a.url && /^https?:/i.test(a.url)) fullURL = url;
              else if (a.url && a.url.indexOf('?') === 0) fullURL = window.location.href.split('?')[0] + url;
              else if (a.url && a.url.indexOf('#') === 0) fullURL = window.location.href.split('#')[0] + url;
              // Inline button feedback — flip the label to "Copied!"
              // for ~1.5s then revert. Keeps the action local instead
              // of bouncing the user's eye to a corner toast.
              var origLabel = btn.textContent;
              navigator.clipboard.writeText(fullURL).then(function() {
                btn.textContent = 'Copied!';
                btn.classList.add('copied');
              }).catch(function() {
                btn.textContent = 'Copy failed';
              }).then(function() {
                setTimeout(function() {
                  btn.textContent = origLabel;
                  btn.classList.remove('copied');
                }, 1500);
              });
              return;
            }
            if (method === 'post') {
              btn.disabled = true;
              fetch(url, {method: 'POST'}).then(function(r) {
                if (!r.ok) return r.text().then(function(t){ throw new Error(t || ('HTTP ' + r.status)); });
                showToast(a.label + ' done');
                loadSessions();
              }).catch(function(err){ showToast(a.label + ' failed: ' + err.message); })
                .then(function(){ btn.disabled = false; });
              return;
            }
            if (method === 'stream') {
              clearTranscript();
              setRunning(true);
              streamFrom(url, {method: 'POST', headers: {'Content-Type': 'application/json'}, body: '{}'});
              return;
            }
            if (method === 'modal') {
              openStreamModal(a, url);
              return;
            }
            if (method === 'related') {
              openRelatedPopover(a, url, btn);
              return;
            }
            if (method === 'load') {
              // Load the session whose id is in url (already
              // substituted from a {FieldName} placeholder) into
              // the current panel. Used for "Back to parent" style
              // cross-session navigation. Decode here because the
              // placeholder substitution URL-encoded the value.
              var targetID = decodeURIComponent(url);
              if (targetID) openSession(targetID);
              return;
            }
            if (method === 'client') {
              // Browser-side action — dispatch by name to a handler
              // registered via window.uiRegisterClientAction. The
              // URL field carries the action name (e.g. "print_
              // transcript"); the handler receives ({sessionId,
              // sessionRec, button}) so it can inspect state /
              // toggle UI / etc.
              var actionName = decodeURIComponent(url);
              var fn = window.UIClientActions && window.UIClientActions[actionName];
              if (typeof fn === 'function') {
                fn({sessionId: sessionId, sessionRec: sessionRec, button: btn, action: a});
              } else {
                showToast('No handler for client action: ' + actionName);
              }
              return;
            }
          }}, [a.label]);
        }
        actionsBar.appendChild(btn);
      });
      // Cancel pinned to the far right via margin-left:auto on the
      // .ui-pl-btn-cancel class. Appended last so flex order matches
      // visual order (action buttons left, Cancel right).
      actionsBar.appendChild(cancelBtn);
      // Re-applies running-state disabling to the freshly-built
      // buttons. Without this, a renderActions call mid-run would
      // produce live (clickable) action buttons.
      applyActionDisabled();
    }
    wrap.appendChild(side);
    wrap.appendChild(main);
    wrap.appendChild(drawerBackdrop);

    function showToast(msg) {
      var t = el('div', {class: 'ui-toast'}, [msg]);
      document.body.appendChild(t);
      setTimeout(function(){ t.remove(); }, 3000);
    }

    function clearTranscript() {
      blockEls = {};
      transcript.innerHTML = '';
    }

    // ensureBlock dispatches to a per-type renderer, falling back to
    // plain "text" if the block.type isn't one we know about.
    // Renderers must return {wrap, body, raw, done, data} where body
    // is the element chunk events should accumulate text into.
    function ensureBlock(id, type, data) {
      if (blockEls[id]) return blockEls[id];
      if (emptyHint.parentNode === transcript) emptyHint.remove();
      // Drop any live status before a structural block lands —
      // the new block IS the next thing, so the "Researching…"
      // status is stale by definition. Matches legacy arena
      // behavior: a real event always clears the prior spinner.
      var prior = transcript.querySelector('.ui-pl-status');
      if (prior) prior.remove();
      // ctx carries panel-level flags the renderers might consult
      // without reaching into pipeline_panel's closure scope. Apps
      // that ship their own renderers via Page.ExtraHeadHTML rely
      // on this — they can't see cfg directly, but they receive
      // ctx.markdown / ctx.cfg here.
      var ctx = {markdown: cfg.markdown, cfg: cfg};
      var renderer = blockRenderers[type] || blockRenderers.text;
      var rec = renderer(data || {type: type}, ctx);
      if (!rec || !rec.wrap) {
        // Defensive: never let a bad renderer leave us blockless.
        rec = blockRenderers.text(data || {type: type}, ctx);
      }
      rec.raw = '';
      rec.done = false;
      rec.data = data || {};
      transcript.appendChild(rec.wrap);
      blockEls[id] = rec;
      scrollTranscript();
      return rec;
    }

    function appendChunk(id, text) {
      var rec = blockEls[id] || ensureBlock(id, 'text', {});
      rec.raw += text;
      // Live render as plain text so partial markdown doesn't break
      // intermediate paint. finalizeBlock runs mdToHTML on done.
      if (rec.body) rec.body.textContent = rec.raw;
      scrollTranscript();
    }

    function finalizeBlock(id) {
      var rec = blockEls[id];
      if (!rec || rec.done) return;
      rec.done = true;
      if (rec.body && cfg.markdown && rec.raw.trim()) {
        rec.body.innerHTML = mdToHTML(rec.raw);
      }
      // Per-renderer onDone hook for any final chrome (e.g. drop the
      // streaming spinner, finalize sources panel).
      if (rec.onDone) rec.onDone(rec);
      scrollTranscript();
    }

    function applyBlockMeta(id, meta) {
      var rec = blockEls[id];
      if (!rec || !rec.onMeta) return;
      rec.onMeta(rec, meta || {});
      scrollTranscript();
    }

    // -------- Block renderers ----------------------------------------------
    // Each renderer takes the block's data object and returns
    // {wrap, body, onMeta?, onDone?}. wrap is the DOM node appended
    // to the transcript; body is where chunk text accumulates;
    // onMeta runs on block_meta events (for sources, summary, etc.);
    // onDone runs on block_done.
    //
    // The registry lives on the window object so apps can register
    // their own renderers from JS shipped via Page.ExtraHeadHTML.
    // Generic types (text, expandable) are registered below; app-
    // specific types (verdict, argument, etc.) per app
    // live in the app's package and load before pipeline_panel
    // mounts. Pipeline_panel takes a snapshot at mount time so a
    // late-arriving app payload doesn't surprise an already-rendered
    // panel; apps should declare their renderers in ExtraHeadHTML
    // (loaded synchronously in the document head) rather than via
    // deferred scripts.
    // Snapshot the global registry at mount time. The window-level
    // registry was initialized at runtime IIFE top so apps loading
    // via Page.ExtraHeadHTML can register before this point.
    var blockRenderers = Object.assign({}, window.UIBlockRenderers || {});

    blockRenderers.text = function(d) {
      // Optional class hint — bridges can declare a CSS class on the
      // block to scope styles (e.g. "ui-pl-final-report" tightens
      // heading→body spacing for synthesized reports).
      // Sanitized via a strict pattern so the wire format can't
      // inject arbitrary attributes.
      var classes = 'ui-pl-block ui-pl-block-text';
      if (d.class && /^[A-Za-z0-9 _-]+$/.test(d.class)) {
        classes += ' ' + d.class;
      }
      var wrap = el('div', {class: classes});
      // Always create the header so block_meta can rewrite the
      // title later (e.g. "Setup phase" → "Complete"
      // when the run finishes). Empty header collapses to nothing
      // visible, so this is safe for blocks that ship without a
      // title.
      var hdr = el('div', {class: 'ui-pl-block-h'}, [d.title || '']);
      if (!d.title) hdr.style.display = 'none';
      wrap.appendChild(hdr);
      var body = el('div', {class: 'ui-pl-block-body'});
      wrap.appendChild(body);
      return {
        wrap: wrap, body: body,
        onMeta: function(rec, meta) {
          if (typeof meta.title === 'string') {
            hdr.textContent = meta.title;
            hdr.style.display = meta.title ? '' : 'none';
          }
        },
      };
    };

    // App-specific renderers (round_header, section_header,
    // argument, verdict) live in each app's web_assets.go.
    // Loaded into window.UIBlockRenderers before pipeline_panel
    // mounts via the app page's ExtraHeadHTML.

    // expandable — generic collapsible card, reused for "Expert
    // Consensus Baseline" and similar named writeups.
    blockRenderers.expandable = function(d) {
      var wrap = el('div', {class: 'ui-pl-expandable'});
      wrap.addEventListener('click', function(){ wrap.classList.toggle('expanded'); });
      var head = el('div', {class: 'ui-pl-expandable-h'});
      // Title can be multi-line (an app payload may emit a
      // "Question: …\nAnswer: …" summary). Split on newlines
      // and render each as its own <div> so the lines stack
      // instead of collapsing whitespace into a single run.
      var nameWrap = el('span', {class: 'ui-pl-expandable-title'});
      var titleStr = String(d.title || '');
      var lines = titleStr.split('\n');
      for (var li = 0; li < lines.length; li++) {
        var line = lines[li];
        if (li === 0) {
          nameWrap.appendChild(document.createTextNode(line));
        } else {
          nameWrap.appendChild(el('div', {class: 'ui-pl-expandable-subtitle'}, [line]));
        }
      }
      head.appendChild(nameWrap);
      head.appendChild(el('span', {class: 'ui-pl-expandable-toggle'}, ['show details']));
      wrap.appendChild(head);
      var body = el('div', {class: 'ui-pl-expandable-body'});
      if (d.body) {
        if (cfg.markdown) body.innerHTML = mdToHTML(d.body);
        else body.textContent = d.body;
      }
      wrap.appendChild(body);
      return {wrap: wrap, body: body};
    };

    function scrollTranscript() {
      var pin = function() { transcript.scrollTop = transcript.scrollHeight; };
      pin();
      requestAnimationFrame(pin);
      setTimeout(pin, 100);
    }

    var runningState = false;
    // applyActionDisabled greys out every action button in actionsBar
    // except the Cancel button while a run is in flight. Buttons get
    // the native disabled attribute; <a> action buttons can't be
    // disabled natively, so they get the .is-disabled class which sets
    // pointer-events:none + opacity to match. Called from setRunning
    // and from renderActions so a re-render mid-run keeps state.
    function applyActionDisabled() {
      var kids = actionsBar.children;
      for (var i = 0; i < kids.length; i++) {
        var k = kids[i];
        if (k === cancelBtn || k.classList.contains('ui-pl-btn-cancel')) continue;
        if (!k.classList.contains('ui-pl-btn')) continue;
        if (runningState) {
          if (k.tagName === 'BUTTON') k.disabled = true;
          k.classList.add('is-disabled');
          k.setAttribute('aria-disabled', 'true');
        } else {
          if (k.tagName === 'BUTTON') k.disabled = false;
          k.classList.remove('is-disabled');
          k.removeAttribute('aria-disabled');
        }
      }
    }
    function setRunning(running) {
      runningState = !!running;
      submitBtn.style.display = running ? 'none' : '';
      cancelBtn.style.display = running ? '' : 'none';
      Object.keys(formInputs).forEach(function(k){ formInputs[k].disabled = !!running; });
      if (prefillBtn) prefillBtn.disabled = !!running;
      // Surface the actions toolbar when a run is in flight so the
      // Cancel button is reachable even before any session_id event
      // populates the per-session URLs.
      if (running) actionsBar.style.display = '';
      applyActionDisabled();
    }

    // setFormVisible toggles the whole submit form (topic, rounds,
    // suggest, start). Hidden when viewing a saved session — the
    // user clicks "+ New" in the sidebar to bring it back.
    function setFormVisible(visible) {
      form.style.display = visible ? '' : 'none';
    }

    // streamFrom drives the SSE reader for both fresh submits and
    // reconnects. fetchOpts is whatever fetch() needs (method/body
    // for POST, default GET for reconnect).
    function streamFrom(url, fetchOpts) {
      activeStream = new AbortController();
      fetchOpts = fetchOpts || {};
      fetchOpts.signal = activeStream.signal;
      return fetch(url, fetchOpts).then(function(r) {
        if (!r.ok) return r.text().then(function(t){ throw new Error(t || ('HTTP ' + r.status)); });
        var reader = r.body.getReader();
        var decoder = new TextDecoder();
        var buf = '';
        function pump() {
          return reader.read().then(function(res) {
            if (res.done) { finish(); return; }
            buf += decoder.decode(res.value, {stream: true});
            var idx;
            while ((idx = buf.indexOf('\n\n')) >= 0) {
              var raw = buf.slice(0, idx);
              buf = buf.slice(idx + 2);
              processEvent(raw);
            }
            return pump();
          });
        }
        return pump();
      }).catch(function(err) {
        if (err.name !== 'AbortError') showToast('Failed: ' + err.message);
        finish();
      });
    }

    function doSubmit() {
      // Validate required fields and gather body.
      var body = {};
      var missing = null;
      (cfg.fields || []).forEach(function(f) {
        var inp = formInputs[f.name];
        // Toggle/checkbox fields contribute a boolean from .checked
        // — taking .value on a checkbox returns "on"/"off" strings.
        if (f.type === 'toggle') {
          body[f.name] = !!inp.checked;
          return;
        }
        var v = inp.value;
        if (f.required && !String(v || '').trim()) { missing = missing || f.name; return; }
        if (v === '' || v == null) return;
        if (f.type === 'number') {
          var n = Number(v);
          if (!isNaN(n)) v = n;
        }
        body[f.name] = v;
      });
      if (missing) { showToast('Required: ' + missing); return; }

      currentSessionId = '';
      clearTranscript();
      // Use the title field's value (e.g. "topic") as the session
      // title strip immediately so users see what's running.
      setFormVisible(false);
      var titleField = cfg.session_title_field || 'Title';
      var liveTitle = body[titleField.toLowerCase()] || body.topic || body.subject || body.message || '';
      setSessionTitle(liveTitle);
      setRunning(true);
      streamFrom(cfg.submit_url, {
        method: 'POST',
        headers: {'Content-Type': 'application/json'},
        body: JSON.stringify(body),
      });
    }

    // tryReconnect attempts to attach to a live pipeline run identified
    // by a ?session={id} URL query param. On success, streams events
    // into the transcript like a fresh submit; on failure (HTTP 404 →
    // session not live), falls back to loading the saved record.
    function tryReconnect(sessionId) {
      if (!cfg.reconnect_url || !sessionId) return false;
      var url = cfg.reconnect_url.replace('{id}', encodeURIComponent(sessionId));
      currentSessionId = sessionId;
      clearTranscript();
      setFormVisible(false);
      renderActions(sessionId);
      setRunning(true);
      // Set the page title from whatever session metadata we have.
      // sessionsByID is populated by the prior loadSessions() call;
      // if the entry isn't there yet (race) fall back to fetching
      // /api/sessions/{id} just for the title so the operator sees
      // the question they're reconnecting to instead of "New run".
      var cached = sessionsByID[sessionId];
      if (cached && cached[titleF]) {
        mobileTitle.textContent = cached[titleF];
        setSessionTitle(cached[titleF]);
      } else if (cfg.session_load_url) {
        var loadURL = cfg.session_load_url.replace('{id}', encodeURIComponent(sessionId));
        fetchJSON(loadURL).then(function(s) {
          var t = (s && s[titleF]) || '';
          if (t) {
            mobileTitle.textContent = t;
            setSessionTitle(t);
          }
        }).catch(function(){});
      }
      activeStream = new AbortController();
      fetch(url, {signal: activeStream.signal}).then(function(r) {
        if (r.status === 404) {
          // Not live anymore — load the saved record instead.
          setRunning(false);
          openSession(sessionId);
          return;
        }
        if (!r.ok) return r.text().then(function(t){ throw new Error(t || ('HTTP ' + r.status)); });
        var reader = r.body.getReader();
        var decoder = new TextDecoder();
        var buf = '';
        function pump() {
          return reader.read().then(function(res) {
            if (res.done) { finish(); return; }
            buf += decoder.decode(res.value, {stream: true});
            var idx;
            while ((idx = buf.indexOf('\n\n')) >= 0) {
              var raw = buf.slice(0, idx);
              buf = buf.slice(idx + 2);
              processEvent(raw);
            }
            return pump();
          });
        }
        return pump();
      }).catch(function(err) {
        if (err.name !== 'AbortError') showToast('Reconnect failed: ' + err.message);
        finish();
      });
      return true;
    }

    function processEvent(raw) {
      var lines = raw.split('\n');
      var ev = 'message';
      var dataStr = '';
      for (var i = 0; i < lines.length; i++) {
        var l = lines[i];
        if (l.indexOf('event: ') === 0) ev = l.slice(7);
        else if (l.indexOf('data: ') === 0) dataStr += (dataStr ? '\n' : '') + l.slice(6);
      }
      var data = {};
      if (dataStr) { try { data = JSON.parse(dataStr); } catch(e) {} }
      switch (ev) {
        case 'session':
          if (data.id) {
            currentSessionId = data.id;
            renderActions(data.id);
          }
          break;
        case 'block':
          // Pass the full data object — renderer pulls type-specific
          // fields (side, round, summary, statement, etc.) out of it.
          var bid = data.id || ('b' + Object.keys(blockEls).length);
          ensureBlock(bid, data.type || 'text', data);
          // When the block event ships a full body (one-shot blocks
          // that don't stream — e.g. a synthesized report),
          // populate rec.raw so block_done's finalizeBlock has
          // something to mdToHTML. Without this the body field on the
          // block event is silently dropped and the block renders
          // empty.
          if (data.body) {
            var brec = blockEls[bid];
            if (brec && brec.body) {
              brec.raw = String(data.body);
              brec.body.textContent = brec.raw;
            }
          }
          if (data.type === 'verdict') liveVerdictBid = bid;
          break;
        case 'block_meta':
          applyBlockMeta(data.id, data);
          break;
        case 'chunk':
          appendChunk(data.id || 'main', data.text || '');
          break;
        case 'chunk_replace':
          // Replace the entire body of a block with new content.
          // Accepts either "text" (legacy streaming chunks)
          // or "body" (new bridges emitting markdown). When
          // cfg.markdown is on AND the payload is full markdown
          // (as opposed to in-flight chunked plain text), render
          // through mdToHTML so headings, lists, and links land
          // styled.  For streaming chunked text we still want
          // textContent so partial markdown doesn't break paint.
          var rec = blockEls[data.id || 'main'];
          if (rec) {
            var raw = String((data.body != null ? data.body : data.text) || '');
            rec.raw = raw;
            // The "body" field signals the caller is sending a
            // complete current snapshot (as opposed to streamed
            // chunks). Render it as markdown so contained-window
            // sections look polished.
            if (data.body != null && cfg.markdown) {
              rec.body.innerHTML = mdToHTML(raw);
            } else {
              rec.body.textContent = raw;
            }
            scrollTranscript();
          }
          break;
        case 'block_done':
          finalizeBlock(data.id);
          break;
        case 'block_remove':
          // Drop a block entirely — DOM node out, blockEls entry
          // gone. Used when an app's flow renders a transient
          // cluster that an app may emit as the
          // should disappear once the underlying work completes
          // and the next block carries the result. Generic — any
          // app can emit this for any transient block id.
          var rm = blockEls[data.id];
          if (rm) {
            if (rm.wrap && rm.wrap.parentNode) rm.wrap.remove();
            delete blockEls[data.id];
          }
          break;
        case 'status':
          // Single-line status that REPLACES any prior status — same
          // "current focus" — and the arena removed the prior status
          // before inserting the new one. Only the most recent
          // "what's happening right now" stays visible; the trail
          // would just clutter the transcript.
          var prior = transcript.querySelector('.ui-pl-status');
          if (prior) prior.remove();
          if (data.text) {
            var s = el('div', {class: 'ui-pl-status ui-pl-status-live'});
            s.appendChild(el('span', {class: 'ui-pl-status-text'}, [data.text]));
            s.appendChild(el('span', {class: 'ui-pl-spinner'}));
            transcript.appendChild(s);
            scrollTranscript();
          }
          break;
        case 'error':
          // Drop any live status when an error lands.
          var liveStatus = transcript.querySelector('.ui-pl-status');
          if (liveStatus) liveStatus.remove();
          var e = el('div', {class: 'ui-chat-error'}, [data.message || 'Error']);
          transcript.appendChild(e);
          scrollTranscript();
          break;
        case 'done':
          // Finalize any block still open.
          Object.keys(blockEls).forEach(function(k){ finalizeBlock(k); });
          // Auto-scroll to the verdict block (mirrors the
          // history-click behavior). Defer past finalize's own
          // scrollTranscript pins (rAF + 100ms) so the verdict
          // scroll wins; also re-pin at 300ms in case anything
          // shifts layout afterward.
          if (liveVerdictBid && blockEls[liveVerdictBid]) {
            var doScroll = function() {
              var rec = blockEls[liveVerdictBid];
              if (!rec || !rec.wrap) return;
              var wRect = rec.wrap.getBoundingClientRect();
              var tRect = transcript.getBoundingClientRect();
              transcript.scrollTop += (wRect.top - tRect.top);
            };
            setTimeout(doScroll, 150);
            setTimeout(doScroll, 350);
          }
          break;
      }
    }

    function finish() {
      setRunning(false);
      activeStream = null;
      loadSessions();
    }

    function doCancel() {
      if (activeStream) activeStream.abort();
      if (cfg.cancel_url && currentSessionId) {
        // LiveSessions.HandleCancel reads ?id= (legacy convention).
        fetch(cfg.cancel_url + '?id=' + encodeURIComponent(currentSessionId), {method: 'POST'}).catch(function(){});
      }
      finish();
    }

    function openSession(id) {
      currentSessionId = id || '';
      clearTranscript();
      transcript.appendChild(emptyHint);
      // Reset form to defaults.
      (cfg.fields || []).forEach(function(f) { formInputs[f.name].value = f.default || ''; });
      setRunning(false);
      renderActions(id);
      setSessionTitle('');
      // No id means "new run" — show form, hide actions toolbar
      // (handled by renderActions). With an id, hide the form so
      // the saved transcript gets the full main column.
      setFormVisible(!id);
      if (!id) {
        mobileTitle.textContent = 'New run';
        loadSessions();
        return;
      }
      var url = cfg.session_load_url.replace('{id}', encodeURIComponent(id));
      fetchJSON(url).then(function(s) {
        var title = (s && s[titleF]) || 'Untitled';
        mobileTitle.textContent = title;
        setSessionTitle(title);
        var blocks = (s && s[blocksF]) || [];
        if (blocks.length) emptyHint.remove();
        var verdictBid = null;
        blocks.forEach(function(b, i) {
          var bid = b.id || ('b' + i);
          ensureBlock(bid, b.type || 'text', b);
          var rec = blockEls[bid];
          if (rec && rec.onMeta && (b.summary || (b.sources && b.sources.length))) {
            rec.onMeta(rec, {summary: b.summary, sources: b.sources || []});
          }
          // verdict's analysis is stored in body for that type;
          // argument's body field is the full text to render.
          var content = b.body || b.analysis || '';
          if (content && rec && rec.body) {
            rec.raw = content;
            if (cfg.markdown) {
              var rendered = mdToHTML(content);
              rec.body.innerHTML = rendered;
              // Diagnostic — surface the gap when the rendered HTML
              // has substantially less content than the raw markdown
              // (mdToHTML regex catastrophic-backtracking on certain
              // markdown patterns). Only logs on big shrinks so
              // normal markdown doesn't spam the console.
              if (window.console && rec.body.textContent.length < content.length * 0.6) {
                console.warn('[ui] block body renders shorter than source —',
                  'raw chars:', content.length,
                  'rendered chars:', rec.body.textContent.length,
                  'block:', bid);
              }
            } else {
              rec.body.textContent = content;
            }
          }
          finalizeBlock(bid);
          if (b.type === 'verdict') verdictBid = bid;
        });
        loadSessions();
        // Saved sessions are typically opened to read the verdict —
        // jump straight to it. finalizeBlock calls scrollTranscript
        // for each block (which schedules pins at now / rAF / 100ms),
        // so we have to fire AFTER the trailing pin or the verdict
        // scroll gets clobbered by scroll-to-bottom. 150ms is enough
        // to let the trailing pin land first.
        var doVerdictScroll = function() {
          if (verdictBid && blockEls[verdictBid]) {
            // offsetTop is relative to the nearest positioned ancestor
            // (which may not be the transcript). Compute the in-
            // container delta with bounding rects so the verdict
            // lands at the top edge of the transcript exactly.
            var wrap = blockEls[verdictBid].wrap;
            var wRect = wrap.getBoundingClientRect();
            var tRect = transcript.getBoundingClientRect();
            transcript.scrollTop += (wRect.top - tRect.top);
          } else {
            transcript.scrollTop = 0;
          }
        };
        setTimeout(doVerdictScroll, 150);
        // Also pin once more at 300ms in case anything else fires.
        setTimeout(doVerdictScroll, 300);
        // Add export button if configured.
        if (cfg.session_export_url) {
          var url = cfg.session_export_url.replace('{id}', encodeURIComponent(id));
          var exportBtn = el('a', {
            class: 'ui-pl-btn secondary',
            href: url, target: '_blank',
            style: 'display:inline-block;margin-top:0.5rem;text-decoration:none',
          }, [cfg.session_export_label || 'Export']);
          transcript.appendChild(exportBtn);
        }
      }).catch(function(err) {
        transcript.appendChild(el('div', {class: 'ui-chat-error'}, ['Load failed: ' + err.message]));
      });
    }

    // sessionsByID caches the most recent sidebar fetch keyed by id
    // so renderActions can read per-session fields (HasDescendants
    // etc.) without a second round-trip. Populated by every
    // loadSessions() call; renderActions reads it on demand.
    var sessionsByID = {};
    function loadSessions() {
      fetchJSON(cfg.sessions_list_url).then(function(list) {
        sideList.innerHTML = '';
        sessionsByID = {};
        (list || []).forEach(function(s){ if (s && s[idF]) sessionsByID[s[idF]] = s; });
        if (!list || !list.length) {
          if (cfg.bulk_select && bulkState.mode) {
            renderBulkBar([], sideList, bulkState, bulkSelected,
              function(s){ return s[idF]; }, loadSessions, function(){});
          }
          sideList.appendChild(el('div', {class: 'ui-chat-empty', style: 'padding:0.5rem'}, ['No runs yet.']));
          return;
        }
        list.sort(function(a, b){ return String(b[dateF] || '').localeCompare(String(a[dateF] || '')); });
        var ids = {}; list.forEach(function(s){ ids[s[idF]] = true; });
        Object.keys(bulkSelected).forEach(function(k){ if (!ids[k]) delete bulkSelected[k]; });

        if (cfg.bulk_select) {
          renderBulkBar(list, sideList, bulkState, bulkSelected,
            function(s){ return s[idF]; },
            loadSessions,
            function() {
              var sel = Object.keys(bulkSelected);
              if (!sel.length) return;
              if (!confirm('Delete ' + sel.length + ' run(s) permanently?')) return;
              Promise.all(sel.map(function(id) {
                var u = cfg.session_delete_url.replace('{id}', encodeURIComponent(id));
                return fetch(u, {method: 'DELETE'});
              })).then(function() {
                if (bulkSelected[currentSessionId]) openSession(null);
                bulkSelected = {};
                bulkState.mode = false;
                if (sideSelectBtn) {
                  sideSelectBtn.classList.remove('active');
                  sideSelectBtn.textContent = 'Select';
                }
                loadSessions();
              }).catch(function(err) { showToast('Delete failed: ' + err.message); });
            });
        }

        list.forEach(function(s) {
          var inMode = cfg.bulk_select && bulkState.mode;
          var selected = !!bulkSelected[s[idF]];
          var item = el('div', {
            class: 'ui-chat-side-item' +
              (s[idF] === currentSessionId ? ' active' : '') +
              (inMode ? ' selectable' : '') +
              (selected ? ' selected' : ''),
            onclick: function(ev) {
              if (ev.target.classList.contains('ui-chat-side-del')) return;
              if (inMode) {
                if (bulkSelected[s[idF]]) delete bulkSelected[s[idF]];
                else bulkSelected[s[idF]] = true;
                loadSessions();
                return;
              }
              openSession(s[idF]);
              closeDrawer();
            },
          });
          var textWrap = el('div', {class: 'ui-chat-side-text'});
          textWrap.appendChild(el('div', {class: 'ui-chat-side-title'}, [String(s[titleF] || 'Untitled')]));
          // Optional meta fields per session. To keep rows compact
          // (more visible at once on both desktop and mobile), all
          // "pill"/"badge" style fields share ONE horizontal row;
          // each "text" field gets a single ellipsized line.
          var pillRow = null;
          (cfg.session_meta_fields || []).forEach(function(mf) {
            var raw = s[mf.field];
            if (raw === undefined || raw === null || raw === '') return;
            var text = String(raw);
            if (mf.truncate && text.length > mf.truncate) {
              text = text.slice(0, mf.truncate - 1).trimEnd() + '…';
            }
            var style = mf.style || 'text';
            if (style === 'badge' || style === 'pill') {
              if (!pillRow) {
                pillRow = el('div', {class: 'ui-pl-side-pillrow'});
                textWrap.appendChild(pillRow);
              }
              var pill = el('span', {class: 'ui-pl-side-pill'});
              if (mf.variants) {
                var key = String(raw).toLowerCase();
                var color = mf.variants[key];
                if (color) pill.style.cssText = 'background:' + color + '20;color:' + color + ';border-color:' + color + '50';
              }
              pill.appendChild(document.createTextNode(text));
              if (style === 'pill') pill.classList.add('pill');
              pillRow.appendChild(pill);
            } else {
              var line = el('div', {class: 'ui-pl-side-metaline'});
              if (mf.label) {
                line.appendChild(el('span', {class: 'ui-pl-side-metalabel'}, [mf.label + ' ']));
              }
              line.appendChild(document.createTextNode(text));
              textWrap.appendChild(line);
            }
          });
          // Date at the bottom — share the row with pills if there
          // are any, otherwise its own muted line.
          if (pillRow) {
            pillRow.appendChild(el('span', {class: 'ui-chat-side-meta', style: 'margin-left:auto'}, [relTime(s[dateF])]));
          } else {
            textWrap.appendChild(el('div', {class: 'ui-chat-side-meta'}, [relTime(s[dateF])]));
          }
          item.appendChild(textWrap);
          if (!inMode) {
            var delBtn = el('button', {
              class: 'ui-chat-side-del', title: 'Delete',
              onclick: function(ev) {
                ev.stopPropagation();
                if (!confirm('Delete this run?')) return;
                var u = cfg.session_delete_url.replace('{id}', encodeURIComponent(s[idF]));
                fetch(u, {method: 'DELETE'}).then(function() {
                  if (s[idF] === currentSessionId) openSession(null);
                  loadSessions();
                });
              },
            }, ['×']);
            item.appendChild(delBtn);
          }
          sideList.appendChild(item);
        });
      }).catch(function(err) {
        sideList.innerHTML = '';
        sideList.appendChild(el('div', {class: 'ui-chat-error', style: 'padding:0.5rem'}, ['Load failed: ' + err.message]));
      });
    }

    loadSessions();

    // Auto-attach to a live pipeline run if the URL carries a
    // session id. Accepts ?session=, ?id=, ?run= (legacy
    // queue links), ?id=, or ?run= so the runtime works with
    // links produced by the legacy provider URLs that haven't
    // been updated to the new convention.
    try {
      var params = new URLSearchParams(window.location.search);
      // Apps declare their preferred deep-link param via
      // cfg.deep_link_param; the generic "session" / "id" / "run"
      // names are always also honored as fallbacks so older
      // bookmarks and shareable links keep working.
      var sid = '';
      if (cfg.deep_link_param) sid = params.get(cfg.deep_link_param) || '';
      if (!sid) {
        sid = params.get('session') || params.get('id') || params.get('run') || '';
      }
      if (sid) {
        if (cfg.reconnect_url) tryReconnect(sid);
        else openSession(sid);
      }
    } catch (_) {}

    return wrap;
  };

  components.codewriter_panel = function(cfg) {
    var idF   = cfg.id_field   || 'id';
    var nameF = cfg.name_field || 'name';
    var langF = cfg.lang_field || 'lang';
    var codeF = cfg.code_field || 'code';
    var dateF = cfg.date_field || 'date';
    var languages = (cfg.languages && cfg.languages.length)
      ? cfg.languages
      : ['bash','sql','python','powershell','go','regex',''];

    var wrap = el('div', {class: 'ui-cw ui-tw'});
    var side = el('div', {class: 'ui-tw-side'});

    // Collapse state — desktop only. Mobile uses the slide-in drawer
    // mechanism inherited from ChatPanel and ignores this state.
    var sideCollapsed = false;
    try { sideCollapsed = localStorage.getItem('cw.sideCollapsed') === '1'; } catch (_) {}
    var collapseBtn = el('button', {
      class: 'ui-tw-collapse', title: 'Hide snippets list',
      onclick: function(){ toggleCollapse(); },
    }, ['‹']);
    function toggleCollapse() {
      sideCollapsed = !sideCollapsed;
      wrap.classList.toggle('side-collapsed', sideCollapsed);
      collapseBtn.title = sideCollapsed ? 'Show snippets list' : 'Hide snippets list';
      collapseBtn.textContent = sideCollapsed ? '›' : '‹';
      try { localStorage.setItem('cw.sideCollapsed', sideCollapsed ? '1' : '0'); } catch (_) {}
    }

    var sideHdrBuilt = renderSideHeader({
      label:     'Snippets',
      className: 'ui-tw-side-h',
      newTitle:  'New snippet',
      onNew:     function(){ openSnippet(null); },
      onClose:   function(){ closeDrawer(); },
      leftExtras: [collapseBtn],
    });
    var sideHdr  = sideHdrBuilt.elt;
    var sideList = el('div', {class: 'ui-tw-side-list'}, ['Loading…']);
    var sideSearch = makeSideSearch(sideList);
    side.appendChild(sideHdr);
    side.appendChild(sideSearch);
    side.appendChild(sideList);

    var drawer = makeDrawer(side, {
      title:          'New snippet',
      hamburgerTitle: 'Snippets',
      newTitle:       'New snippet',
      onNew:          function(){ openSnippet(null); },
    });
    var mobileTitle    = drawer.mobileTitle;
    var drawerBackdrop = drawer.backdrop;
    var openDrawer  = drawer.openDrawer;
    var closeDrawer = drawer.closeDrawer;

    var main = el('div', {class: 'ui-tw-main ui-cw-main'});
    main.appendChild(drawer.mobileHdr);

    // Toolbar — name + lang + Save / Copy / New. Mirrors the legacy
    // codewriter top bar but lives inside the framework page chrome.
    var nameInput = el('input', {
      type: 'text', class: 'ui-cw-name',
      placeholder: cfg.placeholder_name || 'Snippet name…',
    });
    var langSelect = el('select', {class: 'ui-cw-lang'});
    languages.forEach(function(l) {
      langSelect.appendChild(el('option', {value: l}, [l || 'other']));
    });

    var saveBtn = el('button', {class: 'ui-row-btn primary', onclick: function(){ saveSnippet(); }}, ['Save']);
    var copyBtn = el('button', {class: 'ui-row-btn', onclick: function(){ copyEditor(); }}, ['Copy']);
    var newBtn  = el('button', {class: 'ui-row-btn', onclick: function(){ openSnippet(null); }}, ['New']);
    var varsBtn = el('button', {class: 'ui-row-btn', onclick: function(){ openVarsModal('apply'); }}, ['Variables']);
    var valuesBtn = cfg.values_list_url
      ? el('button', {class: 'ui-row-btn', onclick: function(){ openValuesModal(); }}, ['Values'])
      : null;

    // Revision navigation controls — only built when the snippet
    // record has revision-list / revision-load URLs configured. Hidden
    // when no snippet is open (currentID === null) so the toolbar
    // doesn't show "rev 0/0" on a blank New buffer.
    var revGroup = null, revBackBtn = null, revFwdBtn = null, revIndicator = null, revMarkBtn = null;
    if (cfg.revisions_list_url && cfg.revision_load_url) {
      revBackBtn = el('button', {class: 'ui-row-btn ui-cw-rev-btn', title: 'Previous revision',
        onclick: function(){ navigateRevision(-1); }, disabled: 'true'}, ['◀']);
      revFwdBtn = el('button', {class: 'ui-row-btn ui-cw-rev-btn', title: 'Next revision',
        onclick: function(){ navigateRevision(1); }, disabled: 'true'}, ['▶']);
      revIndicator = el('span', {class: 'ui-cw-rev-ind'}, []);
      revMarkBtn = el('button', {class: 'ui-row-btn ui-cw-rev-mark', title: 'Save current editor content as a new (latest) revision',
        onclick: function(){ markAsLatest(); }, style: 'display:none'}, ['Make Latest']);
      revGroup = el('span', {class: 'ui-cw-rev-group', style: 'display:none'},
        [revMarkBtn, revBackBtn, revIndicator, revFwdBtn]);
    }

    var toolbarKids = [nameInput];
    if (revGroup) toolbarKids.push(revGroup);
    toolbarKids.push(langSelect);
    toolbarKids.push(varsBtn);
    if (valuesBtn) toolbarKids.push(valuesBtn);
    toolbarKids.push(saveBtn);
    toolbarKids.push(copyBtn);
    toolbarKids.push(newBtn);
    var toolbar = el('div', {class: 'ui-cw-toolbar'}, toolbarKids);
    main.appendChild(toolbar);

    // Body row — editor (flex:1) + chat pane (right, fixed-ish width).
    var bodyRow = el('div', {class: 'ui-cw-body'});

    // Editor pane — code textarea fills the available space, with an
    // optional collapsible Context section beneath it for reference
    // material the LLM should see alongside the code on every chat
    // turn.
    var editor = el('textarea', {
      class: 'ui-cw-editor',
      placeholder: cfg.placeholder_code || 'Write or paste code here. Save it for later, or chat with the LLM to generate one.',
      spellcheck: 'false',
    });

    var ctxOpen = true;
    var ctxArrow   = el('span', {class: 'ui-cw-ctx-arrow open'}, ['▸']);
    var ctxLabel   = el('span', {}, [' Context (table schemas, reference docs, notes)']);
    var ctxCurrent = el('span', {class: 'ui-cw-ctx-current'}, []);
    var ctxSaveBtn = el('button', {class: 'ui-cw-ctx-btn',
      onclick: function(ev){ ev.stopPropagation(); saveContext(); }}, ['Save']);
    var ctxLoadBtn = el('button', {class: 'ui-cw-ctx-btn',
      onclick: function(ev){ ev.stopPropagation(); openContextsModal(); }}, ['Load']);
    var ctxActions = el('span', {class: 'ui-cw-ctx-actions'}, [ctxSaveBtn, ctxLoadBtn, ctxCurrent]);
    var ctxToggle  = el('div', {class: 'ui-cw-ctx-toggle',
      onclick: function(){ toggleCtx(); }},
      [ctxArrow, ctxLabel, ctxActions]);
    var ctxEditor  = el('textarea', {
      class: 'ui-cw-ctx-editor',
      placeholder: cfg.placeholder_ctx || 'Paste table schemas, DDL, column descriptions, API docs, or any reference material here. The LLM reads this alongside the code on every chat turn.',
      spellcheck: 'false',
    });
    var ctxPane    = el('div', {class: 'ui-cw-ctx-pane open'}, [ctxEditor]);
    var ctxSection = el('div', {class: 'ui-cw-ctx-section'}, [ctxToggle, ctxPane]);

    // Horizontal drag handle between the code editor and the context
    // section. Dragging resizes the context section's height. Wired
    // to editor.UtilsJS()'s editorStartResize, loaded via the page's
    // ExtraHeadHTML.
    var ctxResizer = el('div', {class: 'ui-cw-ctx-resizer'});
    ctxResizer.addEventListener('mousedown', function(ev) {
      if (typeof window.editorStartResize !== 'function') return;
      window.editorStartResize(ev, 'row', {
        target:    ctxSection,
        container: editorWrap,
        resizer:   ctxResizer,
        min:       80,
        pad:       100,
      });
    });

    var editorWrap = el('div', {class: 'ui-cw-editor-wrap'}, [editor, ctxResizer, ctxSection]);

    function toggleCtx() {
      ctxOpen = !ctxOpen;
      ctxArrow.classList.toggle('open', ctxOpen);
      ctxPane.classList.toggle('open', ctxOpen);
    }

    var currentContextID   = null;
    var currentContextName = null;
    function setCurrentContext(id, name) {
      currentContextID = id || null;
      currentContextName = name || null;
      ctxCurrent.textContent = name ? '[' + name + ']' : '';
    }
    function saveContext() {
      if (!cfg.contexts_list_url) {
        showToast('Saving contexts not configured');
        return;
      }
      var body = ctxEditor.value || '';
      if (!body.trim()) { showToast('Context is empty'); return; }
      var name = prompt('Name this context:', currentContextName || '');
      if (name == null) return;
      name = name.trim();
      if (!name) return;
      fetch(cfg.contexts_list_url, {
        method: 'POST',
        headers: {'Content-Type': 'application/json'},
        body: JSON.stringify({id: currentContextID, name: name, body: body}),
      }).then(function(r) {
        if (!r.ok) return r.text().then(function(t){ throw new Error(t || ('HTTP ' + r.status)); });
        return r.json();
      }).then(function(rec) {
        setCurrentContext(rec.id, rec.name);
        showToast('Context saved');
      }).catch(function(err) {
        showToast('Save failed: ' + err.message);
      });
    }
    function loadContext(id) {
      if (!cfg.context_url) return;
      var url = cfg.context_url.replace('{id}', encodeURIComponent(id));
      fetchJSON(url).then(function(rec) {
        ctxEditor.value = rec.body || '';
        setCurrentContext(rec.id, rec.name);
        if (!ctxOpen) toggleCtx();
        closeModal();
      }).catch(function(err) {
        showToast('Load failed: ' + err.message);
      });
    }
    function deleteContext(id) {
      if (!cfg.context_url) return;
      if (!confirm('Delete this saved context?')) return;
      var url = cfg.context_url.replace('{id}', encodeURIComponent(id));
      fetch(url, {method: 'DELETE'}).then(function() {
        if (currentContextID === id) setCurrentContext(null, null);
        openContextsModal();
      });
    }

    // Chat pane — header + scrollable transcript + input area with
    // dual-send buttons (Chat = discuss only, Edit = propose code).
    var chatPane = el('div', {class: 'ui-cw-chat'});
    var chatHdr  = el('div', {class: 'ui-cw-chat-h'});
    chatHdr.appendChild(el('span', {}, ['Chat']));
    var chatClearBtn = el('button', {class: 'ui-row-btn', onclick: function(){ clearChat(); }}, ['Clear']);
    chatHdr.appendChild(chatClearBtn);

    var chatMessages = el('div', {class: 'ui-cw-chat-msgs'});
    var chatInput = el('textarea', {
      class: 'ui-cw-chat-input', rows: '3',
      placeholder: cfg.placeholder_chat || 'Discuss with Chat, or click Edit to apply changes.',
    });
    var chatBtnTalk = el('button', {class: 'ui-row-btn', title: 'Discuss without changing the editor', onclick: function(){ sendChat('chat'); }}, ['Chat']);
    var chatBtnEdit = el('button', {class: 'ui-row-btn primary', title: 'Propose a change to apply to the editor', onclick: function(){ sendChat('edit'); }}, ['Edit']);
    chatInput.addEventListener('keydown', function(ev) {
      if (ev.key === 'Enter' && !ev.shiftKey) {
        ev.preventDefault();
        sendChat(ev.altKey ? 'chat' : 'edit');
      }
    });
    var chatInputArea = el('div', {class: 'ui-cw-chat-input-area'},
      [chatInput, chatBtnTalk, chatBtnEdit]);
    chatPane.appendChild(chatHdr);
    chatPane.appendChild(chatMessages);
    chatPane.appendChild(chatInputArea);

    // Vertical drag handle between editor wrap and chat pane. Width
    // changes are inline-styled on chatPane; persisted across the
    // session via localStorage so the user's preferred split survives
    // page reloads.
    var chatResizer = el('div', {class: 'ui-cw-chat-resizer'});
    chatResizer.addEventListener('mousedown', function(ev) {
      if (typeof window.editorStartResize !== 'function') return;
      window.editorStartResize(ev, 'col', {
        target:    chatPane,
        container: bodyRow,
        resizer:   chatResizer,
        min:       240,
        pad:       240,
        onEnd: function() {
          try { localStorage.setItem('cw.chatWidth', chatPane.style.width || ''); } catch (_) {}
        },
      });
    });
    try {
      var saved = localStorage.getItem('cw.chatWidth');
      if (saved) chatPane.style.width = saved;
    } catch (_) {}

    bodyRow.appendChild(editorWrap);
    bodyRow.appendChild(chatResizer);
    bodyRow.appendChild(chatPane);
    main.appendChild(bodyRow);

    // Floating expand-tab shown when the sidebar is collapsed. Pinned
    // to the left edge of the main pane so the user can always pop
    // the snippets list back without hunting through menus.
    var expandTab = el('button', {
      class: 'ui-tw-expand', title: 'Show snippets list',
      onclick: function(){ toggleCollapse(); },
    }, ['›']);

    wrap.appendChild(side);
    wrap.appendChild(main);
    wrap.appendChild(expandTab);
    wrap.appendChild(drawerBackdrop);

    // Apply persisted collapse state after the wrap is built.
    if (sideCollapsed) {
      wrap.classList.add('side-collapsed');
      collapseBtn.title = 'Show snippets list';
      collapseBtn.textContent = '›';
    }

    // --- Chat state ---
    var chatHistory = [];
    function appendHistory(role, content) {
      chatHistory.push({role: role, content: content});
      if (chatHistory.length > 40) chatHistory = chatHistory.slice(-40);
    }
    function clearChat() {
      chatMessages.innerHTML = '';
      chatHistory = [];
    }
    function addChatMsg(role, html, copyPayload) {
      var msg = el('div', {class: 'ui-cw-msg ' + role});
      msg.innerHTML = html;
      if (copyPayload != null) msg.dataset.copy = copyPayload;
      chatMessages.appendChild(msg);
      chatMessages.scrollTop = chatMessages.scrollHeight;
      return msg;
    }
    function escapeChat(s) {
      return String(s == null ? '' : s)
        .replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;')
        .replace(/"/g, '&quot;').replace(/'/g, '&#39;');
    }
    function formatChatBody(s) {
      // Render fenced code blocks as <pre>; everything else as escaped
      // paragraphs with line breaks. Avoids embedding a literal backtick
      // (would close the Go raw-string holding this JS) by composing
      // the fence delimiter at runtime.
      var fence = String.fromCharCode(96, 96, 96);
      var out = '';
      var parts = String(s || '').split(fence);
      for (var i = 0; i < parts.length; i++) {
        if (i % 2 === 0) {
          var t = parts[i].replace(/^\s+|\s+$/g, '');
          if (t) out += '<p>' + escapeChat(t).replace(/\n/g, '<br>') + '</p>';
        } else {
          var body = parts[i];
          var nl = body.indexOf('\n');
          if (nl >= 0 && body.slice(0, nl).match(/^[a-zA-Z0-9_+-]*$/)) {
            body = body.slice(nl + 1);
          }
          out += '<pre>' + escapeChat(body) + '</pre>';
        }
      }
      return out;
    }
    function setChatBusy(busy) {
      chatBtnTalk.disabled = !!busy;
      chatBtnEdit.disabled = !!busy;
    }
    function sendChat(mode) {
      mode = mode === 'chat' ? 'chat' : 'edit';
      var text = (chatInput.value || '').trim();
      if (!text) return;
      chatInput.value = '';
      var prefix = mode === 'chat' ? '<span class="ui-cw-mode-tag">chat</span> ' : '';
      addChatMsg('user', prefix + escapeChat(text));
      appendHistory('user', text);

      var body = {
        name:    nameInput.value.trim(),
        lang:    langSelect.value,
        code:    editor.value || '',
        context: ctxEditor.value || '',
        message: text,
        mode:    mode,
        history: chatHistory.slice(0, -1),
      };
      setChatBusy(true);
      var thinking = addChatMsg('assistant', '<span class="ui-cw-spinner"></span> Thinking…');

      fetch(cfg.chat_url, {
        method: 'POST',
        headers: {'Content-Type': 'application/json'},
        body: JSON.stringify(body),
      }).then(function(r) {
        if (!r.ok) return r.text().then(function(t){ throw new Error(t || ('HTTP ' + r.status)); });
        return r.json();
      }).then(function(data) {
        thinking.remove();
        // Edit-mode response with code → trigger the inline diff in
        // the editor pane and SUPPRESS the chat bubble entirely. The
        // editor's diff view already shows adds/removes visually with
        // its own +N / -M counters, so a parallel summary in chat is
        // redundant. The assistant text still goes into chatHistory
        // so the LLM has context for the next turn.
        var hasCodeProposal = mode !== 'chat' && data.type === 'code' && data.code && langSelect.value !== 'regex';
        if (hasCodeProposal) {
          if (typeof window.editorShowDiff === 'function') {
            window.editorShowDiff({
              newText: data.code,
              editorPane: editorWrap,
              editorTextarea: editor,
              onApply: function(text) { editor.value = text; },
            });
          } else {
            editor.value = data.code;
          }
        } else {
          // Chat mode, or Edit mode where the LLM didn't return code
          // (just prose) — render the response in the chat panel as
          // normal.
          addChatMsg('assistant', formatChatBody(data.content), data.content || '');
        }
        appendHistory('assistant', data.content || '');
      }).catch(function(err) {
        thinking.remove();
        addChatMsg('assistant', '<span class="ui-cw-err">Error: ' + escapeChat(err.message) + '</span>');
      }).then(function() {
        setChatBusy(false);
      });
    }

    // --- State ---
    var currentID    = null;
    var savedTagShown = false;

    // Revision-history state. Populated by loadRevisions() whenever
    // a snippet is opened or saved; navigateRevision() walks the
    // index and updates the editor / name / lang to match.
    var revisions     = [];
    var revisionIndex = -1;
    function updateRevNav() {
      if (!revGroup) return;
      var n = revisions.length;
      revBackBtn.disabled = revisionIndex <= 0;
      revFwdBtn.disabled  = revisionIndex >= n - 1;
      revIndicator.textContent = n > 0 ? 'rev ' + (revisionIndex + 1) + '/' + n : '';
      // Show "Make Latest" only when the user has scrolled back to
      // an earlier revision — clicking it saves the editor's current
      // contents as a new revision so it becomes the latest.
      revMarkBtn.style.display = (n > 0 && revisionIndex < n - 1) ? 'inline-flex' : 'none';
      revGroup.style.display = n > 0 ? 'inline-flex' : 'none';
    }
    function loadRevisions(snippetID) {
      if (!revGroup) return;
      if (!snippetID) {
        revisions = []; revisionIndex = -1;
        updateRevNav();
        return;
      }
      var url = cfg.revisions_list_url.replace('{id}', encodeURIComponent(snippetID));
      fetchJSON(url).then(function(data) {
        revisions = data || [];
        revisionIndex = revisions.length - 1;
        updateRevNav();
      }).catch(function() {
        revisions = []; revisionIndex = -1;
        updateRevNav();
      });
    }
    function navigateRevision(dir) {
      if (!revGroup) return;
      var idx = revisionIndex + dir;
      if (idx < 0 || idx >= revisions.length) return;
      var url = cfg.revision_load_url.replace('{id}', encodeURIComponent(revisions[idx].id));
      fetchJSON(url).then(function(rev) {
        editor.value = rev[codeF] || rev.code || '';
        if (rev[nameF] || rev.name) nameInput.value = rev[nameF] || rev.name || '';
        var l = rev[langF] || rev.lang || '';
        if (l) {
          for (var i = 0; i < langSelect.options.length; i++) {
            if (langSelect.options[i].value === l) { langSelect.selectedIndex = i; break; }
          }
        }
        revisionIndex = idx;
        updateRevNav();
      }).catch(function(err) {
        showToast('Could not load revision: ' + err.message);
      });
    }
    function markAsLatest() {
      // Saving the current editor content creates a new revision —
      // server-side it's appended to the list and becomes the latest,
      // exactly the behavior the legacy "Make Latest" button had.
      saveSnippet();
    }

    function setMobileTitle(t) { mobileTitle.textContent = t || 'New snippet'; }

    function openSnippet(id) {
      if (id == null) {
        currentID = null;
        nameInput.value = '';
        editor.value = '';
        // langSelect retains the last choice — usually convenient.
        setMobileTitle('New snippet');
        closeDrawer();
        markActive(null);
        loadRevisions(null);
        return;
      }
      var url = (cfg.load_url || (cfg.list_url + '/{id}')).replace('{id}', encodeURIComponent(id));
      fetchJSON(url).then(function(rec) {
        currentID = rec[idF] || id;
        nameInput.value = rec[nameF] || '';
        if (rec[langF]) langSelect.value = rec[langF];
        editor.value = rec[codeF] || '';
        setMobileTitle(rec[nameF] || 'Untitled');
        closeDrawer();
        markActive(currentID);
        loadRevisions(currentID);
      }).catch(function(err) {
        showToast('Load failed: ' + err.message);
      });
    }

    function saveSnippet() {
      var name = (nameInput.value || '').trim();
      var code = editor.value || '';
      if (!name) {
        showToast('Snippet name required');
        nameInput.focus();
        return;
      }
      if (!code) {
        showToast('No code to save');
        editor.focus();
        return;
      }
      var body = {};
      body[idF]   = currentID || '';
      body[nameF] = name;
      body[langF] = langSelect.value || '';
      body[codeF] = code;
      saveBtn.disabled = true;
      fetch(cfg.save_url, {
        method: 'POST',
        headers: {'Content-Type': 'application/json'},
        body: JSON.stringify(body),
      }).then(function(r) {
        if (!r.ok) return r.text().then(function(t){ throw new Error(t || ('HTTP ' + r.status)); });
        return r.json();
      }).then(function(rec) {
        if (rec && rec[idF]) currentID = rec[idF];
        else if (rec && rec.id) currentID = rec.id;
        setMobileTitle(name);
        loadList();
        loadRevisions(currentID);
        flashSaved();
      }).catch(function(err) {
        showToast('Save failed: ' + err.message);
      }).then(function() {
        saveBtn.disabled = false;
      });
    }

    function copyEditor() {
      navigator.clipboard.writeText(editor.value || '').then(function() {
        var orig = copyBtn.textContent;
        copyBtn.textContent = 'Copied!';
        copyBtn.classList.add('copied');
        setTimeout(function() {
          copyBtn.textContent = orig;
          copyBtn.classList.remove('copied');
        }, 1200);
      });
    }

    function flashSaved() {
      if (savedTagShown) return;
      savedTagShown = true;
      var tag = el('span', {class: 'ui-cw-saved'}, ['Saved']);
      toolbar.appendChild(tag);
      setTimeout(function() {
        tag.remove();
        savedTagShown = false;
      }, 1500);
    }

    function deleteSnippet(id) {
      if (!cfg.delete_url) return;
      if (!confirm('Delete this snippet? This cannot be undone.')) return;
      var url = cfg.delete_url.replace('{id}', encodeURIComponent(id));
      fetch(url, {method: 'DELETE'}).then(function(r) {
        if (!r.ok && r.status !== 204) {
          return r.text().then(function(t){ throw new Error(t || ('HTTP ' + r.status)); });
        }
        if (currentID === id) openSnippet(null);
        loadList();
      }).catch(function(err) {
        showToast('Delete failed: ' + err.message);
      });
    }

    function markActive(id) {
      sideList.querySelectorAll('.ui-chat-side-item').forEach(function(it) {
        it.classList.toggle('active', it.dataset.id === id);
      });
    }

    function loadList() {
      fetchJSON(cfg.list_url).then(function(items) {
        items = items || [];
        // Sort newest-first by date.
        items.sort(function(a, b) {
          return String(b[dateF] || '').localeCompare(String(a[dateF] || ''));
        });
        sideList.innerHTML = '';
        if (!items.length) {
          sideList.appendChild(el('div', {class: 'ui-chat-empty'}, [cfg.empty_text || 'No snippets yet. Click + New or chat with the LLM to generate one.']));
          return;
        }
        items.forEach(function(it) {
          var fullName = it[nameF] || '(untitled)';
          var meta = (it[langF] ? it[langF] + ' · ' : '') + relTime(it[dateF]);
          var row = el('div', {
            class: 'ui-chat-side-item' + (it[idF] === currentID ? ' active' : ''),
            // title is the native browser tooltip — shows full name on
            // hover even when the row's text is ellipsized at the
            // halved width. Includes lang+date so a cursor pause
            // surfaces the same metadata that the legacy meta line
            // used to display inline.
            title: fullName + ' — ' + meta,
            onclick: function(ev) {
              if (ev.target.classList.contains('ui-chat-side-del')) return;
              openSnippet(it[idF]);
            },
          });
          row.dataset.id = String(it[idF] || '');
          var textWrap = el('div', {class: 'ui-chat-side-text'});
          textWrap.appendChild(el('div', {class: 'ui-chat-side-title'}, [fullName]));
          row.appendChild(textWrap);
          if (cfg.delete_url) {
            row.appendChild(el('button', {
              class: 'ui-chat-side-del', title: 'Delete',
              onclick: function(ev) { ev.stopPropagation(); deleteSnippet(it[idF]); },
            }, ['×']));
          }
          sideList.appendChild(row);
        });
      }).catch(function(err) {
        sideList.innerHTML = '';
        sideList.appendChild(el('div', {class: 'ui-chat-empty'}, ['Failed to load: ' + err.message]));
      });
    }

    // --- Modal infrastructure (variables / values / contexts) ---
    // One overlay+container reused across modal types. closeModal()
    // collapses both. Each opener clears the container, fills it with
    // its own content, and shows the overlay.
    var modalOverlay = el('div', {class: 'ui-cw-modal-overlay',
      onclick: function(ev){ if (ev.target === modalOverlay) closeModal(); }});
    var modalBox = el('div', {class: 'ui-cw-modal-box'});
    modalOverlay.appendChild(modalBox);
    document.body.appendChild(modalOverlay);
    function openModal()  { modalOverlay.classList.add('open'); }
    function closeModal() {
      modalOverlay.classList.remove('open');
      modalBox.innerHTML = '';
    }
    document.addEventListener('keydown', function(ev) {
      if (ev.key === 'Escape' && modalOverlay.classList.contains('open')) closeModal();
    });

    // --- Variables modal ---
    // Scans the editor for {{NAME}} placeholders. Two modes: "apply"
    // (substitutes into the editor in-place) and "copy" (substitutes
    // and copies to clipboard, leaving the editor untouched).
    function extractVars(code) {
      var re = /\{\{([A-Za-z_][A-Za-z0-9_]*)\}\}/g;
      var out = [];
      var seen = {};
      var m;
      while ((m = re.exec(code)) !== null) {
        if (!seen[m[1]]) { out.push(m[1]); seen[m[1]] = true; }
      }
      return out;
    }
    var savedVarValues = {};
    function loadValuesForPicker() {
      if (!cfg.values_list_url) return Promise.resolve([]);
      return fetchJSON(cfg.values_list_url).catch(function(){ return []; });
    }
    function openVarsModal(action) {
      action = action === 'copy' ? 'copy' : 'apply';
      var vars = extractVars(editor.value || '');
      if (!vars.length) {
        showToast('No {{VARIABLE}} placeholders in this snippet');
        return;
      }
      modalBox.innerHTML = '';
      modalBox.appendChild(el('h3', {}, [action === 'copy' ? 'Fill variables for copy' : 'Set variables']));
      modalBox.appendChild(el('div', {class: 'ui-cw-modal-desc'},
        ['Each variable can be a static value or a saved value from your library.']));
      var fields = el('div', {class: 'ui-cw-var-inputs'});
      modalBox.appendChild(fields);

      loadValuesForPicker().then(function(values) {
        vars.forEach(function(v) {
          var row = el('div', {class: 'ui-cw-var-row'});
          row.appendChild(el('label', {}, [v]));
          var input = el('input', {type: 'text', value: savedVarValues[v] || ''});
          input.dataset.var = v;
          row.appendChild(input);
          if (values && values.length) {
            var picker = el('select', {class: 'ui-cw-var-picker'});
            picker.appendChild(el('option', {value: ''}, ['— pick from library —']));
            values.forEach(function(val) {
              var label = val.name || '';
              if (val.desc) label += ' (' + val.desc + ')';
              picker.appendChild(el('option', {value: val.value || ''}, [label]));
            });
            picker.addEventListener('change', function() {
              if (picker.value) input.value = picker.value;
            });
            row.appendChild(picker);
          }
          fields.appendChild(row);
        });
        var btns = el('div', {class: 'ui-cw-modal-btns'});
        var cancelBtn = el('button', {class: 'ui-row-btn'}, ['Cancel']);
        cancelBtn.addEventListener('click', closeModal);
        var goBtn = el('button', {class: 'ui-row-btn primary'}, [action === 'copy' ? 'Copy' : 'Apply']);
        goBtn.addEventListener('click', function() {
          var code = editor.value || '';
          var inputs = fields.querySelectorAll('input[data-var]');
          for (var i = 0; i < inputs.length; i++) {
            var n = inputs[i].dataset.var;
            var v = inputs[i].value;
            if (v) {
              savedVarValues[n] = v;
              code = code.split('{{' + n + '}}').join(v);
            }
          }
          if (action === 'copy') {
            navigator.clipboard.writeText(code).then(function() {
              showToast('Copied with substitutions');
            });
          } else {
            editor.value = code;
          }
          closeModal();
        });
        btns.appendChild(cancelBtn);
        btns.appendChild(goBtn);
        modalBox.appendChild(btns);
      });
      openModal();
    }

    // --- Copy editor (with optional variable substitution) ---
    function copyEditorWithVars() {
      // Replaces the simple copyEditor handler when {{NAME}} placeholders
      // are present so the user gets a chance to fill them in before
      // the copy lands on the clipboard.
      var code = editor.value || '';
      if (!code) { showToast('Editor is empty'); return; }
      if (extractVars(code).length > 0) {
        openVarsModal('copy');
        return;
      }
      navigator.clipboard.writeText(code).then(function() {
        var orig = copyBtn.textContent;
        copyBtn.textContent = 'Copied!';
        copyBtn.classList.add('copied');
        setTimeout(function() {
          copyBtn.textContent = orig;
          copyBtn.classList.remove('copied');
        }, 1200);
      });
    }
    // Override the placeholder copyEditor with the variable-aware one.
    copyBtn.onclick = copyEditorWithVars;

    // --- Values library modal ---
    // Saved {name, desc, value} records the user can paste into the
    // editor or pick from inside the variables modal. CRUD via
    // values_list_url + value_url.
    function openValuesModal() {
      if (!cfg.values_list_url) return;
      modalBox.innerHTML = '';
      var hdr = el('h3', {}, ['Values']);
      modalBox.appendChild(hdr);
      var listEl = el('div', {class: 'ui-cw-list'}, ['Loading…']);
      modalBox.appendChild(listEl);
      var btns = el('div', {class: 'ui-cw-modal-btns'});
      var closeBtn = el('button', {class: 'ui-row-btn'}, ['Close']);
      closeBtn.addEventListener('click', closeModal);
      var newBtn = el('button', {class: 'ui-row-btn primary'}, ['+ New']);
      newBtn.addEventListener('click', function(){ editValueModal(null); });
      btns.appendChild(closeBtn);
      btns.appendChild(newBtn);
      modalBox.appendChild(btns);
      openModal();

      fetchJSON(cfg.values_list_url).then(function(items) {
        items = items || [];
        items.sort(function(a, b){ return (a.name || '').localeCompare(b.name || ''); });
        listEl.innerHTML = '';
        if (!items.length) {
          listEl.appendChild(el('div', {class: 'ui-cw-empty'},
            ['No saved values yet. Click + New to add one.']));
          return;
        }
        items.forEach(function(it) {
          var row = el('div', {class: 'ui-cw-list-row'});
          var info = el('div', {class: 'ui-cw-list-info'});
          info.appendChild(el('div', {class: 'ui-cw-list-title'}, [it.name || '(unnamed)']));
          if (it.desc) info.appendChild(el('div', {class: 'ui-cw-list-meta'}, [it.desc]));
          var preview = String(it.value || '');
          if (preview.length > 80) preview = preview.slice(0, 80) + '…';
          info.appendChild(el('div', {class: 'ui-cw-list-meta mono'}, [preview]));
          var editBtn = el('button', {class: 'ui-cw-list-btn'}, ['Edit']);
          editBtn.addEventListener('click', function(){ editValueModal(it); });
          var del = el('button', {class: 'ui-cw-list-btn danger'}, ['×']);
          del.addEventListener('click', function() {
            if (!confirm('Delete value "' + (it.name || '') + '"?')) return;
            var url = cfg.value_url.replace('{id}', encodeURIComponent(it.id));
            fetch(url, {method: 'DELETE'}).then(function(){ openValuesModal(); });
          });
          row.appendChild(info);
          row.appendChild(editBtn);
          row.appendChild(del);
          listEl.appendChild(row);
        });
      }).catch(function(err) {
        listEl.innerHTML = '';
        listEl.appendChild(el('div', {class: 'ui-cw-empty'}, ['Failed to load: ' + err.message]));
      });
    }

    function editValueModal(rec) {
      modalBox.innerHTML = '';
      modalBox.appendChild(el('h3', {}, [rec ? 'Edit value' : 'New value']));
      var nameI = el('input', {type: 'text', value: (rec && rec.name) || '', placeholder: 'Name (e.g. MySQL Prod Password)'});
      var descI = el('input', {type: 'text', value: (rec && rec.desc) || '', placeholder: 'Description (optional)'});
      var valueI = el('input', {type: 'text', class: 'mono', value: (rec && rec.value) || '', placeholder: 'Value'});
      modalBox.appendChild(el('label', {}, ['Name']));
      modalBox.appendChild(nameI);
      modalBox.appendChild(el('label', {}, ['Description']));
      modalBox.appendChild(descI);
      modalBox.appendChild(el('label', {}, ['Value']));
      modalBox.appendChild(valueI);
      var btns = el('div', {class: 'ui-cw-modal-btns'});
      var cancel = el('button', {class: 'ui-row-btn'}, ['Cancel']);
      cancel.addEventListener('click', function(){ openValuesModal(); });
      var save = el('button', {class: 'ui-row-btn primary'}, ['Save']);
      save.addEventListener('click', function() {
        var name = (nameI.value || '').trim();
        if (!name) { nameI.focus(); return; }
        var body = {
          id:    rec && rec.id ? rec.id : '',
          name:  name,
          desc:  (descI.value || '').trim(),
          value: valueI.value || '',
        };
        fetch(cfg.values_list_url, {
          method: 'POST',
          headers: {'Content-Type': 'application/json'},
          body: JSON.stringify(body),
        }).then(function(r) {
          if (!r.ok) return r.text().then(function(t){ throw new Error(t || ('HTTP ' + r.status)); });
          openValuesModal();
        }).catch(function(err){ showToast('Save failed: ' + err.message); });
      });
      btns.appendChild(cancel);
      btns.appendChild(save);
      modalBox.appendChild(btns);
      openModal();
    }

    // --- Contexts library modal ---
    function openContextsModal() {
      if (!cfg.contexts_list_url) return;
      modalBox.innerHTML = '';
      modalBox.appendChild(el('h3', {}, ['Saved contexts']));
      var listEl = el('div', {class: 'ui-cw-list'}, ['Loading…']);
      modalBox.appendChild(listEl);
      var btns = el('div', {class: 'ui-cw-modal-btns'});
      var closeBtn = el('button', {class: 'ui-row-btn'}, ['Close']);
      closeBtn.addEventListener('click', closeModal);
      btns.appendChild(closeBtn);
      modalBox.appendChild(btns);
      openModal();

      fetchJSON(cfg.contexts_list_url).then(function(items) {
        items = items || [];
        items.sort(function(a, b){ return (a.name || '').localeCompare(b.name || ''); });
        listEl.innerHTML = '';
        if (!items.length) {
          listEl.appendChild(el('div', {class: 'ui-cw-empty'},
            ['No saved contexts. Use Save in the Context section to add one.']));
          return;
        }
        items.forEach(function(it) {
          var row = el('div', {class: 'ui-cw-list-row'});
          var info = el('div', {class: 'ui-cw-list-info'});
          info.appendChild(el('div', {class: 'ui-cw-list-title'}, [it.name || '(unnamed)']));
          if (it.date) info.appendChild(el('div', {class: 'ui-cw-list-meta'}, [relTime(it.date)]));
          info.style.cursor = 'pointer';
          info.addEventListener('click', function(){ loadContext(it.id); });
          var del = el('button', {class: 'ui-cw-list-btn danger'}, ['×']);
          del.addEventListener('click', function(ev) {
            ev.stopPropagation();
            deleteContext(it.id);
          });
          row.appendChild(info);
          row.appendChild(del);
          listEl.appendChild(row);
        });
      }).catch(function(err) {
        listEl.innerHTML = '';
        listEl.appendChild(el('div', {class: 'ui-cw-empty'}, ['Failed to load: ' + err.message]));
      });
    }

    // Initial state.
    loadList();
    // ?snippet=<id> deep-link.
    try {
      var params = new URLSearchParams(window.location.search);
      var sid = params.get('snippet');
      if (sid) openSnippet(sid);
    } catch (_) {}

    return wrap;
  };

  components.article_editor = function(cfg) {
    var idF      = cfg.id_field      || 'ID';
    var subjectF = cfg.subject_field || 'Subject';
    var bodyF    = cfg.body_field    || 'Body';
    var dateF    = cfg.date_field    || 'Date';
    var imageF   = cfg.image_field   || 'ImageURL';

    var wrap = el('div', {class: 'ui-tw'});

    // --- Sidebar (articles list) ---
    var side = el('div', {class: 'ui-tw-side'});
    // Collapse state — desktop only. Mobile uses the slide-in drawer
    // mechanism inherited from ChatPanel and ignores this state.
    var sideCollapsed = false;
    try { sideCollapsed = localStorage.getItem('tw.sideCollapsed') === '1'; } catch(e) {}
    var collapseBtn = el('button', {
      class: 'ui-tw-collapse', title: 'Hide articles list',
      onclick: function(){ toggleCollapse(); },
    }, ['‹']);
    var sideHdrBuilt = renderSideHeader({
      label:     'Articles',
      className: 'ui-tw-side-h',
      newTitle:  'New article',
      onNew:     function(){ openArticle(null); },
      onClose:   function(){ closeDrawer(); },
      leftExtras: [collapseBtn],
    });
    var sideHdr  = sideHdrBuilt.elt;
    var sideList = el('div', {class: 'ui-tw-side-list'}, ['Loading…']);
    var sideSearch = makeSideSearch(sideList);
    side.appendChild(sideHdr);
    side.appendChild(sideSearch);
    side.appendChild(sideList);

    // Floating expand-tab shown when the sidebar is collapsed. Sits
    // pinned to the left edge of the main pane so the user can always
    // bring the list back without hunting for a menu.
    var expandTab = el('button', {
      class: 'ui-tw-expand', title: 'Show articles list',
      onclick: function(){ toggleCollapse(); },
    }, ['›']);

    function toggleCollapse() {
      sideCollapsed = !sideCollapsed;
      wrap.classList.toggle('side-collapsed', sideCollapsed);
      collapseBtn.title = sideCollapsed ? 'Show articles list' : 'Hide articles list';
      collapseBtn.textContent = sideCollapsed ? '›' : '‹';
      try { localStorage.setItem('tw.sideCollapsed', sideCollapsed ? '1' : '0'); } catch(e) {}
    }

    var drawer = makeDrawer(side, {
      title:          'New article',
      hamburgerTitle: 'Articles',
      newTitle:       'New article',
      onNew:          function(){ openArticle(null); },
    });
    var mobileTitle    = drawer.mobileTitle;
    var drawerBackdrop = drawer.backdrop;

    // --- Main pane (editor + assistant) ---
    var main = el('div', {class: 'ui-tw-main'});
    main.appendChild(drawer.mobileHdr);

    var titleBar = el('div', {class: 'ui-tw-titlebar'});
    var titleInput = el('input', {type: 'text', class: 'ui-tw-title',
      placeholder: cfg.placeholder_title || 'Article title…'});
    var savedTag = el('span', {class: 'ui-tw-saved'}, []);

    // Declarative toolbar — apps populate cfg.actions with the
    // buttons they want. The runtime maps each entry to a generic
    // handler based on Method:
    //   "client"   → call into window.UIClientActions[<url>] with
    //                an editor handle. The app registered the
    //                handler from its own package (ExtraHeadHTML)
    //                — this is the supported path for any
    //                app-specific flow.
    //   "post"     → POST to URL with {id} substituted
    //   "open"     → window.open(URL, _blank)
    //   "redirect" → set window.location.href
    //   "builtin"  → legacy: invokes a hard-coded named flow that
    //                lives in this file. New code should use
    //                "client" instead; "builtin" is preserved for
    //                in-flight ports.
    //
    // editorAPI is the handle passed to client actions. Forward-
    // declared here because the closure variables it reads
    // (titleInput, bodyArea, currentID, …) are var-hoisted and
    // get their real values further down in the mount function.
    // At click time they're set; at the time we build editorAPI,
    // they're undefined but unreferenced. The methods read them
    // lazily so this works.
    var editorAPI = {
      getBody:  function()    { return bodyArea.value; },
      setBody:  function(s)   { bodyArea.value = s == null ? '' : String(s); },
      getTitle: function()    { return titleInput.value; },
      setTitle: function(s)   { titleInput.value = s == null ? '' : String(s); },
      getID:    function()    { return currentID; },
      getImage: function()    { return currentImageURL; },
      setImage: function(url) { showImage(url); },
      save:     function()    { saveArticle(); },
      toast:    function(msg) { showToast(msg); },
      busy:     function(btn, label) { setBtnBusy(btn, label); },
      restore:  function(btn) { restoreBtn(btn); },
      confirm:  function(msg) { return confirm(msg); },
      appendAssistant: function(role, content) { asstAppend(role, content); },
    };

    var actionButtons = [];
    // Holdover dispatcher for the two slide-in panel flows that
    // still live in this file (rules, merge). Everything else is
    // a "client" action registered from the app's package via
    // window.uiRegisterClientAction. Rules and merge will move
    // out once a generic SlidePanel primitive exists.
    function builtinAction(name) {
      switch (name) {
        case 'rules': return toggleRules;
        case 'merge': return toggleMerge;
      }
      return null;
    }
    (cfg.actions || []).forEach(function(action) {
      var classes = 'ui-row-btn';
      if (action.variant) classes += ' ' + action.variant;
      var btn = el('button', {class: classes, title: action.title || ''},
        [action.label || '(action)']);
      btn.addEventListener('click', function() {
        if (action.confirm && !confirm(action.confirm)) return;
        var method = action.method || 'post';
        if (method === 'client') {
          var name = action.url || '';
          var fn = window.UIClientActions && window.UIClientActions[name];
          if (typeof fn === 'function') {
            fn({editor: editorAPI, button: btn, action: action});
          } else {
            showToast('No handler for client action: ' + name);
          }
          return;
        }
        if (method === 'builtin') {
          var fn = builtinAction(action.url || '');
          // Pass the clicked button so the handler can drive its
          // own busy/spinner state without relying on a globally
          // named button variable (which doesn't exist any more
          // now that the toolbar is declarative).
          if (fn) fn(btn);
          else showToast('Unknown built-in action: ' + (action.url || ''));
          return;
        }
        var url = (action.url || '').replace('{id}', encodeURIComponent(currentID || ''));
        if (method === 'open')          { window.open(url, '_blank', 'noopener'); }
        else if (method === 'redirect') { window.location.href = url; }
        else {
          fetchJSON(url, {method: 'POST'}).catch(function(err){
            showToast('Failed: ' + err.message);
          });
        }
      });
      actionButtons.push(btn);
    });

    // Less-frequent actions tucked under a "More" button so the
    // titlebar stays readable. The CONTENTS are driven entirely by
    // cfg.extra_actions — the framework just renders the popover and
    // wires generic POST / open / redirect / builtin handling. Apps
    // declare what they want from page.go; nothing here is
    // app-specific. The two built-in handlers (suggest_title,
    // generate_image) exist because their UX has side-effects beyond
    // a plain POST; new built-ins can be added the same way.
    var extras = (cfg.extra_actions || []).slice();
    var extrasBtn = null, extrasMenu = null;
    if (extras.length) {
      extrasBtn = el('button', {class: 'ui-row-btn', title: 'More actions'}, ['More ▾']);
      extrasMenu = el('div', {class: 'ui-tw-extras-menu', style: 'display:none'});
      extras.forEach(function(action) {
        var entry = el('button', {class: 'ui-tw-extras-item', title: action.title || ''},
          [action.label || '(action)']);
        entry.addEventListener('click', function() {
          extrasMenu.style.display = 'none';
          if (action.confirm && !confirm(action.confirm)) return;
          var method = action.method || 'post';
          if (method === 'client') {
            var name = action.url || '';
            var fn = window.UIClientActions && window.UIClientActions[name];
            if (typeof fn === 'function') {
              fn({editor: editorAPI, button: entry, action: action});
            } else {
              showToast('No handler for client action: ' + name);
            }
            return;
          }
          var url = (action.url || '').replace('{id}', encodeURIComponent(currentID || ''));
          if (method === 'open') {
            window.open(url, '_blank', 'noopener');
          } else if (method === 'redirect') {
            window.location.href = url;
          } else {
            // POST (default). No payload — the action URL itself
            // encodes whatever the server needs.
            fetchJSON(url, {method: 'POST'}).catch(function(err) {
              showToast('Failed: ' + err.message);
            });
          }
        });
        extrasMenu.appendChild(entry);
      });
      extrasBtn.addEventListener('click', function(ev) {
        ev.stopPropagation();
        extrasMenu.style.display = extrasMenu.style.display === 'none' ? 'block' : 'none';
      });
      document.addEventListener('click', function(ev) {
        if (extrasMenu.style.display === 'none') return;
        if (extrasMenu.contains(ev.target) || extrasBtn.contains(ev.target)) return;
        extrasMenu.style.display = 'none';
      });
    }
    // Inline revision navigation — back/forward arrows + indicator +
    // "Make current" button instead of a slide-in panel. Hidden when
    // there's only one revision (or the article hasn't been saved yet).
    var revBackBtn = cfg.revisions_list_url ? el('button', {
      class: 'ui-row-btn compact', title: 'Previous revision',
      onclick: function(){ navigateRevision(-1); },
    }, ['◀']) : null;
    var revFwdBtn = cfg.revisions_list_url ? el('button', {
      class: 'ui-row-btn compact', title: 'Next revision',
      onclick: function(){ navigateRevision(1); },
    }, ['▶']) : null;
    var revIndicator = cfg.revisions_list_url ? el('span', {class: 'ui-tw-rev-indicator'}, []) : null;
    var revMakeCurrentBtn = cfg.revisions_list_url ? el('button', {
      class: 'ui-row-btn', title: 'Save the displayed revision as the latest version',
      onclick: function(){ saveArticle(); },
    }, ['Make current']) : null;
    if (revMakeCurrentBtn) revMakeCurrentBtn.style.display = 'none';
    // The titlebar-level Delete button used to live here. Removed —
    // delete now happens per-article via the × button on each sidebar
    // row (matches codewriter's pattern). Saves toolbar real estate
    // and removes a destructive control from a high-traffic toolbar.
    var saveBtn = el('button', {class: 'ui-row-btn primary', onclick: function(){ saveArticle(); }}, ['Save']);

    // Group the revision controls inside a bordered span so they read
    // as a single visual unit — matches the codewriter toolbar's
    // .ui-cw-rev-group treatment. Hidden when no article is open or
    // there's only one revision.
    var revGroup = null;
    if (revBackBtn || revFwdBtn || revIndicator || revMakeCurrentBtn) {
      var revKids = [];
      if (revMakeCurrentBtn) revKids.push(revMakeCurrentBtn);
      if (revBackBtn)        revKids.push(revBackBtn);
      if (revIndicator)      revKids.push(revIndicator);
      if (revFwdBtn)         revKids.push(revFwdBtn);
      revGroup = el('span', {class: 'ui-cw-rev-group', style: 'display:none'}, revKids);
    }

    titleBar.appendChild(titleInput);
    titleBar.appendChild(savedTag);
    // Revision group sits as the leftmost button cluster on the
    // titlebar (immediately after the title input and saved tag) —
    // matches codewriter where rev navigation is the first button
    // group after the name input.
    if (revGroup) titleBar.appendChild(revGroup);
    actionButtons.forEach(function(btn){ titleBar.appendChild(btn); });
    if (extrasBtn) {
      var extrasWrap = el('span', {class: 'ui-tw-extras-wrap'}, [extrasBtn, extrasMenu]);
      titleBar.appendChild(extrasWrap);
    }
    titleBar.appendChild(saveBtn);
    main.appendChild(titleBar);

    // Rules slide-in panel — keeps the slide-in pattern for editing
    // the rules text block; revisions are now inline arrows.
    var rulesPanel = el('div', {class: 'ui-tw-revs'});
    rulesPanel.style.display = 'none';
    main.appendChild(rulesPanel);

    // Merge slide-in panel — picks a saved merge source (or pastes
    // content) and combines it with the current article.
    var mergePanel = el('div', {class: 'ui-tw-revs ui-tw-merge-panel'});
    mergePanel.style.display = 'none';
    main.appendChild(mergePanel);

    // Optional image preview row (hidden until a generated image arrives).
    var imageRow = el('div', {class: 'ui-tw-image-row'});
    imageRow.style.display = 'none';
    main.appendChild(imageRow);

    // Revisions slide-in panel. Anchored over the editor; toggleable.
    var revsPanel = el('div', {class: 'ui-tw-revs'});
    revsPanel.style.display = 'none';
    main.appendChild(revsPanel);

    var bodyArea = el('textarea', {class: 'ui-tw-body',
      placeholder: cfg.placeholder_body || 'Article body in markdown…'});
    main.appendChild(bodyArea);

    // --- Assistant chat (below the editor) ---
    // Drag handle between the editor body and the assistant pane.
    // Drag up to expand the assistant; drag down to shrink. Saved
    // height persists per-user via localStorage so the layout sticks.
    var asstResizer = el('div', {class: 'ui-tw-resizer', title: 'Drag to resize the assistant pane'});
    var asstWrap = el('div', {class: 'ui-tw-asst'});
    var asstThread = el('div', {class: 'ui-tw-asst-thread'},
      [el('div', {class: 'ui-tw-asst-empty'}, ['Ask the assistant to discuss or rewrite this article.'])]);
    var asstInputRow = el('div', {class: 'ui-tw-asst-input-row'});
    var modeBtn = el('button', {class: 'ui-chat-mode active', title: 'Edit mode — assistant may rewrite the article',
      onclick: function() {
        chatMode = (chatMode === 'edit') ? 'chat' : 'edit';
        modeBtn.textContent = chatMode === 'edit' ? 'Edit' : 'Chat';
        modeBtn.classList.toggle('active', chatMode === 'edit');
        modeBtn.title = chatMode === 'edit'
          ? 'Edit mode — assistant may rewrite the article'
          : 'Chat mode — discussion only, never touches the article';
      }}, ['Edit']);
    var asstInput = el('textarea', {class: 'ui-chat-input', rows: '1',
      placeholder: 'Ask the assistant…'});
    var asstSend  = el('button', {class: 'ui-chat-send', onclick: function(){ doAssist(); }}, ['Send']);
    asstInputRow.appendChild(modeBtn);
    asstInputRow.appendChild(asstInput);
    asstInputRow.appendChild(asstSend);
    asstWrap.appendChild(asstThread);
    asstWrap.appendChild(asstInputRow);
    main.appendChild(asstResizer);
    main.appendChild(asstWrap);

    // Restore saved height (if any). Override the default 35% cap so
    // the saved value sticks even when it's larger than 35%.
    try {
      var savedH = parseInt(localStorage.getItem('tw.asst.height') || '0', 10);
      if (savedH > 80) {
        asstWrap.style.height = savedH + 'px';
        asstWrap.style.maxHeight = 'none';
      }
    } catch(e) {}

    // Drag-to-resize. Tracking moves the boundary between editor
    // body (above) and the assistant pane (below). Clamp to
    // [80px, 80% of viewport] so the user can't accidentally
    // disappear either pane.
    asstResizer.addEventListener('mousedown', function(ev) {
      ev.preventDefault();
      var startY = ev.clientY;
      var startH = asstWrap.offsetHeight;
      document.body.style.cursor = 'ns-resize';
      document.body.style.userSelect = 'none';
      function move(e) {
        var dy = startY - e.clientY;
        var maxH = Math.floor(window.innerHeight * 0.8);
        var newH = Math.max(80, Math.min(maxH, startH + dy));
        asstWrap.style.height = newH + 'px';
        asstWrap.style.maxHeight = 'none';
      }
      function up() {
        document.removeEventListener('mousemove', move);
        document.removeEventListener('mouseup', up);
        document.body.style.cursor = '';
        document.body.style.userSelect = '';
        try { localStorage.setItem('tw.asst.height', asstWrap.offsetHeight); } catch(e) {}
      }
      document.addEventListener('mousemove', move);
      document.addEventListener('mouseup', up);
    });
    // Touch parity for mobile / iPad — same drag behavior.
    asstResizer.addEventListener('touchstart', function(ev) {
      var t = ev.touches[0]; if (!t) return;
      var startY = t.clientY;
      var startH = asstWrap.offsetHeight;
      function move(e) {
        var t2 = e.touches[0]; if (!t2) return;
        var dy = startY - t2.clientY;
        var maxH = Math.floor(window.innerHeight * 0.8);
        var newH = Math.max(80, Math.min(maxH, startH + dy));
        asstWrap.style.height = newH + 'px';
        asstWrap.style.maxHeight = 'none';
        e.preventDefault();
      }
      function up() {
        document.removeEventListener('touchmove', move);
        document.removeEventListener('touchend', up);
        try { localStorage.setItem('tw.asst.height', asstWrap.offsetHeight); } catch(e) {}
      }
      document.addEventListener('touchmove', move, {passive: false});
      document.addEventListener('touchend', up);
    });

    wrap.appendChild(side);
    wrap.appendChild(main);
    wrap.appendChild(expandTab);
    wrap.appendChild(drawerBackdrop);
    if (sideCollapsed) {
      wrap.classList.add('side-collapsed');
      collapseBtn.title = 'Show articles list';
      collapseBtn.textContent = '›';
    }

    // --- State ---
    var currentID = null;
    var currentImageURL = '';
    var lastSavedSubject = '';
    var lastSavedBody = '';
    var chatHistory = [];
    var chatMode = 'edit'; // 'edit' or 'chat'
    var asstSending = false;
    // Revision navigation state — populated by reloadRevisions() after
    // every successful save. revisionIndex points at the entry in
    // revisions[] currently displayed in the editor; the Make-current
    // button is visible when we're not at the latest entry.
    var revisions = [];
    var revisionIndex = -1;

    var openDrawer  = drawer.openDrawer;
    var closeDrawer = drawer.closeDrawer;

    // --- Sidebar list ---
    var bulkSelected = {}; // article id -> true
    var bulkState    = {mode: false};
    function loadList() {
      fetchJSON(cfg.list_url).then(function(items) {
        sideList.innerHTML = '';
        items = items || [];
        items.sort(function(a, b){ return String(b[dateF] || '').localeCompare(String(a[dateF] || '')); });
        var ids = {}; items.forEach(function(it){ ids[it[idF]] = true; });
        Object.keys(bulkSelected).forEach(function(k){ if (!ids[k]) delete bulkSelected[k]; });
        if (!items.length) {
          if (cfg.bulk_select && bulkState.mode) {
            renderBulkBar([], sideList, bulkState, bulkSelected,
              function(it){ return it[idF]; }, loadList, function(){});
          }
          sideList.appendChild(el('div', {class: 'ui-chat-empty', style: 'padding:0.5rem;text-align:left'}, ['No articles yet.']));
          return;
        }
        if (cfg.bulk_select) {
          renderBulkBar(items, sideList, bulkState, bulkSelected,
            function(it){ return it[idF]; },
            loadList,
            function() {
              var keys = Object.keys(bulkSelected);
              if (!keys.length) return;
              if (!confirm('Delete ' + keys.length + ' article(s) permanently?')) return;
              Promise.all(keys.map(function(id) {
                var url = cfg.delete_url.replace('{id}', encodeURIComponent(id));
                return fetchJSON(url, {method: 'DELETE'}).catch(function(){});
              })).then(function() {
                if (bulkSelected[currentID]) openArticle(null);
                bulkSelected = {};
                bulkState.mode = false;
                loadList();
              });
            });
        }
        items.forEach(function(it) {
          var inMode = cfg.bulk_select && bulkState.mode;
          var selected = !!bulkSelected[it[idF]];
          var fullSubject = it[subjectF] || '(untitled)';
          var rowMeta = relTime(it[dateF]);
          var row = el('div', {
            class:
              'ui-chat-side-item' +
              (it[idF] === currentID ? ' active' : '') +
              (inMode ? ' selectable' : '') +
              (selected ? ' selected' : ''),
            // Hover tooltip with full title + date so the row can be
            // shorter without losing information when the title
            // ellipsizes at the narrower sidebar width.
            title: fullSubject + ' — ' + rowMeta,
          }, [
            el('div', {class: 'ui-chat-side-text'}, [
              el('div', {class: 'ui-chat-side-title'}, [fullSubject]),
            ]),
          ]);
          // Per-row delete button (× in the right corner) — matches
          // codewriter. Bulk-select mode hides it; checkboxes drive
          // multi-delete instead. The titlebar Delete button is kept
          // off the toolbar entirely since this gives one-click access
          // per article.
          if (cfg.delete_url && !inMode) {
            row.appendChild(el('button', {
              class: 'ui-chat-side-del', title: 'Delete article',
              onclick: function(ev) {
                ev.stopPropagation();
                if (!confirm('Delete "' + fullSubject + '" permanently?')) return;
                var url = cfg.delete_url.replace('{id}', encodeURIComponent(it[idF]));
                fetchJSON(url, {method: 'DELETE'}).then(function() {
                  if (currentID === it[idF]) openArticle(null);
                  loadList();
                  showToast('Deleted');
                }).catch(function(err){ showToast('Delete failed: ' + err.message); });
              },
            }, ['×']));
          }
          row.addEventListener('click', function(ev) {
            // Per-row × delete is a button child — let its own click
            // handler fire and short-circuit the row open.
            if (ev.target.classList.contains('ui-chat-side-del')) return;
            if (inMode) {
              if (bulkSelected[it[idF]]) delete bulkSelected[it[idF]];
              else bulkSelected[it[idF]] = true;
              loadList();
            } else {
              openArticle(it[idF]); closeDrawer();
            }
          });
          sideList.appendChild(row);
        });
      }).catch(function(err){ sideList.textContent = 'Failed: ' + err.message; });
    }

    function openArticle(id) {
      currentID = id;
      currentImageURL = '';
      asstThread.innerHTML = '';
      asstThread.appendChild(el('div', {class: 'ui-tw-asst-empty'},
        [id ? 'Ask the assistant to discuss or rewrite this article.' : 'Start typing your article — the assistant can help once you have something to work with.']));
      chatHistory = [];
      hideImage();
      if (!id) {
        titleInput.value = '';
        bodyArea.value = '';
        lastSavedSubject = '';
        lastSavedBody = '';
        savedTag.textContent = '';
        mobileTitle.textContent = 'New article';
        revisions = []; revisionIndex = -1;
        updateRevNav();
        loadList();
        return;
      }
      var url = cfg.load_url.replace('{id}', encodeURIComponent(id));
      fetchJSON(url).then(function(rec) {
        titleInput.value = rec[subjectF] || '';
        bodyArea.value   = rec[bodyF]    || '';
        lastSavedSubject = titleInput.value;
        lastSavedBody    = bodyArea.value;
        savedTag.textContent = 'saved ' + relTime(rec[dateF]);
        mobileTitle.textContent = rec[subjectF] || 'Untitled';
        if (rec[imageF]) showImage(rec[imageF]);
        loadList();
        reloadRevisions();
      }).catch(function(err){ showToast('Load failed: ' + err.message); });
    }

    function saveArticle() {
      var subject = titleInput.value.trim();
      var body    = bodyArea.value;
      if (!subject && !body) { showToast('Nothing to save'); return; }
      // Server-side accepts lowercase keys; encoding/json case-folds
      // for the inbound decode. Image URL is the new persisted field.
      var record = {
        id:        currentID || '',
        subject:   subject,
        body:      body,
        image_url: currentImageURL || '',
      };
      saveBtn.disabled = true;
      savedTag.textContent = 'saving…';
      fetchJSON(cfg.save_url, {
        method: 'POST', headers: {'Content-Type': 'application/json'},
        body: JSON.stringify(record),
      }).then(function(saved) {
        saveBtn.disabled = false;
        if (saved && saved[idF]) currentID = saved[idF];
        lastSavedSubject = subject;
        lastSavedBody    = body;
        savedTag.textContent = 'saved just now';
        mobileTitle.textContent = subject || 'Untitled';
        loadList();
        reloadRevisions();
      }).catch(function(err) {
        saveBtn.disabled = false;
        savedTag.textContent = '';
        showToast('Save failed: ' + err.message);
      });
    }

    // Image preview + persistence helpers.
    function showImage(url) {
      currentImageURL = url || '';
      if (!url) { hideImage(); return; }
      imageRow.innerHTML = '';
      imageRow.style.display = '';
      imageRow.appendChild(el('img', {src: url, class: 'ui-tw-image'}));
      imageRow.appendChild(el('div', {class: 'ui-tw-image-actions'}, [
        el('button', {class: 'ui-row-btn', onclick: function() {
          if (!confirm('Remove the header image from this article?')) return;
          hideImage();
          showToast('Image removed — Save to persist');
        }}, ['Remove']),
      ]));
    }
    function hideImage() {
      currentImageURL = '';
      imageRow.style.display = 'none';
      imageRow.innerHTML = '';
    }

    // Revision navigation — back/forward arrows + "Make current".
    function reloadRevisions() {
      if (!cfg.revisions_list_url || !currentID) {
        revisions = []; revisionIndex = -1; updateRevNav();
        return;
      }
      var url = cfg.revisions_list_url.replace('{id}', encodeURIComponent(currentID));
      fetchJSON(url).then(function(items) {
        revisions = items || [];
        revisions.sort(function(a, b){ return String(a.date || '').localeCompare(String(b.date || '')); });
        revisionIndex = revisions.length - 1;
        updateRevNav();
      }).catch(function() { revisions = []; revisionIndex = -1; updateRevNav(); });
    }
    function updateRevNav() {
      if (!revBackBtn) return;
      var n = revisions.length;
      revBackBtn.disabled = revisionIndex <= 0;
      revFwdBtn.disabled = revisionIndex >= n - 1;
      revIndicator.textContent = n > 0 ? 'rev ' + (revisionIndex + 1) + '/' + n : '';
      revMakeCurrentBtn.style.display = (n > 0 && revisionIndex < n - 1) ? 'inline-flex' : 'none';
      // The whole rev group is hidden until there are revisions to
      // navigate. Use explicit inline-flex / none so the value beats
      // the bordered-span CSS rules that don't carry display:none.
      if (revGroup) revGroup.style.display = n > 0 ? 'inline-flex' : 'none';
    }
    function navigateRevision(dir) {
      var idx = revisionIndex + dir;
      if (idx < 0 || idx >= revisions.length) return;
      if (!cfg.revision_load_url) { showToast('Revision load not configured'); return; }
      var url = cfg.revision_load_url.replace('{revid}', encodeURIComponent(revisions[idx].id));
      fetchJSON(url).then(function(rev) {
        bodyArea.value = rev.body || rev[bodyF] || '';
        if (rev.subject || rev[subjectF]) titleInput.value = rev.subject || rev[subjectF];
        revisionIndex = idx;
        updateRevNav();
      }).catch(function(err){ showToast('Load failed: ' + err.message); });
    }

    // --- Assistant ---
    function doAssist() {
      if (asstSending) return;
      var msg = asstInput.value.trim();
      if (!msg) return;
      asstInput.value = '';
      autoresizeAsst();
      asstSending = true;
      asstSend.disabled = true;
      // Push user msg.
      asstAppend('user', msg);
      var thinking = asstAppend('assistant', '');
      thinking.querySelector('.ui-chat-msg-body').innerHTML =
        '<span class="ui-chat-typing"><span></span><span></span><span></span></span>';
      var historyForSend = chatHistory.slice();
      chatHistory.push({role: 'user', content: msg});
      fetchJSON(cfg.chat_url, {
        method: 'POST', headers: {'Content-Type': 'application/json'},
        body: JSON.stringify({
          subject: titleInput.value,
          body:    bodyArea.value,
          message: msg,
          mode:    chatMode,
          history: historyForSend,
        }),
      }).then(function(d) {
        asstSending = false;
        asstSend.disabled = false;
        thinking.remove();
        if (!d) { asstAppend('assistant', '(empty response)'); return; }
        if (d.error) { asstAppend('assistant', 'Error: ' + d.error); return; }
        if (d.type === 'article' && d.content) {
          // Article rewrite — show the proposal as an inline diff in
          // the editor pane (matches codewriter's pattern). Apply
          // copies the new text into the body textarea + suggested
          // title; Reject restores the textarea. The chat bubble is
          // a brief pointer rather than a full duplicate of the
          // content — the visual diff in the editor is the primary
          // affordance.
          if (typeof window.editorShowDiff === 'function') {
            window.editorShowDiff({
              newText: d.content,
              editorPane: main,
              editorTextarea: bodyArea,
              onApply: function(text) {
                bodyArea.value = text;
                if (d.title) titleInput.value = d.title;
                showToast('Applied — remember to Save');
              },
            });
            asstAppend('assistant', 'Review proposed changes in editor window.');
          } else {
            // Diff helper not loaded — fall back to the legacy
            // in-chat Approve/Deny pattern so the proposal is still
            // actionable.
            var diffLines = computeLineDiff(bodyArea.value, d.content);
            var msgEl = asstAppend('assistant', '');
            msgEl.querySelector('.ui-chat-msg-body').innerHTML = renderLineDiff(diffLines);
            var bar = el('div', {class: 'ui-chat-actions'});
            var statusLine = function(text) {
              bar.innerHTML = '';
              bar.appendChild(el('span', {class: 'ui-chat-act-status'}, [text]));
            };
            var approveBtn = el('button', {class: 'ui-row-btn success', onclick: function(){
              bodyArea.value = d.content;
              if (d.title) titleInput.value = d.title;
              statusLine('✓ Applied — Save to keep');
              showToast('Applied — remember to Save');
            }}, ['Approve']);
            var denyBtn = el('button', {class: 'ui-row-btn danger', onclick: function(){
              statusLine('✗ Denied');
            }}, ['Deny']);
            var copyBtn = el('button', {class: 'ui-row-btn', onclick: function(){
              navigator.clipboard && navigator.clipboard.writeText(d.content);
              showToast('Copied');
            }}, ['Copy']);
            bar.appendChild(approveBtn);
            bar.appendChild(denyBtn);
            bar.appendChild(copyBtn);
            msgEl.appendChild(bar);
          }
          chatHistory.push({role: 'assistant', content: d.content});
        } else {
          var text = d.content || '';
          asstAppend('assistant', text);
          chatHistory.push({role: 'assistant', content: text});
        }
      }).catch(function(err) {
        asstSending = false;
        asstSend.disabled = false;
        thinking.remove();
        asstAppend('assistant', 'Error: ' + err.message);
      });
    }

    function asstAppend(role, content) {
      // Drop the empty-state placeholder once a real message appears.
      var empty = asstThread.querySelector('.ui-tw-asst-empty');
      if (empty) empty.remove();
      var msg = el('div', {class: 'ui-chat-msg ' + (role === 'assistant' ? 'assistant' : 'user')});
      var body = el('div', {class: 'ui-chat-msg-body'});
      body.textContent = content || '';
      msg.appendChild(body);
      asstThread.appendChild(msg);
      asstThread.scrollTop = asstThread.scrollHeight;
      return msg;
    }

    function diffStats(oldText, newText) {
      // Cheap line-count diff so the user has SOME signal about how
      // big the rewrite is before applying.
      var oldLines = (oldText || '').split('\n').length;
      var newLines = (newText || '').split('\n').length;
      return { add: Math.max(0, newLines - oldLines), remove: Math.max(0, oldLines - newLines) };
    }

    // computeLineDiff runs an LCS-based diff over two text blocks split
    // on newlines and returns an array of {type, text} records. Type is
    // '=' (unchanged), '-' (in old but not new), or '+' (in new but not
    // old). Used by the assistant proposal renderer to draw red/green
    // line markers like a github-style diff. O(m*n) memory — fine for
    // typical articles (a few hundred lines); a Myers-style algorithm
    // would be linear-ish in practice but adds code without measurable
    // wins at the article length we deal with.
    function computeLineDiff(a, b) {
      var oldL = (a || '').split('\n');
      var newL = (b || '').split('\n');
      var m = oldL.length, n = newL.length;
      // Build LCS DP table.
      var dp = new Array(m + 1);
      for (var i = 0; i <= m; i++) {
        dp[i] = new Array(n + 1);
        dp[i][0] = 0;
      }
      for (var j = 0; j <= n; j++) dp[0][j] = 0;
      for (var i2 = 1; i2 <= m; i2++) {
        for (var j2 = 1; j2 <= n; j2++) {
          if (oldL[i2-1] === newL[j2-1]) dp[i2][j2] = dp[i2-1][j2-1] + 1;
          else dp[i2][j2] = Math.max(dp[i2-1][j2], dp[i2][j2-1]);
        }
      }
      // Backtrack to construct the diff.
      var out = [];
      var i3 = m, j3 = n;
      while (i3 > 0 || j3 > 0) {
        if (i3 > 0 && j3 > 0 && oldL[i3-1] === newL[j3-1]) {
          out.unshift({type: '=', text: oldL[i3-1]});
          i3--; j3--;
        } else if (j3 > 0 && (i3 === 0 || dp[i3][j3-1] >= dp[i3-1][j3])) {
          out.unshift({type: '+', text: newL[j3-1]});
          j3--;
        } else if (i3 > 0) {
          out.unshift({type: '-', text: oldL[i3-1]});
          i3--;
        }
      }
      return out;
    }

    function renderLineDiff(lines) {
      var add = 0, rem = 0;
      lines.forEach(function(l){ if (l.type === '+') add++; else if (l.type === '-') rem++; });
      var summary = '<div class="ui-tw-diff-h">Proposed rewrite &middot; <span class="add">+' + add + '</span> / <span class="rem">&minus;' + rem + '</span></div>';
      var rows = lines.map(function(l) {
        var cls = l.type === '+' ? 'add' : (l.type === '-' ? 'rem' : 'same');
        var prefix = l.type === '+' ? '+ ' : (l.type === '-' ? '- ' : '  ');
        var text = (l.text || '').replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;');
        return '<div class="ui-tw-diff-row ' + cls + '"><span class="prefix">' + prefix + '</span>' + text + '</div>';
      }).join('');
      return summary + '<div class="ui-tw-diff">' + rows + '</div>';
    }

    function autoresizeAsst() {
      asstInput.style.height = 'auto';
      asstInput.style.height = Math.min(asstInput.scrollHeight, 120) + 'px';
    }
    asstInput.addEventListener('input', autoresizeAsst);
    asstInput.addEventListener('keydown', function(ev) {
      if (ev.key === 'Enter' && !ev.shiftKey) { ev.preventDefault(); doAssist(); }
    });

    // --- Toolbar actions -------------------------------------------------
    // setBtnBusy marks a toolbar button as in-flight: disables it and
    // swaps the text for a spinner + loading label. restoreBtn() puts
    // the original label back. Common to all long-running toolbar
    // actions (reprocess, suggest title, image generate) so the user
    // always has a visible "still working" signal.
    function setBtnBusy(btn, label) {
      if (!btn) return;
      btn.disabled = true;
      btn.dataset.origLabel = btn.textContent;
      btn.innerHTML = '<span class="ui-spinner"></span>' + label;
    }
    function restoreBtn(btn) {
      if (!btn) return;
      btn.disabled = false;
      var orig = btn.dataset.origLabel;
      if (orig) {
        btn.textContent = orig;
        delete btn.dataset.origLabel;
      }
    }

    // toggleMerge opens a slide-in panel that lets the user choose a
    // saved merge source (or paste content directly), add optional
    // guidance, and fire a merge call. The result applies directly
    // to the editor (no Approve/Deny — user explicitly initiated).
    function toggleMerge() {
      if (mergePanel.style.display !== 'none') {
        mergePanel.style.display = 'none';
        return;
      }
      // Hide siblings — only one overlay at a time.
      revsPanel.style.display = 'none';
      rulesPanel.style.display = 'none';
      mergePanel.innerHTML = '';
      mergePanel.style.display = '';

      var header = el('div', {class: 'ui-tw-revs-h'}, [
        el('span', {text: 'Merge with another source'}),
        el('button', {class: 'ui-row-btn', onclick: function(){ mergePanel.style.display = 'none'; }}, ['Close']),
      ]);
      mergePanel.appendChild(header);

      var hint = el('div', {class: 'ui-tw-rules-hint'},
        ['Pick a saved source from the dropdown OR paste content into the textarea below. Optional guidance shapes the merge (e.g. "favor the saved source\'s wording", "strip code blocks").']);
      mergePanel.appendChild(hint);

      // Saved-sources picker (rendered when MergeSourcesURL is set).
      var sourceSelect = null;
      if (cfg.merge_sources_url) {
        sourceSelect = el('select', {class: 'ui-form-select', style: 'width:100%;margin-bottom:0.5rem'});
        sourceSelect.appendChild(el('option', {value: ''}, ['(paste content below or pick a saved source)']));
        sourceSelect.addEventListener('change', function() {
          if (!sourceSelect.value || !cfg.merge_source_url) return;
          var url = cfg.merge_source_url.replace('{id}', encodeURIComponent(sourceSelect.value));
          fetchJSON(url).then(function(rec) {
            if (rec && (rec.body || rec.Body)) {
              pasteArea.value = rec.body || rec.Body;
            }
          }).catch(function(err){ showToast('Load source failed: ' + err.message); });
        });
        fetchJSON(cfg.merge_sources_url).then(function(items) {
          (items || []).forEach(function(s) {
            var opt = el('option', {value: s.id || s.ID, title: relTime(s.date || s.Date)},
              [(s.name || s.Name) + ' — ' + relTime(s.date || s.Date)]);
            sourceSelect.appendChild(opt);
          });
        }).catch(function(){});
        mergePanel.appendChild(sourceSelect);
      }

      var pasteArea = el('textarea', {class: 'ui-tw-rules-ta',
        placeholder: 'Paste the content to merge in here. Or pick a saved source above to load it.',
        style: 'min-height:140px'});
      mergePanel.appendChild(pasteArea);

      var guidance = el('input', {type: 'text', class: 'ui-form-input',
        placeholder: 'Optional guidance (how should the merge resolve conflicts?)',
        style: 'margin-top:0.5rem'});
      mergePanel.appendChild(guidance);

      var statusLine = el('div', {class: 'ui-tw-rules-saved'});
      var saveSourceBtn = (cfg.merge_sources_url) ? el('button', {class: 'ui-row-btn',
        title: 'Save the pasted content as a reusable merge source',
        onclick: function() {
          var name = window.prompt('Name this merge source:', '');
          if (!name) return;
          if (!pasteArea.value.trim()) { showToast('Paste something first'); return; }
          fetchJSON(cfg.merge_sources_url, {
            method: 'POST', headers: {'Content-Type': 'application/json'},
            body: JSON.stringify({name: name, body: pasteArea.value}),
          }).then(function() {
            showToast('Saved');
            // Refresh the dropdown.
            if (sourceSelect) {
              while (sourceSelect.children.length > 1) sourceSelect.removeChild(sourceSelect.lastChild);
              fetchJSON(cfg.merge_sources_url).then(function(items) {
                (items || []).forEach(function(s) {
                  var opt = el('option', {value: s.id || s.ID},
                    [(s.name || s.Name) + ' — ' + relTime(s.date || s.Date)]);
                  sourceSelect.appendChild(opt);
                });
              });
            }
          }).catch(function(err){ showToast('Save source failed: ' + err.message); });
        }}, ['Save as source']) : null;
      var mergeRunBtn = el('button', {class: 'ui-row-btn success',
        onclick: function() {
          var other = pasteArea.value.trim();
          if (!other) { showToast('Need something to merge with'); return; }
          if (!bodyArea.value.trim()) { showToast('Current article is empty — nothing to merge into'); return; }
          if (!confirm('Merge the source into the current article? The body will be replaced with the merged result.')) return;
          setBtnBusy(mergeRunBtn, 'Merging…');
          statusLine.textContent = '';
          fetchJSON(cfg.merge_url, {
            method: 'POST', headers: {'Content-Type': 'application/json'},
            body: JSON.stringify({
              subject:  titleInput.value,
              body:     bodyArea.value,
              other:    other,
              mode:     'edit',
              guidance: guidance.value,
            }),
          }).then(function(d) {
            restoreBtn(mergeRunBtn);
            if (!d) { showToast('Empty response'); return; }
            if (d.error) { showToast('Error: ' + d.error); return; }
            var merged = d.content || d.body || '';
            if (d.type === 'article' && merged) {
              bodyArea.value = merged;
              if (d.title) titleInput.value = d.title;
              // Auto-save so the merge produces a revision. ◀ reverts
              // if the merge result isn't what the user wanted.
              saveArticle();
              showToast('Merged and saved — use ◀ to revert if needed');
              mergePanel.style.display = 'none';
            } else if (merged) {
              // Server returned chat-style instead of article — show it.
              statusLine.textContent = 'Merge returned conversational text instead of an article body — see the assistant pane.';
              asstAppend('assistant', merged);
            } else {
              showToast('Merge produced no output');
            }
          }).catch(function(err) {
            restoreBtn(mergeRunBtn);
            showToast('Merge failed: ' + err.message);
          });
        }}, ['Merge into article']);
      var actions = el('div', {class: 'ui-tw-rules-actions'}, [
        statusLine,
        el('div', {style: 'display:flex;gap:0.4rem'}, [saveSourceBtn, mergeRunBtn].filter(Boolean)),
      ]);
      mergePanel.appendChild(actions);
      pasteArea.focus();
    }

    function toggleRules() {
      if (rulesPanel.style.display !== 'none') {
        rulesPanel.style.display = 'none';
        return;
      }
      // Hide the revisions panel if it's open — only one overlay at a time.
      revsPanel.style.display = 'none';
      rulesPanel.innerHTML = '';
      rulesPanel.style.display = '';
      var header = el('div', {class: 'ui-tw-revs-h'}, [
        el('span', {text: 'Rules'}),
        el('button', {class: 'ui-row-btn', onclick: function(){ rulesPanel.style.display = 'none'; }}, ['Close']),
      ]);
      var ta = el('textarea', {class: 'ui-tw-rules-ta',
        placeholder: 'One rule per line. Examples:\n  Strip mentions of sample system.\n  Never post API keys, passwords, or other secrets.\n  Match the existing article tone — terse, factual.'});
      ta.value = '(loading…)';
      ta.disabled = true;
      var savedHint = el('div', {class: 'ui-tw-rules-saved'});
      var saveBtn = el('button', {class: 'ui-row-btn', onclick: function() {
        saveBtn.disabled = true;
        savedHint.textContent = 'saving…';
        fetchJSON(cfg.rules_url, {
          method: 'POST', headers: {'Content-Type': 'application/json'},
          body: JSON.stringify({rules: ta.value}),
        }).then(function() {
          saveBtn.disabled = false;
          savedHint.textContent = 'saved — applies to next assistant message';
          setTimeout(function(){ savedHint.textContent = ''; }, 3000);
        }).catch(function(err) {
          saveBtn.disabled = false;
          savedHint.textContent = '';
          showToast('Save failed: ' + err.message);
        });
      }}, ['Save rules']);
      var hint = el('div', {class: 'ui-tw-rules-hint'},
        ['Lines you write here are appended to the assistant\'s system prompt as constraints. Each line is one rule.']);
      rulesPanel.appendChild(header);
      rulesPanel.appendChild(hint);
      rulesPanel.appendChild(ta);
      rulesPanel.appendChild(el('div', {class: 'ui-tw-rules-actions'}, [savedHint, saveBtn]));
      fetchJSON(cfg.rules_url).then(function(d) {
        ta.disabled = false;
        ta.value = (d && d.rules) || '';
        ta.focus();
      }).catch(function(err) {
        ta.disabled = false;
        ta.value = '';
        savedHint.textContent = 'Failed to load existing rules';
      });
    }

    // toggleRevisions retained for backward compatibility — the inline
    // nav (back/forward arrows + Make current) is the primary UX now.
    function toggleRevisions() {
    }

    loadList();
    return wrap;
  };

  components.card = function(cfg) {
    var wrap = el('div', {class: 'ui-card'});
    wrap.innerHTML = cfg.html || '';
    // Re-execute any inline <script> tags. innerHTML doesn't run them
    // (per HTML5), so we manually clone each script into a fresh
    // element the browser will execute. Keep this for the escape-hatch
    // case where the Card's body needs to fetch + render data.
    wrap.querySelectorAll('script').forEach(function(old) {
      var s = document.createElement('script');
      for (var i = 0; i < old.attributes.length; i++) {
        s.setAttribute(old.attributes[i].name, old.attributes[i].value);
      }
      s.text = old.textContent;
      old.parentNode.replaceChild(s, old);
    });
    return wrap;
  };

  components.error = function(cfg) {
    return el('div', {class: 'ui-card', text: 'UI error: ' + (cfg.message || 'unknown')});
  };

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
    if (cfg.back_url || cfg.show_title) {
      var header = el('div', {class: 'ui-page-header'});
      if (cfg.back_url) {
        header.appendChild(el('a', {class: 'ui-back-link', href: cfg.back_url, title: 'Back'}, ['← Back']));
      }
      if (cfg.show_title && cfg.title) {
        header.appendChild(el('h1', {class: 'ui-page-title'}, [cfg.title]));
      }
      // Live-sessions pill — polls /api/live every 10s and shows a
      // running/queued count badge with a click-through popover. Lets
      // operators see at a glance from any framework page that
      // background work is in flight, plus jump straight to it.
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
      // Update the document title in case the rendered title differs.
      if (cfg.title) document.title = cfg.title;
      root.appendChild(header);
    }

    if (cfg.sticky) mountComponent(cfg.sticky, root);

    (cfg.sections || []).forEach(function(s) {
      // NoChrome sections skip the card wrapper — body mounts directly
      // into the page root with no padding/bg/border. Used when the
      // contained component (e.g. ChatPanel) manages its own layout
      // and the section card would just create double-nested boxes.
      if (s.no_chrome) {
        if (s.body) mountComponent(s.body, root);
        return;
      }
      var section = el('div', {class: 'ui-section'});
      if (s.title) {
        var headerWrap = el('div', {class: 'ui-section-h'}, [
          el('span', {text: s.title}),
          el('span', {class: 'ui-section-h-r'}),
        ]);
        section.appendChild(headerWrap);
      }
      if (s.subtitle) section.appendChild(el('div', {class: 'ui-section-sub'}, [s.subtitle]));
      if (s.body) mountComponent(s.body, section);
      root.appendChild(section);
    });

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
`
