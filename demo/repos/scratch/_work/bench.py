"""Single-row INSERTs per second into SQLite, each in a transaction of its own,
for the journal and sync modes worth comparing."""

import os
import sqlite3
import tempfile
import time

N = 2000

for journal, sync in [("DELETE", "FULL"), ("WAL", "FULL"), ("WAL", "NORMAL")]:
    with tempfile.TemporaryDirectory() as d:
        db = sqlite3.connect(os.path.join(d, "bench.db"), isolation_level=None)
        db.execute(f"PRAGMA journal_mode={journal}")
        db.execute(f"PRAGMA synchronous={sync}")
        db.execute("CREATE TABLE links (key TEXT PRIMARY KEY, url TEXT)")
        start = time.perf_counter()
        for i in range(N):
            db.execute("INSERT INTO links VALUES (?, ?)", (f"k{i}", f"https://example.com/{i}"))
        rate = N / (time.perf_counter() - start)
        print(f"{journal:7} {sync:7} {rate:9,.0f} inserts/s")
        db.close()
