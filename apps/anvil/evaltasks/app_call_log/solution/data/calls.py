import json
from gohort import records

calls = [r for r in records() if r.get("kind") == "call"]
calls.reverse()
print(json.dumps([{k: r.get(k) for k in ("id", "caller", "phone", "direction", "minutes", "outcome")} for r in calls]))
