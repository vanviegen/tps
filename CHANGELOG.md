# Changelog

## 0.6.0

- The dashboard runs on macOS, with its projects on Linux hosts over SSH;
  releases include macOS binaries.
- Merges go through a queue per project, one at a time: rebased onto the
  latest branch, tested, and sent back to the agent with the test's output
  when it fails. Queued cards can be reordered, and the test skipped.
- A merged task carries on in a "Follow-up" task, so every task is one commit,
  named after its commit message. Agents can ask to carry on once their work
  is merged (and deployed).
- Tasks run on Claude Code alone; pi support is removed.
- Tasks and project defaults set an effort beside each model, for the agent
  and the reviewer. Changed settings apply from the agent's next turn, and a
  message sent mid-turn steers the agent rather than restarting it.
- "Wait for" replaces "Start after": it also holds back a running task until
  the tasks it waits for are done.
- A line above the chat input says what the task is busy with, and for how
  long. Cards and the task's top bar show the lines its work changes, and the
  top bar the context gauge.
- A forwarded port opens in a tab of the task's VS Code once something
  listens on it.
- The merge gate in `Containerfile.dev` is renamed from `LABEL tps.check` to
  `LABEL tps.test`.
- Automated reviews see the agent's turn summaries between the user's
  messages.
- A task's right-click menu, on the board and in the sidebar, is its header
  menu.
- Fixes: flaky podman sockets, toolbox downloads, SSH hosts with a space in
  their home directory, OpenRouter budgets and turn costs, deploys'
  `git push origin`, and the chat input jumping while editing a long message.

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
