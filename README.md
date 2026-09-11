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

The sidebar on the left is the way around. It lists every project, in the
order you put them in (drag one, or use *Move up* and *Move down* in its
menu), each wearing a colour of its own and a chip with its initials, made
from its name — Aberdeen becomes A, ShoTest ST, wild-mail WM — until you
spell them out yourself. A project and its tasks sit together on a wash of
that colour. Under each project sit its *open* tasks: the ones waiting for
you, the bright ones among them, the ones an agent is on, and the ones you
have VS Code open on (see below), each with its phase icon. Muted tasks are the ones left out:
muting is how you put a task that waits for you out of sight until you want
it back. The sidebar folds up into a narrow strip whenever VS Code comes on
screen, and by the icon beside the logo:
the chips, the phase icons and the first word of each title then, which is
what the `subject: change` form of task titles is for — hovering a task gives
the whole of it. Under the
projects are *Add project* and *Manage hosts*, and under those only the hosts
that want something from you — a login, a connection that failed, a daemon
to update — since a host that works has nothing to say.

*Add project* takes a directory holding a git repository, like
`~/projects/app`, on the host you pick there, and names the project after the
directory unless you say otherwise. *Manage hosts* shows each host as a box
outlined in what it is doing — up, busy, stopped, in trouble — and holds *Add
host*, which takes whatever you would type after `ssh`: `user@host`, a
`~/.ssh/config` alias, or `-p 2222 -J jump host` — it is also the last entry
of the project dialog's host list, for a project on a machine that is not on
the board yet. TPS uses your ssh, so keys, agents, an open multiplexed session
and passwords all work. A login it wants (a password, an unknown host key)
waits in the sidebar rather than taking over the screen: click it when you
are ready. A new host gets the daemon installed and started, and the projects
it already has are listed along with it.

Right-click a project in the sidebar for what can be done with it: adding a
task, opening its checkout, its settings — its name, initials and colour, and
the settings its new tasks start out with — moving it, and removing it. A
host's menu (right-click its box, or the ⋮ in its header) holds connecting,
copying your claude login there, updating or stopping its daemon, and removing
it. Removing a host takes it off this dashboard only; removing a project takes
it off its host, with the tasks it holds, for every dashboard.

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
container (the image installs the CLI; the socket is at `DOCKER_HOST`) talk to
a socket TPS serves, backed by the host's podman and held
to what the task may have (see `podnester/`, a library of its own). What it
starts are siblings of its container on a private network, named and labeled
after it, gone when its container is recreated; named volumes stay until the
task is deleted. Bind mounts and published ports mean what they would inside
the task's container.

## How a task flows

A project's page is its board, under a line with its name, where it stands,
and the buttons that add a task, open its checkout in VS Code, and set its
default task settings. Seven phases share four columns: *Human* has the muted
tasks at its foot, under a marker and dimmed, and *Done* holds the tasks that
are merging at its top, their icon turning until they are through, with the
ones closed without merging under a *Not merged* marker at its foot. A marker
shows only while there is something under it, and a column is one drop target
whichever part of it you aim at: a card dropped on *Human* waits for you (not
muted), and one dropped on *Done* asks how the task should end.
A task's page beside the sidebar is two columns: on the left its settings
while it is in Plan and its chat with the agent afterwards, the whole height;
on the right the description while in Plan, VS Code (running in the container)
on its workspace once it has one, and a note of the merge once it is over.
The line between the two columns is a handle: drag it to move the split,
double-click it to put it back where it was, and the arrow keys nudge it while
it has the focus. Where you leave it is this browser's, for every task.
The chat's top right corner holds the icons for running the project, rebasing
onto the latest default branch, rebuilding the container, the task's settings,
its menu (where it goes next, deleting — the card's menu, from its own page)
and *Close*. The title is a field in the task's settings, beside the model and
the budget. Rebasing and rebuilding show only while the task is
yours, and the rebase only when the branch has moved on: both change the
ground under a
workspace, which is not something to do to a running agent.

Opening a task with a workspace opens VS Code on it, and that stays open —
listed in the sidebar, the container up and code-server running in it — until
you close it, however much you look elsewhere meanwhile. *Close* stops VS Code
and takes the task out of the sidebar (a task waiting for you stays listed,
without VS Code). Closing the tab does the same for everything it had open:
the daemon keeps a code-server running for exactly as long as some dashboard
holds the task, and stops it the moment none does, whether by Close or by a
connection that went away. A reload finds the sessions where it left them. The
same goes for VS Code on a project's own checkout, behind *Open main branch*
(named for whatever its default branch is), which the ctrl-L palette lists as
the project's *main branch* too. Views you switch between are kept warm for a
few minutes, so switching back is instant.

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
   call; type to steer it mid-run or to send follow-ups. Images pasted into
   the message are attached to it: each shows as a thumbnail you can take back
   off until you send, and the message carries the path the agent reads it at
   — `/uploads/image.png`, a read-only directory in the container that keeps
   everything the task was ever sent.
3. **Human**: Claude's turn ended, or you pressed stop, or you assigned the
   task to yourself. Chat to send the agent back in, open VS Code to work
   yourself, or merge. When the default branch has moved on since the clone
   was made, the card says by how much and the rebase icon replays the task's
   work on top of it: what is in the tree is parked in a temporary commit
   first and unpacked again afterwards, so nothing has to be committed for
   this, and conflicts go the way a merge's do — a fresh agent resolves them.
   The agent working on the task is told what happened the next time it is
   sent in, rather than mid-turn.
4. **Muted**: Human, with the task put away: at the foot of the Human column
   and out of the sidebar, for what waits for you but not today. Opening a
   muted task takes it back to Human — looking at it is picking it up.
5. **Merge**: The working tree becomes a single commit on the default branch.
   The agent proposes the commit message when it reports the task ready; with
   *Merge when ready* (a task setting, out of the project's defaults) that
   merge happens by itself, otherwise you confirm the message first. If
   the default branch moved since the clone was made, the work is rebased onto
   it. Conflicts are handed to a fresh agent that resolves the rebase (guided
   by the commit messages on both sides), after which the merge completes; the
   task stays in this column meanwhile. If the agent gives up midway, the task
   goes back to you with a warning; sending the agent back in resumes the
   rebase. Merging is a moment rather than a place, so its cards sit at the top
   of the *Done* column and say so by turning.
6. **Done**: Merged. The workspace goes — its work is on the branch, and the
   container with it — but the conversation stays until the task is deleted,
   so the task can be picked up again for follow-up work: a chat message sends
   the agent back in with everything it knew, and moving the card to Human
   (or the *Pick up as human* button beside the chat) takes it on yourself.
   Either way it gets a new clone of the default branch as it stands then,
   exactly like a task leaving Plan, so the follow-up builds on its own merged
   work and on whatever other tasks landed meanwhile. The
   agent is told what happened: that its work became a commit, that the old
   workspace (and anything only in it) is gone, and that what it changes now
   becomes a commit of its own.
7. **Closed**: Over without being merged, for work that should not land: the
   task keeps its worktree, unmerged and off the branch, so it still opens in
   VS Code, can still be merged later, and takes its work with it when it is
   deleted. Ending a task by hand — dropping it on *Done*, or picking *Done*
   from its menu — asks which of the two endings is meant: a tab with the
   commit message to merge (the one it opens on, that being how a task normally
   ends) and a tab for finishing without merging. A task that is closed already
   has *Merge* in its menu, for merging it after the fact.

Moving a task back to Plan discards all of its work, after a confirmation.

## Notes

- Everything about projects and tasks lives on the host that runs them: task
  state in `~/.config/tps/projects.json`, the workspaces (repo clone, claude
  session state, chat log) and the downloaded tools under
  `~/.local/share/tps/`, and the daemon's socket, log and, on SSH hosts, its
  binary next to them. Projects, with the names, colours and initials you
  gave them, are part of that: the dashboard only keeps the list of hosts
  and the order of the projects (which spans hosts), in
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
- Services are the named, long-running commands of a task — the project's dev
  server, its test suite, a screenshot review app — each run detached in the
  task's container, with its output kept. `Containerfile.dev` declares the ones
  a project comes with: its `CMD` line is the service `app`, and a
  `LABEL tps.service.<name>="command"` line declares another. Agents start
  services too, declared or ad hoc, with `tps-service-manager` (`run`, `stop`,
  `restart`, `await`, `logs`, `ps`; it is on their PATH, and in the VS Code
  terminal), which is also what the dashboard uses, so the two never disagree
  about what runs. The play button in a task's chat opens the list: a click on
  a service opens its console (and starts one that never ran), where *Stop*,
  *Restart* and *Run again* do what they say and *Background* (or Esc) leaves
  it running behind a closed console; the play button is red while anything
  runs. A service that ends badly on its own leaves a note in the chat.
- The `EXPOSE` lines of `Containerfile.dev` say which ports the container
  forwards: each is published on a random localhost port of its host when the
  container starts (so a change to them takes a rebuild), and tunnelled to the
  dashboard's machine from an SSH host. They are listed under the services in
  the play button's menu — `8080 → 56123`, the port inside and the one it is on
  here — and open the page in a new tab once something answers HTTP there; the
  task's card shows a green globe per such port meanwhile.
- Each task tracks what its agent runs cost; an optional budget limit parks
  the task for you when spending reaches it.
- Things that happen to a task while no agent is running in it are not shouted
  at the agent mid-turn and not lost either: they wait, and go in ahead of
  whatever sends it in next, so it knows what it is coming back to before it
  acts on what it remembers. It hears that its workspace was rebased, that its
  container was rebuilt or recycled after idling (background processes and
  hand-installed tools gone with it), that its image fell back to the default
  one because `Containerfile.dev` broke, that its turn was cut off by the user
  or by a TPS restart, and that the task was parked on its budget. A note
  replaces an earlier one about the same thing, or is taken back when it no
  longer holds (a file that builds again, a workspace that was merged away).
- Container builds use the task's clone as their only build context, and code
  runs as an unprivileged user in a rootless container. A container nobody
  holds open and no agent works in is torn down after 15 minutes; a workspace
  lasts until the task merges (which is what makes it disposable) or is
  deleted. code-server runs in the container only while a dashboard has the
  task open, so closing it never takes an agent down with it.
- Ctrl-L (⌘L) opens *Go to*: type a few letters of a project or a task, Enter
  takes you there. It works from inside VS Code too, which is where the keyboard
  usually is; the only thing it costs you there is "select current line", and
  the address bar keeps it everywhere else. `?` lists every shortcut that works
  where you are.
- The rest are ctrl-shift (⇧⌘) and work while the button they press is on
  screen: **S** starts a task and **F** opens the project's files in VS Code,
  both from a project's page; **G** merges the task you are looking at while it
  offers to be merged; **X** closes it; **L** folds the sidebar away and back.
  Ctrl-enter sends a chat message, and assigns a plan to the agent. Unlike
  *Go to*, these don't reach into VS Code — while it has the focus it has the
  keyboard.
- The dashboard installs as an app: the browser's *Install* — Chrome, Edge and
  Brave put it in the address bar, phones call it *Add to home screen* — gives
  TPS a window of its own, off the tab strip and with its own icon, which is
  what a desktop pins and groups separately from the browser. It is the same
  page from the same localhost, so `tps` still has to be running here, and
  `http://localhost` counts as a secure page for this; a dashboard reached
  over the network by name or address does not, and no browser will install it.
- *Ready notifications*, in a task's menu, is a checkbox that has this browser
  announce that task the moment an agent hands it back or its merge is over:
  the desktop's own notification, so a dashboard behind other windows still
  reaches you, and clicking it brings that window forward on the task.
  Permission is asked the first time one is switched on, and a notification is
  sent right away so you can see that it took. The setting is per task and per
  browser, as the permission is. There is no service worker and no web push
  behind this: a window on TPS — a tab, or the installed app — has to be open
  for anything to be sent, and closing it takes its notifications with it.
- Hacking on TPS: `npm run watch` for the web UI, `go build` and restart
  `tps` for the rest; the boards then offer to update the daemon. `go test
  ./...` covers the pure parts, and `npm run icons` redraws the app icon (one
  description of the artwork, written out as the SVG and the PNGs a browser
  installs it from).
