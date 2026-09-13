import unittest

from snip import KEY_LENGTH, new_key


class KeyTest(unittest.TestCase):
    def test_length(self):
        self.assertEqual(len(new_key(set())), KEY_LENGTH)

    def test_avoids_taken_keys(self):
        taken = {"a", "b"}
        for _ in range(200):
            self.assertNotIn(new_key(taken), taken)


if __name__ == "__main__":
    unittest.main()
