"""Salesforce, stubbed: no network call is made. Wire to the real API later."""
import hashlib


def log_task(phone, outcome):
    """A made-up Task id for the call (stub)."""
    return "00T" + hashlib.sha1((phone + "|" + outcome).encode()).hexdigest()[:15]
