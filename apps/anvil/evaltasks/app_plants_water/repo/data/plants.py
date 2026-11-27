import json
from gohort import records

plants = [r for r in records() if r.get("kind") == "plant"]
plants.sort(key=lambda r: r.get("name", "").lower())
print(json.dumps([{"id": r.get("id"), "name": r.get("name", "")} for r in plants]))
