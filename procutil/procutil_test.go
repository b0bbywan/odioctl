package procutil

import (
	"errors"
	"os/exec"
	"testing"
)

func TestExitCode(t *testing.T) {
	if code, err := ExitCode(nil); code != 0 || err != nil {
		t.Errorf("nil → %d, %v", code, err)
	}
	if code, err := ExitCode(exec.Command("sh", "-c", "exit 3").Run()); code != 3 || err != nil {
		t.Errorf("exit 3 → %d, %v", code, err)
	}
	notRun := exec.Command("/nonexistent/binary").Run()
	if code, err := ExitCode(notRun); code != 0 || !errors.Is(err, notRun) {
		t.Errorf("not run → %d, %v", code, err)
	}
}
