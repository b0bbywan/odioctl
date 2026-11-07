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
	run     string   // GET /upgrade's "run", JSON; "" → no upgrade backend (GET /server says so)
	then    string   // GET /upgrade's run from its second call on, when set
	gets    int      // GET /upgrade calls
	events  []string // "type data", one SSE event each
	query   string
	started int
	start   int  // POST /upgrade/start's status
	noStart bool // can_upgrade false: no upgrade unit
}

func (f *fakeOdioAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch r.Method + " " + r.URL.Path {
	case "GET /server":
		fmt.Fprintf(w, `{"hostname":"odio","backends":{"systemd":true,"upgrade":%t}}`, f.run != "")
	case "GET /upgrade":
		if f.run == "" {
			http.NotFound(w, r)
			return
		}
		f.gets++
		run := f.run
		if f.then != "" && f.gets > 1 {
			run = f.then
		}
		fmt.Fprintf(w, `{"current":"1","latest":"2","upgrade_available":true,"can_upgrade":%t,"run":%s}`, !f.noStart, run)
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
	oldRetry, oldStale, oldMax := RunRetry, RunStale, RunRetryMax
	RunRetry, RunStale, RunRetryMax = 10*time.Millisecond, stale, 40*time.Millisecond
	t.Cleanup(func() { RunRetry, RunStale, RunRetryMax = oldRetry, oldStale, oldMax })
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
		Run{State: RunRunning, Progress: pct(30, "setup"), CanStart: true},
		Run{State: RunRunning, Progress: pct(42, "mpd"), CanStart: true},
		Run{State: RunRunning, Progress: pct(0, ""), CanStart: true},
		Run{State: RunIdle, CanStart: true},
	)
	api.mu.Lock()
	query := api.query
	api.mu.Unlock()
	if query != "types=upgrade.info,upgrade.progress" {
		t.Errorf("query = %q", query)
	}
}

// A failed end line says what failed: before the unit's end (a unit's run),
// or after odio-api closed the run on it (a shell's).
func TestFollowRunFailure(t *testing.T) {
	end := `upgrade.progress {"event":"end","success":false,"changed":1,"step":"mpd","error":"Install mpd: no package\nthe whole stderr"}`
	failed := `upgrade.info {"state":"failed","origin":"systemd"}`
	for name, events := range map[string][]string{
		"unit":  {end, failed},
		"shell": {failed, end},
	} {
		t.Run(name, func(t *testing.T) {
			fastRetry(t, time.Hour)
			api := &fakeOdioAPI{}
			api.set(`{"state":"running"}`, events...)
			srv := httptest.NewServer(api)
			defer srv.Close()
			rec := &recorder{}
			follow(t, srv.URL, rec, quiet)
			got := rec.waitFor(t, 3)[2]
			want := Failure{Step: "mpd", Error: "Install mpd: no package"}
			if got.State != RunFailed || got.Failure == nil || *got.Failure != want {
				t.Fatalf("third report = %+v (failure %+v), want failed with %+v", got, got.Failure, want)
			}
		})
	}
}

// can_upgrade is GET /upgrade's alone: the run's events keep it.
func TestFollowRunCannotStart(t *testing.T) {
	fastRetry(t, time.Hour)
	api := &fakeOdioAPI{noStart: true}
	api.set(`{"state":"idle"}`, `upgrade.info {"state":"running","origin":"socket"}`)
	srv := httptest.NewServer(api)
	defer srv.Close()
	rec := &recorder{}
	follow(t, srv.URL, rec, quiet)
	wantRuns(t, rec.waitFor(t, 2), Run{State: RunIdle}, Run{State: RunRunning})
}

// odio-api restarting (an upgrade of its own) hangs up: the next connection
// picks up from the snapshot, and the run stays what it was meanwhile.
// A keepalive mid-run is 30s without a word: GET /upgrade again, so a
// missed end cannot pin the run. Idle, a keepalive asks nothing.
func TestFollowRunRechecksAtKeepalive(t *testing.T) {
	fastRetry(t, time.Hour)
	RunRetry = time.Hour // only the keepalive can correct it, not a reconnect
	api := &fakeOdioAPI{then: `{"state":"idle","origin":"systemd"}`}
	api.set(`{"state":"running","origin":"systemd"}`,
		`server.info "love"`, // the end was missed: idle now
		`server.info "love"`, // idle: nothing to ask
	)
	srv := httptest.NewServer(api)
	defer srv.Close()
	rec := &recorder{}
	stop := follow(t, srv.URL, rec, quiet)
	wantRuns(t, rec.waitFor(t, 2),
		Run{State: RunRunning, CanStart: true},
		Run{State: RunIdle, CanStart: true},
	)
	stop()
	api.mu.Lock()
	defer api.mu.Unlock()
	if api.gets != 2 {
		t.Errorf("GET /upgrade calls = %d, want the snapshot and one recheck", api.gets)
	}
}

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

// downURL is an address nothing listens on: odio-api away.
func downURL(t *testing.T) string {
	srv := httptest.NewServer(http.NotFoundHandler())
	srv.Close()
	return srv.URL
}

// No odio-api: one line per outage, and the run unknown.
func TestFollowRunOutage(t *testing.T) {
	fastRetry(t, 50*time.Millisecond)
	base := downURL(t)

	var mu sync.Mutex
	var logs []string
	logf := func(format string, args ...any) {
		mu.Lock()
		defer mu.Unlock()
		logs = append(logs, fmt.Sprintf(format, args...))
	}
	rec := &recorder{}
	stop := follow(t, base, rec, logf)
	if got := rec.waitFor(t, 3); got[0].Known() || got[0].Off {
		t.Errorf("first report = %+v, want unknown", got[0])
	}
	stop()
	mu.Lock()
	defer mu.Unlock()
	if len(logs) != 1 || !strings.Contains(logs[0], "refused") {
		t.Errorf("logs = %q, want one line", logs)
	}
}

// Never heard, there is no run to keep: unknown at once, not after RunStale.
func TestFollowRunNeverHeard(t *testing.T) {
	fastRetry(t, time.Hour)
	rec := &recorder{}
	follow(t, downURL(t), rec, quiet)
	if got := rec.waitFor(t, 1); got[0].Known() {
		t.Errorf("first report = %+v, want unknown", got[0])
	}
}

// GET /server says the upgrade backend is off: said so at once, and nothing
// subscribed to.
func TestFollowRunNoUpgradeBackend(t *testing.T) {
	fastRetry(t, time.Hour)
	api := &fakeOdioAPI{} // run "": backends.upgrade false
	srv := httptest.NewServer(api)
	defer srv.Close()
	rec := &recorder{}
	follow(t, srv.URL, rec, quiet)
	if got := rec.waitFor(t, 1); got[0].Known() || !got[0].Off {
		t.Errorf("first report = %+v, want off", got[0])
	}
	api.mu.Lock()
	defer api.mu.Unlock()
	if api.query != "" {
		t.Errorf("subscribed to /events?%s with no upgrade backend", api.query)
	}
}

func TestRetryAfter(t *testing.T) {
	oldRetry, oldStale, oldMax := RunRetry, RunStale, RunRetryMax
	RunRetry, RunStale, RunRetryMax = 2*time.Second, 15*time.Second, time.Minute
	t.Cleanup(func() { RunRetry, RunStale, RunRetryMax = oldRetry, oldStale, oldMax })
	for _, c := range []struct {
		last, away time.Duration
		off        bool
		want       time.Duration
	}{
		{0, 0, false, 2 * time.Second},                              // odio-api restarting
		{2 * time.Second, 10 * time.Second, false, 2 * time.Second}, // still young
		{2 * time.Second, 16 * time.Second, false, 4 * time.Second}, // past RunStale: doubling
		{32 * time.Second, time.Minute, false, time.Minute},         // capped
		{time.Minute, time.Hour, false, time.Minute},
		{0, 0, true, 2 * time.Second}, // backend off: doubling from the start
		{2 * time.Second, 2 * time.Second, true, 4 * time.Second},
	} {
		if got := retryAfter(c.last, c.away, c.off); got != c.want {
			t.Errorf("retryAfter(%s, %s, %v) = %s, want %s", c.last, c.away, c.off, got, c.want)
		}
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
