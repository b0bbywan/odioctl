package upgrade

// odio-api's upgrade backend, as odioctl web drives and follows it. odio-api
// starts odio-upgrade.service (POST /upgrade/start), tracks it whoever started
// it, and relays the progress odios' odio_progress callback streams to its
// socket (`apply --progress`): its run is the upgrade card's.

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// A run's states, odio-api's words.
const (
	RunIdle    = "idle"    // none yet, or the last one succeeded
	RunRunning = "running" // the unit is activating
	RunFailed  = "failed"  // the last one failed, until the next starts
)

// Progress is a percent of the run and the step it is in (an odios role).
type Progress struct {
	Percent int
	Step    string
}

// Run is odio-api's run. State is "" while odio-api is not heard from.
type Run struct {
	State    string
	Progress *Progress // while running, once a percent came
}

func (r Run) Known() bool   { return r.State != "" }
func (r Run) Running() bool { return r.State == RunRunning }

func (r Run) Equal(o Run) bool {
	if r.State != o.State || (r.Progress == nil) != (o.Progress == nil) {
		return false
	}
	return r.Progress == nil || *r.Progress == *o.Progress
}

// RunRetry is the pause before reconnecting to odio-api. RunStale is how long
// it may stay away before its run is unknown: longer than odio-api's own
// restart, which an upgrade of it causes mid-run.
var (
	RunRetry = 2 * time.Second
	RunStale = 15 * time.Second
)

// ErrRunning is odio-api refusing a second run.
var ErrRunning = errors.New("an upgrade is already running")

// odioAPIClient has no timeout: the stream lasts as long as the process.
var odioAPIClient = &http.Client{}

// FollowRun reports odio-api's run at base (http://host:port) whenever it
// changes, until ctx is done, reconnecting as long as it takes. Each
// connection starts from GET /upgrade's snapshot.
func FollowRun(ctx context.Context, base string, report func(Run), logf func(string, ...any)) {
	var down time.Time // since when odio-api is away, zero while heard
	for {
		connected, err := followRunOnce(ctx, base, report)
		if ctx.Err() != nil {
			return
		}
		if connected || down.IsZero() { // once per outage, not every RunRetry
			logf("upgrade: odio-api at %s: %v, retrying every %s", base, err, RunRetry)
			down = time.Now()
		}
		if time.Since(down) >= RunStale {
			report(Run{})
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(RunRetry):
		}
	}
}

// followRunOnce follows one connection to its end, which it returns;
// connected is whether it got as far as the snapshot.
func followRunOnce(ctx context.Context, base string, report func(Run)) (connected bool, err error) {
	body, err := get(ctx, base+"/events?types=upgrade.info,upgrade.progress")
	if err != nil {
		return false, err
	}
	defer func() { _ = body.Close() }()
	r := bufio.NewReader(body) // no line limit: upgrade.info carries a whole report
	var cur Run
	var event, data string
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			if err == io.EOF {
				err = errors.New("odio-api closed the stream")
			}
			return connected, err
		}
		line = strings.TrimRight(line, "\r\n")
		switch {
		case strings.HasPrefix(line, "event:"):
			event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		case strings.HasPrefix(line, "data:"):
			data += strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " ")
		case line == "": // an event ends
			if !connected {
				// server.info "connected" comes first: subscribed, so the
				// snapshot cannot predate what the stream sends next.
				if cur, err = fetchRun(ctx, base); err != nil {
					return false, err
				}
				connected = true
				report(cur)
			}
			switch event {
			case "upgrade.info": // the run, or the detector's report: only the run has a state
				if run, ok := parseRun([]byte(data)); ok {
					cur = run
					report(cur)
				}
			case "upgrade.progress":
				if p, ok := parseProgressLine(data); ok && cur.Running() {
					cur.Progress = &p
					report(cur)
				}
			}
			event, data = "", ""
		}
	}
}

// StartRun asks odio-api to start the upgrade unit. It answers once the
// unit is enqueued; the run then comes over FollowRun.
func StartRun(ctx context.Context, base string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/upgrade/start", nil)
	if err != nil {
		return err
	}
	resp, err := odioAPIClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	switch resp.StatusCode {
	case http.StatusAccepted:
		return nil
	case http.StatusConflict:
		return ErrRunning
	}
	msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	return fmt.Errorf("odio-api: %s: %s", resp.Status, strings.TrimSpace(string(msg)))
}

func get(ctx context.Context, url string) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := odioAPIClient.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		return nil, fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	return resp.Body, nil
}

// fetchRun is GET /upgrade's run.
func fetchRun(ctx context.Context, base string) (Run, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	body, err := get(ctx, base+"/upgrade")
	if err != nil {
		return Run{}, err
	}
	defer func() { _ = body.Close() }()
	var status struct {
		Run json.RawMessage `json:"run"`
	}
	if err := json.NewDecoder(body).Decode(&status); err != nil {
		return Run{}, fmt.Errorf("GET /upgrade: %w", err)
	}
	run, ok := parseRun(status.Run)
	if !ok {
		return Run{}, errors.New("GET /upgrade: no run")
	}
	return run, nil
}

// parseRun reads odio-api's run snapshot: its state, and while running the
// percent and step it has.
func parseRun(data []byte) (Run, bool) {
	var snap struct {
		State   *string `json:"state"`
		Percent *int    `json:"percent"`
		Step    *string `json:"step"`
	}
	if json.Unmarshal(data, &snap) != nil || snap.State == nil || *snap.State == "" {
		return Run{}, false
	}
	run := Run{State: *snap.State}
	if run.Running() && snap.Percent != nil {
		run.Progress = &Progress{Percent: *snap.Percent}
		if snap.Step != nil {
			run.Progress.Step = *snap.Step
		}
	}
	return run, true
}

// parseProgressLine reads one odio_progress line: begin starts a playbook
// at 0 (the unit may run a disable, then the upgrade), progress moves it,
// end says nothing the run's state will not.
func parseProgressLine(data string) (Progress, bool) {
	var line struct {
		Event   string  `json:"event"`
		Percent *int    `json:"percent"`
		Step    *string `json:"step"`
	}
	if err := json.Unmarshal([]byte(data), &line); err != nil {
		return Progress{}, false
	}
	switch line.Event {
	case "begin":
		return Progress{}, true
	case "progress":
		if line.Percent == nil || line.Step == nil {
			return Progress{}, false
		}
		return Progress{Percent: *line.Percent, Step: *line.Step}, true
	}
	return Progress{}, false
}
