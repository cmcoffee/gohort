package flows

// The Python generators behind the native flows of the structured exports
// (Excel, PowerPoint) and Word's fallback where pandoc is not installed.
// Each is written into the run's folder as gen.py and run as
// `python3 gen.py {in} {out}`: it reads its input file (the export's JSON
// payload, {"title", "date", "data"}, or for Word the Markdown) and writes
// the document to the output file. Their packages are provisioned before the
// run (Flow.Py) and reach the sandbox on its PYTHONPATH.
//
// They were core's export registry's built-in formats; moved here so every
// export, whether an agent's export tool, Scribe or convert_file asks for it,
// is one job an admin can adapt.

const xlsxGeneratorPy = `
import sys, json
import openpyxl
payload = json.load(open(sys.argv[1]))
data = payload.get("data") or {}
if isinstance(data, str):
    s = data.strip()
    data = json.loads(s) if s[:1] in ("{", "[") else {"rows": [[data]]}
if isinstance(data, list):
    data = {"rows": data}
sheets = data.get("sheets")
if not sheets:
    sheets = [{"name": data.get("sheet_name", "Sheet1"), "rows": data.get("rows", [])}]
wb = openpyxl.Workbook()
default = wb.active
for i, sh in enumerate(sheets):
    name = str(sh.get("name", "Sheet%d" % (i + 1)))[:31]
    ws = default if i == 0 else wb.create_sheet()
    ws.title = name
    for row in sh.get("rows", []):
        ws.append(row if isinstance(row, list) else [row])
wb.save(sys.argv[2])
`

const docxGeneratorPy = `
import sys
from docx import Document
md = open(sys.argv[1], encoding="utf-8", errors="replace").read()
doc = Document()
for raw in md.split("\n"):
    s = raw.strip()
    if not s:
        continue
    if s.startswith("### "):
        doc.add_heading(s[4:], level=3)
    elif s.startswith("## "):
        doc.add_heading(s[3:], level=2)
    elif s.startswith("# "):
        doc.add_heading(s[2:], level=1)
    elif s[:2] in ("- ", "* "):
        doc.add_paragraph(s[2:], style="List Bullet")
    else:
        doc.add_paragraph(s)
doc.save(sys.argv[2])
`

const pptxGeneratorPy = `
import sys, json
from pptx import Presentation
payload = json.load(open(sys.argv[1]))
data = payload.get("data") or {}
if isinstance(data, str):
    s = data.strip()
    if s[:1] in ("{", "["):
        data = json.loads(s)
    else:
        data = {"slides": [{"title": payload.get("title", ""),
                            "bullets": [ln.strip() for ln in data.split("\n") if ln.strip()]}]}
if isinstance(data, list):
    data = {"slides": data}
slides = data.get("slides", [])
prs = Presentation()
deck_title = payload.get("title", "")
if deck_title:
    s = prs.slides.add_slide(prs.slide_layouts[0])
    s.shapes.title.text = deck_title
for sl in slides:
    slide = prs.slides.add_slide(prs.slide_layouts[1])
    slide.shapes.title.text = str(sl.get("title", ""))
    tf = slide.placeholders[1].text_frame
    for i, b in enumerate(sl.get("bullets", [])):
        p = tf.paragraphs[0] if i == 0 else tf.add_paragraph()
        p.text = str(b)
prs.save(sys.argv[2])
`
