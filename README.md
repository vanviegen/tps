# TPS

A kanban-style manager for AI coding agents. Each task works in its own clone
of your repository, inside a disposable podman dev container, driven by the
`claude` CLI through a chat you watch and steer from a live web dashboard.

## Requirements

- Linux with node >= 23.6, git, and rootless podman
- The `claude` CLI logged in on the host (its credentials are shared with the
  containers), or `ANTHROPIC_API_KEY` set

## Getting started

```sh
npm install
npm run build     # bundles the web UI
npm start         # http://localhost:4820/ (localhost-only, no login)
```

Add a project (any local git repository) from the dashboard. If it doesn't
have a `Containerfile.dev` yet, TPS generates one, Debian based with
code-server and claude-code, lets you pick extra toolchains (preselected by
looking at the repo), and commits it. The boilerplate layers are identical
across projects, so podman shares them.

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
   that moment, so it includes their merged work. The chat shows what it is doing, tool call by tool call; type to
   steer it mid-run or to send follow-ups. The agent can also edit
   `Containerfile.dev` and ask for a rebuild, continuing in the new container.
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

- Task state (title, description, phase) lives in `~/.config/tps/`, the
  workspaces (repo clone, claude session state, chat log) under
  `~/.local/share/tps/`; there is no database and your repository only ever
  receives the final merge commit.
- Containers get `PORT=8080` published on a random localhost port; while
  something answers HTTP there, the task shows a green globe that opens it.
- Each task tracks what its agent runs cost; an optional budget limit parks
  the task for you when spending reaches it.
- Container builds use the task's clone as their only build context, and code
  runs as an unprivileged user in a rootless container. Idle containers are
  torn down after 15 minutes; workspaces persist until the task is deleted.
- `npm run watch` + `npm run dev` for hacking on TPS itself.
