# The demo video

`node demo/video/make.mjs` makes `out/tps.mp4` (and its subtitles,
`out/tps.srt`) in three steps, which can be run one at a time by name:

- **voice** speaks each line of `script.md` with a synthetic voice, through
  OpenRouter, unless `voice/` has a recording of your own for it.
- **shoot** starts TPS in a home of its own (`home/`, seeded by
  `demo/seed.sh`), drives the dashboard through `beats.mjs` in Chromium, and
  records the screen to `out/capture.mkv`. It takes as long as the story does:
  ten to twenty minutes.
- **cut** takes each beat's stretch of the recording (or, for a montage,
  slices of other beats'), speeds it up where the beat says so, puts the line
  and the beat's captions on it, and joins the beats in the order of their
  numbers, levelling the soundtrack as a whole; and a 720p copy under 10 MB,
  `out/tps-github.mp4`, which GitHub plays inline once uploaded there.

`script.md` is the script: what is said, in its Voice column, and what is
shown meanwhile. `beats.mjs` is the second half of that in code.

## Agents on tape

The agents in the video are real, but they run on tape. `replay/` is a stand-in
for claude, installed in the shoot's toolbox, which runs the real claude
behind a proxy: every answer of the model it has a tape of comes from
`cassettes/`, at the pace it was recorded, and every other one is asked of the
API and taped. Everything else happens for real: tools run, files change,
containers build, tests run, merges land. A shoot that finds a tape for every
answer comes out the same every time, and costs nothing.

The tapes are filed by task: `cassettes/snip/4/` is the fourth task the story
makes, its agent's and its reviewer's, and `cassettes/host/` holds the task
titles. An agent that took a turn you don't want in the video is re-recorded by
deleting its task's directory, and the whole story by deleting `cassettes/`.

The tapes here were recorded on sonnet. A story recorded afresh (or a task of
it) runs on the model `MODEL` names, which is set as the project's default
before its first task: `MODEL=sonnet node demo/video/make.mjs shoot`. What the
agents do then is whatever they do, so look the video over and re-record what
you don't like.

Steering and timing: a tape belongs to the nth answer of a conversation, so
anything that changes how many answers came before it (a message reaching the
agent at another point of its turn, tasks merging in another order) has the
rest of that conversation asked anew, and taped. The beats send messages at
fixed points of an agent's work for that reason (see `running` in
`beats.mjs`).

## Your own voice

A recording in `voice/`, named after its beat (`voice/1.2.wav`, `.mp3`,
`.m4a`, …), is used instead of the synthetic voice, its rumble filtered
out and its level matched to the rest. Then run `voice` and `cut` again: no new shoot is needed. A
beat whose recording is longer than its footage holds its last frame, and a
shorter one leaves a pause. For lines you change, change `script.md` too: it is
where the subtitles come from.
