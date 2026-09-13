# snip

A URL shortener in one file of Python and no dependencies: post a URL, get a
short key back, follow the key to the URL.

```sh
python3 snip.py --port 8000          # then open http://localhost:8000/
curl -d '{"url": "https://example.com/a/very/long/one"}' http://localhost:8000/
```

The links live in memory: a restart forgets them.

## Tests

```sh
python3 -m unittest discover
```
