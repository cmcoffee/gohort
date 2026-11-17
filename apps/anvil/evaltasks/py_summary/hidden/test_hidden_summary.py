import unittest

from summary import summarize


class TestSummaryHidden(unittest.TestCase):
    def test_values(self):
        rows = [{"n": 4}, {"n": None}, {}, {"n": 2}]
        self.assertEqual(summarize(rows, "n"), {"count": 2, "total": 6, "mean": 3.0, "min": 2, "max": 4})

    def test_none_left(self):
        self.assertEqual(summarize([{}, {"n": None}], "n"),
                         {"count": 0, "total": None, "mean": None, "min": None, "max": None})


if __name__ == "__main__":
    unittest.main()
