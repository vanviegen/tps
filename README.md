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
npm install && npm run build       # bundles the web UI into web/dist
CGO_ENABLED=0 go build -o tps .    # embeds it; one static binary is all you need
./tps                              # http://localhost:4820/, opens your browser
```

*Add project* in the sidebar registers any git repository on that host. To
use another machine, *Add host* it first: whatever you would type after
`ssh`, so `user@host`, a `~/.ssh/config` alias, or `-p 2222 -J jump host`.
TPS uses your ssh, so keys, agents, an open multiplexed session and
passwords all work; prompts appear in the browser. Then add projects that
live there.

If a repository has no `Containerfile.dev` yet, TPS generates a Debian based
one, lets you pick toolchains (preselected by looking at the repo), and
commits it. The file is a plain dev image definition with nothing
TPS-specific in it: code-server, claude and an init are downloaded once per
host and mounted into every container. Any base works, as long as it has
bash and git, and a user with uid 1000 who owns a home directory.

## How a task flows

Every project gets a board with four columns. The sidebar is the whole tree:
hosts, their projects (with a count of the tasks sitting in Human), their
tasks, and under each task its three pages: Agent (the chat), Code (VS Code in the container) and
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
  session state, chat log) and the downloaded tools under
  `~/.local/share/tps/`, and the daemon's socket, log and, on SSH hosts, its
  binary next to them. The dashboard only
  keeps its list of hosts in `~/.config/tps/hosts.json`. There is no database
  and your repository only ever receives the final merge commit.
- The daemon keeps working when the dashboard goes away: merges, dependent
  tasks and budgets are handled without it, and a returning dashboard shows
  what happened. While an agent works or a dashboard is connected, the daemon
  asks the machine not to suspend (via `systemd-inhibit`, where allowed).
- Daemons are not replaced behind your back. A host whose daemon runs another
  build of TPS shows an update button in the sidebar;
  the daemon then restarts into this build as soon as no agent turn, build or
  merge is running, and its containers carry on. An update happens by itself
  only when the daemon is too old for this TPS to talk to, and never the other
  way around: a host with a newer TPS asks you to update this one instead.
  For a host of another architecture, build for it and pass
  `--daemon-binary`. Right-click a host in the sidebar for the rest: stop
  its daemon (with its workspaces), copy your claude login there, remove it.
- Containers get `PORT=8080` published on a random localhost port of their
  host; while something answers HTTP there, the task shows a green globe that
  opens it (tunnelled for SSH hosts).
- Each task tracks what its agent runs cost; an optional budget limit parks
  the task for you when spending reaches it.
- Container builds use the task's clone as their only build context, and code
  runs as an unprivileged user in a rootless container. Idle containers are
  torn down after 15 minutes; workspaces persist until the task is deleted.
- Hacking on TPS: `npm run watch` for the web UI, `go build` and restart
  `tps` for the rest; the sidebar then offers to update the daemon. `go test
  ./...` covers the pure parts.
