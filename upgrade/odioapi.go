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
// CanStart is its can_upgrade: without an upgrade unit, no POST /upgrade/start.
type Run struct {
	State    string
	Progress *Progress // while running, once a percent came
	CanStart bool
	Failure  *Failure // failed, once the playbook's end line said why
	Off      bool     // unknown because odio-api's upgrade backend is off
}

// Failure is what a failed playbook's end line says: the step it was in, and
// its first failed task with the message.
type Failure struct {
	Step  string
	Error string
}

func (r Run) Known() bool   { return r.State != "" }
func (r Run) Running() bool { return r.State == RunRunning }

func (r Run) Equal(o Run) bool {
	if r.State != o.State || r.CanStart != o.CanStart || r.Off != o.Off ||
		(r.Progress == nil) != (o.Progress == nil) || (r.Failure == nil) != (o.Failure == nil) {
		return false
	}
	return (r.Progress == nil || *r.Progress == *o.Progress) &&
		(r.Failure == nil || *r.Failure == *o.Failure)
}

// RunRetry is the pause before reconnecting to odio-api. RunStale is how long
// it may stay away before its run is unknown: longer than odio-api's own
// restart, which an upgrade of it causes mid-run.
var (
	RunRetry    = 2 * time.Second
	RunStale    = 15 * time.Second
	RunRetryMax = time.Minute // the pause past RunStale doubles up to this
)

// ErrRunning is odio-api refusing a second run.
var ErrRunning = errors.New("an upgrade is already running")

// ErrNoUpgrades is odio-api answering without its upgrade backend: nothing
// to follow or start until it restarts with one.
var ErrNoUpgrades = errors.New("odio-api has no upgrade backend")

// odioAPIClient has no timeout: the stream lasts as long as the process.
var odioAPIClient = &http.Client{}

// FollowRun reports odio-api's run at base (http://host:port) whenever it
// changes, until ctx is done, reconnecting as long as it takes. Each
// connection starts from GET /upgrade's snapshot. Unknown is reported after
// RunStale away, or at once while odio-api never answered: no run to keep.
func FollowRun(ctx context.Context, base string, report func(Run), logf func(string, ...any)) {
	var down time.Time // since when odio-api is away, zero while heard
	heard := false     // a connection got its snapshot once
	wait := RunRetry
	for {
		connected, err := followRunOnce(ctx, base, report)
		if ctx.Err() != nil {
			return
		}
		heard = heard || connected
		if connected || down.IsZero() { // once per outage, not every try
			logf("upgrade: odio-api at %s: %v, retrying", base, err)
			down, wait = time.Now(), 0
		}
		off := errors.Is(err, ErrNoUpgrades)
		switch {
		case off:
			report(Run{Off: true})
		case !heard || time.Since(down) >= RunStale:
			report(Run{})
		}
		wait = retryAfter(wait, time.Since(down), off)
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}

// retryAfter is the pause before the next try: RunRetry while an outage is
// young (odio-api restarting mid-run), then doubling up to RunRetryMax; at
// once when its backend is off, which only a restart of it changes.
func retryAfter(last, away time.Duration, off bool) time.Duration {
	if !off && away < RunStale {
		return RunRetry
	}
	return min(max(2*last, RunRetry), RunRetryMax)
}

// upgradesOn asks GET /server whether odio-api runs its upgrade backend,
// before subscribing to anything.
func upgradesOn(ctx context.Context, base string) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	body, err := get(ctx, base+"/server")
	if err != nil {
		return err
	}
	defer func() { _ = body.Close() }()
	var info struct {
		Backends struct {
			Upgrade bool `json:"upgrade"`
		} `json:"backends"`
	}
	if err := json.NewDecoder(body).Decode(&info); err != nil {
		return fmt.Errorf("GET /server: %w", err)
	}
	if !info.Backends.Upgrade {
		return ErrNoUpgrades
	}
	return nil
}

// followRunOnce follows one connection to its end, which it returns;
// connected is whether it got as far as the snapshot.
func followRunOnce(ctx context.Context, base string, report func(Run)) (connected bool, err error) {
	if err := upgradesOn(ctx, base); err != nil {
		return false, err
	}
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
					run.CanStart = cur.CanStart // GET /upgrade's alone
					cur = next(cur, run)
					report(cur)
				}
			case "server.info": // "love" comes after 30s without an event: mid-run, an end missed?
				if data == `"love"` && cur.Running() {
					snap, err := fetchRun(ctx, base)
					if err != nil {
						return connected, err
					}
					if snap.State != cur.State {
						cur = next(cur, snap)
						report(cur)
					}
				}
			case "upgrade.progress":
				p, failure, ok := parseProgressLine(data)
				switch {
				case ok && p != nil && cur.Running():
					cur.Progress, cur.Failure = p, nil // a next playbook: the last one's end is over
					report(cur)
				case ok && failure != nil && (cur.Running() || cur.State == RunFailed):
					cur.Failure = failure // a shell's run is failed already: odio-api closed it on this line
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
		Run        json.RawMessage `json:"run"`
		CanUpgrade bool            `json:"can_upgrade"`
	}
	if err := json.NewDecoder(body).Decode(&status); err != nil {
		return Run{}, fmt.Errorf("GET /upgrade: %w", err)
	}
	run, ok := parseRun(status.Run)
	if !ok {
		return Run{}, errors.New("GET /upgrade: no run")
	}
	run.CanStart = status.CanUpgrade
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

// next is run after cur: a failed one keeps the failure cur's end line
// said, which a unit's run sends before the unit ends.
func next(cur, run Run) Run {
	if run.State == RunFailed && cur.Running() {
		run.Failure = cur.Failure
	}
	return run
}

// parseProgressLine reads one odio_progress line: begin starts a playbook
// at 0 (the unit may run a disable, then the upgrade), progress moves it,
// a failed end says what failed.
func parseProgressLine(data string) (*Progress, *Failure, bool) {
	var line struct {
		Event   string  `json:"event"`
		Percent *int    `json:"percent"`
		Step    *string `json:"step"`
		Success *bool   `json:"success"`
		Error   string  `json:"error"`
	}
	if err := json.Unmarshal([]byte(data), &line); err != nil {
		return nil, nil, false
	}
	switch line.Event {
	case "begin":
		return &Progress{}, nil, true
	case "progress":
		if line.Percent == nil || line.Step == nil {
			return nil, nil, false
		}
		return &Progress{Percent: *line.Percent, Step: *line.Step}, nil, true
	case "end":
		if line.Success == nil || *line.Success || line.Error == "" {
			return nil, nil, false
		}
		f := &Failure{Error: firstLine(line.Error, 200)}
		if line.Step != nil {
			f.Step = *line.Step
		}
		return nil, f, true
	}
	return nil, nil, false
}

// firstLine is s's first line, cut at n runes: a task's stderr can be pages.
func firstLine(s string, n int) string {
	s, _, _ = strings.Cut(strings.TrimSpace(s), "\n")
	if r := []rune(s); len(r) > n {
		s = string(r[:n]) + "…"
	}
	return s
}
