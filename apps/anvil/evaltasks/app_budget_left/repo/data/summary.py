import json
from gohort import records

entries = [r for r in records() if r.get("kind") in ("income", "expense")]
spent = sum(float(r.get("amount", 0)) for r in entries)
print(json.dumps({"spent": round(spent, 2), "count": len(entries)}))
