package upgrade

import (
	"errors"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// fakeSystemctl answers `show` from its fields and records every call.
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
