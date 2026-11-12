# TPS in four and a half minutes: the script of the demo video

One project, from an empty folder to deployed and looked after: `snip`, a URL
shortener. The **Voice** column is what is said, and is read from here by
`make.mjs`: it is what the subtitles say, and what the synthetic voice speaks
where `voice/` has no recording of the line. What is shown meanwhile is
`beats.mjs`, beat by beat; the other two columns say what that is, for
reading.

A beat lasts as long as its line takes to say. ⏩ marks a beat whose footage
is sped up to fit its line, with a ⏩ badge in the corner saying how much.

## 0. Hello

| | Voice | Screen | Camera |
|-|-|-|-|
| 0.1 | Hi, and welcome to TPS. My name is Frank, and I built TPS as my personal dashboard for doing agentic software development. | A second each of what is to come: the agent beside VS Code, the merge dialog, the board of every project, the app, a diff, the merge queue, a save point, a deploy. | The TPS title over it, in a vignette. |
| 0.2 | And for the past couple of months, I've basically been *living* inside of this app, working on many projects simultaneously. In this video, I'll give you a quick tour. | More of what is to come: typing a plan, the review settings, steering, the Containerfile, the reviewer, the steps branch, a follow-up, a schedule, Ctrl-L, an OpenRouter budget. | The title stays. |

## 1. A project is born

| | Voice | Screen | Camera |
|-|-|-|-|
| 1.1 | Let's start a project from an empty folder. | *localhost* › *Add project…*, `~/projects/snip`, confirm: an empty board. | Zoom on the dialog, then out. |
| 1.2 | Just type what you want, and send it off. | A new task: the plan is typed into the message box; Ctrl-Enter. | Zoom on the message box. |
| 1.3 | Under the hood, TPS uses Claude Code, and will allow you to log in with your subscription. | The localhost page: its *Claude account* row, *Sign in to claude*. | Zoom on the row, the button ringed. |
| 1.4 | Every task gets its own clone of the repository, and runs in a container of its own. So agents are sandboxed. | The chat: the container image is built, the agent's first lines come in. | Whole window. ⏩ |
| 1.5 | Also, an agent gets to change its project's Containerfile, so that it can customize its environment to suit the project. | The tests task's `Containerfile.dev` in VS Code. | Zoom on the file, the line installing pytest ringed. |

## 2. Watching it work

| | Voice | Screen | Camera |
|-|-|-|-|
| 2.1 | On the left, you see the agent doing its thing. On the right is VS Code, running within the task's container. | Tool calls stream down the chat; `snip.py` opens in VS Code. | Labels *agent* and *VS Code* over the two halves. ⏩ |
| 2.2 | Each task has a normal agent conversation, so you can answer its questions, provide feedback, or steer mid-turn. | Typed while the agent works: "Make the keys four characters, base62." The agent answers it. | Zoom on the message box, then out. ⏩ |

## 3. Looking it over

| | Voice | Screen | Camera |
|-|-|-|-|
| 3.1 | When the agent is done, TPS can optionally run the results through a review agent to recursively improve. | A task under review: the reviewer's lines in the chat. | Zoom on the chat. |
| 3.2 | After that, you *may* configure an automatic merge and even deploy, but generally I'll want to look at the output first. | The project's settings: *On review accept* › *Merge it*, *Deploy after every merge*; then the board, the first task in Human. | Zoom on each setting, ringed; then on the card. |
| 3.3 | I can review changes in VS Code, providing feedback to the agent or making some changes of my own. | The first task's VS Code: its source control, `snip.py` open. | Zoom on VS Code. |
| 3.4 | The complete change set shows up as uncommitted in git, so it's easy to evaluate as a whole. | The list of changes. | Zoom on the list. |
| 3.5 | But there is also a branch that contains a commit for every step in the task's conversation. | The chat's *Save point*s: each turn's commit on that branch, its id beside it. | Zoom on the save points, ringed. |
| 3.6 | TPS can run services within the container that can expose ports to your localhost, allowing you to test-drive changes to the web apps. | The task menu, the `app` service started; the pointer on its port button, and the app opens in a VS Code tab. | Whole window, then onto the app in VS Code. |

## 4. Landing it

| | Voice | Screen | Camera |
|-|-|-|-|
| 4.1 | When you're happy, press Merge. The whole task lands as a single commit, rebased on top of the main branch. Any merge conflicts are resolved by the agent. | *Merge…*: the dialog with its commit message. *Merge*; the card goes to Done. | Zoom on the commit message, then out. |
| 4.2 | If you have a test service configured, it will need to pass before merges land. If not, TPS will send the agent back in. | The tests task's chat: its merge failed the test, and the agent is sent in to fix it. | Zoom on that line, ringed. |
| 4.3 | Merges can be queued, and you can reorder them just by dragging. | The board: cards through Review, the merge queue, and into Done. | Whole window. ⏩ |
| 4.4 | And if later on, long after you've merged, something turns out to be wrong with the task's code, you can just jump back into the conversation. Its agent will pick up today's code, while remembering what it did previously. | A merged task, typed: "Names should not care about case: /Docs and /docs are the same link." A *Follow-up* card appears in Agent. | Chip: *a week later*. Zoom on the message, then on the new card. |

## 5. Many things

| | Voice | Screen | Camera |
|-|-|-|-|
| 5.1 | Of course, I generally have many tasks for various different projects open at one time. On the kanban board, you get an overview of *what's happening* in each of them. | The board of every project, snip's agents at work. | Whole window. |
| 5.2 | TPS can run projects on your own laptop if you use Linux, but it can also connect to remote Linux machines over SSH. The agents will keep running there while you suspend your laptop. | *Add host*, typed `robin@buildbox`. | Zoom on the dialog. |

## 6. Shipping

| | Voice | Screen | Camera |
|-|-|-|-|
| 6.1 | Optionally, you can have your agent configure a deployment script. Deployment happens from a separate non-agent container, that can have access to secrets. | The tests task's `Containerfile.dev`. | The `LABEL tps.deploy` line ringed. |
| 6.2 | You can either manually deploy, by clicking up to which *done* task you want to release, or you can configure the project to deploy everything that is merged automatically. | The Done column; the top card pointed at, its rocket clicked and confirmed: the column says it is deploying, then the *Deployed* line moves up to the top. | Zoom on the column. ⏩ |
| 6.3 | If you need versioned releases, TPS is smart about that too, instructing your agents when and how to increment versions and when to add to an unreleased version. You generally only need to click deploy. | The follow-up of 4.4, made after that deploy: its reviewer says 0.1.0 already shipped, so the change goes in a new 0.1.1; its agent, rebasing, moves an entry out of the deployed 0.1.0 into 0.1.1. | Zoom on each, ringed. |

## 7. More

| | Voice | Screen | Camera |
|-|-|-|-|
| 7.1 | TPS has a host of other awesome features that I'll just briefly mention to impress you. | The board of every project. | Whole window. |
| 7.2 | Our containers support nested containers. | A terminal in the scratch task's VS Code: a Dockerfile, and `docker build` running its `RUN echo hello from a nested container`. | Zoom on the terminal. ⏩ |
| 7.3 | You can fork and revert conversations to any point. | In a chat, a *Save point*: the dialog offers to revert to it or fork from it. | Zoom on the save point, then on the dialog. |
| 7.4 | Recurring tasks can be made to instantiate on a schedule. They can do fully autonomous work and deployment, if you want them to. | standup's scheduled task: enabled, 07:30, Monday to Friday, and its prompt. | Zoom on the schedule. |
| 7.5 | There's a Ctrl-L command palette for keyboard navigation. | Ctrl-L, "sta": the palette, then the standup board. | Zoom on the palette. |
| 7.6 | And you can assign tasks OpenRouter budget to access other models, like for audio, images, and video. | standup's text-to-speech task: its OpenRouter budget, and the agent asking for more. | Zoom on the budget, ringed. |

## 8. Bye

| | Voice | Screen | Camera |
|-|-|-|-|
| 8.1 | So yeah, if you want to go ahead and try TPS, that'd be great. | The TPS reports meme from the README. | Over the dimmed board. |
| 8.2 | It's free and open source, and just a single static binary to download and run. | The board of every project. | Dimmed under the robot and `github.com/vanviegen/tps`. |
