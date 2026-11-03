package daemon

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Scheduled tasks: a task that stays in Plan as a template, and at its time of
// day, on the days it is set to, makes a task of its own — its title with the
// date, its settings, its prompt sent to the agent. Nobody is at the controls
// of such a run: its agent is told so (see scheduledPrompt), a question it puts
// to a human is refused (see syncAsksL), and a budget it runs out of is
// stretched a few times for it to wrap up in, before the run is parked like
// any other task (see overrunL). A turn ending with the work not ready sends
// the agent back in rather than handing the run to the user, unless the agent
// asks for a person in so many words (see humanRequiredFile).

// Schedule is when a scheduled task runs.
type Schedule struct {
	Enabled  bool    `json:"enabled"`
	Time     string  `json:"time"`               // "15:04", the host's local time
	Days     [7]bool `json:"days"`               // by time.Weekday, Sunday first
	Parallel bool    `json:"parallel,omitempty"` // a run starts even while the previous one is still at work
	LastRun  string  `json:"lastRun,omitempty"`  // the day it last came up, as time.DateOnly
}

var errScheduled = errors.New("A scheduled task stays in Plan: each of its runs is a task of its own")

var timeOfDayRe = regexp.MustCompile(`^([01]\d|2[0-3]):[0-5]\d$`)

// newSchedule is what a scheduled task starts out with: every day at nine.
func newSchedule() *Schedule {
	return &Schedule{Enabled: true, Time: "09:00", Days: [7]bool{true, true, true, true, true, true, true}}
}

func (s *Schedule) passed(now time.Time) bool { return now.Format("15:04") >= s.Time }

// due: the schedule's time has come today, and it has not come up yet today.
// A run the daemon was not there for is made up for later that day.
func (s *Schedule) due(now time.Time) bool {
	return s.Enabled && s.Days[now.Weekday()] && s.passed(now) && s.LastRun != now.Format(time.DateOnly)
}

// next is when the schedule comes up next: a time already past when it is due
// now, the zero time when it never comes up.
func (s *Schedule) next(now time.Time) time.Time {
	if !s.Enabled {
		return time.Time{}
	}
	hm, _ := time.Parse("15:04", s.Time)
	for d := range 8 {
		day := now.AddDate(0, 0, d)
		if s.Days[day.Weekday()] && (d > 0 || s.LastRun != day.Format(time.DateOnly)) {
			return time.Date(day.Year(), day.Month(), day.Day(), hm.Hour(), hm.Minute(), 0, 0, now.Location())
		}
	}
	return time.Time{}
}

// apply sets whatever the dashboard's partial names. A schedule takes effect
// from the next time it comes up: one set to a time already past today first
// runs tomorrow.
func (s *Schedule) apply(raw map[string]any) {
	if v, ok := raw["enabled"].(bool); ok {
		s.Enabled = v
	}
	if v, ok := raw["time"].(string); ok && timeOfDayRe.MatchString(v) {
		s.Time = v
	}
	if v, ok := raw["days"].([]any); ok && len(v) == len(s.Days) {
		for i, d := range v {
			s.Days[i], _ = d.(bool)
		}
	}
	if v, ok := raw["parallel"].(bool); ok {
		s.Parallel = v
	}
	if now := time.Now(); s.passed(now) {
		s.LastRun = now.Format(time.DateOnly)
	}
}

// runSchedulesL starts the runs whose time has come. One that comes up while
// its previous run is still at work is skipped for the day, unless it is set
// to run beside it.
func (p *Project) runSchedulesL(now time.Time) {
	for _, t := range p.tasks {
		s := t.info.Schedule
		if s == nil || !s.due(now) {
			continue
		}
		s.LastRun = now.Format(time.DateOnly)
		p.m.saveL()
		_ = os.MkdirAll(t.dir(), 0o755) // for its log, a scheduled task never having a workspace
		switch {
		case strings.TrimSpace(t.info.Description) == "":
			t.note("Today's run was skipped: the prompt is empty")
		case !s.Parallel && p.runningL(t.tid):
			t.note("Today's run was skipped: the previous one is still at work")
		default:
			run := p.addTaskL(&TaskInfo{
				Title: t.info.Title + " " + s.LastRun, TitleAsked: true, Description: t.info.Description, Scheduled: t.tid,
				Model: t.info.Model, ReviewModel: t.info.ReviewModel, OnReady: t.info.OnReady, OnAccept: t.info.OnAccept,
				ReviewLoops: copyPtr(t.info.ReviewLoops), Budget: copyPtr(t.info.Budget), ORBudget: copyPtr(t.info.ORBudget),
				Phase: PhasePlan, PhaseAt: now.UnixMilli(),
			})
			t.note("Started today's run: " + run.info.Title)
			go func() {
				if err := run.Assign("agent", ""); err != nil {
					logf("%s: scheduled run: %v", run.key(), err)
				}
			}()
		}
	}
}

// runningL: a run of the scheduled task is still at work.
func (p *Project) runningL(tid string) bool {
	for _, t := range p.tasks {
		if t.info.Scheduled == tid && t.agentPhaseL() {
			return true
		}
	}
	return false
}

// humanRequiredFile is how a scheduled run's agent asks for a person: a file
// it makes in the workspace, which is taken away once the run is handed over.
// Anything less deliberate — a question, a turn that simply stops — is sent
// back to it.
const humanRequiredFile = ".tps-human-required"

// humanRequired: the agent of a scheduled run asks for a person.
func (t *Task) humanRequired() bool {
	return t.info.Scheduled != "" && exists(filepath.Join(t.repoDir(), humanRequiredFile))
}

// maxReadyNudges: how often in a row a scheduled run's agent is sent back in
// for ending its turn with the work not ready, before the run goes to the user
// anyway, as one stuck in a loop.
const maxReadyNudges = 3

// readyNudgeL says whether to send a scheduled run's agent back in for ending
// its turn with the work not ready, and counts it.
func (t *Task) readyNudgeL() bool {
	if t.info.Scheduled == "" || t.readyNudges >= maxReadyNudges {
		return false
	}
	t.readyNudges++
	t.note(fmt.Sprintf("The agent's turn ended with the work not ready, and nobody watches this run; sending it back in (%d of %d)", t.readyNudges, maxReadyNudges))
	return true
}

// A scheduled run that reaches its budget has nobody to raise it: it gets
// overrunStep more to wrap up in, up to maxOverruns times.
const (
	overrunStep = 2.0
	maxOverruns = 5
)

func (t *Task) canOverrunL() bool { return t.info.Scheduled != "" && t.info.Overruns < maxOverruns }

// overrunL raises the budget of a scheduled run that reached it, and reports
// whether it did. The agent hears why with its next turn (see queueL).
func (t *Task) overrunL() bool {
	if !t.canOverrunL() {
		return false
	}
	t.info.Overruns++
	budget := *t.info.Budget + overrunStep
	t.info.Budget = &budget
	t.note(fmt.Sprintf("The task budget is used up (spent $%.2f); with nobody to raise it, the agent gets $%g more to wrap up in (%d of %d)",
		t.info.Spent, overrunStep, t.info.Overruns, maxOverruns))
	t.queueL("budget", wrapUpPrompt)
	t.publishL()
	return true
}
