package configtxt

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

var block = Block{Begin: "# BEGIN test", End: "# END test", Disabled: "#test-disabled: "}

func TestOverlay(t *testing.T) {
	for line, want := range map[string][2]string{
		"dtoverlay=vc4-kms-v3d":             {"vc4-kms-v3d", ""},
		" dtoverlay = gpio-ir,gpio_pin=25 ": {"gpio-ir", "gpio_pin=25"},
	} {
		name, params, ok := Overlay(line)
		if !ok || name != want[0] || params != want[1] {
			t.Errorf("Overlay(%q) = %q, %q, %v", line, name, params, ok)
		}
	}
	for _, line := range []string{"#dtoverlay=gpio-ir", "dtparam=audio=on", "dtoverlay="} {
		if _, _, ok := Overlay(line); ok {
			t.Errorf("Overlay(%q) matched", line)
		}
	}
}

func TestSplitWithoutEndSwallowsToEOF(t *testing.T) {
	before, got, after := block.Split([]string{"a", block.Begin, "b", "c"})
	if !slices.Equal(before, []string{"a"}) || !slices.Equal(got, []string{block.Begin, "b", "c"}) || len(after) != 0 {
		t.Errorf("split = %v, %v, %v", before, got, after)
	}
}

func TestRestDisablesAndRemoveRestores(t *testing.T) {
	const text = "\nkeep=1\nmine=1\n\n# BEGIN test\nx\n# END test\n\n"
	rest := block.Rest(text, func(line string) bool { return line == "mine=1" })
	if !slices.Equal(rest, []string{"keep=1", "#test-disabled: mine=1"}) {
		t.Errorf("rest = %q", rest)
	}
	disabled := strings.Join(rest, "\n") + "\n\n# BEGIN test\nx\n# END test\n"
	if got := block.Remove(disabled); got != "keep=1\nmine=1\n" {
		t.Errorf("remove = %q", got)
	}
}

func TestWriteKeepsAOneTimeBackup(t *testing.T) {
	cfg := filepath.Join(t.TempDir(), "config.txt")
	if err := os.WriteFile(cfg, []byte("pristine\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"first\n", "second\n"} {
		if err := Write(cfg, text); err != nil {
			t.Fatal(err)
		}
	}
	bak, err := os.ReadFile(cfg + ".odioctl.bak")
	if err != nil || string(bak) != "pristine\n" {
		t.Errorf("backup = %q, %v; want the pristine file", bak, err)
	}
	if text, _ := Read(cfg); text != "second\n" {
		t.Errorf("config = %q", text)
	}
}

func TestRunEdit(t *testing.T) {
	d := t.TempDir()
	cfg := filepath.Join(d, "config.txt")
	if err := os.WriteFile(cfg, []byte("a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	old := RebootFlag
	RebootFlag = filepath.Join(d, "run", "reboot-required")
	t.Cleanup(func() { RebootFlag = old })
	edit := func(text string) string { return text + "b\n" }

	var out, errb strings.Builder
	if rc := RunEdit(&out, &errb, cfg, true, "edited", edit); rc != 0 || out.String() != "a\nb\n" || RebootRequired() {
		t.Errorf("dry-run: rc = %d, out = %q, reboot = %v", rc, out.String(), RebootRequired())
	}
	out.Reset()
	if rc := RunEdit(&out, &errb, cfg, false, "edited", edit); rc != 0 || !strings.Contains(out.String(), "edited in "+cfg+" — reboot required") {
		t.Errorf("rc = %d, out = %q", rc, out.String())
	}
	if text, _ := Read(cfg); text != "a\nb\n" || !RebootRequired() {
		t.Errorf("config = %q, reboot = %v", text, RebootRequired())
	}
	out.Reset()
	same := func(text string) string { return text }
	if rc := RunEdit(&out, &errb, cfg, false, "edited", same); rc != 0 || out.String() != "no change\n" {
		t.Errorf("rc = %d, out = %q", rc, out.String())
	}
	if rc := RunEdit(&out, &errb, filepath.Join(d, "nope"), false, "edited", edit); rc != 2 {
		t.Errorf("missing file: rc = %d", rc)
	}
}
