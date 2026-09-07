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
machine when needed, and installs and starts one on every SSH host you add.
Several dashboards (a laptop, a desktop) can use the same host at once; each
just mirrors what the daemon has.

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
npm install && npm run build   # bundles the web UI into web/dist
go build -o tps .              # embeds it; the binary is all you need
./tps                          # http://localhost:4820/, opens your browser
```

Add a project from the dashboard: any git repository on this machine. To use
another machine, add it as a host first (its name as you would give it to
`ssh`; `~/.ssh/config` aliases, agent keys and passwords all work, and
prompts appear in the browser). Then add projects that live there.

If a repository has no `Containerfile.dev` yet, TPS generates one, Debian
based with code-server and claude-code, lets you pick extra toolchains
(preselected by looking at the repo), and commits it. The boilerplate layers
are identical across projects, so podman shares them.

## How a task flows

Every project gets a board with four columns. The sidebar lists the projects
(with a count of the tasks sitting in Human) and their tasks; under each task
sit its three pages: Agent (the chat), Code (VS Code in the container) and
Settings (how the run is doing, the merge button, and the settings below).

1. **Plan**: A title, a markdown description, and settings (model,
   dependencies, budget, merge behaviour). Nothing exists on disk yet. Once
   the task leaves this column the title and description are fixed; the other
   settings stay tweakable.
2. **Agent**: Assigning the task creates a clone of the repository (hardlinked
   objects, so nearly free), spins up the container, and hands the description
   to claude. A task that depends on other tasks waits in this column first,
   and starts once each of them is merged or deleted; the clone is made at
   that moment, so it includes their merged work. The chat shows what it is
   doing, tool call by tool call; type to steer it mid-run or to send
   follow-ups. The agent can also edit `Containerfile.dev` and ask for a
   rebuild, continuing in the new container.
3. **Human**: Claude's turn ended, or you pressed stop, or you assigned the
   task to yourself. Chat to send the agent back in, open VS Code (running in
   the container) to work yourself, or merge.
4. **Merge**: The working tree becomes a single commit on the default branch.
   The agent proposes the commit message when it reports the task ready; with
   *Merge without confirmation* (a project setting each task can override)
   that merge happens by itself, otherwise you confirm the message first.

If the default branch moved since the clone was made, the work is rebased onto
it during the merge. Conflicts are handed to a fresh agent that resolves the
rebase (guided by the commit messages on both sides) and reports ready again.
Moving a task back to Plan discards all of its work, after a confirmation.

## Notes

- Everything about projects and tasks lives on the host that runs them: task
  state in `~/.config/tps/projects.json`, the workspaces (repo clone, claude
  session state, chat log) under `~/.local/share/tps/`, and the daemon's
  socket, log and, on SSH hosts, its binary next to them. The dashboard only
  keeps its list of hosts in `~/.config/tps/hosts.json`. There is no database
  and your repository only ever receives the final merge commit.
- The daemon keeps working when the dashboard goes away: merges, dependent
  tasks and budgets are handled without it, and a returning dashboard shows
  what happened. While an agent works or a dashboard is connected, the daemon
  asks the machine not to suspend (via `systemd-inhibit`, where allowed).
- A rebuilt `tps` replaces running daemons by itself, once no agent is busy;
  for a host of another architecture, build for it and pass
  `--daemon-binary`. *Stop daemon* in the Hosts dialog shuts one down (with
  its workspaces); it comes back when needed.
- Containers get `PORT=8080` published on a random localhost port of their
  host; while something answers HTTP there, the task shows a green globe that
  opens it (tunnelled for SSH hosts).
- Each task tracks what its agent runs cost; an optional budget limit parks
  the task for you when spending reaches it.
- Container builds use the task's clone as their only build context, and code
  runs as an unprivileged user in a rootless container. Idle containers are
  torn down after 15 minutes; workspaces persist until the task is deleted.
- Hacking on TPS: `npm run watch` for the web UI, `go build -o tps . && ./tps`
  to pick up changes (the daemon restarts into the new binary). `go test ./...`
  covers the pure parts.
