import json
import os

kind = os.environ.get("kind", "expense").strip().lower()
try:
    amount = round(float(os.environ.get("amount", "")), 2)
except ValueError:
    amount = None
if kind not in ("income", "expense") or amount is None:
    print(json.dumps({"message": "Give an amount, and say whether it is income or an expense."}))
else:
    rec = {"kind": kind, "amount": amount, "note": os.environ.get("note", "").strip()}
    print(json.dumps({"message": "Added.", "records": [rec]}))
