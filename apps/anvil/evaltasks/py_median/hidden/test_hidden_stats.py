import unittest

from stats import median


class TestMedianHidden(unittest.TestCase):
    def test_cases(self):
        self.assertEqual(median([1, 2, 3, 4]), 2.5)
        self.assertEqual(median([5]), 5)
        self.assertEqual(median([4, 1, 3, 2]), 2.5)
        with self.assertRaises(ValueError):
            median([])


if __name__ == "__main__":
    unittest.main()
