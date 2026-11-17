def summarize(rows, field):
    """Count, total and mean of field across rows (a list of dicts)."""
    values = [row[field] for row in rows]
    total = sum(values)
    return {"count": len(values), "total": total, "mean": total / len(values)}
