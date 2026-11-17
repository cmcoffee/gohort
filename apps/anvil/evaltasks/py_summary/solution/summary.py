def summarize(rows, field):
    """Count, total, mean, min and max of field across rows (a list of dicts)."""
    values = [row[field] for row in rows if row.get(field) is not None]
    if not values:
        return {"count": 0, "total": None, "mean": None, "min": None, "max": None}
    total = sum(values)
    return {"count": len(values), "total": total, "mean": total / len(values),
            "min": min(values), "max": max(values)}
