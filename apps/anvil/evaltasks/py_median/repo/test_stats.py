import unittest

from stats import median


class TestMedian(unittest.TestCase):
    def test_odd(self):
        self.assertEqual(median([3, 1, 2]), 2)


if __name__ == "__main__":
    unittest.main()
