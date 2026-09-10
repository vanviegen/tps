# TPS

A kanban-style manager for AI coding agents. Each task works in its own clone
of your repository, inside a disposable podman dev container, driven by the
`claude` CLI through a chat you watch and steer from a live web dashboard.
Projects can live on this machine or on any host you reach over SSH, and the
work keeps going while the dashboard is closed.

## How it is put together

One binary, two roles. `tps` is the dashboard: it serves the web UI on
localhost and relays to daemons. `tps --daemon` is the workflow itself: it
owns the projects and tasks of one host, runs git, podman and claude there,
and keeps running until you stop it. The dashboard starts a daemon on this
machine when needed, and installs and starts one on every SSH host it uses.
The daemon owns the list of projects on its host, their names included; the
dashboard only keeps the hosts to connect to. Several dashboards (a laptop,
a desktop) can use the same daemon at once, and all show the same projects.

## Requirements

- On every host that runs projects: Linux with rootless podman and git, and
  the `claude` CLI logged in there (or `ANTHROPIC_API_KEY` in the daemon's
  environment). The dashboard can copy your local claude login to a host.
  Podman 4 or later: its API service is used for the docker socket tasks get
  (your `podman.socket` if systemd runs one, else one the daemon starts).
- For hosts you reach over SSH: lingering enabled for your user
  (`loginctl enable-linger`), so containers and the daemon outlive your login.
  TPS warns when it is off.
- Building: Go 1.24+ and node (only for bundling the web UI).

## Getting started

```sh
npm install && npm run build       # bundles the web UI into web/dist
CGO_ENABLED=0 go build -o tps .    # embeds it; one static binary is all you need
./tps                              # http://localhost:4820/, opens your browser
```

The front page holds the hosts on the left and the projects on the right. Each
host is a box outlined in what it is doing — up, busy, stopped, in trouble —
saying where it is, how much of the board lives there, and anything it wants
from you. The projects are one line each: name, how many tasks sit in every
phase (the ones waiting for you in warning colour), host and directory. A
project with a task waiting for you comes first, then the one that moved most
recently.

*Add project* takes a directory holding a git repository, like
`~/projects/app`, on the host you pick there, and names the project after the
directory unless you say otherwise. *Add host* takes whatever you would type
after `ssh`: `user@host`, a `~/.ssh/config` alias, or `-p 2222 -J jump host`
— it is also the last entry of the project dialog's host list, for a project
on a machine that is not on the board yet. TPS uses your ssh, so keys, agents,
an open multiplexed session and passwords all work. A login it wants (a
password, an unknown host key) waits in that host's box rather than taking
over the screen: click it when you are ready. A new host gets the daemon
installed and started, and the projects it already has are listed along with
it.

Right-click a project or a host (or use the ⋮ in a host's header) for what can
be done with it: renaming, connecting, copying your claude login to a host,
updating or stopping its daemon, and removing it. Removing a host
takes it off this dashboard only; removing a project takes it off its host,
with the tasks it holds, for every dashboard.

Tasks run in a Debian image with the basics (git, curl, a compiler). A
repository with its own `Containerfile.dev` at the root gets that instead: a
plain image definition with nothing TPS-specific in it, as code-server,
claude and an init are downloaded once per host and mounted into every
container. Any base works, as long as it has bash and git, and a user with
uid 1000 who owns a home directory. The agent knows about this: when it lacks
a tool, it can write or extend `Containerfile.dev` and continue in a container
built from it, and the file is merged along with the rest of its work. Edit it
yourself and the rebuild button above the log does the same for you. Should
the file ever break (a bad edit, a merge conflict), the task falls back to the
default image so you and the agent can still get in, and the agent is told to
fix it first.

A task can run containers of its own: `docker` and `docker compose` in the
container talk to a socket TPS serves, backed by the host's podman and held
to what the task may have (see `podnester/`, a library of its own). What it
starts are siblings of its container on a private network, named and labeled
after it, gone when its container is recreated; named volumes stay until the
task is deleted. Bind mounts and published ports mean what they would inside
the task's container.

## How a task flows

The window is two columns. The left one opens with the trail of where you are —
TPS / project / task, each crumb a link back up, wearing a badge that counts
what waits for a human elsewhere — and holds what there is to do here: the
project's settings while no task is picked, and otherwise the task's phase
icon (which opens the list of phases to move it to), the gear with its
settings, and its chat with the agent.

The right column is whatever the left one names: the project's board of five
columns while no task is picked, the description of a task still in Plan, VS
Code (running in the container) on the task's workspace once it is not, and VS
Code on the project's own checkout behind the *"main" branch* button. A view
you switch away from is kept warm for a few minutes, so switching back is
instant.

A project's settings are its name, where it lives, and its *default task
settings*: the model, the budget limit and the merge behaviour that every new
task there is created with. They are copied into the task, so changing them
says what the next tasks start out with and leaves the ones that exist alone.

1. **Plan**: A title, a markdown description, and settings (model, the tasks
   to start after, budget, merge behaviour), which start on the project's
   defaults. The models to pick from are the ones the claude on the project's
   host offers (it is asked until it answers); when it cannot be asked, the
   dropdown says why and falls back to a built-in list. *default* leaves the
   choice to claude's own configuration. Nothing exists on disk yet. Once the
   task leaves this column the description is fixed; the title and the other
   settings stay tweakable.

   *Start after* names tasks this one follows: it hands itself to the agent
   once each of them is done or deleted — and you have closed its plan, so
   that a task you are still writing is never sent off behind your back. The
   plan says what it is waiting for while it waits.
2. **Agent**: Assigning the task creates a clone of the repository (hardlinked
   objects, so nearly free), spins up the container, and hands the description
   to claude. A task that follows others is cloned when their work is in, so
   it includes it. The chat shows what the agent is doing, tool call by tool
   call; type to steer it mid-run or to send follow-ups.
3. **Human**: Claude's turn ended, or you pressed stop, or you assigned the
   task to yourself. Chat to send the agent back in, open VS Code to work
   yourself, or merge.
4. **Merge**: The working tree becomes a single commit on the default branch.
   The agent proposes the commit message when it reports the task ready; with
   *Merge when ready* (a task setting, out of the project's defaults) that
   merge happens by itself, otherwise you confirm the message first. If
   the default branch moved since the clone was made, the work is rebased onto
   it. Conflicts are handed to a fresh agent that resolves the rebase (guided
   by the commit messages on both sides), after which the merge completes; the
   task stays in this column meanwhile. If the agent gives up midway, the task
   goes back to you with a warning; sending the agent back in resumes the rebase.
5. **Done**: Merged. The workspace goes — its work is on the branch, and the
   container with it — but the conversation stays until the task is deleted,
   so the task can be picked up again for follow-up work: a chat message sends
   the agent back in with everything it knew, and moving the card to Human
   takes it on yourself. Either way it gets a new clone of the default branch
   as it stands then, exactly like a task leaving Plan, so the follow-up builds
   on its own merged work and on whatever other tasks landed meanwhile. The
   agent is told what happened: that its work became a commit, that the old
   workspace (and anything only in it) is gone, and that what it changes now
   becomes a commit of its own.

Moving a task back to Plan discards all of its work, after a confirmation.

## Notes

- Everything about projects and tasks lives on the host that runs them: task
  state in `~/.config/tps/projects.json`, the workspaces (repo clone, claude
  session state, chat log) and the downloaded tools under
  `~/.local/share/tps/`, and the daemon's socket, log and, on SSH hosts, its
  binary next to them. Projects, with the names you gave them, are part of
  that: the dashboard only keeps the list of hosts, in
  `~/.config/tps/dashboard.json`. There is no database and your repository
  only ever receives the final merge commit.
- Removing a project is a change on its host, so every dashboard using it
  sees the project go, and its tasks and workspaces are deleted. Removing a
  *host* changes nothing on the host itself: its projects and tasks are back
  when you add it again.
- The daemon keeps working when the dashboard goes away: merges, dependent
  tasks and budgets are handled without it, and a returning dashboard shows
  what happened. While an agent works or a dashboard is connected, the daemon
  asks the machine not to suspend (via `systemd-inhibit`, where allowed).
- Daemons follow this TPS: one that runs another build restarts into it by
  itself, as soon as no agent turn, build or merge is running. Its containers
  carry on, and the board says so while it waits. Never the other way around:
  a host with a newer TPS asks you to update this one instead. For a host of
  another architecture, build for it and pass `--daemon-binary`; the board's
  "Update daemon" button retries a push that failed. The host's menu on the
  front page holds the rest: stop the daemon (with its workspaces), copy your
  claude login there.
- A `CMD` line in `Containerfile.dev` says how to run the project, listening on
  `$PORT` (published on a random localhost port of its host). The play button
  beside a task runs it in the task's container, showing the console and, once
  something answers, the live site (tunnelled for SSH hosts); the task's card
  shows a green globe meanwhile. Closing the dialog stops the run, unless you
  keep it going.
- Each task tracks what its agent runs cost; an optional budget limit parks
  the task for you when spending reaches it.
- Container builds use the task's clone as their only build context, and code
  runs as an unprivileged user in a rootless container. Idle containers are
  torn down after 15 minutes; a workspace lasts until the task merges (which
  is what makes it disposable) or is deleted.
- Ctrl-L (⌘L) opens *Go to*: type a few letters of a project or a task, Enter
  takes you there. It works from inside VS Code too, which is where the keyboard
  usually is; the only thing it costs you there is "select current line", and
  the address bar keeps it everywhere else. `?` lists every shortcut that works
  where you are.
- Hacking on TPS: `npm run watch` for the web UI, `go build` and restart
  `tps` for the rest; the boards then offer to update the daemon. `go test
  ./...` covers the pure parts.
