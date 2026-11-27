import json
import os

name = os.environ.get("name", "").strip()
try:
    days = int(os.environ.get("water_days", "").strip())
except ValueError:
    days = None
if not name:
    print(json.dumps({"message": "Give the plant a name."}))
else:
    rec = {"kind": "plant", "name": name}
    if days is not None:
        rec["water_days"] = days
    print(json.dumps({"message": "Added " + name + ".", "records": [rec]}))
