import json
import os

title = os.environ.get("title", "").strip()
if not title:
    print(json.dumps({"message": "Say what the task is."}))
else:
    print(json.dumps({"message": "Added.", "records": [{"kind": "task", "title": title, "done": False}]}))
