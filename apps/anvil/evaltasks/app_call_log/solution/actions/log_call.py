import json
import os

from salesforce import log_task

phone = os.environ.get("phone", "").strip()
if not phone:
    print(json.dumps({"message": "The phone number is needed to log a call."}))
else:
    try:
        minutes = int(float(os.environ.get("minutes", "0") or 0))
    except ValueError:
        minutes = 0
    outcome = os.environ.get("outcome", "resolved").strip() or "resolved"
    rec = {"kind": "call", "caller": os.environ.get("caller", "").strip(), "phone": phone,
           "direction": os.environ.get("direction", "inbound").strip() or "inbound",
           "minutes": minutes, "outcome": outcome}
    print(json.dumps({"message": "Logged.", "records": [rec],
                      "result": {"salesforce_task_id": log_task(phone, outcome)}}))
