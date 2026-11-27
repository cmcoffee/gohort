import json
import os

name = os.environ.get("name", "").strip()
if not name:
    print(json.dumps({"message": "Give the plant a name."}))
else:
    print(json.dumps({"message": "Added " + name + ".", "records": [{"kind": "plant", "name": name}]}))
