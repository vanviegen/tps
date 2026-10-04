#!/bin/bash
# Fill a home directory with a TPS that has been in use for a few weeks: two
# small projects in ~/projects, and a board's worth of tasks around them —
# merged ones whose commits are in the git log, one waiting to be merged with
# its work sitting in a workspace, a couple in the plan column (one of them
# waiting for another), a parked one, a closed one, one waiting for more
# OpenRouter budget, and an experiment among the scratch tasks.
#
# Nothing seeded here runs an agent on its own: every task is in a phase that
# waits (plan, human, muted, done, closed), and the one task set to follow
# another waits on a plan that is not going anywhere until someone starts it.
# Opening the demo therefore costs nothing until a button is pressed — and
# what a press then costs is a haiku, which is what every task and both
# projects are set to.
#
# The project's 'app' service runs this with --force before starting TPS, so
# that every restart of it opens the same board rather than whatever the last
# look around left behind. Tests may want the same fixture: point TPS_DEMO_HOME
# at a directory of their own, and nothing outside it is touched.
#
#   demo/seed.sh            seed, unless this home has been seeded already
#   demo/seed.sh --force    throw the seeded projects and tasks away and redo them
#
# A demo that has been clicked around in is more than the files seeded here, so
# --force undoes more than they are: the containers and build caches its tasks
# left behind go as well, and the daemon holding the board in memory is told to
# stop (by taking its registry away) rather than left to write that board back.
# It is safe to run against a live dashboard; the dashboard starts a fresh
# daemon on the seeded board within a second or two of reconnecting.
#
# Seeding itself is not TPS-specific beyond the two things it writes: the
# config (~/.config/tps/projects.json) and the task directories under
# ~/.local/share/tps/tasks/<project>/<task>, which hold a workspace clone, the
# claude session directory, and the condensed chat log the dashboard shows.

set -euo pipefail

SRC="$(cd "$(dirname "$0")" && pwd)"
HOME_DIR="${TPS_DEMO_HOME:-$HOME}"
CONFIG="$HOME_DIR/.config/tps/projects.json"
DATA="$HOME_DIR/.local/share/tps"
PROJECTS="$HOME_DIR/projects"
NOW=$(date +%s)

# dropDaemon: the TPS daemon on this home holds the whole board in memory and
# writes it back when it saves, so a reseed under a live one would be undone
# the moment it does. Taking its registry away is how it is told: a daemon
# whose config file is no longer the one it wrote stops (see
# quitIfConfigReplaced), and the rm below then has nothing to race with. One
# with an agent mid-turn finishes that turn first, so this waits a while.
dropDaemon() {
	local was i
	was=$(listener)
	rm -f "$CONFIG"
	[ -n "$was" ] || return 0
	echo "demo/seed.sh: waiting for the TPS daemon on $HOME_DIR to stop…"
	for i in $(seq 120); do
		sleep 1
		[ "$(listener)" = "$was" ] || return 0
	done
	# Seeding anyway beats leaving this home without a registry at all: what
	# is written below is what the daemon reads once its work is done and it
	# stops over the changed file, or the next daemon after that.
	echo "demo/seed.sh: it is still there — it has work in hand, or is not a TPS that stops itself" >&2
}

# listener: which daemon is listening on the socket, as the socket's inode, and
# nothing at all when none is. The file outlives a daemon that was killed
# rather than stopped — the container going down with it, say — so its
# presence says nothing; /proc/net/unix lists what has a listener on it (flags
# 00010000), which is also how a daemon that has been replaced by the next one
# tells itself apart from it.
listener() {
	awk -v path="$DATA/daemon.sock" '$4 == "00010000" && $NF == path { print $7 }' /proc/net/unix 2>/dev/null
}

# rmContainers: the container TPS makes per task. It takes those down with it,
# but not one it never adopted — a stopped leftover from a daemon that was
# killed rather than stopped. The task that takes its name again would, still
# bound to the workspace directory this script is about to replace.
rmContainers() {
	local ids
	command -v podman >/dev/null 2>&1 || return 0
	ids=$(podman ps -aq --filter name=tps-snip- --filter name=tps-standup- 2>/dev/null || true)
	[ -n "$ids" ] && podman rm -f $ids >/dev/null
	return 0
}

if [ "${1:-}" = --force ]; then
	dropDaemon
	rmContainers
	rm -rf "$PROJECTS/snip" "$PROJECTS/standup" "$DATA/scratch" "$DATA/tasks/snip" "$DATA/tasks/standup" "$DATA/tasks/scratch" \
		"$DATA/cache/snip" "$DATA/cache/standup" "$CONFIG"
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

# The scratch project, made the way TPS makes it: a repository holding nothing
# but an empty root commit (see ensureScratch).
git init -q -b main "$DATA/scratch"
git -C "$DATA/scratch" update-ref HEAD "$(git -C "$DATA/scratch" -c user.name=TPS -c user.email=tps@localhost \
	commit-tree "$(git -C "$DATA/scratch" mktree </dev/null)" -m "Initial commit")"

# --- task directories ---------------------------------------------------------

# workspace <project> <task> [repository] : the clone a task works in, made the
# way TPS makes one (a plain clone of the project, committing as TPS).
workspace() {
	local dir="$DATA/tasks/$1/$2"
	mkdir -p "$dir/claude"
	rm -rf "$dir/repo"
	git clone -q -b main "${3:-$PROJECTS/$1}" "$dir/repo"
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

# point <project> <task> <name> : a save point in the chat, holding the
# workspace as it is the way TPS holds it (see step in daemon/mark.go): a commit
# on its steps branch, if the tree differs from the last one.
point() {
	local repo="$DATA/tasks/$1/$2/repo" last tree commit
	last=$(git -C "$repo" rev-parse --verify --quiet tps-steps || git -C "$repo" rev-parse HEAD)
	git -C "$repo" add -A
	tree=$(git -C "$repo" write-tree)
	git -C "$repo" reset -q
	commit=$last
	if [ "$tree" != "$(git -C "$repo" rev-parse "$last^{tree}")" ]; then
		commit=$(git -C "$repo" commit-tree "$tree" -p "$last" -m "$3")
	fi
	git -C "$repo" update-ref refs/heads/tps-steps "$commit"
	say 1 "\"k\":\"mark\",\"id\":\"mark-$CHAT_AT\",\"text\":\"$3\",\"mark\":{\"commit\":\"$commit\"}"
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
# Two turns, the second one the user coming back with something they spotted:
# a task the demo opens on has a conversation in it, not a single command.
workspace snip 3
chat snip 3 1
point snip 3 Start
cp -a "$SRC/repos/snip/_work/." "$DATA/tasks/snip/3/repo/"
say 0 '"k":"user","text":"Anything that is not an http(s) link should be refused with a 400, not shortened. A javascript: URL that redirects is a nasty thing to hand out."'
say 11 '"k":"thinking","text":"The check belongs where the URL arrives, after read_url and before a key is made.","detail":"The check belongs where the URL arrives, after read_url and before a key is made.\n\nread_url already normalises both body shapes, so one test on its result covers the JSON and the form path at once."'
say 5 '"k":"tool","id":"d1","name":"Read","text":"snip.py","arg":"snip.py","res":"110 lines"'
say 14 '"k":"tool","id":"d2","name":"Edit","text":"snip.py","arg":"add SCHEMES and the 400","res":"2 edits applied"'
say 7 '"k":"tool","id":"d3","name":"Edit","text":"README.md","arg":"note what is accepted","res":"1 edit applied"'
say 9 '"k":"tool","id":"d4","name":"Bash","text":"post a javascript: url","arg":"curl -sd {\"url\": \"javascript:alert(1)\"} http://localhost:8000/","res":"400 {\"error\": \"only http and https links can be shortened\"}"'
say 6 '"k":"tool","id":"d5","name":"Bash","text":"run the tests","arg":"python3 -m unittest discover","res":"Ran 5 tests in 0.002s — OK"'
say 4 '"k":"text","text":"`do_POST` now refuses anything that does not start with `http://` or `https://` with a 400 and a short message; the README says so. Checked by hand with a `javascript:` URL, and the suite still passes.\n\nThe scheme list is deliberately a prefix check rather than a urlparse: `urlparse` accepts `javascript:alert(1)` as a scheme too, and the allowlist is what matters here."'
say 2 '"k":"result","text":"turn finished · 58s · $0.29"'
say 79200 '"k":"user","text":"Two things before this goes in: HTTPS://EXAMPLE.COM comes back as a 400 here, and none of it is covered by a test."'
say 10 '"k":"thinking","text":"startswith is case-sensitive; the scheme is the one part of a URL that is not.","detail":"startswith is case-sensitive; the scheme is the one part of a URL that is not.\n\nLower-casing the whole URL only to test its prefix is fine — the URL that gets stored is still the one that came in. A named function is easier to test than a branch inside do_POST, so the check moves out of the handler."'
say 13 '"k":"tool","id":"d6","name":"Edit","text":"snip.py","arg":"move the check into allowed()","res":"2 edits applied"'
say 16 '"k":"tool","id":"d7","name":"Edit","text":"test_snip.py","arg":"a SchemeTest for both, the case, and the refusals","res":"2 edits applied"'
say 8 '"k":"tool","id":"d8","name":"Bash","text":"run the tests","arg":"python3 -m unittest discover","res":"Ran 8 tests in 0.003s — OK"'
say 5 '"k":"tool","id":"d9","name":"Bash","text":"post an upper-case url","arg":"curl -sd {\"url\": \"HTTPS://EXAMPLE.COM/\"} http://localhost:8000/","res":"201 {\"key\": \"Kp3v\", \"short\": \"http://localhost:8000/Kp3v\"}"'
say 4 '"k":"text","text":"Good catch — the scheme is the one part of a URL that is not case-sensitive, and `startswith` is. The check is now `allowed(url)`, which lower-cases before comparing and is a function rather than a branch, so the three cases have tests: http and https go through, `HTTPS://` goes through, and `javascript:`, `file:`, `data:` and a bare hostname do not. Eight tests, all green.\n\nThe URL that gets stored is still the one that arrived; only the comparison is lower-cased."'
say 2 '"k":"result","text":"turn finished · 47s · $0.14"'
point snip 3 'Agent: Refuse links that are not http(s), whatever their case'
say 1 '"k":"note","ready":true,"text":"the agent reports the task is ready to merge"'

# scratch #1 — an experiment that belongs to no project, its answer waiting
# to be read, the script it was found with in the workspace.
workspace scratch 1 "$DATA/scratch"
cp -a "$SRC/repos/scratch/_work/." "$DATA/tasks/scratch/1/repo/"
chat scratch 1 0
say 0 '"k":"user","text":"Quick experiment: how many single-row INSERTs per second does SQLite manage here when every row is committed on its own? With and without WAL. Wondering whether it would do for a small URL shortener."'
say 8 '"k":"thinking","text":"Autocommit per row is the worst case, and the realistic one for a shortener.","detail":"Autocommit per row is the worst case, and the realistic one for a shortener: one link, one commit. What dominates is the fsync per commit, so journal_mode and synchronous are the knobs; DELETE/FULL is the default, WAL/FULL and WAL/NORMAL the two worth comparing."'
say 21 '"k":"tool","id":"s1","name":"Write","text":"bench.py","arg":"bench.py","res":"wrote 24 lines"'
say 9 '"k":"tool","id":"s2","name":"Bash","text":"run the benchmark","arg":"python3 bench.py","res":"DELETE FULL 190/s · WAL FULL 750/s · WAL NORMAL 48,300/s"'
say 6 '"k":"text","text":"2,000 single-row INSERTs, each its own transaction, on a temporary file:\n\n| journal | synchronous | inserts/s |\n|---|---|---:|\n| DELETE (default) | FULL | 190 |\n| WAL | FULL | 750 |\n| WAL | NORMAL | 48,300 |\n\nWhat a commit costs is its fsyncs. WAL halves them; `synchronous=NORMAL` in WAL mode drops the one per commit altogether, at the price of losing the last few commits (never the database) on a power cut.\n\nFor a URL shortener that writes once per link, even the default is far more than it will ever see, and WAL also lets readers carry on while a write is in progress. `bench.py` is in the workspace if you want to try another disk."'
say 2 '"k":"result","text":"turn finished · 52s · $0.06"'

# standup #4 — the agent waiting on an OpenRouter request beyond its budget.
workspace standup 4
chat standup 4 0
say 0 '"k":"user","text":"Add `standup say`: read today'"'"'s notes aloud, for the days I join the call from my phone. Use a text-to-speech model through OpenRouter."'
say 7 '"k":"thinking","text":"Before wiring a model in, try one: that takes a key, and the budget left is too little.","detail":"Before wiring a model in, try one or two voices on a real note: that takes an OpenRouter key, and the task has too little OpenRouter budget left for a few minutes of audio."'
say 4 '"k":"tool","id":"g1","name":"Read","text":"standup.js","arg":"standup.js","res":"76 lines"'
say 6 '"k":"tool","id":"g2","name":"Bash","text":"ask for an OpenRouter key","arg":"tps-guest-tool openrouter 2"'
say 1 '"k":"note","text":"the agent asks for $2 of OpenRouter spending, more than the task'"'"'s OpenRouter budget has left"'

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

# Into place in one step: a daemon may be reading it the moment it appears.
mkdir -p "$(dirname "$CONFIG")"
cat >"$CONFIG.new" <<JSON
{
	"projects": [
		{
			"dir": "$DATA/scratch",
			"name": "Scratch",
			"color": "#f06b8a",
			"scratch": true,
			"defaults": { "model": "claude: haiku", "onReady": "human" },
			"activity": $(ms 0 1),
			"nextTask": 2,
			"tasks": {
				"1": {
					"title": "SQLite insert speed, with and without WAL",
					"model": "claude: haiku",
					"phase": "human",
					"started": true,
					"spent": 0.06,
					"context": 21000,
					"window": {"model": "claude: haiku", "limit": 167000, "parts": [{"name": "System prompt", "tokens": 6300}, {"name": "System tools", "tokens": 10100}, {"name": "Skills", "tokens": 1500}]},
					"phaseAt": $(ms 0 1)
				}
			}
		},
		{
			"dir": "$PROJECTS/snip",
			"name": "snip",
			"color": "#45c4d6",
			"defaults": { "model": "claude: haiku" },
			"activity": $(ms 0 2),
			"nextTask": 8,
			"tasks": {
				"1": {
					"title": "Shorten a URL, follow it back",
					"description": "A first cut: POST a URL, get a short key, GET the key to be redirected. One file, standard library only, links in memory.",
					"model": "claude: haiku",
					"phase": "done",
					"started": true,
					"merged": true,
					"spent": 1.87,
					"context": 118000,
					"window": {"model": "claude: haiku", "limit": 167000, "parts": [{"name": "System prompt", "tokens": 6300}, {"name": "System tools", "tokens": 10100}, {"name": "Skills", "tokens": 1500}]},
					"phaseAt": $(ms 23)
				},
				"2": {
					"title": "A dev container so tasks can run the tests",
					"description": "Tasks can't run the tests: the default image has no python. Add a Containerfile.dev with the test suite as a service, and a first test for the key generator.",
					"model": "claude: haiku",
					"phase": "done",
					"started": true,
					"merged": true,
					"spent": 0.34,
					"context": 31000,
					"window": {"model": "claude: haiku", "limit": 167000, "parts": [{"name": "System prompt", "tokens": 6300}, {"name": "System tools", "tokens": 10100}, {"name": "Skills", "tokens": 1500}]},
					"phaseAt": $(ms 9)
				},
				"3": {
					"title": "Refuse links that aren't http(s)",
					"description": "Anything that is not an http(s) link should be refused with a 400 rather than shortened: handing out a redirect to a javascript: URL is a nasty thing to do.",
					"model": "claude: haiku",
					"phase": "human",
					"started": true,
					"ready": true,
					"spent": 0.43,
					"context": 46000,
					"window": {"model": "claude: haiku", "limit": 167000, "parts": [{"name": "System prompt", "tokens": 6300}, {"name": "System tools", "tokens": 10100}, {"name": "Skills", "tokens": 1500}]},
					"phaseAt": $(ms 0 3)
				},
				"4": {
					"title": "Keep the links in a file",
					"description": "The links live in memory, so a restart forgets every short link handed out. Write them to JSON instead, atomically.",
					"model": "claude: haiku",
					"phase": "done",
					"started": true,
					"merged": true,
					"spent": 1.12,
					"context": 87000,
					"window": {"model": "claude: haiku", "limit": 167000, "parts": [{"name": "System prompt", "tokens": 6300}, {"name": "System tools", "tokens": 10100}, {"name": "Skills", "tokens": 1500}]},
					"phaseAt": $(ms 4)
				},
				"5": {
					"title": "A /stats page",
					"description": "A read-only /stats page: links made per day for the last fortnight, and the ten most followed. Plain HTML, no charting library.",
					"model": "claude: haiku",
					"phase": "plan",
					"phaseAt": $(ms 2)
				},
				"6": {
					"title": "Expire links after 90 days",
					"description": "Links that nobody has followed in 90 days should be dropped on startup, and the count of what went should show up on the stats page.",
					"model": "claude: haiku",
					"phase": "plan",
					"startAfter": ["5"],
					"phaseAt": $(ms 2)
				},
				"7": {
					"title": "Move the store to SQLite",
					"description": "Put the links in SQLite instead of a JSON file, so more than one process can serve them.",
					"model": "claude: haiku",
					"phase": "closed",
					"started": true,
					"spent": 0.11,
					"context": 12000,
					"window": {"model": "claude: haiku", "limit": 167000, "parts": [{"name": "System prompt", "tokens": 6300}, {"name": "System tools", "tokens": 10100}, {"name": "Skills", "tokens": 1500}]},
					"phaseAt": $(ms 12)
				}
			}
		},
		{
			"dir": "$PROJECTS/standup",
			"name": "standup",
			"color": "#c8d35a",
			"defaults": { "model": "claude: haiku", "budget": 5 },
			"activity": $(ms 3),
			"nextTask": 5,
			"tasks": {
				"1": {
					"title": "A dev container and the first tests",
					"description": "Give this a Containerfile.dev like snip has, and tests for add and copy.",
					"model": "claude: haiku",
					"phase": "done",
					"started": true,
					"merged": true,
					"spent": 0.27,
					"context": 24500,
					"window": {"model": "claude: haiku", "limit": 167000, "parts": [{"name": "System prompt", "tokens": 6300}, {"name": "System tools", "tokens": 10100}, {"name": "Skills", "tokens": 1500}]},
					"budget": 5,
					"phaseAt": $(ms 6)
				},
				"2": {
					"title": "standup week should skip the weekend",
					"description": "Saturday and Sunday are always empty and push the useful days off the screen: show the last seven weekdays instead.",
					"model": "claude: haiku",
					"phase": "plan",
					"budget": 5,
					"phaseAt": $(ms 3)
				},
				"3": {
					"title": "Publish it to npm",
					"description": "Work out what it takes to publish this as a package people can npx: the bin entry, the files list, a licence, and a release note in the README.",
					"model": "claude: haiku",
					"phase": "muted",
					"budget": 5,
					"phaseAt": $(ms 5)
				},
				"4": {
					"title": "standup say: read today's notes aloud",
					"description": "Read today's notes aloud, for the days I join the call from my phone, with a text-to-speech model through OpenRouter.",
					"model": "claude: haiku",
					"phase": "human",
					"started": true,
					"spent": 0.04,
					"context": 19000,
					"window": {"model": "claude: haiku", "limit": 167000, "parts": [{"name": "System prompt", "tokens": 6300}, {"name": "System tools", "tokens": 10100}, {"name": "Skills", "tokens": 1500}]},
					"budget": 5,
					"openrouterBudget": 1,
					"openrouterGranted": 0.25,
					"openrouterAsk": 2,
					"phaseAt": $(ms 0 1)
				}
			}
		}
	]
}
JSON
mv "$CONFIG.new" "$CONFIG"

# The claude login this container shares, where a TPS started in it keeps
# its own: without it the demo dashboard is there, but its agents cannot run.
if [ -d /claude-auth ] && [ ! -e "$DATA/claude" ]; then
	mkdir -p "$DATA"
	ln -s /claude-auth "$DATA/claude"
fi

echo "demo/seed.sh: seeded $PROJECTS/{snip,standup}, a scratch task and $CONFIG"
