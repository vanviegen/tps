package daemon

import (
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestContainerfileServices(t *testing.T) {
	cf := "FROM a\nCMD [\"old\"]\nLABEL tps.service.gone=\"x\"\nFROM b\n# CMD nope\nRUN echo CMD\n  cmd npm \\\nrun dev\n" +
		"LABEL org.opencontainers.image.title=\"TPS\" tps.service.test=\"npm test -- --ci\" tps.service.review=storybook\n" +
		"LABEL tps.service.bad/name=\"x\" tps.service.empty=\"\"\n"
	got := containerfileServices(cf)
	want := []declaredService{{"app", "npm run dev"}, {"test", "npm test -- --ci"}, {"review", "storybook"}}
	if !slices.Equal(got, want) {
		t.Errorf("declared: %v", got)
	}
	if got := containerfileServices("FROM x\nCMD [\"node\", \"a b.js\"]\nLABEL tps.service.app=\"make run\"\n"); !slices.Equal(got, []declaredService{{"app", "make run"}}) {
		t.Errorf("label overrides CMD: %v", got)
	}
	if got := containerfileServices("FROM x\nCMD [\"node\", \"a b.js\", \"it's\"]\n"); !slices.Equal(got, []declaredService{{"app", `node 'a b.js' 'it'\''s'`}}) {
		t.Errorf("exec form: %v", got)
	}
	if _, caches := containerfileDeclarations("FROM x\nLABEL tps.cache=\"/a/b, /c\" tps.service.t=x\nLABEL tps.cache=\"rel /d\"\n"); !slices.Equal(caches, []string{"/a/b", "/c", "/d"}) {
		t.Errorf("caches: %v", caches)
	}
	if _, caches := containerfileDeclarations("FROM x\nLABEL tps.cache=\"/a /b\"\nFROM y\n"); caches != nil {
		t.Errorf("caches of an earlier stage: %v", caches)
	}
	if containerfileServices(defaultContainerfile) != nil {
		t.Error("no CMD")
	}
}

func TestParseLabels(t *testing.T) {
	got := parseLabels(`a=1 "b c"="two \"quoted\" words" d=un\ quoted e=`)
	want := [][2]string{{"a", "1"}, {"b c", `two "quoted" words`}, {"d", "un quoted"}, {"e", ""}}
	if !slices.Equal(got, want) {
		t.Errorf("labels: %q", got)
	}
	if got := parseLabels("key value"); len(got) != 0 {
		t.Errorf("old form: %q", got)
	}
}

func TestReadServices(t *testing.T) {
	dir := t.TempDir()
	write := func(name, file, content string) {
		_ = os.MkdirAll(filepath.Join(dir, name), 0o755)
		if err := os.WriteFile(filepath.Join(dir, name, file), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("test", "cmd", "npm test\n")
	write("test", "pid", "123\n")
	write("test", "exit", "1\n")
	write("web", "cmd", "serve\n")
	write("web", "pid", "124\n")
	write("old", "cmd", "x\n")
	write("old", "pid", "1\n")
	write("old", "exit", "143\n")
	write("old", "stopped", "")
	write(".declared", "app", "npm run dev\n")
	declared := []declaredService{{"app", "npm run dev"}, {"web", "serve --declared"}}
	got := readServices(dir, declared)
	if len(got) != 4 || got[0].Name != "app" || got[0].Status != "idle" || got[0].Cmd != "npm run dev" || !got[0].Declared {
		t.Errorf("declared idle: %+v", got)
	}
	if got[1].Name != "web" || got[1].Status != "running" || got[1].Cmd != "serve" || got[1].Started == 0 {
		t.Errorf("declared running keeps the command it ran with: %+v", got[1])
	}
	if got[2].Name != "old" || got[2].Status != "stopped" || *got[2].Code != 143 || got[2].Declared {
		t.Errorf("stopped: %+v", got[2])
	}
	if got[3].Name != "test" || got[3].Status != "exited" || *got[3].Code != 1 || got[3].Ended == 0 {
		t.Errorf("exited: %+v", got[3])
	}
	write("test", "log", "line1\nline2\n")
	if tail := serviceLogTail(dir, "test"); tail != "line1\nline2\n" {
		t.Errorf("tail: %q", tail)
	}
	if tail := serviceLogTail(dir, "app"); tail != "" {
		t.Errorf("no log: %q", tail)
	}
	writeDeclared(dir, declared)
	if readFile(filepath.Join(dir, ".declared", "web")) != "serve --declared\n" || exists(filepath.Join(dir, ".declared", "test")) {
		t.Error("declared files")
	}
	clearServices(dir)
	if entries, _ := os.ReadDir(dir); len(entries) != 1 || entries[0].Name() != ".declared" {
		t.Errorf("clear keeps the declarations only: %v", entries)
	}
}

func TestProbePort(t *testing.T) {
	// A listener that holds connections open: something listens, no HTTP.
	hold, _ := net.Listen("tcp", "127.0.0.1:0")
	defer hold.Close()
	go func() {
		for {
			c, err := hold.Accept()
			if err != nil {
				return
			}
			defer c.Close()
		}
	}()
	if p := probePort(hold.Addr().(*net.TCPAddr).Port); !p.Open || p.HTTP {
		t.Errorf("held open: %+v", p)
	}
	// Podman's forwarder with nothing behind it: accepts, then closes at once.
	shut, _ := net.Listen("tcp", "127.0.0.1:0")
	defer shut.Close()
	go func() {
		for {
			c, err := shut.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	if p := probePort(shut.Addr().(*net.TCPAddr).Port); p.Open {
		t.Errorf("closed at once: %+v", p)
	}
	web := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(404) }))
	defer web.Close()
	if p := probePort(web.Listener.Addr().(*net.TCPAddr).Port); !p.Open || !p.HTTP {
		t.Errorf("http: %+v", p)
	}
	closed, _ := net.Listen("tcp", "127.0.0.1:0")
	port := closed.Addr().(*net.TCPAddr).Port
	closed.Close()
	if p := probePort(port); p.Open {
		t.Errorf("nothing there: %+v", p)
	}
}
