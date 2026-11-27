import json
import os
from gohort import records

want = os.environ.get("id", "")
for r in records():
    if r.get("kind") == "task" and r.get("id") == want:
        r["done"] = not r.get("done")
        print(json.dumps({"records": [r]}))
        break
else:
    print(json.dumps({"message": "No such task."}))
