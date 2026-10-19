package input

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/b0bbywan/odioctl/configtxt"
)

const fixture = `# For more options and information see
# http://rptl.io/configtxt
dtparam=audio=on
dtoverlay=vc4-kms-v3d

[cm4]
otg_mode=1
`

func TestPinsLeaveI2CAndI2SAlone(t *testing.T) {
	for _, held := range []int{2, 3, 18, 19, 20, 21} {
		if slices.Contains(Pins, held) {
			t.Errorf("GPIO %d is offered", held)
		}
	}
	for _, s := range []string{"18", "0", "28", "", "x", "25,gpio_pull=up"} {
		if _, ok := ParsePin(s); ok {
			t.Errorf("ParsePin(%q) accepted", s)
		}
	}
	if pin, ok := ParsePin("25"); !ok || pin != 25 {
		t.Errorf("ParsePin(25) = %d, %v", pin, ok)
	}
}

func TestParseNothingConfigured(t *testing.T) {
	if p := Parse(fixture); p.IRPin != 0 || p.Managed || len(p.StrayLines) != 0 {
		t.Errorf("parse = %+v", p)
	}
}

func TestParseUnmanagedIRLine(t *testing.T) {
	for line, want := range map[string]int{
		"dtoverlay=gpio-ir":                          18, // the overlay's default
		"dtoverlay=gpio-ir,gpio_pin=25":              25,
		"dtoverlay=gpio-ir,gpio_pull=up,gpio_pin=17": 17,
	} {
		p := Parse(fixture + line + "\n")
		if p.IRPin != want || p.Managed || !slices.Equal(p.StrayLines, []string{line}) {
			t.Errorf("%s: parse = %+v", line, p)
		}
	}
	// the transmitter is another overlay
	if p := Parse("dtoverlay=gpio-ir-tx,gpio_pin=17\n"); p.IRPin != 0 || len(p.StrayLines) != 0 {
		t.Errorf("gpio-ir-tx: parse = %+v", p)
	}
}

func TestApplyIRPutsTheBlockLastWithItsPin(t *testing.T) {
	out := ApplyIR(fixture, 25)
	want := fixture + "\n" + Begin + "\n[all]\ndtoverlay=gpio-ir,gpio_pin=25\n" + End + "\n"
	if out != want {
		t.Errorf("out:\n%s", out)
	}
	if p := Parse(out); p.IRPin != 25 || !p.Managed || len(p.StrayLines) != 0 {
		t.Errorf("parse = %+v", p)
	}
}

func TestApplyIRIdempotentAndSwitch(t *testing.T) {
	once := ApplyIR(fixture, 25)
	if twice := ApplyIR(once, 25); twice != once {
		t.Error("not idempotent")
	}
	switched := ApplyIR(once, 26)
	if strings.Count(switched, Begin) != 1 || strings.Contains(switched, "gpio_pin=25") ||
		!strings.Contains(switched, "dtoverlay=gpio-ir,gpio_pin=26") {
		t.Errorf("switched:\n%s", switched)
	}
}

func TestApplyIRDisablesTheFilesOwnLineAndUnapplyRestoresIt(t *testing.T) {
	text := fixture + "dtoverlay=gpio-ir,gpio_pin=17\n"
	out := ApplyIR(text, 25)
	if !strings.Contains(out, DisabledPrefix+"dtoverlay=gpio-ir,gpio_pin=17\n") {
		t.Errorf("stray line not disabled:\n%s", out)
	}
	if p := Parse(out); p.IRPin != 25 || len(p.StrayLines) != 0 {
		t.Errorf("parse = %+v", p)
	}
	if got := UnapplyIR(out); got != text {
		t.Errorf("got:\n%s", got)
	}
}

func TestApplyIREmptyFile(t *testing.T) {
	if out := ApplyIR("", 25); !strings.HasPrefix(out, Begin) {
		t.Errorf("out = %q", out)
	}
}

func TestUnapplyIRRestoresOriginal(t *testing.T) {
	if got := UnapplyIR(ApplyIR(fixture, 25)); got != fixture {
		t.Errorf("got:\n%s", got)
	}
	if got := UnapplyIR(fixture); got != fixture {
		t.Errorf("without a block:\n%s", got)
	}
}

func TestStatus(t *testing.T) {
	d := t.TempDir()
	cfg := filepath.Join(d, "config.txt")
	if err := os.WriteFile(cfg, []byte(ApplyIR(fixture, 25)), 0o644); err != nil {
		t.Fatal(err)
	}
	old := configtxt.RebootFlag
	configtxt.RebootFlag = filepath.Join(d, "run", "reboot-required")
	t.Cleanup(func() { configtxt.RebootFlag = old })

	if s := GetStatus(cfg); !s.Supported || s.IRPin != 25 || !s.Managed || s.RebootRequired {
		t.Errorf("status = %+v", s)
	}
	if changed, err := WouldChangeIR(cfg, 25); err != nil || changed {
		t.Errorf("WouldChangeIR(25) = %v, %v", changed, err)
	}
	if changed, err := WouldChangeIR(cfg, 26); err != nil || !changed {
		t.Errorf("WouldChangeIR(26) = %v, %v", changed, err)
	}
	if s := GetStatus(filepath.Join(d, "nope")); s.Supported {
		t.Errorf("status = %+v", s)
	}
}
