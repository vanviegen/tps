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

The front page is a column per host — this machine first — holding the
projects listed there. *Add project* in a host's column takes a directory
holding a git repository on it, like `~/projects/app`, and names the project
after the directory unless you say otherwise. *Add host*, under the columns,
takes whatever you would type after `ssh`: `user@host`, a `~/.ssh/config`
alias, or `-p 2222 -J jump host`. TPS uses your ssh, so keys, agents, an open
multiplexed session and passwords all work; prompts appear in the browser. A
new host gets the daemon installed and started, and the projects it already
has are listed along with it.

Right-click a project or a host (or use the ⋮ in a host's header) for what can
be done with it: renaming, its settings, connecting, copying your claude login
to a host, updating or stopping its daemon, and taking it off the list — which
changes nothing on the host itself, so adding it again brings its tasks back.

Tasks run in a Debian image with the basics (git, curl, a compiler). A
repository with its own `Containerfile.dev` at the root gets that instead: a
plain image definition with nothing TPS-specific in it, as code-server,
claude and an init are downloaded once per host and mounted into every
container. Any base works, as long as it has bash and git, and a user with
uid 1000 who owns a home directory. The agent knows about this: when it lacks
a tool, it can write or extend `Containerfile.dev` and continue in a container
built from it, and the file is merged along with the rest of its work. Should
the file ever break (a bad edit, a merge conflict), the task falls back to the
default image so you and the agent can still get in, and the agent is told to
fix it first.

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

1. **Plan**: A title, a markdown description, and settings (model, the tasks
   to start after, budget, merge behaviour). The models to pick from are the
   ones the claude on the project's host offers (it is asked, once per daemon
   run); *default* leaves the choice to claude's own configuration, and is
   what a new task gets. Nothing exists on disk yet. Once the task leaves
   this column the description is fixed; the title and the other settings
   stay tweakable.

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
   *Merge without confirmation* (a project setting each task can override)
   that merge happens by itself, otherwise you confirm the message first. If
   the default branch moved since the clone was made, the work is rebased onto
   it. Conflicts are handed to a fresh agent that resolves the rebase (guided
   by the commit messages on both sides), after which the merge completes; the
   task stays in this column meanwhile. If the agent gives up midway, the task
   goes back to you with a warning; sending the agent back in resumes the rebase.
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
  torn down after 15 minutes; workspaces persist until the task is deleted.
- Ctrl-L (⌘L) opens *Go to*: type a few letters of a project or a task, Enter
  takes you there. It works from inside VS Code too, which is where the keyboard
  usually is; the only thing it costs you there is "select current line", and
  the address bar keeps it everywhere else. `?` lists every shortcut that works
  where you are.
- Hacking on TPS: `npm run watch` for the web UI, `go build` and restart
  `tps` for the rest; the boards then offer to update the daemon. `go test
  ./...` covers the pure parts.
