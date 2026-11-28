import json
from gohort import records

calls = [r for r in records() if r.get("kind") == "call"]
print(json.dumps({"calls": len(calls), "minutes": sum(int(r.get("minutes") or 0) for r in calls),
                  "escalated": sum(1 for r in calls if r.get("outcome") == "escalated")}))
