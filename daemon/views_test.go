package daemon

import (
	"testing"
	"time"
)

// request-human-views leaves a port and path for the daemon to read, the
// newest replacing the last, until withdrawn. One made while nobody has the
// task open flags it until someone does; the one a restarted daemon finds is
// not news.
func TestRequestViews(t *testing.T) {
	task, _ := testTask(t)
	st := &guestTool{root: task.servicesDir()}
	resetServices(st.root) // makes the dir, as the container's mount is there
	for _, args := range [][]string{{"x"}, {"0"}, {"70000"}, {"1", "/", "x"}} {
		if st.requestViews(args) == nil {
			t.Errorf("request-human-views %q went through", args)
		}
	}
	if err := st.requestViews([]string{"3847"}); err != nil {
		t.Fatal(err)
	}
	task.lock()
	defer task.unlock()
	task.syncViewL()
	if v := task.view; v == nil || v.Port != 3847 || v.Path != "/" || task.viewNew {
		t.Fatalf("found on the first read: %+v, new %v", v, task.viewNew)
	}
	time.Sleep(10 * time.Millisecond) // a new mtime
	if err := st.requestViews([]string{"3000", "report.html"}); err != nil {
		t.Fatal(err)
	}
	task.syncViewL()
	if v := task.view; v.Port != 3000 || v.Path != "/report.html" || !task.viewNew {
		t.Errorf("a request made while nobody looks: %+v, new %v", v, task.viewNew)
	}
	if err := st.requestViews(nil); err != nil {
		t.Fatal(err)
	}
	task.syncViewL()
	if task.view != nil || task.viewNew {
		t.Errorf("after withdrawing it: %+v, new %v", task.view, task.viewNew)
	}
	if err := st.requestViews([]string{"3000"}); err != nil {
		t.Fatal(err)
	}
	resetServices(task.servicesDir())
	task.syncViewL()
	if task.view != nil || task.viewNew {
		t.Errorf("after a reset: %+v, new %v", task.view, task.viewNew)
	}
}
