import json
from gohort import records

entries = [r for r in records() if r.get("kind") in ("income", "expense")]
income = sum(float(r.get("amount", 0)) for r in entries if r.get("kind") == "income")
spent = sum(float(r.get("amount", 0)) for r in entries if r.get("kind") == "expense")
print(json.dumps({"spent": round(spent, 2), "left": round(income - spent, 2), "count": len(entries)}))
