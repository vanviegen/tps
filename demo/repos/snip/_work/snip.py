#!/usr/bin/env python3
"""snip: a URL shortener that fits on a page.

    GET  /            a one-field form
    POST /            {"url": "https://..."} -> {"key": "b7Qx", "short": "http://host/b7Qx"}
    GET  /<key>       302 to the URL that key stands for
"""

import argparse
import json
import random
import string
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

from store import Store

ALPHABET = string.ascii_letters + string.digits
SCHEMES = ("http://", "https://")
KEY_LENGTH = 4

FORM = b"""<!doctype html><title>snip</title>
<h1>snip</h1>
<form method="post" action="/">
  <input name="url" size="60" placeholder="https://...">
  <button>shorten</button>
</form>
"""


def new_key(taken):
    """A key no link has yet. Four characters are 15 million links; long
    before that many, the retry below is what keeps them apart."""
    while True:
        key = "".join(random.choice(ALPHABET) for _ in range(KEY_LENGTH))
        if key not in taken:
            return key


def allowed(url):
    """Only a link a browser follows as a link. The scheme is compared in
    lower case: HTTPS:// is a URL too, and refusing it would be a bug.
    A prefix check rather than urlparse, which reads javascript:alert(1)
    as a scheme just as happily; the allowlist is the point."""
    return url.lower().startswith(SCHEMES)


class Handler(BaseHTTPRequestHandler):
    server_version = "snip"

    def do_GET(self):
        key = self.path.lstrip("/")
        if not key:
            return self.send(200, FORM, "text/html; charset=utf-8")
        url = self.server.store.get(key)
        if url is None:
            return self.send(404, b"no such link\n")
        self.send_response(302)
        self.send_header("Location", url)
        self.end_headers()

    def do_POST(self):
        length = int(self.headers.get("Content-Length") or 0)
        body = self.rfile.read(length).decode("utf-8", "replace")
        url = self.read_url(body)
        if not url:
            return self.send(400, b'{"error": "no url given"}\n', "application/json")
        if not allowed(url):
            return self.send(400, b'{"error": "only http and https links can be shortened"}\n', "application/json")
        key = new_key(self.server.store.keys())
        self.server.store.put(key, url)
        short = "http://%s/%s" % (self.headers.get("Host", "localhost"), key)
        self.send(201, json.dumps({"key": key, "short": short}).encode() + b"\n", "application/json")

    def read_url(self, body):
        """The url out of a JSON body or a form post, whichever came in."""
        if body.lstrip().startswith("{"):
            try:
                return (json.loads(body).get("url") or "").strip()
            except ValueError:
                return ""
        from urllib.parse import parse_qs

        return (parse_qs(body).get("url", [""])[0]).strip()

    def send(self, status, body, content_type="text/plain; charset=utf-8"):
        self.send_response(status)
        self.send_header("Content-Type", content_type)
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, fmt, *args):
        print("%s - %s" % (self.address_string(), fmt % args), flush=True)


def main():
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--host", default="127.0.0.1")
    ap.add_argument("--port", type=int, default=8000)
    ap.add_argument("--file", default="links.json", help="where the links are kept")
    args = ap.parse_args()

    server = ThreadingHTTPServer((args.host, args.port), Handler)
    server.store = Store(args.file)
    print("snip on http://%s:%d (%d links)" % (args.host, args.port, len(server.store.keys())), flush=True)
    server.serve_forever()


if __name__ == "__main__":
    main()
