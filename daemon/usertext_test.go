package daemon

import "testing"

// show-user-text leaves a text for the daemon to read, the newest replacing
// the last, until withdrawn or the container goes.
func TestShowUserText(t *testing.T) {
	task, _ := testTask(t)
	st := &guestTool{root: task.servicesDir()}
	resetServices(st.root) // makes the dir, as the container's mount is there
	text := func() string {
		s, _ := task.p.m.hub.Get("projects", task.p.pid, "tasks", task.tid, "userText").(string)
		return s
	}
	task.lock()
	defer task.unlock()
	for _, step := range []struct {
		args []string
		want string
	}{
		{[]string{"Look at", "*this*"}, "Look at *this*"},
		{[]string{"That"}, "That"},
		{nil, ""},
	} {
		if err := st.showUserText(step.args); err != nil {
			t.Fatal(err)
		}
		task.syncUserTextL()
		if got := text(); got != step.want {
			t.Errorf("after %q: %q", step.args, got)
		}
	}
	if err := st.showUserText([]string{"Gone soon"}); err != nil {
		t.Fatal(err)
	}
	resetServices(task.servicesDir())
	task.syncUserTextL()
	if got := text(); got != "" {
		t.Errorf("after a reset: %q", got)
	}
}
