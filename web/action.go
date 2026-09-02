package web

// actionRun is one started component action: its process, the last of its
// output, and the link it printed. It owns the drain goroutine for the whole
// life of the process.

import (
	"bufio"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/b0bbywan/odioctl/components"
)

const maxOutputLines = 20

type actionRun struct {
	proc      ActionProcess
	scheme    string // the stdout token to surface as a link
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
func startAction(spawn func([]string) (ActionProcess, error), action components.Action, host string) (*actionRun, error) {
	home, _ := os.UserHomeDir()
	argv := make([]string, len(action.Argv))
	for i, part := range action.Argv {
		part = strings.ReplaceAll(part, "{host}", host)
		argv[i] = strings.ReplaceAll(part, "{home}", home)
	}
	proc, err := spawn(argv)
	if err != nil {
		return nil, userErrorf("cannot run %s: %v", strings.Join(argv, " "), err)
	}
	run := &actionRun{
		proc:      proc,
		scheme:    action.LinkScheme,
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
			if url := findLink(line, r.scheme); url != "" {
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

func findLink(line, scheme string) string {
	for _, w := range strings.Fields(line) {
		if strings.HasPrefix(w, scheme) {
			return w
		}
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

func (r *actionRun) result(action components.Action) *ActionResult {
	return &ActionResult{
		Title:     action.Label,
		Output:    r.text(),
		URL:       r.link(),
		LinkLabel: action.LinkLabel,
	}
}

// note sums up a finished run for the page: "Done.", or the failure with the
// tail of what it printed.
func (r *actionRun) note() string {
	code := r.proc.ExitCode()
	if code == 0 {
		return "Done."
	}
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
	return strings.TrimSpace(fmt.Sprintf("Failed (exit %d). %s", code, detail))
}
