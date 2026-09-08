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
The dashboard keeps only its own list of projects; the daemon behind each
one holds the tasks. Several dashboards (a laptop, a desktop) can use the
same daemon at once, each showing the projects it lists.

## Requirements

- On every host that runs projects: Linux with rootless podman and git, and
  the `claude` CLI logged in there (or `ANTHROPIC_API_KEY` in the daemon's
  environment). The dashboard can copy your local claude login to a host.
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

*Add project*, the bottom entry of the project dropdown, takes a directory
holding a git repository: `~/projects/app` for one on this machine, `host:~/projects/app`
for one on another, where `host` is whatever you would type after `ssh`, so
`user@host`, a `~/.ssh/config` alias, or `-p 2222 -J jump host`. Hosts you
already use are suggested as you type. TPS uses your ssh, so keys, agents,
an open multiplexed session and passwords all work; prompts appear in the
browser. A new host gets the daemon installed and started.

Tasks run in a Debian image with the basics (git, curl, a compiler). A
repository with its own `Containerfile.dev` at the root gets that instead: a
plain image definition with nothing TPS-specific in it, as code-server,
claude and an init are downloaded once per host and mounted into every
container. Any base works, as long as it has bash and git, and a user with
uid 1000 who owns a home directory. The agent knows about this: when it lacks
a tool, it can write or extend `Containerfile.dev` and continue in a container
built from it, and the file is merged along with the rest of its work.

## How a task flows

The window is two columns. The left one holds a project dropdown and, under
it, a task dropdown; both list the ones waiting for a human first, then the
most recent, and both wear a badge counting what they hide that wants a human.
The gear beside each opens its settings, and the phase icon beside the task
opens the list of phases to move it to.

The right column is whatever the left one names: the project's board of five
columns while no task is picked, VS Code (running in the container) on the
task's workspace once one is, and VS Code on the project's own checkout for the
*"main" branch* entry of the task list. A view you switch away from is kept
warm for a few minutes, so switching back is instant.

Under the selectors, a task shows its description while it is still in Plan,
and its chat with the agent once it is not: the log, and the box to steer it
with.

1. **Plan**: A title, a markdown description, and settings (model,
   dependencies, budget, merge behaviour). Nothing exists on disk yet. Once
   the task leaves this column the description is fixed; the title and the
   other settings stay tweakable.
2. **Agent**: Assigning the task creates a clone of the repository (hardlinked
   objects, so nearly free), spins up the container, and hands the description
   to claude. A task that depends on other tasks waits in this column first,
   and starts once each of them is done or deleted; the clone is made at
   that moment, so it includes their merged work. The chat shows what it is
   doing, tool call by tool call; type to steer it mid-run or to send
   follow-ups.
3. **Human**: Claude's turn ended, or you pressed stop, or you assigned the
   task to yourself. Chat to send the agent back in, open VS Code to work
   yourself, or merge.
4. **Merge**: The working tree becomes a single commit on the default branch.
   The agent proposes the commit message when it reports the task ready; with
   *Merge without confirmation* (a project setting each task can override)
   that merge happens by itself, otherwise you confirm the message first. If
   the default branch moved since the clone was made, the work is rebased onto
   it. Conflicts are handed to a fresh agent that resolves the rebase (guided
   by the commit messages on both sides), after which the merge completes; the
   task stays in this column meanwhile.
5. **Done**: Merged. The workspace stays around until the task is deleted, so
   the chat can still be picked up, which reopens the task.

Moving a task back to Plan discards all of its work, after a confirmation.

## Notes

- Everything about projects and tasks lives on the host that runs them: task
  state in `~/.config/tps/projects.json`, the workspaces (repo clone, claude
  session state, chat log) and the downloaded tools under
  `~/.local/share/tps/`, and the daemon's socket, log and, on SSH hosts, its
  binary next to them. The dashboard only keeps its list of projects, with
  the names you gave them, in `~/.config/tps/dashboard.json`. There is no
  database and your repository only ever receives the final merge commit.
- Taking a project off the list changes nothing on its host: the daemon keeps
  its tasks, and they are back when the project is added again. A project
  without tasks is forgotten by the daemon too.
- The daemon keeps working when the dashboard goes away: merges, dependent
  tasks and budgets are handled without it, and a returning dashboard shows
  what happened. While an agent works or a dashboard is connected, the daemon
  asks the machine not to suspend (via `systemd-inhibit`, where allowed).
- Daemons are not replaced behind your back. The board of a project whose
  daemon runs another build of TPS offers to update it; the daemon then
  restarts into this build as soon as no agent turn, build or merge is
  running, and its containers carry on. An update happens by itself only
  when the daemon is too old for this TPS to talk to, and never the other
  way around: a host with a newer TPS asks you to update this one instead.
  For a host of another architecture, build for it and pass
  `--daemon-binary`. The project settings (the gear beside the project) hold
  the rest: stop the daemon (with its workspaces), copy your claude login there.
- Containers get `PORT=8080` published on a random localhost port of their
  host; while something answers HTTP there, the task's card shows a green globe
  that opens it (tunnelled for SSH hosts).
- Each task tracks what its agent runs cost; an optional budget limit parks
  the task for you when spending reaches it.
- Container builds use the task's clone as their only build context, and code
  runs as an unprivileged user in a rootless container. Idle containers are
  torn down after 15 minutes; workspaces persist until the task is deleted.
- Hacking on TPS: `npm run watch` for the web UI, `go build` and restart
  `tps` for the rest; the boards then offer to update the daemon. `go test
  ./...` covers the pure parts.
