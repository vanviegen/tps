# Changelog

## 0.6.0

- The dashboard runs on macOS, with its projects on Linux hosts over SSH;
  releases include macOS binaries.
- Merges go through a queue per project, one at a time: rebased onto the
  latest branch, tested, and sent back to the agent with the test's output
  when it fails. Queued cards can be reordered, and the test skipped.
- A task's right column has a tab beside VS Code for each forwarded port that
  answers HTTP, which can be reloaded or pointed at another URL.
- Agents show the user a note at the bottom of the chat with
  `show-user-text`; `ask-human` is gone. The guest tool lives at
  `/tps/guest-tool`, and the merge gate is `LABEL tps.test`.
- A merged task carries on in a "Follow-up" task, so every task is one commit,
  and tasks are named after their commit message.
- Agents can now request to continue running after their work so far has been
  merged (and deployed).
- Cards and the task's top bar show the lines its work adds and removes; the
  context gauge moved from the composer to the top bar, and a card ready to
  merge has a ✔ after its title.
- A line above the chat input says what goes on in the task, and for how
  long ("Thinking · 96s"): Initializing, Thinking, Tooling, Reviewing,
  Testing or Merging.
- A scheduled run's title says, on hover, which scheduled task started it and
  when.
- `await-task` prints the state a task is in, among them merging, merged and
  deployed. Automated reviews see the agent's turn summaries between the
  user's messages.
- "Wait for" replaces "Start after": it also holds back a running task until
  the tasks it waits for are done.
- Tasks run on Claude Code alone, now 2.1.322; pi support is removed. VS Code
  is code-server 4.145.2.
- Workspaces carry the project checkout's remotes, so a deploy's
  `git push origin` works.
- A task's right-click menu, on the board and in the sidebar, is the same as
  its header menu.
- Fixes: podman sockets that stop answering are picked anew, toolbox
  downloads retry, terminal output in log details plays out as in service
  consoles, SSH hosts whose home directory has a space in it work, and an
  agent asking for its OpenRouter key again takes from the task's OpenRouter
  budget only what the key lacks.

## 0.5.0

- Scheduled tasks can be run now, besides at their set times.
- Host file permissions: host paths can be mounted into every task's
  container, not only the deploy's, and read/write.
- Workspaces start with seeds (ignored paths such as `node_modules`, copied
  from the checkout), and agents can suggest changes to the project's
  settings.
- Agents can create tasks in other projects on their host and wait for them to
  be merged or deployed.
- The board shows a deploy that is running, and a deploy recorded by hand
  (`refs/tps/deployed`) for projects TPS does not deploy.
- Fixes: VS Code starts in tasks with caches in the home directory, and the
  sidebar renders on a host with a scratch task.
