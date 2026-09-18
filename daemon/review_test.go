package daemon

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// The verdict is one word, however the reviewer dressed it up; anything else
// is a list of changes to make.
func TestReviewAccepted(t *testing.T) {
	for _, text := range []string{"Accept", "accept", " Accept.\n", "**Accept**", "`Accept`"} {
		if !reviewAccepted(text) {
			t.Errorf("%q should be an acceptance", text)
		}
	}
	for _, text := range []string{"", "Accept, but the naming is off", "- the naming is off", "Accepted"} {
		if reviewAccepted(text) {
			t.Errorf("%q should not be an acceptance", text)
		}
	}
}

// The reviewer is given the commit to review against, the proposed message,
// and the user's own words with the agent's turns between them named as gaps.
func TestReviewPrompt(t *testing.T) {
	prompt := reviewPrompt("abc123", "Tidy the log", []string{"make it tidy", "and quick, too"})
	for _, want := range []string{"git diff abc123", "Tidy the log", "make it tidy", "and quick, too", "then the agent worked"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("the prompt should mention %q:\n%s", want, prompt)
		}
	}
	if strings.Count(prompt, "then the agent worked") != 1 { // one gap between two messages
		t.Errorf("one gap per message after the first:\n%s", prompt)
	}
	if lone := reviewPrompt("abc123", "Tidy the log", nil); !strings.Contains(lone, "nothing was written down") {
		t.Errorf("a task nobody wrote for should say so:\n%s", lone)
	}
}

// What the user asked for is read back out of the chat log, in order, and
// nothing the agent or TPS said comes with it.
func TestUserSaid(t *testing.T) {
	task, _ := testTask(t)
	if err := os.MkdirAll(task.dir(), 0o755); err != nil {
		t.Fatal(err)
	}
	first := newEntry("user")
	first.Text = "make it tidy"
	task.appendChat(first)
	task.note("the agent's turn ended")
	said := newEntry("text")
	said.Text = "done, I think"
	task.appendChat(said)
	second := newEntry("user")
	second.Text = "and quick, too"
	task.appendChat(second)

	if got := task.userSaid(); len(got) != 2 || got[0] != "make it tidy" || got[1] != "and quick, too" {
		t.Errorf("the user's own messages, in order: %q", got)
	}
}

// The loops are a budget: spent, the review's feedback goes to the user, and a
// task with no turn left to pay for sends nothing back either.
func TestLoopBackWanted(t *testing.T) {
	task, _ := testTask(t)
	if !task.loopBackWantedL() {
		t.Error("a task that says nothing sends the work back once")
	}
	none := 0
	task.info.ReviewLoops = &none
	if task.loopBackWantedL() {
		t.Error("a task that sends nothing back should not")
	}
	two := 2
	task.info.ReviewLoops = &two
	if !task.loopBackWantedL() {
		t.Error("the first loop back should run")
	}
	task.info.ReviewLoop = 2
	if task.loopBackWantedL() {
		t.Error("the loops are used up")
	}
	task.info.ReviewLoop = 0
	budget := 1.0
	task.info.Budget, task.info.Spent = &budget, 1.0
	if task.loopBackWantedL() {
		t.Error("a task out of budget has no turn to send the work back to")
	}
}

// A task says what becomes of finished work, and says nothing until it does.
func TestAnswers(t *testing.T) {
	task, _ := testTask(t)
	if task.onReadyL() != AnswerReview || task.onAcceptL() != AnswerHuman {
		t.Error("a task that says nothing has its work read over and then waits for its user")
	}
	task.applyL(map[string]any{"onReady": "merge", "onAccept": "merge", "reviewLoops": "2"})
	if task.onReadyL() != AnswerMerge || task.onAcceptL() != AnswerMerge || reviewLoops(task.info.ReviewLoops) != 2 {
		t.Errorf("the answers a dashboard sends: %+v", task.info)
	}
	task.applyL(map[string]any{"onReady": "review"})
	// Answers a question does not offer, and things that are not answers at
	// all, leave what was set alone.
	task.applyL(map[string]any{"onReady": "nonsense", "onAccept": "review"})
	if task.onReadyL() != AnswerReview || task.onAcceptL() != AnswerMerge {
		t.Errorf("only what a question offers may answer it: %+v", task.info)
	}
}

// A loop-back limit arrives as a number or as the text of one, and only a
// count is an answer at all.
func TestParseLoops(t *testing.T) {
	cases := map[any]int{3.0: 3, "2": 2, "-1": 0, 99.0: maxReviewLoops}
	for raw, want := range cases {
		if got, ok := parseLoops(raw); !ok || got != want {
			t.Errorf("parseLoops(%v) = %d, %v, want %d", raw, got, ok, want)
		}
	}
	for _, raw := range []any{"", "nonsense", nil, true} {
		if got, ok := parseLoops(raw); ok {
			t.Errorf("parseLoops(%v) = %d, %v, want no answer", raw, got, ok)
		}
	}
}

// A verdict of Accept hands the work on as the agent left it; one asking for
// changes with no rounds left puts the feedback in the user's hands instead.
// Either way the phase ends on a save point.
func TestFinishReview(t *testing.T) {
	task, _ := testTask(t)
	if err := task.ensureWorkspace(); err != nil {
		t.Fatal(err)
	}
	task.info.Phase, task.info.CommitMessage = PhaseReview, "Tidy the log"
	none := 0
	task.info.ReviewLoops = &none // no round to send the work back in

	task.finishReview(TurnEnd{Text: "- the naming is off"})
	if task.info.Phase != PhaseHuman {
		t.Errorf("the last round's feedback is the user's: %s", task.info.Phase)
	}
	// Kept for the message box, under a line saying it is a machine's reading
	// rather than the user's own word (see reviewFeedbackPrompt).
	if !strings.Contains(task.info.Review, "- the naming is off") || !strings.Contains(task.info.Review, "automated review") {
		t.Errorf("the feedback should be kept for the message box, marked as a review's: %q", task.info.Review)
	}
	if !strings.Contains(task.lastMark(t), "Review") {
		t.Error("the review phase should end on a save point of its own")
	}

	task.info.Phase, task.info.Review = PhaseReview, ""
	task.finishReview(TurnEnd{Text: "Accept"})
	if task.info.Phase != PhaseHuman || task.info.Review != "" {
		t.Errorf("accepted work waits for the merge, with nothing to answer: %s %q", task.info.Phase, task.info.Review)
	}

	// A review that could not run has settled nothing, so it must not stand in
	// for one that accepted: even a task that merges by itself waits for the user.
	task.info.OnAccept, task.info.Phase = AnswerMerge, PhaseReview
	task.finishReview(TurnEnd{Failed: true})
	if task.info.Phase != PhaseHuman {
		t.Errorf("a review that did not run leaves the task with the user: %s", task.info.Phase)
	}
	if !strings.Contains(task.lastMark(t), "did not run") {
		t.Errorf("the save point should say the review did not run: %q", task.lastMark(t))
	}
}

// lastMark is the message of the newest save point in the task's chat log.
func (t *Task) lastMark(tb *testing.T) string {
	tb.Helper()
	last := ""
	for _, entry := range t.readChat() {
		raw, ok := entry.V.(json.RawMessage)
		if !ok {
			continue
		}
		var e ChatEntry
		if json.Unmarshal(raw, &e) == nil && e.K == "mark" {
			last = e.Text
		}
	}
	return last
}

// There is nothing to review in a task that has no work of its own: one that
// is merged or closed (its workspace gone), and one picked back up into a
// clean clone of the branch. It says so and stays where it is, rather than
// paying a reviewer to find an empty diff.
func TestReviewNeedsWork(t *testing.T) {
	task, _ := testTask(t)
	task.info.Phase = PhaseDone
	if err := task.MoveTo(PhaseReview); err == nil {
		t.Error("a finished task has no workspace to review")
	}
	if task.info.Phase != PhaseDone {
		t.Errorf("a refused review should leave the task where it is: %s", task.info.Phase)
	}

	if err := task.ensureWorkspace(); err != nil { // as picking it up again gives it
		t.Fatal(err)
	}
	task.info.Phase = PhaseHuman
	if err := task.StartReview(); err == nil {
		t.Error("a clone of the branch with nothing on top of it is nothing to review")
	}
	if task.info.Phase != PhaseHuman {
		t.Errorf("a refused review should leave the task where it is: %s", task.info.Phase)
	}
}
