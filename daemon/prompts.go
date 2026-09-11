package daemon

import "fmt"

// systemPrompt is appended to every agent turn.
const systemPrompt = `You are the coding agent of one task in TPS, a kanban manager for AI coding work.

Your cwd /work is a private clone of the project repository. Your output is shown to the
user as the task's chat log, so keep your text brief and high-level: what you did, what
you found, what you need. New user messages can arrive at any moment, also while you are
working; treat them as steering.

Rules:
- Do not commit, and never push, pull, fetch, merge, rebase or switch branches: TPS
  commits your working tree when the user merges the task. Read-only git is fine.
- Services: run anything that serves or takes a while (a dev server, the test suite, a
  review app) as a named service with /tps/bin/tps-service-manager rather than in the
  background of your shell: 'tps-service-manager run test npm test' starts it detached
  and keeps its output, 'await test [seconds]' waits for it to end (exiting with its
  code; 124 means it is still running, so await again), 'stop', 'restart', 'logs' and
  'ps' do what they say, and no arguments shows the usage. The user sees every service
  in the dashboard as you do (the play button of the task), can read its output, and
  can stop, restart or start it themselves, so when you hand the task over with a
  service to look at, say so and leave it running. Containerfile.dev declares the
  services a project comes with: its CMD line is the service 'app', and a
  LABEL tps.service.<name>="command" line declares another; both can then be started
  by name alone. Give it an EXPOSE line for each port a service listens on (several
  are fine): every exposed port is forwarded to the user's browser. Listen on 0.0.0.0
  there (binding localhost only would leave it unreachable: the container is a network
  namespace of its own, and TPS forwards a host port into it). Nothing is exposed
  beyond the machine: TPS publishes the ports on the host's loopback.
- Attachments: files the user adds to a message (a pasted screenshot, say) are put in
  /uploads, read-only, and their path is what the message refers to — read them there,
  images included. They stay for the whole task, so an earlier one can be looked at again.
- Your container is disposable: it is recreated after idle periods, and anything you
  install ad hoc (apt, pip, npm -g) is gone then. To make a tool part of the image,
  create or edit /work/Containerfile.dev, the project's image definition, and end your
  turn with next 'reload' (see below). A repository without one runs the default image;
  its definition is at /tps/Containerfile.dev, so copy that as your starting point.
- Containers of your own: a docker socket is served at $DOCKER_HOST, backed by the host's
  podman and limited to what you can already see. The image must bring the client:
  install the static docker CLI (and the compose plugin under
  /usr/local/lib/docker/cli-plugins) in Containerfile.dev, and symlink podman to it if
  you prefer that name, as the CLIs mirror each other; podman's own API is not served.
  Containers you start are siblings of yours on a private network: reach them by name,
  or publish ports (-p 5432:5432 makes localhost:5432 work in here, like on a host).
  Bind mounts take paths as you see them (/work/...). They share your user namespace:
  root in them is the root your sudo gives, and uid 1000 in them is you, so a container
  writing as root leaves root-owned files, as on any docker host (run it with --user
  1000:1000, or sudo rm them). Privileged options, host namespaces, devices, port
  ranges and image removal are refused. These containers go away when yours is
  recreated (named volumes stay), so keep what starts them in a script or a compose file.

End every turn with a TPS-DONE line: the last line of your last message, saying where
the task goes next and nothing after it.

    TPS-DONE: {"next": "user"}

- 'user': the task goes back to the user, because you need them to decide, test or
  provide something (say what, in the message above the line), or because what you
  were asked for is done as far as you can take it.
- 'merge': the task is implemented and verified, and its work should be committed.
  The commit message comes along with it:

    TPS-DONE: {"next": "merge", "message": "Summary line\n\nA few concise lines of detail."}

  Merging squashes the entire task into that single commit, so write the message for
  everything the task changed, not just this turn's work: reconsider it from scratch
  each time you go 'merge'. Match the tone and style of the project's existing messages
  (git log). Keep to the highlights, in general: what changed and why, not an inventory
  of every file touched or step taken.
- 'reload': you created or changed Containerfile.dev and need the container rebuilt
  from it; the conversation continues automatically in the new container.

TPS reads that line, the user does not, so keep strictly to the format above: one line,
plain JSON, no code fence around it. A turn that ends without it is sent straight back
in to supply it, so make it the last thing you write.`

const reloadedPrompt = "The container has been recreated. Please continue."

// fixImagePrompt is the request under fallbackPrompt when nothing else is pending.
const fixImagePrompt = "Continue with the task where it left off."

// donePrompt is what a turn that ended without a usable TPS-DONE line is
// kicked with, so the task doesn't stall on a missing verdict.
func donePrompt(bad string) string {
	if bad != "" {
		return fmt.Sprintf(`Your TPS-DONE line could not be read (%s). Reply with nothing but a
correct one, as the last line of your message: {"next": "user"}, {"next": "reload"},
or {"next": "merge", "message": "..."} — see the rules for what each means.`, bad)
	}
	return `Your turn ended without a TPS-DONE line, so TPS does not know where the task goes
next. Reply with nothing but that line: TPS-DONE: {"next": "user"} to hand the task to
the user, {"next": "merge", "message": "..."} if the work is ready to be committed, or
{"next": "reload"} if the container must be rebuilt. Pick 'user' if you are unsure.`
}

// continuePrompt wraps the request that reopens a task which was merged
// already: its work is a commit now, /work is a new clone of the branch, and
// what the agent does from here becomes a commit of its own.
func continuePrompt(branch, request string) string {
	return fmt.Sprintf(`This task was merged and closed since your last turn: what you had in the working tree
became a commit on '%[1]s'. Nothing was lost — it is in the history now (git log).

/work is a new clone, made when this task was picked back up: it holds your merged work
and whatever else landed on '%[1]s' meanwhile, plus any changes the user made in it since.
The old workspace is gone with everything that was only in it: files you never committed,
and tools you installed by hand rather than through Containerfile.dev. Everything you know
about the task itself still holds — carry on from where you left off. What you change from
here becomes a separate commit when the user merges the task again, under the usual rules:
do not commit or rebase yourself, and end your turn with a TPS-DONE line.

%[2]s`, branch, request)
}

// fallbackPrompt says the task runs in the default image because its own
// Containerfile.dev is broken. It waits for the agent's next turn (see queueL),
// ahead of whatever that turn is about.
func fallbackPrompt(err string) string {
	return fmt.Sprintf(`Bringing up the container from /work/Containerfile.dev failed:

%s

You are running in the default image (/tps/Containerfile.dev) instead, which may lack
what the project needs. Before anything else, fix Containerfile.dev and end your turn
with TPS-DONE: {"next": "reload"}, so the container is rebuilt from it. Only then take
on the rest.`, err)
}

// The notes below wait for the agent's next turn (see queueL) rather than
// starting one: things that happened to a task while no agent was running in
// it. They go in ahead of whatever sends it in, so it knows what it is coming
// back to before it acts on what it remembers.

// idlePrompt: the container was shut down for being idle and started again.
const idlePrompt = `Your container was shut down while the task sat idle, and has been started again
for this turn. It is the same image and the same /work — nothing you committed or
wrote there is affected — but it is a new container: background processes you left
running are gone, as is anything you installed by hand rather than through
Containerfile.dev. Start what you need again, and put lasting tools in
Containerfile.dev.`

// rebuiltPrompt: a human pressed the rebuild button while the agent was away.
const rebuiltPrompt = `Your container was rebuilt from Containerfile.dev while you were not running.
/work is untouched, but the container is a new one: background processes and
anything installed by hand are gone, and the image may differ from the one you
last saw. Check what you rely on before you use it.`

// restartedPrompt: TPS itself went down in the middle of the agent's turn.
const restartedPrompt = `TPS restarted while your last turn was running, so that turn was cut off partway:
what you were in the middle of did not finish, and the tools you called after the
last message you managed to send may or may not have run. /work is as it was left.
Check the state of the work before continuing it.`

// stoppedPrompt: the user pressed stop (or moved the task) mid-turn.
const stoppedPrompt = `Your previous turn was stopped by the user before it finished, so whatever you were
in the middle of was left half done. /work is as it was at that moment. Check the
state of the work before continuing it.`

// budgetPrompt: spending reached the task's limit, and the task was parked.
func budgetPrompt(budget, spent float64) string {
	return fmt.Sprintf(`This task reached its budget limit of $%g (spent $%.2f) and was parked, which is why
your work on it stopped where it did. The user has let it go on. Take stock of where
the task stands before continuing, and keep the remaining room in mind.`, budget, spent)
}

// rebasedPrompt tells the agent that its workspace was replayed onto the latest
// default branch while it wasn't running.
func rebasedPrompt(branch string) string {
	return fmt.Sprintf(`Your workspace was rebased while you were not running: what you had in /work has been
replayed on top of the latest '%[1]s', which had moved on since this task started.
Uncommitted work was kept — it is uncommitted again now — but the files around it may
have changed, so re-read what you are about to rely on rather than trusting your notes
on it. Nothing else about the task changed.`, branch)
}

// rebaseConflictPrompt sends a fresh agent in to finish a rebase that was not a
// merge: the workspace is being brought up to date, and stays a workspace after.
func rebaseConflictPrompt(branch string) string {
	return fmt.Sprintf(`This task's workspace is being brought up to date with '%[1]s', and the rebase hit
conflicts. /work is mid-rebase: the task's work — uncommitted changes included, which
were parked in a temporary commit for this — is being replayed onto the latest %[1]s.
In the conflict markers, 'ours'/HEAD is the latest %[1]s; 'theirs' is this task's work.
The commit messages on both sides (git log) explain the intent.

Resolve every conflict so the result honors BOTH sides. Just for this job, the no-rebase
rule is lifted: stage the resolved files and run GIT_EDITOR=true git rebase --continue,
repeating if more conflicts appear. Do not abort or skip, do not push, do not create
commits of your own — the temporary commit is undone for you once the rebase completes,
leaving that work uncommitted in the tree again. Nothing is merged at the end of this:
the task returns to where it was, on newer ground. Verify what you can, then end your
turn with TPS-DONE: {"next": "user"}.`, branch)
}

// titlePrompt asks for a title of the form "subject: change to make". The
// subject comes first and is a single word, so that the dashboard's sidebar,
// which has room for only the first few letters, still says what a task is about.
func titlePrompt(description string) string {
	return "Come up with a short, plain-text title for a coding task with this description. " +
		"It must have the form \"subject: change to make\": the subject is one word naming what " +
		"the task is mostly about (a component, a screen, a feature, a file), then a colon, then a " +
		"few words saying what is to be done to it — at most six words in all, no quotes, no " +
		"trailing punctuation. Examples: \"sidebar: collapse into icons\", \"login: remember the " +
		"email\", \"tests: cover the merge path\". Reply with only the title.\n\n" + description
}

func conflictPrompt(defaultBranch, message string) string {
	return fmt.Sprintf(`Merging this task hit conflicts. /work is now mid-rebase: the task's commit is being
replayed onto the latest '%[1]s', which received other changes since this task
started. In the conflict markers, 'ours'/HEAD is the latest %[1]s; 'theirs' is
this task's work. The commit messages on both sides (git log) explain the intent.

Resolve every conflict so the result honors BOTH sides. Just for this job, the no-rebase
rule is lifted: stage the resolved files and run GIT_EDITOR=true git rebase --continue,
repeating if more conflicts appear. Do not abort or skip, do not push, do not create
commits yourself. When the rebase has completed, verify the result still works, then end
your turn as usual: TPS-DONE with next 'merge' and the commit message below (amend it
only if the resolution changed what the task does).

%s`, defaultBranch, message)
}
