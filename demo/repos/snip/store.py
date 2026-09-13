"""The link table, a JSON file.

A shortener's table is a few hundred kilobytes long before anyone notices, so
it is read once and written whole. The write goes via a temporary file in the
same directory and a rename, which is atomic: a crash halfway leaves either the
old table or the new one, never half of either.
"""

import json
import os
import threading


class Store:
    def __init__(self, path):
        self.path = path
        self.lock = threading.Lock()
        self.links = {}
        if os.path.exists(path):
            with open(path, encoding="utf-8") as f:
                self.links = json.load(f)

    def get(self, key):
        return self.links.get(key)

    def keys(self):
        return set(self.links)

    def put(self, key, url):
        with self.lock:
            self.links[key] = url
            self.flush()

    def flush(self):
        tmp = self.path + ".tmp"
        with open(tmp, "w", encoding="utf-8") as f:
            json.dump(self.links, f, indent=1, sort_keys=True)
        os.replace(tmp, self.path)
