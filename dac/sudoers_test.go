package dac

import (
	"os"
	"strings"
	"testing"
)

// data/sudoers/odioctl is generated from the catalog — fail on drift.

func fragment(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("../data/sudoers/odioctl")
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestFileMatchesCatalog(t *testing.T) {
	if fragment(t) != SudoersFragment() {
		t.Error("data/sudoers/odioctl is out of date — run go generate ./dac")
	}
}

func TestNoWildcards(t *testing.T) {
	for _, line := range strings.Split(fragment(t), "\n") {
		if strings.HasPrefix(line, "%odioctl") && strings.ContainsAny(line, "*?") {
			t.Errorf("wildcard in %q", line)
		}
	}
}

func TestGrantsTheOdioctlGroupOnly(t *testing.T) {
	// `odio` is odios' state-access group, and the installing user is in it
	// too — keying root on it would hand root to every state reader.
	var rules int
	for _, line := range strings.Split(fragment(t), "\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		rules++
		if !strings.HasPrefix(line, "%odioctl ") && !strings.HasPrefix(line, "Defaults:%odioctl ") {
			t.Errorf("rule for someone else: %q", line)
		}
	}
	if rules == 0 {
		t.Error("no rules found")
	}
}
