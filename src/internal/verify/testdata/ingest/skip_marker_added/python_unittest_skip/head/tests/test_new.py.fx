import unittest


class T(unittest.TestCase):
    @unittest.skip("later")
    def test_new(self):
        pass
