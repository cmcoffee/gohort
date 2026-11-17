def median(values):
    """The middle value of values; for an even count, the mean of the two middle ones."""
    if not values:
        raise ValueError("median of no values")
    ordered = sorted(values)
    mid = len(ordered) // 2
    if len(ordered) % 2:
        return ordered[mid]
    return (ordered[mid - 1] + ordered[mid]) / 2
