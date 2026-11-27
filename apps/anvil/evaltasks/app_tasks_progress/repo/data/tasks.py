import json
from gohort import records

tasks = [r for r in records() if r.get("kind") == "task"]
print(json.dumps([{"id": r.get("id"), "title": r.get("title", ""), "done": bool(r.get("done"))} for r in tasks]))
