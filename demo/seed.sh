#!/bin/bash
# Fill a home directory with a TPS that has been in use for a few weeks: two
# small projects in ~/projects, and a board's worth of tasks around them —
# merged ones whose commits are in the git log, one waiting to be merged with
# its work sitting in a workspace, a couple in the plan column (one of them
# waiting for another), a parked one and a closed one.
#
# The project's 'app' service runs this before starting TPS, so the play button
# gives a dashboard with something on it instead of an empty sidebar. Tests may
# want the same fixture: point TPS_DEMO_HOME at a directory of their own, and
# nothing outside it is touched.
#
#   demo/seed.sh            seed, unless this home has been seeded already
#   demo/seed.sh --force    throw the seeded projects and tasks away and redo them
#
# Nothing here is TPS-specific beyond the two files it writes: the config
# (~/.config/tps/projects.json) and the task directories under
# ~/.local/share/tps/tasks/<project>/<task>, which hold a workspace clone, the
# claude session directory, and the condensed chat log the dashboard shows.

set -euo pipefail

SRC="$(cd "$(dirname "$0")" && pwd)"
HOME_DIR="${TPS_DEMO_HOME:-$HOME}"
CONFIG="$HOME_DIR/.config/tps/projects.json"
DATA="$HOME_DIR/.local/share/tps"
PROJECTS="$HOME_DIR/projects"
NOW=$(date +%s)

if [ "${1:-}" = --force ]; then
	rm -rf "$PROJECTS/snip" "$PROJECTS/standup" "$DATA/tasks/snip" "$DATA/tasks/standup" "$CONFIG"
elif [ -e "$CONFIG" ]; then
	echo "demo/seed.sh: $CONFIG exists, leaving it alone (--force to reseed)"
	exit 0
fi

# ms since the epoch, N days (and optionally N hours) ago: what phaseAt and the
# chat log are stamped with.
ms() { echo $(( (NOW - ${1:-0} * 86400 - ${2:-0} * 3600) * 1000 )); }

# --- the repositories ---------------------------------------------------------
#
# Each is built commit by commit out of demo/repos/<name>: that tree is the
# final state, and demo/repos/<name>/_stages/<n>/<file> holds the version a
# given commit had, where a file changed along the way. So the history is real
# — the merged tasks below are the commits that land these changes — without
# keeping a patch series anywhere.

repo_init() {
	REPO="$PROJECTS/$1"
	SRC_TREE="$SRC/repos/$1"
	rm -rf "$REPO"
	mkdir -p "$REPO"
	git -C "$REPO" init -q -b main
	git -C "$REPO" config user.name "Robin Aalders"
	git -C "$REPO" config user.email "robin@example.com"
}

# copy <stage> <file>... : the given files into the repo, taking the version
# under _stages/<stage>/ where there is one. Stage "-" is the final tree.
copy() {
	local stage="$1" from f
	shift
	for f; do
		from="$SRC_TREE/$f"
		if [ "$stage" != - ] && [ -e "$SRC_TREE/_stages/$stage/$f" ]; then
			from="$SRC_TREE/_stages/$stage/$f"
		fi
		install -D -m "$(stat -c %a "$from")" "$from" "$REPO/$f"
	done
}

# commit <days ago> <message>
commit() {
	local when
	when="$(date -d "@$(( NOW - $1 * 86400 ))" --iso-8601=seconds)"
	git -C "$REPO" add -A
	GIT_AUTHOR_DATE="$when" GIT_COMMITTER_DATE="$when" git -C "$REPO" commit -q -m "$2"
}

repo_init snip
copy 1 README.md .gitignore snip.py
commit 23 "A URL shortener that fits on a page"
copy 2 README.md test_snip.py Containerfile.dev
commit 9 "A dev container, and a test for the key generator"
copy - README.md snip.py store.py test_snip.py
commit 4 "Keep the links in a file, so a restart no longer forgets them"

repo_init standup
copy 1 README.md .gitignore package.json standup.js
commit 16 "standup: add a line, see today, see the week"
copy - README.md Containerfile.dev test/standup.test.js
commit 6 "A dev container, and tests for add and copy"

# --- task directories ---------------------------------------------------------

# workspace <project> <task> : the clone a task works in, made the way TPS
# makes one (a plain clone of the project, committing as TPS).
workspace() {
	local dir="$DATA/tasks/$1/$2"
	mkdir -p "$dir/claude"
	rm -rf "$dir/repo"
	git clone -q -b main "$PROJECTS/$1" "$dir/repo"
	git -C "$dir/repo" config user.name TPS
	git -C "$dir/repo" config user.email tps@localhost
}

# chat <project> <task> <days ago> : start a chat log; say adds to it.
chat() {
	CHAT="$DATA/tasks/$1/$2/chat.jsonl"
	CHAT_AT=$(ms "$3" 1)
	mkdir -p "$(dirname "$CHAT")"
	: >"$CHAT"
}

# say <seconds since the previous line> <the entry's fields, as JSON>
say() {
	CHAT_AT=$(( CHAT_AT + $1 * 1000 ))
	printf '{%s,"t":%s}\n' "$2" "$CHAT_AT" >>"$CHAT"
}

# snip #2 — merged: the change that added the dev container.
chat snip 2 10
say 0 '"k":"user","text":"Tasks cant run the tests, since the image has no python. Add a Containerfile.dev, with the test suite as a service, and a first test for the key generator."'
say 9 '"k":"thinking","text":"python:3.12-slim has bash and a shell; it needs git and a uid 1000 user for TPS.","detail":"python:3.12-slim has bash and a shell; it needs git and a uid 1000 user for TPS.\n\nThe slim images have no user at 1000, so useradd it. The test service goes in a LABEL, and the server port in EXPOSE."'
say 4 '"k":"tool","id":"c1","name":"Read","text":"snip.py","arg":"snip.py","res":"94 lines"'
say 6 '"k":"tool","id":"c2","name":"Write","text":"Containerfile.dev","arg":"Containerfile.dev","res":"wrote 14 lines"'
say 31 '"k":"tool","id":"c3","name":"Write","text":"test_snip.py","arg":"test_snip.py","res":"wrote 17 lines"'
say 12 '"k":"tool","id":"c4","name":"Bash","text":"run the tests","arg":"python3 -m unittest discover -v","res":"Ran 2 tests in 0.004s — OK"'
say 8 '"k":"text","text":"Added `Containerfile.dev` (python 3.12, git, a uid 1000 user) with the test suite as the `test` service and port 8000 exposed, plus `test_snip.py` covering the key length and that a taken key is never handed out twice. Both tests pass."'
say 2 '"k":"result","text":"turn finished · 71s · $0.34"'
say 3 '"k":"note","text":"merged into main as 1 commit"'

# snip #3 — waiting to be merged: its work is in the workspace, uncommitted.
workspace snip 3
cp "$SRC/repos/snip/_work/snip.py" "$SRC/repos/snip/_work/README.md" "$DATA/tasks/snip/3/repo/"
chat snip 3 1
say 0 '"k":"user","text":"Anything that is not an http(s) link should be refused with a 400, not shortened. A javascript: URL that redirects is a nasty thing to hand out."'
say 11 '"k":"thinking","text":"The check belongs where the URL arrives, after read_url and before a key is made.","detail":"The check belongs where the URL arrives, after read_url and before a key is made.\n\nread_url already normalises both body shapes, so one test on its result covers the JSON and the form path at once."'
say 5 '"k":"tool","id":"d1","name":"Read","text":"snip.py","arg":"snip.py","res":"110 lines"'
say 14 '"k":"tool","id":"d2","name":"Edit","text":"snip.py","arg":"add SCHEMES and the 400","res":"2 edits applied"'
say 7 '"k":"tool","id":"d3","name":"Edit","text":"README.md","arg":"note what is accepted","res":"1 edit applied"'
say 9 '"k":"tool","id":"d4","name":"Bash","text":"post a javascript: url","arg":"curl -sd {\"url\": \"javascript:alert(1)\"} http://localhost:8000/","res":"400 {\"error\": \"only http and https links can be shortened\"}"'
say 6 '"k":"tool","id":"d5","name":"Bash","text":"run the tests","arg":"python3 -m unittest discover","res":"Ran 5 tests in 0.002s — OK"'
say 4 '"k":"text","text":"`do_POST` now refuses anything that does not start with `http://` or `https://` with a 400 and a short message; the README says so. Checked by hand with a `javascript:` URL, and the suite still passes.\n\nThe scheme list is deliberately a prefix check rather than a urlparse: `urlparse` accepts `javascript:alert(1)` as a scheme too, and the allowlist is what matters here."'
say 2 '"k":"result","text":"turn finished · 58s · $0.29"'

# snip #7 — closed without merging. Its workspace is made the way an older TPS
# left one, and parked by the daemon at startup.
workspace snip 7
chat snip 7 12
say 0 '"k":"user","text":"Move the links from the JSON file into SQLite, so two processes can serve the same table."'
say 8 '"k":"thinking","text":"Two writers is the reason, but nothing runs two snips yet.","detail":"Two writers is the reason, but nothing runs two snips yet. Worth saying before writing a schema."'
say 5 '"k":"tool","id":"e1","name":"Read","text":"store.py","arg":"store.py","res":"36 lines"'
say 7 '"k":"text","text":"Before I write a schema: the JSON store is rewritten whole under a lock and renamed into place, which is safe for one process. Two processes would need SQLite (or a real database), but nothing starts a second snip today — the Containerfile runs one.\n\nShall I do it anyway, or leave it until there is a reason to run two?"'
say 2 '"k":"result","text":"turn finished · 24s · $0.11"'
say 40 '"k":"user","text":"Fair enough. Parking this until we actually need it."'
say 1 '"k":"note","text":"task closed without merging"'

# standup #1 — merged: the dev container and the first tests.
chat standup 1 7
say 0 '"k":"user","text":"Give this a Containerfile.dev like snip has, and some tests for add and copy."'
say 7 '"k":"thinking","text":"node:22-slim already has a uid 1000 (node), so no useradd here.","detail":"node:22-slim already has a uid 1000 (node), so no useradd here."'
say 5 '"k":"tool","id":"f1","name":"Read","text":"standup.js","arg":"standup.js","res":"76 lines"'
say 8 '"k":"tool","id":"f2","name":"Write","text":"Containerfile.dev","arg":"Containerfile.dev","res":"wrote 13 lines"'
say 26 '"k":"tool","id":"f3","name":"Write","text":"test/standup.test.js","arg":"test/standup.test.js","res":"wrote 27 lines"'
say 11 '"k":"tool","id":"f4","name":"Bash","text":"run the tests","arg":"npm test","res":"# pass 2 # fail 0"'
say 6 '"k":"text","text":"The tests drive the CLI as a subprocess with `STANDUP_FILE` pointed at a temporary file, so they never touch your own notes. `npm test` is the `test` service in the image."'
say 2 '"k":"result","text":"turn finished · 65s · $0.27"'
say 3 '"k":"note","text":"merged into main as 1 commit"'

# --- the config ---------------------------------------------------------------

mkdir -p "$(dirname "$CONFIG")"
cat >"$CONFIG" <<JSON
{
	"projects": [
		{
			"dir": "$PROJECTS/snip",
			"name": "snip",
			"color": "#45c4d6",
			"defaults": { "model": "default" },
			"activity": $(ms 0 2),
			"nextTask": 8,
			"tasks": {
				"1": {
					"title": "Shorten a URL, follow it back",
					"description": "A first cut: POST a URL, get a short key, GET the key to be redirected. One file, standard library only, links in memory.",
					"model": "default",
					"phase": "done",
					"started": true,
					"merged": true,
					"spent": 1.87,
					"phaseAt": $(ms 23)
				},
				"2": {
					"title": "A dev container so tasks can run the tests",
					"description": "Tasks can't run the tests: the default image has no python. Add a Containerfile.dev with the test suite as a service, and a first test for the key generator.",
					"model": "default",
					"phase": "done",
					"started": true,
					"merged": true,
					"spent": 0.34,
					"phaseAt": $(ms 9)
				},
				"3": {
					"title": "Refuse links that aren't http(s)",
					"description": "Anything that is not an http(s) link should be refused with a 400 rather than shortened: handing out a redirect to a javascript: URL is a nasty thing to do.",
					"model": "default",
					"phase": "human",
					"started": true,
					"commitMessage": "Only shorten http and https links\n\nA URL with any other scheme is refused with a 400 rather than given a key: a\nshort link that redirects to javascript: is worth more to an attacker than to\nanyone else. The README says what is accepted.",
					"spent": 0.29,
					"phaseAt": $(ms 0 3)
				},
				"4": {
					"title": "Keep the links in a file",
					"description": "The links live in memory, so a restart forgets every short link handed out. Write them to JSON instead, atomically.",
					"model": "default",
					"phase": "done",
					"started": true,
					"merged": true,
					"spent": 1.12,
					"phaseAt": $(ms 4)
				},
				"5": {
					"title": "A /stats page",
					"description": "A read-only /stats page: links made per day for the last fortnight, and the ten most followed. Plain HTML, no charting library.",
					"model": "default",
					"phase": "plan",
					"phaseAt": $(ms 2)
				},
				"6": {
					"title": "Expire links after 90 days",
					"description": "Links that nobody has followed in 90 days should be dropped on startup, and the count of what went should show up on the stats page.",
					"model": "default",
					"phase": "plan",
					"startAfter": ["5"],
					"phaseAt": $(ms 2)
				},
				"7": {
					"title": "Move the store to SQLite",
					"description": "Put the links in SQLite instead of a JSON file, so more than one process can serve them.",
					"model": "default",
					"phase": "closed",
					"started": true,
					"spent": 0.11,
					"phaseAt": $(ms 12)
				}
			}
		},
		{
			"dir": "$PROJECTS/standup",
			"name": "standup",
			"color": "#c8d35a",
			"defaults": { "model": "default", "budget": 5 },
			"activity": $(ms 3),
			"nextTask": 4,
			"tasks": {
				"1": {
					"title": "A dev container and the first tests",
					"description": "Give this a Containerfile.dev like snip has, and tests for add and copy.",
					"model": "default",
					"phase": "done",
					"started": true,
					"merged": true,
					"spent": 0.27,
					"budget": 5,
					"phaseAt": $(ms 6)
				},
				"2": {
					"title": "standup week should skip the weekend",
					"description": "Saturday and Sunday are always empty and push the useful days off the screen: show the last seven weekdays instead.",
					"model": "default",
					"phase": "plan",
					"budget": 5,
					"phaseAt": $(ms 3)
				},
				"3": {
					"title": "Publish it to npm",
					"description": "Work out what it takes to publish this as a package people can npx: the bin entry, the files list, a licence, and a release note in the README.",
					"model": "default",
					"phase": "muted",
					"budget": 5,
					"phaseAt": $(ms 5)
				}
			}
		}
	]
}
JSON

# The claude login this container shares, where a TPS started in it keeps
# its own: without it the demo dashboard is there, but its agents cannot run.
if [ -d /claude-auth ] && [ ! -e "$DATA/claude" ]; then
	mkdir -p "$DATA"
	ln -s /claude-auth "$DATA/claude"
fi

echo "demo/seed.sh: seeded $PROJECTS/{snip,standup} and $CONFIG"
