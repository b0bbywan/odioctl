package web

// actionRun is one started component action: its process, the last of its
// output, and the link it printed. It owns the drain goroutine for the whole
// life of the process.

import (
	"bufio"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/b0bbywan/odioctl/components"
)

const maxOutputLines = 20

type actionRun struct {
	proc      ActionProcess
	id        string // the modal's element id (actionKey.domID)
	argv      []string
	started   time.Time
	scheme    string // the stdout token to surface as a link
	skip      string // a link containing this is not the one
	keepLink  bool   // the link outlives the process (components.Action)
	linkLabel string
	title     string
	found     chan struct{} // closed when the link is out (or at EOF)

	mu     sync.Mutex
	output []string
	url    string
}

// startAction spawns argv with {host} and {home} filled in — the only two
// substitutions, both from what the server knows rather than from the request
// body — and begins draining its output.
func startAction(spawn func([]string) (ActionProcess, error), action components.Action, host, home string) (*actionRun, error) {
	argv := make([]string, len(action.Argv))
	for i, part := range action.Argv {
		if home == "" && strings.Contains(part, "{home}") {
			return nil, userErrorf("cannot run %s: the home directory is unknown", action.Label)
		}
		part = strings.ReplaceAll(part, "{host}", host)
		argv[i] = strings.ReplaceAll(part, "{home}", home)
	}
	proc, err := spawn(argv)
	if err != nil {
		return nil, userErrorf("cannot run %s: %v", strings.Join(argv, " "), err)
	}
	run := &actionRun{
		proc:      proc,
		argv:      argv,
		started:   time.Now(),
		scheme:    action.LinkScheme,
		skip:      action.LinkSkip,
		keepLink:  action.LinkOutlivesRun,
		linkLabel: action.LinkLabel,
		title:     action.Label,
		found:     make(chan struct{}),
	}
	go run.drain()
	return run, nil
}

// drain reads the output until EOF — a full pipe would wedge the child — but
// stops *recording* once the link is out: what a login helper prints after
// the user is through is the credential it just obtained, and nothing that
// lands in the output is worth painting into a browser.
func (r *actionRun) drain() {
	var once sync.Once
	signal := func() { once.Do(func() { close(r.found) }) }
	defer signal() // EOF: nothing more is coming
	rd := bufio.NewReader(r.proc.Output())
	for {
		line, err := rd.ReadString('\n')
		if line != "" && r.link() == "" {
			r.record(line)
			if url := findLink(line, r.scheme, r.skip); url != "" {
				r.mu.Lock()
				r.url = url
				r.mu.Unlock()
				signal()
			}
		}
		if err != nil {
			return
		}
	}
}

func findLink(line, scheme, skip string) string {
	for _, w := range strings.Fields(line) {
		if !strings.HasPrefix(w, scheme) {
			continue
		}
		if skip != "" && strings.Contains(w, skip) {
			continue
		}
		return w
	}
	return ""
}

func (r *actionRun) record(line string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.output = append(r.output, line)
	if len(r.output) > maxOutputLines {
		r.output = r.output[1:]
	}
}

// awaitLink blocks until the link is printed, EOF, or timeout.
func (r *actionRun) awaitLink(timeout time.Duration) string {
	select {
	case <-r.found:
	case <-time.After(timeout):
	}
	return r.link()
}

func (r *actionRun) alive() bool { return r.proc.Alive() }

func (r *actionRun) elapsed() time.Duration { return time.Since(r.started).Round(time.Second) }

func (r *actionRun) link() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.url
}

// text is a snapshot of the output: the drain goroutine outlives the process.
func (r *actionRun) text() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return strings.Join(r.output, "")
}

// pending is what the row and the modal show: the link while it can still be
// followed, otherwise how the run ended.
func (r *actionRun) pending() (string, actionNote) {
	if r.alive() {
		return r.link(), actionNote{}
	}
	if url := r.link(); url != "" && r.keepLink && r.proc.ExitCode() == 0 {
		return url, actionNote{}
	}
	return "", r.note()
}

// result is the modal: the pending link or the end of the run, and the output.
func (r *actionRun) result() *ActionResult {
	r.settle()
	url, note := r.pending()
	return &ActionResult{ID: r.id, Title: r.title, Output: r.text(), URL: url, LinkLabel: r.linkLabel, Note: note}
}

// actionNote is the outcome of a finished run: success (the row shows a Done
// badge where the link was), or the failure with the tail of what it printed.
type actionNote struct {
	Text   string
	Failed bool
}

// OK is a run that ended well — the templates' predicate for a Done badge.
func (n actionNote) OK() bool { return n.Text != "" && !n.Failed }

// settle waits for the link or EOF before the output is read: the exit is
// reaped before the drain has the last line. Bounded, a grandchild could
// keep the pipe open for ever.
func (r *actionRun) settle() {
	select {
	case <-r.found:
	case <-time.After(500 * time.Millisecond):
	}
}

func (r *actionRun) note() actionNote {
	code := r.proc.ExitCode()
	if code == 0 {
		return actionNote{Text: "Done."}
	}
	r.settle()
	var parts []string
	for _, line := range strings.Split(r.text(), "\n") {
		if t := strings.TrimSpace(line); t != "" {
			parts = append(parts, t)
		}
	}
	detail := strings.Join(parts, " ")
	if len(detail) > 200 {
		detail = detail[len(detail)-200:]
	}
	return actionNote{
		Text:   strings.TrimSpace(fmt.Sprintf("Failed (exit %d). %s", code, detail)),
		Failed: true,
	}
}
