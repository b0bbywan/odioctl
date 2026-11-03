package upgrade

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeOdioAPI answers GET /upgrade with its run and streams its events on
// /events in odio-api's framing (server.info "connected" first), then
// hangs up — as odio-api restarting would.
type fakeOdioAPI struct {
	mu      sync.Mutex
	run     string   // GET /upgrade's "run", JSON; "" → 404, no upgrade backend
	events  []string // "type data", one SSE event each
	query   string
	started int
	start   int // POST /upgrade/start's status
}

func (f *fakeOdioAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch r.Method + " " + r.URL.Path {
	case "GET /upgrade":
		if f.run == "" {
			http.NotFound(w, r)
			return
		}
		fmt.Fprintf(w, `{"current":"1","latest":"2","upgrade_available":true,"run":%s}`, f.run)
	case "GET /events":
		f.query = r.URL.RawQuery
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "event: server.info\ndata: \"connected\"\n\n")
		for _, e := range f.events {
			typ, data, _ := strings.Cut(e, " ")
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", typ, data)
		}
	case "POST /upgrade/start":
		f.started++
		w.WriteHeader(f.start)
		fmt.Fprint(w, "upgrade: systemd unit not configured\n")
	default:
		http.NotFound(w, r)
	}
}

func (f *fakeOdioAPI) set(run string, events ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.run, f.events = run, events
}

// recorder collects reports, and says when there are n.
type recorder struct {
	mu  sync.Mutex
	got []Run
}

func (r *recorder) report(run Run) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.got = append(r.got, run)
}

func (r *recorder) waitFor(t *testing.T, n int) []Run {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		r.mu.Lock()
		if len(r.got) >= n {
			got := append([]Run(nil), r.got...)
			r.mu.Unlock()
			return got
		}
		r.mu.Unlock()
		time.Sleep(5 * time.Millisecond)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	t.Fatalf("got %v, want %d reports", r.got, n)
	return nil
}

// fastRetry shortens the retry and the outage a run outlives.
func fastRetry(t *testing.T, stale time.Duration) {
	oldRetry, oldStale := RunRetry, RunStale
	RunRetry, RunStale = 10*time.Millisecond, stale
	t.Cleanup(func() { RunRetry, RunStale = oldRetry, oldStale })
}

// follow runs FollowRun against base until stop or the test's end, and
// waits for it to return: it reads RunRetry, which fastRetry restores.
func follow(t *testing.T, base string, rec *recorder, logf func(string, ...any)) (stop func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		FollowRun(ctx, base, rec.report, logf)
		close(done)
	}()
	stop = func() {
		cancel()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("FollowRun still running after ctx is done")
		}
	}
	t.Cleanup(stop)
	return stop
}

func pct(percent int, step string) *Progress { return &Progress{Percent: percent, Step: step} }

func wantRuns(t *testing.T, got []Run, want ...Run) {
	t.Helper()
	for i, w := range want {
		if i >= len(got) || !got[i].Equal(w) {
			t.Fatalf("reports = %+v, want %+v first", got, want)
		}
	}
}

func TestFollowRunSnapshotThenStream(t *testing.T) {
	fastRetry(t, time.Hour)
	api := &fakeOdioAPI{}
	api.set(`{"state":"running","origin":"systemd","percent":30,"step":"setup"}`,
		`upgrade.progress {"event":"progress","percent":42,"current":3,"step":"mpd"}`,
		`upgrade.progress {"event":"end","success":true}`,                    // the run's state says that
		`upgrade.info {"current":"1","latest":"2","upgrade_available":true}`, // the detector's, not the run
		`upgrade.progress {"event":"begin","total":7}`,
		`upgrade.info {"state":"idle","origin":"systemd","finished_at":"2026-11-03T10:00:00Z"}`,
		`upgrade.progress {"event":"progress","percent":99,"current":7,"step":"late"}`, // no run to move
	)
	srv := httptest.NewServer(api)
	defer srv.Close()
	rec := &recorder{}
	follow(t, srv.URL, rec, quiet)

	got := rec.waitFor(t, 4)
	wantRuns(t, got,
		Run{State: RunRunning, Progress: pct(30, "setup")},
		Run{State: RunRunning, Progress: pct(42, "mpd")},
		Run{State: RunRunning, Progress: pct(0, "")},
		Run{State: RunIdle},
	)
	api.mu.Lock()
	query := api.query
	api.mu.Unlock()
	if query != "types=upgrade.info,upgrade.progress" {
		t.Errorf("query = %q", query)
	}
}

// odio-api restarting (an upgrade of its own) hangs up: the next connection
// picks up from the snapshot, and the run stays what it was meanwhile.
func TestFollowRunReconnects(t *testing.T) {
	fastRetry(t, time.Hour)
	api := &fakeOdioAPI{}
	api.set(`{"state":"running"}`)
	srv := httptest.NewServer(api)
	defer srv.Close()
	rec := &recorder{}
	follow(t, srv.URL, rec, quiet)
	rec.waitFor(t, 1)
	api.set(`{"state":"failed"}`)
	deadline := time.Now().Add(2 * time.Second)
	for {
		got := rec.waitFor(t, 1)
		last := got[len(got)-1]
		if last.State == RunFailed {
			break
		}
		if !last.Known() {
			t.Fatalf("reports = %+v: a short outage made the run unknown", got)
		}
		if time.Now().After(deadline) {
			t.Fatalf("reports = %+v, want failed after reconnecting", got)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// No odio-api, or one without an upgrade backend: one line per outage, and
// the run unknown once it lasted RunStale.
func TestFollowRunOutage(t *testing.T) {
	fastRetry(t, 50*time.Millisecond)
	api := &fakeOdioAPI{} // GET /upgrade is a 404
	srv := httptest.NewServer(api)
	defer srv.Close()

	var mu sync.Mutex
	var logs []string
	logf := func(format string, args ...any) {
		mu.Lock()
		defer mu.Unlock()
		logs = append(logs, fmt.Sprintf(format, args...))
	}
	rec := &recorder{}
	stop := follow(t, srv.URL, rec, logf)
	if got := rec.waitFor(t, 1); got[0].Known() {
		t.Errorf("first report = %+v, want unknown", got[0])
	}
	stop()
	mu.Lock()
	defer mu.Unlock()
	if len(logs) != 1 || !strings.Contains(logs[0], "404") {
		t.Errorf("logs = %q, want one line", logs)
	}
}

func TestStartRun(t *testing.T) {
	api := &fakeOdioAPI{}
	srv := httptest.NewServer(api)
	defer srv.Close()
	ctx := context.Background()

	api.start = http.StatusAccepted
	if err := StartRun(ctx, srv.URL); err != nil {
		t.Errorf("202: %v", err)
	}
	api.start = http.StatusConflict
	if err := StartRun(ctx, srv.URL); !errors.Is(err, ErrRunning) {
		t.Errorf("409: %v, want ErrRunning", err)
	}
	api.start = http.StatusNotFound
	if err := StartRun(ctx, srv.URL); err == nil || !strings.Contains(err.Error(), "systemd unit not configured") {
		t.Errorf("404: %v, want odio-api's word", err)
	}
	if api.started != 3 {
		t.Errorf("started = %d", api.started)
	}
}
