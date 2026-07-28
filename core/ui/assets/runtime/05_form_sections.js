  // --- structured "sections" editor -------------------------------------
  //
  // Presents ONE markdown string as an outline of headed blocks instead of
  // a single textarea. The stored value stays that markdown string: parse
  // on load, re-serialize on every edit. There is no second format, so
  // every server-side reader of the field keeps working (prompt builders,
  // exporters, CRUD tools that write it as plain text) and an author can
  // still paste raw markdown into it.
  //
  // A section's MODE is inferred from the shape of its own body rather
  // than stored as metadata anywhere:
  //
  //   every non-blank line "- x"    -> list
  //   every non-blank line "1. x"   -> steps
  //   anything else                 -> prose
  //
  // Switching a section to list rewrites its lines as bullets, which the
  // parser then infers back as list. That round-trip is what lets the
  // editor remember a per-section choice with no schema change anywhere.
  // Content matching no shape falls to prose and is carried verbatim —
  // the editor never rewrites what it cannot model.

  var UI_SEC_HEADING = /^(#{1,6})\s+(.*\S)\s*$/;
  var UI_SEC_MODES = [
    ['prose', '¶', 'Paragraph'],
    ['list', '•', 'Bullet list'],
    ['steps', '1.', 'Numbered steps'],
    ['table', '▦', 'Table'],
  ];
  // A markdown table's second line is its separator: dashes per column,
  // optionally colon-anchored for alignment. Two columns minimum — a
  // one-column "table" is indistinguishable from prose that happens to
  // start with a pipe, and treating it as one would be a worse guess.
  var UI_SEC_TABLE_SEP = /^\s*\|?\s*:?-+:?\s*(\|\s*:?-+:?\s*)+\|?\s*$/;

  // uiSplitRow splits one table row into trimmed cells, honoring the
  // GFM escape for a literal pipe. Splitting on a bare /\|/ would tear a
  // cell containing "a \| b" into two.
  function uiSplitRow(line) {
    var s = line.trim();
    if (s.charAt(0) === '|') s = s.substring(1);
    if (s.charAt(s.length - 1) === '|' && s.charAt(s.length - 2) !== '\\') {
      s = s.substring(0, s.length - 1);
    }
    var cells = [], cur = '';
    for (var i = 0; i < s.length; i++) {
      var ch = s.charAt(i);
      if (ch === '\\' && s.charAt(i + 1) === '|') { cur += '|'; i++; continue; }
      if (ch === '|') { cells.push(cur.trim()); cur = ''; continue; }
      cur += ch;
    }
    cells.push(cur.trim());
    return cells;
  }

  function uiEscapeCell(s) {
    return String(s == null ? '' : s).replace(/\|/g, '\\|').replace(/[\r\n]+/g, ' ').trim();
  }

  // uiParseAlign reads the separator row into per-column alignment.
  function uiParseAlign(line) {
    return uiSplitRow(line).map(function(c) {
      var left = c.charAt(0) === ':';
      var right = c.charAt(c.length - 1) === ':';
      if (left && right) return 'center';
      if (right) return 'right';
      if (left) return 'left';
      return '';
    });
  }

  // uiSectionTable pulls {head, align, rows} out of a table body. Rows
  // are padded or clipped to the header's width so the grid is always
  // rectangular — a ragged row is legal markdown but has nowhere to live
  // in a cell editor.
  function uiSectionTable(lines) {
    var body = lines.filter(function(l) { return l.trim() !== ''; });
    var head = uiSplitRow(body[0]);
    var align = uiParseAlign(body[1]);
    var rows = [];
    for (var i = 2; i < body.length; i++) {
      var cells = uiSplitRow(body[i]);
      while (cells.length < head.length) cells.push('');
      rows.push(cells.slice(0, head.length));
    }
    while (align.length < head.length) align.push('');
    return {head: head, align: align.slice(0, head.length), rows: rows};
  }

  // uiParseSections splits markdown into {title, level, lines} records.
  // Any ATX heading starts a new section and its level is remembered so
  // serializing puts back exactly the heading the author wrote. Text
  // ahead of the first heading becomes a level-0 "intro" section; an
  // empty one is dropped (the common case for a doc opening on a
  // heading).
  function uiParseSections(text) {
    var out = [];
    var cur = {title: '', level: 0, lines: []};
    String(text == null ? '' : text).split(/\r?\n/).forEach(function(line) {
      var m = UI_SEC_HEADING.exec(line);
      if (m) {
        out.push(cur);
        cur = {title: m[2], level: m[1].length, lines: []};
        return;
      }
      cur.lines.push(line);
    });
    out.push(cur);
    return out.filter(function(s, i) {
      return i > 0 || s.level > 0 || s.lines.join('\n').trim() !== '';
    });
  }

  // uiInferSectionMode returns 'list', 'steps', or 'prose' for a body, or
  // '' when the body is empty (the caller then falls back to whatever the
  // app declared for that slot). A mixed body — a bullet next to a bare
  // sentence, or a wrapped continuation line — is deliberately prose:
  // reshaping content the editor can only half-model would lose text.
  function uiInferSectionMode(lines) {
    var body = [];
    lines.forEach(function(l) { if (l.trim() !== '') body.push(l.trim()); });
    if (!body.length) return '';
    // Table first: its rows contain pipes, not bullets, so it can't
    // collide with the list checks below. Requires a header AND a
    // separator, which is what distinguishes a real table from prose
    // that merely uses a pipe character.
    if (body.length >= 2 && UI_SEC_TABLE_SEP.test(body[1]) && body[0].indexOf('|') >= 0) {
      return 'table';
    }
    var bullets = 0, steps = 0;
    body.forEach(function(l) {
      if (/^[-*]\s+\S/.test(l)) bullets++;
      else if (/^\d+[.)]\s+\S/.test(l)) steps++;
    });
    if (bullets === body.length) return 'list';
    if (steps === body.length) return 'steps';
    return 'prose';
  }

  // uiSectionItems strips bullet/number prefixes off each non-blank line.
  function uiSectionItems(lines) {
    return lines.map(function(l) { return l.trim(); })
      .filter(function(l) { return l !== ''; })
      .map(function(l) {
        return l.replace(/^[-*]\s+/, '').replace(/^\d+[.)]\s+/, '');
      });
  }

  // uiSectionBody renders one section's content (no heading). Empty
  // string means the section has nothing in it.
  function uiSectionBody(s) {
    if (s.mode === 'table') {
      var head = (s.head || []).map(uiEscapeCell);
      if (!head.length) return '';
      // A table with a header but no data rows still serializes: the
      // header IS content the author wrote, and dropping it would delete
      // the columns they just defined.
      var sep = (s.align || []).slice(0, head.length);
      while (sep.length < head.length) sep.push('');
      var out = ['| ' + head.join(' | ') + ' |'];
      out.push('| ' + sep.map(function(a) {
        if (a === 'center') return ':---:';
        if (a === 'right') return '---:';
        if (a === 'left') return ':---';
        return '---';
      }).join(' | ') + ' |');
      (s.rows || []).forEach(function(r) {
        var cells = [];
        for (var i = 0; i < head.length; i++) cells.push(uiEscapeCell(r[i]));
        // Drop rows that are entirely blank — an empty row added and
        // then abandoned is not content.
        if (cells.join('') === '') return;
        out.push('| ' + cells.join(' | ') + ' |');
      });
      return out.join('\n');
    }
    if (s.mode === 'list' || s.mode === 'steps') {
      var items = (s.items || []).map(function(x) { return String(x).trim(); })
        .filter(function(x) { return x !== ''; });
      if (!items.length) return '';
      return items.map(function(x, i) {
        return (s.mode === 'steps' ? (i + 1) + '. ' : '- ') + x;
      }).join('\n');
    }
    return String(s.text == null ? '' : s.text).replace(/^\s+|\s+$/g, '');
  }

  // uiSerializeSections writes the outline back to markdown. Canonical
  // and stable: heading, body, one blank line between sections, no
  // trailing newline — so a save that changed nothing produces a
  // byte-identical string. This value goes into LLM payloads, where
  // gratuitous churn costs a prompt-cache hit.
  //
  // An empty section is skipped ONLY when it never existed in the
  // document — a declared slot the author hasn't filled, or a free
  // section added but left blank. Those are UI affordances, and an empty
  // "## Examples" in a prompt is noise the model has to read.
  //
  // A heading the author actually wrote is kept even with an empty body,
  // because a body can be legitimately empty: "## Rules" followed
  // straight by "### Hard" owns no lines of its own, and dropping it
  // would delete the author's structure behind their back. Removing a
  // section is what the × button is for.
  function uiSerializeSections(secs) {
    var blocks = [];
    secs.forEach(function(s) {
      var body = uiSectionBody(s);
      var keepBare = s.fromDoc && s.level > 0 && String(s.title || '').trim() !== '';
      if (body === '' && !keepBare) return;
      if (s.level > 0) {
        if (body === '') {
          blocks.push(new Array(s.level + 1).join('#') + ' ' + String(s.title || '').trim());
          return;
        }
        var hashes = new Array(s.level + 1).join('#');
        blocks.push(hashes + ' ' + String(s.title || '').trim() + '\n' + body);
      } else {
        blocks.push(body);
      }
    });
    return blocks.join('\n\n');
  }

  // uiSectionState turns a parsed record into live editor state, honoring
  // the app's declared mode only when the body is empty (content always
  // wins over the skeleton — the author's markdown is the truth).
  //
  // fromDoc distinguishes a section READ from the stored value from one
  // synthesized for a declared-but-absent slot. Serialization needs it:
  // an author's heading survives an empty body, a placeholder does not.
  function uiSectionState(raw, spec, fromDoc) {
    var mode = uiInferSectionMode(raw.lines) || (spec && spec.mode) || 'prose';
    var s = {
      title: raw.title || '',
      level: raw.level,
      mode: mode,
      spec: spec || null,
      fromDoc: !!fromDoc,
      collapsed: false,
      text: '',
      items: [],
      head: [],
      align: [],
      rows: [],
    };
    if (mode === 'table') {
      var tbl = uiSectionTable(raw.lines);
      s.head = tbl.head;
      s.align = tbl.align;
      s.rows = tbl.rows;
    } else if (mode === 'list' || mode === 'steps') {
      s.items = uiSectionItems(raw.lines);
    } else {
      s.text = raw.lines.join('\n').replace(/^\s+|\s+$/g, '');
    }
    return s;
  }

  // uiApplySectionValue writes generated text into ONE section, adopting
  // the shape of the reply when it clearly has one (all bullets, all
  // numbered) and otherwise coercing it into whatever the section
  // already is. Also tolerates a reply that repeats the section's own
  // heading, which models do regularly no matter how the prompt is
  // worded.
  function uiApplySectionValue(s, text) {
    var lines = String(text == null ? '' : text).split(/\r?\n/);
    while (lines.length && lines[0].trim() === '') lines.shift();
    if (lines.length && UI_SEC_HEADING.test(lines[0])) lines.shift();
    var shape = uiInferSectionMode(lines);
    if (shape === 'table') {
      var tbl = uiSectionTable(lines);
      s.mode = 'table';
      s.head = tbl.head;
      s.align = tbl.align;
      s.rows = tbl.rows;
      s.text = '';
      s.items = [];
    } else if (shape === 'list' || shape === 'steps') {
      s.mode = shape;
      s.items = uiSectionItems(lines);
      s.text = '';
    } else if (s.mode === 'table') {
      // The model answered a table section with something that isn't a
      // table. Take it as prose rather than forcing it into cells.
      s.mode = 'prose';
      s.text = lines.join('\n').replace(/^\s+|\s+$/g, '');
      s.head = []; s.align = []; s.rows = [];
    } else if (s.mode === 'list' || s.mode === 'steps') {
      s.items = uiSectionItems(lines);
      s.text = '';
    } else {
      s.text = lines.join('\n').replace(/^\s+|\s+$/g, '');
      s.items = [];
    }
  }

  // buildSectionsEditor constructs the outline editor.
  //
  // opts:
  //   specs      — [{title, mode, help, placeholder, required}] declared skeleton
  //   allowFree  — user may add sections beyond the skeleton
  //   level      — heading level for skeleton + new sections (default 2)
  //   initial    — the markdown string to open with
  //   onChange   — fn(markdown, immediate); immediate=true on a structural
  //                edit (add/remove/move/mode switch), false while typing
  //   onSuggest  — optional fn(sectionTitle, currentBody, apply). When
  //                set, each section header gets a ✨ button. Call
  //                apply(text) to replace that section's body; not
  //                calling it leaves the section alone. Absent = no
  //                per-section suggestion.
  //
  // Returns {node, setValue, getValue}. setValue reloads without firing
  // onChange, so a programmatic write (the Suggest path, a raw-mode
  // switch) does not echo back as a change.
  function buildSectionsEditor(opts) {
    var specs = Array.isArray(opts.specs) ? opts.specs : [];
    var level = opts.level > 0 ? opts.level : 2;
    var allowFree = !!opts.allowFree;
    var node = el('div', {class: 'ui-sections-list'});
    var secs = [];

    function emit(immediate) {
      if (opts.onChange) opts.onChange(uiSerializeSections(secs), !!immediate);
    }

    // hintFor prefers the app's per-section placeholder, then its help
    // text, so an unfilled slot still says what belongs in it.
    function hintFor(s) {
      if (!s.spec) return '';
      return s.spec.placeholder || s.spec.help || '';
    }

    // load matches parsed sections to declared specs by case-insensitive
    // title. Document order is preserved for what the author already
    // wrote; declared sections missing from the document are appended as
    // empty slots. An empty value therefore renders the skeleton in
    // declared order, which is the case that matters most.
    function load(text) {
      var parsed = uiParseSections(text);
      var byTitle = {};
      specs.forEach(function(sp) {
        if (sp && sp.title) byTitle[String(sp.title).toLowerCase()] = sp;
      });
      var used = {};
      secs = parsed.map(function(raw) {
        var key = String(raw.title || '').toLowerCase();
        var sp = byTitle[key];
        if (sp) used[key] = true;
        return uiSectionState(raw, sp, true);
      });
      specs.forEach(function(sp) {
        if (!sp || !sp.title) return;
        if (used[String(sp.title).toLowerCase()]) return;
        secs.push(uiSectionState({title: sp.title, level: level, lines: []}, sp, false));
      });
    }

    // convert reshapes a section between modes without losing text.
    //
    // Leaving a table goes through its own MARKDOWN rather than its
    // cells: dropping to prose hands back the exact table source to edit
    // by hand, which is the only lossless answer when the target shape
    // has no columns. Entering a table from prose or a list makes a
    // one-column grid, each line a row, so the author can then split
    // columns out.
    function convert(s, to) {
      if (s.mode === to) return;
      if (s.mode === 'table') {
        var src = uiSectionBody(s);
        s.head = []; s.align = []; s.rows = [];
        if (to === 'prose') {
          s.text = src;
        } else {
          s.items = src.split(/\r?\n/).filter(function(l) { return l.trim() !== ''; });
        }
        s.mode = to;
        return;
      }
      if (to === 'table') {
        var lines = (s.mode === 'prose')
          ? String(s.text || '').split(/\r?\n/)
          : (s.items || []);
        var cells = lines.map(function(l) { return String(l).trim(); })
          .filter(function(l) { return l !== ''; });
        s.head = [cells.length ? 'Column 1' : 'Column 1'];
        s.align = [''];
        s.rows = cells.map(function(c) { return [c]; });
        s.text = ''; s.items = [];
        s.mode = to;
        return;
      }
      if (to === 'prose') {
        s.text = (s.items || []).join('\n');
        s.items = [];
      } else if (s.mode === 'prose') {
        s.items = uiSectionItems(String(s.text || '').split(/\r?\n/));
        s.text = '';
      }
      // list <-> steps keeps the same items; only the marker changes.
      s.mode = to;
    }

    function renderHead(s, idx) {
      var head = el('div', {class: 'ui-sec-head'});
      var caret = el('button', {
        type: 'button', class: 'ui-sec-caret',
        title: s.collapsed ? 'Expand' : 'Collapse',
      }, [s.collapsed ? '▸' : '▾']);
      caret.addEventListener('click', function() {
        s.collapsed = !s.collapsed;
        render();
      });
      head.appendChild(caret);

      // A declared section's title is fixed text, not an input: the
      // skeleton is the app's contract and renaming a slot would silently
      // detach it from its help, placeholder, and required flag.
      if (s.level === 0) {
        // Not a slot: this is whatever sits ahead of the first heading,
        // which for a document written before anyone sectioned it is the
        // whole thing. Labeling it "Unsectioned" rather than giving it a
        // content-sounding name says what it actually is and hints that
        // it wants breaking up into the slots below.
        head.appendChild(el('span', {
          class: 'ui-sec-title-fixed intro',
          title: 'Text ahead of the first heading. Move it into sections, or leave it — it stays at the top either way.',
        }, ['Unsectioned']));
      } else if (s.spec) {
        head.appendChild(el('span', {class: 'ui-sec-title-fixed'}, [s.title]));
      } else {
        var ti = el('input', {
          type: 'text', class: 'ui-sec-title-input',
          value: s.title, placeholder: 'Section title…',
        });
        ti.addEventListener('input', function() { s.title = ti.value; emit(false); });
        ti.addEventListener('blur', function() { s.title = ti.value; emit(true); });
        head.appendChild(ti);
      }

      var modes = el('div', {class: 'ui-sec-modes'});
      UI_SEC_MODES.forEach(function(m) {
        var b = el('button', {
          type: 'button', title: m[2],
          class: 'ui-sec-mode' + (s.mode === m[0] ? ' on' : ''),
        }, [m[1]]);
        b.addEventListener('click', function() {
          if (s.mode === m[0]) return;
          convert(s, m[0]);
          render();
          emit(true);
        });
        modes.appendChild(b);
      });
      head.appendChild(modes);

      if (opts.onSuggest) {
        var sg = el('button', {
          type: 'button', class: 'ui-sec-suggest',
          title: 'Draft this section with assistance',
        }, ['✨']);
        sg.addEventListener('click', function() {
          // The workbench is modal and owns its own busy state, so the
          // button does not latch. apply() may be called once, never, or
          // after a long conversation.
          opts.onSuggest(s.title || '', uiSectionBody(s), function(text) {
            if (text == null) return;
            uiApplySectionValue(s, text);
            render();
            emit(true);
          });
        });
        head.appendChild(sg);
      }

      // Every section moves, declared ones included. The outline seeds a
      // document, it does not constrain one: an author who can add their
      // own sections can obviously also decide that Rules belongs above
      // Approach. Order is carried by the markdown itself, so a move
      // sticks the same way any other edit does.
      //
      // The level-0 unsectioned block is the one exception, and for a
      // correctness reason rather than a policy one: it serializes with
      // no heading of its own, so anywhere but first its lines would be
      // absorbed into the preceding section on the next parse. It cannot
      // move, and nothing may move above it.
      var floor = (secs.length && secs[0].level === 0) ? 1 : 0;
      if (s.level > 0) {
        var mover = function(label, delta) {
          var b = el('button', {
            type: 'button', class: 'ui-sec-move',
            title: delta < 0 ? 'Move up' : 'Move down',
          }, [label]);
          var to = idx + delta;
          if (to < floor || to >= secs.length) b.disabled = true;
          b.addEventListener('click', function() {
            if (to < floor || to >= secs.length) return;
            var moved = secs.splice(idx, 1)[0];
            secs.splice(to, 0, moved);
            render();
            emit(true);
          });
          return b;
        };
        // Arrows, not triangles. ▴/▾ render at wildly different optical
        // weights across fonts, and ▾ is already the collapse caret two
        // buttons to the left — the same glyph meaning two things in one
        // row. ↑/↓ are a matched pair everywhere and read as "move"
        // rather than "expand".
        head.appendChild(mover('↑', -1));
        head.appendChild(mover('↓', 1));
      }

      // Delete stays off for declared slots, because it could not do
      // what it says: load() re-offers any declared section the document
      // lacks, so the slot would reappear on the next open. Emptying one
      // already achieves the real goal — an empty section writes nothing
      // into the saved markdown.
      if (!s.spec && s.level > 0) {
        var del = el('button', {type: 'button', class: 'ui-sec-del', title: 'Delete section'}, ['×']);
        del.addEventListener('click', async function() {
          if (uiSectionBody(s) !== '') {
            var name = s.title || 'this section';
            if (!(await window.uiConfirm('Delete "' + name + '" and its contents?'))) return;
          }
          secs.splice(idx, 1);
          render();
          emit(true);
        });
        head.appendChild(del);
      }
      return head;
    }

    function renderProse(s, body) {
      var ta = el('textarea', {
        class: 'ui-form-textarea ui-sec-text',
        rows: '3', placeholder: hintFor(s),
      });
      ta.value = s.text || '';
      function autosize() {
        ta.style.height = 'auto';
        // scrollHeight reads 0 inside a hidden container (a collapsed
        // parent, an inactive wizard step); the rows=3 fallback stands
        // until the next input event runs this while visible.
        if (ta.scrollHeight > 0) ta.style.height = (ta.scrollHeight + 2) + 'px';
      }
      ta.addEventListener('input', function() { s.text = ta.value; autosize(); emit(false); });
      ta.addEventListener('blur', function() { s.text = ta.value; emit(true); });
      body.appendChild(ta);
      setTimeout(autosize, 0);
    }

    // renderItems owns the +/- rows for list and steps modes. It reuses
    // the .ui-rules-* classes so this reads as the same widget users
    // already know from the rules field.
    function renderItems(s, body, focusIdx) {
      body.innerHTML = '';
      if (!s.items.length) {
        body.appendChild(el('div', {class: 'ui-rules-empty'},
          [hintFor(s) || 'Nothing here yet — add an item below.']));
      }
      s.items.forEach(function(it, i) {
        var row = el('div', {class: 'ui-rules-row'});
        row.appendChild(el('span', {class: 'ui-rules-num'},
          [s.mode === 'steps' ? (i + 1) + '.' : '•']));
        var inp = el('input', {type: 'text', class: 'ui-rules-input', value: it, placeholder: 'item…'});
        inp.addEventListener('input', function() { s.items[i] = inp.value; emit(false); });
        inp.addEventListener('blur', function() { s.items[i] = inp.value; emit(true); });
        inp.addEventListener('keydown', function(ev) {
          if (ev.key === 'Enter') {
            ev.preventDefault();
            s.items[i] = inp.value;
            s.items.splice(i + 1, 0, '');
            renderItems(s, body, i + 1);
            emit(true);
          } else if (ev.key === 'Backspace' && inp.value === '' && s.items.length > 1) {
            ev.preventDefault();
            s.items.splice(i, 1);
            renderItems(s, body, Math.max(0, i - 1));
            emit(true);
          }
        });
        row.appendChild(inp);
        var x = el('button', {type: 'button', class: 'ui-rules-del', title: 'Remove'}, ['×']);
        x.addEventListener('click', function() {
          s.items.splice(i, 1);
          renderItems(s, body, Math.max(0, i - 1));
          emit(true);
        });
        row.appendChild(x);
        body.appendChild(row);
      });
      var add = el('button', {type: 'button', class: 'ui-rules-add'}, ['+ Add item']);
      add.addEventListener('click', function() {
        s.items.push('');
        renderItems(s, body, s.items.length - 1);
      });
      body.appendChild(add);
      if (focusIdx != null) {
        var ins = body.querySelectorAll('.ui-rules-input');
        if (ins[focusIdx]) ins[focusIdx].focus();
      }
    }

    // renderTable draws the cell grid. Layout mirrors the markdown it
    // serializes to: a header row, an alignment strip standing in for
    // the separator row, then data rows.
    function renderTable(s, body, focus) {
      body.innerHTML = '';
      if (!s.head || !s.head.length) { s.head = ['Column 1']; s.align = ['']; }
      var cols = s.head.length;
      var grid = el('div', {class: 'ui-sec-table'});
      grid.style.gridTemplateColumns = 'repeat(' + cols + ', minmax(5rem, 1fr)) auto';

      function cellInput(value, cls, onCommit) {
        var inp = el('input', {type: 'text', class: cls, value: value == null ? '' : value});
        inp.addEventListener('input', function() { onCommit(inp.value); emit(false); });
        inp.addEventListener('blur', function() { onCommit(inp.value); emit(true); });
        return inp;
      }

      // Header row + a per-column delete.
      s.head.forEach(function(h, c) {
        var wrap = el('div', {class: 'ui-sec-th'});
        wrap.appendChild(cellInput(h, 'ui-sec-cell head', function(v) { s.head[c] = v; }));
        if (cols > 1) {
          var delCol = el('button', {type: 'button', class: 'ui-sec-colx', title: 'Delete this column'}, ['×']);
          delCol.addEventListener('click', async function() {
            var hasData = (s.rows || []).some(function(r) { return String(r[c] || '').trim() !== ''; });
            if (hasData && !(await window.uiConfirm('Delete the "' + (s.head[c] || 'column') + '" column and its cells?'))) return;
            s.head.splice(c, 1);
            s.align.splice(c, 1);
            (s.rows || []).forEach(function(r) { r.splice(c, 1); });
            renderTable(s, body, null);
            emit(true);
          });
          wrap.appendChild(delCol);
        }
        grid.appendChild(wrap);
      });
      var addCol = el('button', {type: 'button', class: 'ui-sec-addcol', title: 'Add a column'}, ['+']);
      addCol.addEventListener('click', function() {
        s.head.push('Column ' + (s.head.length + 1));
        s.align.push('');
        (s.rows || []).forEach(function(r) { r.push(''); });
        renderTable(s, body, null);
        emit(true);
      });
      grid.appendChild(addCol);

      // Alignment strip — one control per column, standing in for the
      // markdown separator row it serializes to. Click cycles.
      var ALIGN_CYCLE = ['', 'left', 'center', 'right'];
      var ALIGN_GLYPH = {'': '─', left: '⇤', center: '↔', right: '⇥'};
      s.head.forEach(function(_, c) {
        var a = s.align[c] || '';
        var b = el('button', {
          type: 'button', class: 'ui-sec-align',
          title: 'Column alignment: ' + (a || 'default') + ' (click to change)',
        }, [ALIGN_GLYPH[a]]);
        b.addEventListener('click', function() {
          var i = ALIGN_CYCLE.indexOf(s.align[c] || '');
          s.align[c] = ALIGN_CYCLE[(i + 1) % ALIGN_CYCLE.length];
          renderTable(s, body, null);
          emit(true);
        });
        grid.appendChild(b);
      });
      grid.appendChild(el('span', {}));

      // Data rows.
      (s.rows || []).forEach(function(row, r) {
        for (var c = 0; c < cols; c++) {
          (function(rr, cc) {
            grid.appendChild(cellInput(row[cc], 'ui-sec-cell', function(v) { s.rows[rr][cc] = v; }));
          })(r, c);
        }
        var delRow = el('button', {type: 'button', class: 'ui-sec-rowx', title: 'Delete this row'}, ['×']);
        delRow.addEventListener('click', function() {
          s.rows.splice(r, 1);
          renderTable(s, body, null);
          emit(true);
        });
        grid.appendChild(delRow);
      });
      body.appendChild(grid);

      var addRow = el('button', {type: 'button', class: 'ui-rules-add'}, ['+ Add row']);
      addRow.addEventListener('click', function() {
        var blank = [];
        for (var i = 0; i < cols; i++) blank.push('');
        s.rows = s.rows || [];
        s.rows.push(blank);
        renderTable(s, body, s.rows.length - 1);
      });
      body.appendChild(addRow);

      if (focus != null) {
        var inputs = body.querySelectorAll('.ui-sec-cell:not(.head)');
        var first = inputs[focus * cols];
        if (first) first.focus();
      }
    }

    function renderSection(s, idx) {
      var empty = uiSectionBody(s) === '';
      var wrap = el('div', {class: 'ui-sec' + (s.collapsed ? ' collapsed' : '') + (empty ? ' empty' : '')});
      if (s.spec && s.spec.required && empty) wrap.classList.add('missing');
      wrap.appendChild(renderHead(s, idx));

      if (s.collapsed) {
        var prev = uiSectionBody(s).replace(/\s+/g, ' ').trim();
        if (prev.length > 90) prev = prev.substring(0, 90) + '…';
        wrap.appendChild(el('div', {class: 'ui-sec-preview'},
          [prev || hintFor(s) || 'Empty']));
        return wrap;
      }

      var body = el('div', {class: 'ui-sec-body'});
      if (s.mode === 'table') renderTable(s, body, null);
      else if (s.mode === 'list' || s.mode === 'steps') renderItems(s, body, null);
      else renderProse(s, body);
      wrap.appendChild(body);
      if (s.spec && s.spec.help) {
        wrap.appendChild(el('div', {class: 'ui-sec-help'}, [s.spec.help]));
      }
      return wrap;
    }

    // render rebuilds the whole outline. Called only on STRUCTURAL edits
    // (add, remove, move, mode switch, collapse) — typing mutates state
    // in place, because a rebuild here would steal focus mid-keystroke.
    function render() {
      node.innerHTML = '';
      secs.forEach(function(s, idx) { node.appendChild(renderSection(s, idx)); });
      if (allowFree) {
        var add = el('button', {type: 'button', class: 'ui-sec-add'}, ['+ Add section']);
        add.addEventListener('click', function() {
          secs.push(uiSectionState({title: '', level: level, lines: []}, null, false));
          render();
          var titles = node.querySelectorAll('.ui-sec-title-input');
          if (titles.length) titles[titles.length - 1].focus();
        });
        node.appendChild(add);
      }
    }

    load(String(opts.initial == null ? '' : opts.initial));
    render();

    return {
      node: node,
      getValue: function() { return uiSerializeSections(secs); },
      setValue: function(text) { load(String(text == null ? '' : text)); render(); },
    };
  }
