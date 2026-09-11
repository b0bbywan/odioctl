package upgrade

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeSystemctl answers `show` from its fields and records every call;
// the state is read from WaitUnit's goroutine, hence the lock.
type fakeSystemctl struct {
	mu     sync.Mutex
	active string
	result string
	code   int
	calls  []string
}

func (f *fakeSystemctl) install(t *testing.T) {
	t.Helper()
	old := Systemctl
	Systemctl = func(args ...string) (string, error) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.calls = append(f.calls, strings.Join(args, " "))
		if args[0] == "show" {
			// systemd's order, not the -p one
			return "Result=" + f.result + "\nExecMainStatus=" + itoa(f.code) + "\nActiveState=" + f.active + "\n", nil
		}
		return "", nil
	}
	t.Cleanup(func() { Systemctl = old })
}

func (f *fakeSystemctl) set(active, result string, code int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.active, f.result, f.code = active, result, code
}

func itoa(n int) string { return strconv.Itoa(n) }

func quiet(string, ...any) {}

func TestShowUnitReadsSystemctlByKey(t *testing.T) {
	f := &fakeSystemctl{active: "failed", result: "exit-code", code: 2}
	f.install(t)
	u, err := ShowUnit()
	if err != nil {
		t.Fatal(err)
	}
	if u != (UnitState{Active: "failed", Result: "exit-code", Code: 2}) || u.Running() || !u.Failed() {
		t.Errorf("u = %+v", u)
	}
	if f.calls[0] != "show -p ActiveState -p Result -p ExecMainStatus "+Unit {
		t.Errorf("calls = %v", f.calls)
	}
}

func TestShowUnitReportsSystemctlFailure(t *testing.T) {
	old := Systemctl
	Systemctl = func(...string) (string, error) { return "", errors.New("no user bus") }
	t.Cleanup(func() { Systemctl = old })
	if _, err := ShowUnit(); err == nil {
		t.Error("want an error")
	}
}

func TestStartUnitDoesNotBlock(t *testing.T) {
	f := &fakeSystemctl{}
	f.install(t)
	if err := StartUnit(); err != nil {
		t.Fatal(err)
	}
	if len(f.calls) != 1 || f.calls[0] != "start --no-block "+Unit {
		t.Errorf("calls = %v", f.calls)
	}
}

func TestWaitUnitEndsWhenTheInvocationLinkGoes(t *testing.T) {
	dir := t.TempDir()
	link := filepath.Join(dir, "invocation:"+Unit)
	if err := os.Symlink("0123456789abcdef", link); err != nil {
		t.Fatal(err)
	}
	f := &fakeSystemctl{active: "activating"}
	f.install(t)
	oldPoll, oldRecheck := UnitPoll, UnitRecheck
	UnitPoll, UnitRecheck = time.Hour, time.Hour // only the link's removal may end it
	t.Cleanup(func() { UnitPoll, UnitRecheck = oldPoll, oldRecheck })

	wait := WatchUnit(dir, quiet) // subscribed here, so the removal cannot be missed
	done := make(chan UnitState, 1)
	go func() { done <- wait() }()
	f.set("inactive", "success", 0)
	_ = os.Remove(link)
	select {
	case u := <-done:
		if u.Active != "inactive" || u.Result != "success" {
			t.Errorf("u = %+v", u)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("WaitUnit did not see the link go")
	}
}

func TestWaitUnitPollsWithoutTheUnitsDir(t *testing.T) {
	f := &fakeSystemctl{active: "activating"}
	f.install(t)
	oldPoll, oldRecheck := UnitPoll, UnitRecheck
	UnitPoll, UnitRecheck = 20*time.Millisecond, time.Hour
	t.Cleanup(func() { UnitPoll, UnitRecheck = oldPoll, oldRecheck })
	var logs strings.Builder
	var mu sync.Mutex
	logf := func(format string, args ...any) {
		mu.Lock()
		defer mu.Unlock()
		fmt.Fprintf(&logs, format+"\n", args...)
	}

	done := make(chan UnitState, 1)
	go func() { done <- WaitUnit(filepath.Join(t.TempDir(), "nowhere"), logf) }()
	time.Sleep(50 * time.Millisecond)
	f.set("failed", "exit-code", 1)
	select {
	case u := <-done:
		if !u.Failed() || u.Code != 1 {
			t.Errorf("u = %+v", u)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("WaitUnit did not poll to the end")
	}
	mu.Lock()
	defer mu.Unlock()
	if !strings.Contains(logs.String(), "cannot watch") {
		t.Errorf("logs = %q", logs.String())
	}
}
