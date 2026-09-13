import os
import tempfile
import unittest

from snip import KEY_LENGTH, new_key
from store import Store


class KeyTest(unittest.TestCase):
    def test_length(self):
        self.assertEqual(len(new_key(set())), KEY_LENGTH)

    def test_avoids_taken_keys(self):
        taken = {"a", "b"}
        for _ in range(200):
            self.assertNotIn(new_key(taken), taken)


class StoreTest(unittest.TestCase):
    def setUp(self):
        self.dir = tempfile.TemporaryDirectory()
        self.path = os.path.join(self.dir.name, "links.json")
        self.addCleanup(self.dir.cleanup)

    def test_missing_key(self):
        self.assertIsNone(Store(self.path).get("nope"))

    def test_survives_a_restart(self):
        Store(self.path).put("b7Qx", "https://example.com/")
        self.assertEqual(Store(self.path).get("b7Qx"), "https://example.com/")

    def test_keys(self):
        store = Store(self.path)
        store.put("one", "https://example.com/1")
        store.put("two", "https://example.com/2")
        self.assertEqual(store.keys(), {"one", "two"})


if __name__ == "__main__":
    unittest.main()
