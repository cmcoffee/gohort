import json
from gohort import records

tasks = [r for r in records() if r.get("kind") == "task"]
print(json.dumps({"done": sum(1 for r in tasks if r.get("done")), "total": len(tasks)}))
