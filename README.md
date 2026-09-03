# TPS

A kanban-style manager for AI coding agents. Each task lives on its own git
branch, gets a disposable dev container (podman) with VS Code in the browser,
and is implemented and optionally reviewed by the `claude` CLI. You plan,
review and merge from a live web dashboard.

All state lives in git: TPS keeps no database. Task metadata sits in `.tps/`
files on the task's branch, so everything survives restarts, travels with the
repo, and can be inspected with plain git.

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
have a `.tps/Containerfile` yet, TPS generates one, Debian based with
code-server and claude-code, lets you pick extra toolchains (preselected by
looking at the repo), and commits it. The boilerplate layers are identical
across projects, so podman shares them.

## How a task flows

Every project gets a board with four columns:

1. **Plan**: A new task is a branch (`tps/<id>`) on the default branch's
   current tip, with the description in `.tps/TASK.md` and all task settings
   in `.tps/state.json`. "Refine plan" lets claude iterate on TASK.md with
   you.
2. **Implement**: The branch is rebased onto the latest default branch first
   (skipped when that would conflict). The implementer agent works in small
   commits whose messages explain the changes; if a review model is set, a
   reviewer agent then checks the work and files findings as `##` sections in
   `.tps/REVIEW.md` (small fixes it just makes itself). Findings send the
   implementer back in, up to the task's *Maximum automatic review cycles*
   (default 1); it resolves each item in its own commit, removing the section
   as it goes. A clean review finishes the phase.
3. **Review**: Your turn: VS Code opens `.tps/REVIEW.md` for your notes. Move
   the task back to Implement to have them addressed, or to Done to merge.
   With *Skip human review* enabled this phase is skipped entirely.
4. **Done**: The work is squashed into one commit (task title plus the list
   of change descriptions, `.tps/` stripped apart from the Containerfile),
   rebased onto the default branch and fast-forwarded. On conflicts, claude
   gets a container on a temporary branch to resolve the rebase. The task
   branch is kept.

Tasks can depend on other tasks, and optionally start implementing themselves
once all dependencies are done; together with *Skip human review* that gives
a fully unsupervised pipeline. Agent instructions live in one workflow
document (`server/prompts.ts`) handed to every run as its system prompt,
along with its role: implementer, reviewer or planner.

Workspaces (worktree + container) exist only while needed. On shutdown,
uncommitted work is saved in a clearly marked tmp commit that is soft-reset on
the next start; idle workspaces are torn down after 15 minutes. Editing the
Containerfile mid-implementation recreates the container and resumes the
session, which survives because claude's state lives in `.tps/claude/` inside
the worktree. Containers get `PORT=8080` published on a random localhost
port; while something answers HTTP there, the task shows a green globe that
opens it.

## Notes

- Task worktrees are local clones (hardlinked objects) under
  `~/.local/share/tps/`, so git works inside the container without exposing
  the real repository; every TPS commit is pushed straight back to the
  project repo.
- Container builds use the worktree as their only build context, and code
  runs as an unprivileged user in a rootless container.
- Creating a task requires a clean project worktree, since the task branches
  off the committed tip.
- The registered project list is the one bit of state outside git:
  `~/.config/tps/projects.json`.
- `npm run watch` + `npm run dev` for hacking on TPS itself.
