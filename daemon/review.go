package daemon

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// The review phase: work an agent reports ready is read over by a second agent
// before it is handed on. That agent is given the change as a diff against the
// commit the task grew from, and everything the user asked for in their own
// words; it answers with "Accept" or with a list of what must change, and may
// fix the small and obvious itself as it goes.
//
// Three settings say what becomes of finished work, one for each moment where
// it can go more than one way: when the agent reports it ready (assign it to
// the user, have it reviewed, merge it), when a review asks for changes (assign
// it, or send it back to the agent, up to so many times), and when a review
// accepts it (assign it, or merge it). Each is the whole answer to its own
// question, so no two of them have to be read together to know what a task
// will do.
//
// It is the task's one agent slot with another mind in it, not a second agent
// beside it: the phase says whose turn it is, kick puts the session the phase
// asks for in the container (see startSession), and its turn ends in
// onTurnEnd like any other. So everything that holds a task together already —
// stopping, budgets, save points, a container that must not be recycled under a
// running agent, a daemon that restarts — holds here without knowing about it.
// What differs is the prompt it is started with and what is made of its answer.

// loopBackWantedL: a review asking for changes may send the work back to the
// agent once more. The loops are counted per thing the user asked for (see
// SendChat), and a task that has spent its budget has no turn left to send.
func (t *Task) loopBackWantedL() bool {
	return t.info.ReviewLoop < reviewLoops(t.info.ReviewLoops) && !t.overBudgetL()
}

// setReviewL records the feedback that is waiting for the user, which the
// dashboard puts in the task's message box for them to send, change or drop.
func (t *Task) setReviewL(text string) {
	if t.info.Review == text {
		return
	}
	t.info.Review = text
	t.p.m.saveL()
	t.pubL("review", nonEmpty(text))
}

// StartReview reviews the work as it stands, whatever the task's round limit
// says: a user who asks for a review by hand (the board's Review column) gets
// one. Its verdict is acted on like any other round's.
func (t *Task) StartReview() error {
	if !t.hasWorkspace() {
		return errors.New("The task has no workspace to review: it is still in Plan, or finished already")
	}
	// A clone of the branch with nothing on top of it — a merged task picked
	// back up, say — is nothing to show a reviewer, and reading an empty diff
	// is not free.
	if changes, err := t.changes(); err == nil && len(changes) == 0 {
		return errors.New("There is nothing to review: this task's workspace holds no work of its own")
	}
	t.stopAgent()
	// Whatever is in the tree is not the reviewer's doing, and becomes a save
	// point of its own before it starts (see mark).
	t.mark("Human", "")
	t.beginReview()
	return nil
}

// beginReview puts the task in the review phase and sends the reviewer in. The
// task's own answers say what becomes of the verdict (see finishReview); asking
// for a review is what this does.
func (t *Task) beginReview() {
	base, err := git(t.repoDir(), "merge-base", "HEAD", "origin/"+t.p.defaultBranch)
	if err != nil {
		t.noteErr("the automated review needs the commit this task grew from, and could not read it", err)
		t.lock()
		t.setPhaseL(PhaseHuman)
		t.unlock()
		return
	}
	said := t.userSaid()
	t.lock()
	t.setReviewL("") // what is about to be said replaces what was said before
	t.setPhaseL(PhaseReview)
	t.unlock()
	prompt := reviewPrompt(base, said)
	// The note carries the whole of what the reviewer is given, so that the
	// one thing in the log with no conversation behind it can still be read in
	// full: opening it is how you see what this verdict was formed on.
	t.note("reading the work over: automated review", reviewSystem+"\n\n"+prompt)
	t.kick(prompt)
}

// finishReview acts on the reviewer's turn: the save point its own fixes need,
// and then the agent again, the user, or the merge.
func (t *Task) finishReview(end TurnEnd) {
	text := strings.TrimSpace(end.Text)
	if why := reviewMissing(end, text); why != "" {
		// Nothing was read over, so nothing is settled. Merging work on the
		// strength of a review that never ran is the one thing this must not
		// do: the task goes to the user, who can send it back in, merge it
		// anyway, or see to what stopped the reviewer.
		t.note("the automated review did not run (" + why + "); the task is yours")
		t.mark("Review", "did not run")
		t.lock()
		t.setPhaseL(PhaseHuman)
		t.unlock()
		return
	}
	accepted := reviewAccepted(text)
	if accepted {
		t.mark("Review", "accepted the work")
	} else {
		t.mark("Review", "asked for changes")
	}
	t.lock()
	if !accepted {
		if t.loopBackWantedL() {
			t.info.ReviewLoop++
			loop, loops := t.info.ReviewLoop, reviewLoops(t.info.ReviewLoops)
			t.setPhaseL(PhaseAgent) // the work is the agent's again, so the kick below sends it in
			t.unlock()
			t.note(fmt.Sprintf("the review asks for changes; sending the agent back in (%d of %d)", loop, loops))
			t.kick(reviewFeedbackPrompt(text))
			return
		}
		t.setReviewL(reviewFeedbackPrompt(text))
		t.note("the review asks for changes; they are waiting in the message box")
		if t.overBudgetL() {
			t.noteBudgetL()
		}
		t.setPhaseL(PhaseHuman)
		t.unlock()
		return
	}
	if t.onAcceptL() == AnswerMerge {
		t.unlock()
		t.note("the review accepts the work; merging")
		_ = t.Merge("")
		return
	}
	t.noteReadyL("the review accepts the work; the task is ready to merge")
	t.setPhaseL(PhaseHuman)
	t.unlock()
}

// reviewMissing says why a turn left no review to act on, and "" when it left one.
func reviewMissing(end TurnEnd, text string) string {
	switch {
	case end.Failed && end.NoLogin: // the failure is what tells it apart from a reviewer quoting one
		return "claude could not sign in on this host"
	case end.Limited:
		return "claude's usage limit stopped it"
	case end.Failed:
		return "the turn failed"
	case text == "":
		return "the reviewer said nothing"
	}
	return ""
}

// userSaid is everything the user themselves said, in order: the description
// the task was handed over with, and every message they sent after it. What
// TPS told the agent in between, and what the agent answered, is not in here —
// the reviewer is told that much in so many words (see reviewPrompt).
func (t *Task) userSaid() []string {
	var said []string
	for _, entry := range t.readChat() {
		raw, ok := entry.V.(json.RawMessage)
		if !ok {
			continue
		}
		var e ChatEntry
		if json.Unmarshal(raw, &e) != nil || e.K != "user" {
			continue
		}
		if text := strings.TrimSpace(e.Text); text != "" {
			said = append(said, text)
		}
	}
	return said
}

// reviewAccepted reads the reviewer's verdict: the single word "Accept" and
// nothing else. Emphasis, a code fence or a full stop around it still count —
// the word is what was asked for, and the shape it arrives in is not worth
// another round of work over.
func reviewAccepted(text string) bool {
	return strings.Trim(strings.ToLower(strings.TrimSpace(text)), " \t\n`*_.!#\"'") == "accept"
}
