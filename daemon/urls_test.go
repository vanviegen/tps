package daemon

import (
	"testing"
	"time"
)

// set-url leaves a URL for the daemon to read, and one without a URL takes
// it away. One set while nobody has the task open flags it until someone does;
// those a restarted daemon finds are not news.
func TestSetURL(t *testing.T) {
	task, _ := testTask(t)
	st := &guestTool{root: task.servicesDir()}
	for _, args := range [][]string{{"x", "javascript:alert(1)"}, {"no/id", "http://localhost:3000/"}, {}} {
		if st.setURL(args) == nil {
			t.Errorf("set-url %q went through", args)
		}
	}
	if err := st.setURL([]string{"review", "http://localhost:3000/"}); err != nil {
		t.Fatal(err)
	}
	task.lock()
	defer task.unlock()
	task.syncURLsL()
	if len(task.urls) != 1 || task.urls[0].ID != "review" || task.urls[0].URL != "http://localhost:3000/" || task.urlsNew {
		t.Fatalf("found on the first read: %+v, new %v", task.urls, task.urlsNew)
	}
	time.Sleep(10 * time.Millisecond) // a new mtime
	if err := st.setURL([]string{"review", "http://localhost:3000/other"}); err != nil {
		t.Fatal(err)
	}
	task.syncURLsL()
	if !task.urlsNew {
		t.Error("a URL set while nobody looks should flag the task")
	}
	task.seenURLsL()
	if err := st.setURL([]string{"review"}); err != nil {
		t.Fatal(err)
	}
	task.syncURLsL()
	if task.urls != nil || task.urlsNew {
		t.Errorf("after removing: %+v, new %v", task.urls, task.urlsNew)
	}
	if err := st.setURL([]string{"review"}); err != nil {
		t.Errorf("removing what is not there: %v", err)
	}
}
