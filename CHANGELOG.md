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
- "Wait for" replaces "Start after": it also holds back a running task until
  the tasks it waits for are done.
- Tasks run on Claude Code alone; pi support is removed.
- Workspaces carry the project checkout's remotes, so a deploy's
  `git push origin` works.
- Fixes: podman sockets that stop answering are picked anew, toolbox
  downloads retry, and terminal output in log details plays out as in service
  consoles.

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
