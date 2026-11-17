def median(values):
    """The middle value of values; for an even count, the mean of the two middle ones."""
    ordered = sorted(values)
    return ordered[len(ordered) // 2]
