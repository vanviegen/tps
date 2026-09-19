#!/bin/sh
# Rebuild and restart tps whenever a .go or .ts file changes.
cd "$(dirname "$0")"
trap 'kill $pid 2>/dev/null; exit' INT TERM

while true; do
  pid=
  npm install && npm run build
  if CGO_ENABLED=0 go build -o tps .; then
    ./tps -no-open &
    pid=$!
  fi
  inotifywait -qq -e modify,create,delete,move --include '\.(go|ts)$' $(go list -f '{{.Dir}}' ./...) web
  [ -n "$pid" ] && kill "$pid" 2>/dev/null && wait "$pid" 2>/dev/null
done
